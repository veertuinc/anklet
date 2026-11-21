package github

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v75/github"
	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
)

type memRedis struct {
	redis.Cmdable
	mu         sync.Mutex
	lists      map[string][]string
	lremResult *int64
}

func newMemRedis() *memRedis {
	return &memRedis{lists: make(map[string][]string)}
}

func (m *memRedis) Exists(_ context.Context, keys ...string) *redis.IntCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := redis.NewIntCmd(context.Background())
	var n int64
	for _, k := range keys {
		if len(m.lists[k]) > 0 {
			n++
		}
	}
	cmd.SetVal(n)
	return cmd
}

func (m *memRedis) Scan(_ context.Context, cursor uint64, match string, _ int64) *redis.ScanCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := redis.NewScanCmd(context.Background(), nil)
	if cursor != 0 {
		cmd.SetVal(nil, 0)
		return cmd
	}
	prefix := strings.TrimSuffix(match, "*")
	var out []string
	for k := range m.lists {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	cmd.SetVal(out, 0)
	return cmd
}

func (m *memRedis) LRange(_ context.Context, key string, _, _ int64) *redis.StringSliceCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := redis.NewStringSliceCmd(context.Background())
	cmd.SetVal(append([]string(nil), m.lists[key]...))
	return cmd
}

func (m *memRedis) LIndex(_ context.Context, key string, index int64) *redis.StringCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := redis.NewStringCmd(context.Background())
	list := m.lists[key]
	if index < 0 {
		index = int64(len(list)) + index
	}
	if index < 0 || index >= int64(len(list)) {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	cmd.SetVal(list[index])
	return cmd
}

func (m *memRedis) LRem(_ context.Context, key string, _ int64, value any) *redis.IntCmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd := redis.NewIntCmd(context.Background())
	if m.lremResult != nil {
		cmd.SetVal(*m.lremResult)
		return cmd
	}
	target := value.(string)
	for i, item := range m.lists[key] {
		if item == target {
			m.lists[key] = append(m.lists[key][:i], m.lists[key][i+1:]...)
			cmd.SetVal(1)
			return cmd
		}
	}
	cmd.SetVal(0)
	return cmd
}

func generateTestRSAPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	return string(pem.EncodeToMemory(block))
}

func TestAuthenticateAndReturnGitHubClientModes(t *testing.T) {
	t.Parallel()

	ctx := testContextWithLogger()
	pemKey := generateTestRSAPEM(t)

	// PAT only
	client, wrapper, err := AuthenticateAndReturnGitHubClient(ctx, "", 0, 0, "ghp_test", "", "veertuinc")
	if err != nil || client == nil || wrapper == nil {
		t.Fatalf("PAT auth failed: client=%v wrapper=%v err=%v", client, wrapper, err)
	}
	if orgClient, err := wrapper.ClientForOrganization(ctx, "veertuinc"); err != nil || orgClient != client {
		t.Fatalf("PAT ClientForOrganization should return default client: (%v, %v)", orgClient, err)
	}

	// Missing app_id with private_key
	if _, _, err := AuthenticateAndReturnGitHubClient(ctx, pemKey, 0, 123, "", "", "veertuinc"); err == nil {
		t.Fatal("expected error when private_key set without app_id")
	}

	// App with explicit installation_id
	client, wrapper, err = AuthenticateAndReturnGitHubClient(ctx, pemKey, 1001, 2002, "", "", "veertuinc")
	if err != nil || client == nil || wrapper == nil || wrapper.appsTransport == nil {
		t.Fatalf("App auth failed: client=%v wrapper=%v err=%v", client, wrapper, err)
	}

	// Enterprise hybrid (classic PAT + App)
	client, wrapper, err = AuthenticateAndReturnGitHubClient(ctx, pemKey, 1001, 0, "ghp_ent", "veertu-inc", "")
	if err != nil || client == nil || wrapper == nil || wrapper.appsTransport == nil {
		t.Fatalf("Enterprise hybrid auth failed: client=%v wrapper=%v err=%v", client, wrapper, err)
	}
}

func TestFindEnterpriseInstallationIDDirectAnd404Fallback(t *testing.T) {
	t.Parallel()

	t.Run("direct endpoint succeeds", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/enterprises/veertu-inc/installation" {
				t.Errorf("path = %s", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":777}`))
		}))
		defer srv.Close()
		client := gogithub.NewClient(srv.Client())
		u, _ := url.Parse(srv.URL + "/")
		client.BaseURL = u

		id, err := findEnterpriseInstallationID(testContextWithLogger(), client, "veertu-inc")
		if err != nil || id != 777 {
			t.Fatalf("findEnterpriseInstallationID() = (%d, %v), want 777", id, err)
		}
	})

	t.Run("404 falls back to ListInstallations", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/enterprises/veertu-inc/installation" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			if r.URL.Path == "/app/installations" {
				_, _ = w.Write([]byte(`[{"id":888,"target_type":"Enterprise","account":{"login":"veertu-inc"}}]`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()
		client := gogithub.NewClient(srv.Client())
		u, _ := url.Parse(srv.URL + "/")
		client.BaseURL = u

		id, err := findEnterpriseInstallationID(testContextWithLogger(), client, "veertu-inc")
		if err != nil || id != 888 {
			t.Fatalf("fallback findEnterpriseInstallationID() = (%d, %v), want 888", id, err)
		}
	})
}

func TestExecuteGitHubClientFunctionPrimaryRateLimitAnd404AuthAbort(t *testing.T) {
	t.Parallel()

	t.Run("404 with invalid auth aborts without sleeping", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer srv.Close()
		ghClient := gogithub.NewClient(srv.Client())
		u, _ := url.Parse(srv.URL + "/")
		ghClient.BaseURL = u

		ctx := context.WithValue(testContextWithLogger(), config.ContextKey("githubwrapperclient"), NewGitHubClientWrapper(ghClient))
		start := time.Now()
		_, _, _, err := ExecuteGitHubClientFunction(ctx, ctx, func() (*string, *gogithub.Response, error) {
			return nil, nil, errors.New("404 Not Found")
		})
		if err == nil || !strings.Contains(err.Error(), "authentication failure") {
			t.Fatalf("expected auth failure error, got %v", err)
		}
		if time.Since(start) > 2*time.Second {
			t.Fatalf("expected immediate abort without 30s backoff")
		}
	})

	t.Run("primary rate limit pauses until reset and retries", func(t *testing.T) {
		ctx := context.WithValue(testContextWithLogger(), config.ContextKey("globals"), &config.Globals{
			Plugins: map[string]map[string]*config.PluginGlobal{},
		})
		var calls atomic.Int32
		_, res, _, err := ExecuteGitHubClientFunction(ctx, ctx, func() (*string, *gogithub.Response, error) {
			n := calls.Add(1)
			val := "done"
			req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/test", nil)
			if n == 1 {
				resp := &gogithub.Response{
					Response: &http.Response{StatusCode: 200, Request: req},
					Rate: gogithub.Rate{
						Limit:     5000,
						Remaining: 5,
						Reset:     gogithub.Timestamp{Time: time.Now().Add(50 * time.Millisecond)},
					},
				}
				return &val, resp, nil
			}
			resp := &gogithub.Response{
				Response: &http.Response{StatusCode: 200, Request: req},
				Rate:     gogithub.Rate{Limit: 5000, Remaining: 4999},
			}
			return &val, resp, nil
		})
		if err != nil || res == nil || *res != "done" || calls.Load() != 2 {
			t.Fatalf("got (res=%v, calls=%d, err=%v), want (done, 2, nil)", res, calls.Load(), err)
		}
	})
}

func TestRedeliveryBudgetRefreshAndReserve(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rate_limit" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		n := calls.Add(1)
		reset := time.Now().Add(50 * time.Millisecond).Unix()
		remaining := 4
		if n > 1 {
			remaining = 100
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resources": map[string]any{
				"core": map[string]any{
					"limit":     5000,
					"remaining": remaining,
					"reset":     reset,
				},
			},
		})
	}))
	defer srv.Close()
	client := gogithub.NewClient(srv.Client())
	u, _ := url.Parse(srv.URL + "/")
	client.BaseURL = u

	ctx := context.WithValue(testContextWithLogger(), config.ContextKey("globals"), &config.Globals{
		Plugins: map[string]map[string]*config.PluginGlobal{},
	})
	b := &RedeliveryBudget{}
	if err := b.Reserve(ctx, ctx, client, 2); err != nil {
		t.Fatalf("first Reserve(2) error = %v", err)
	}
	b.Record(2)
	// Next call exceeds initial budget (2), triggering wait + refresh to remaining=100 (budget=50).
	if err := b.Reserve(ctx, ctx, client, 2); err != nil {
		t.Fatalf("second Reserve(2) after reset error = %v", err)
	}
	if b.Budget() != 50 {
		t.Fatalf("Budget after refresh = %d, want 50", b.Budget())
	}
}

func TestQueueHelpersAndWorkflowJobStatusMapping(t *testing.T) {
	t.Parallel()

	job1 := QueueJob{
		Type: "WorkflowJobPayload",
		WorkflowJob: SimplifiedWorkflowJob{
			ID:     gogithub.Ptr(int64(501)),
			RunID:  gogithub.Ptr(int64(9001)),
			Status: gogithub.Ptr("queued"),
		},
		Repository: Repository{Owner: gogithub.Ptr("veertuinc"), Name: gogithub.Ptr("anklet")},
	}
	job1JSON, _ := json.Marshal(job1)
	job2 := QueueJob{
		Type: "WorkflowJobPayload",
		WorkflowJob: SimplifiedWorkflowJob{
			ID:     gogithub.Ptr(int64(502)),
			RunID:  gogithub.Ptr(int64(9002)),
			Status: gogithub.Ptr("queued"),
		},
		Repository: Repository{Owner: gogithub.Ptr("veertuinc"), Name: gogithub.Ptr("anklet")},
	}
	job2JSON, _ := json.Marshal(job2)

	fake := newMemRedis()
	fake.lists["anklet/jobs/github/queued/veertuinc"] = []string{string(job1JSON), string(job2JSON)}
	fake.lists["anklet/jobs/github/queued/veertuinc/{H1}"] = []string{string(job1JSON)}

	db := &database.Database{Client: fake, MaxRetries: 0}
	ctx := context.WithValue(testContextWithLogger(), config.ContextKey("database"), db)

	// GetJobJSONFromQueueByID
	found, err := GetJobJSONFromQueueByID(ctx, 502, "anklet/jobs/github/queued/veertuinc")
	if err != nil || !strings.Contains(found, "502") {
		t.Fatalf("GetJobJSONFromQueueByID(502) = (%q, %v)", found, err)
	}

	// GetJobFromQueue
	head, err := GetJobFromQueue(ctx, "anklet/jobs/github/queued/veertuinc/{H1}")
	if err != nil || head.WorkflowJob.ID == nil || *head.WorkflowJob.ID != 501 {
		t.Fatalf("GetJobFromQueue() = (%+v, %v)", head, err)
	}

	// GetJobFromQueueByKeyAndValue
	byKey, err := GetJobFromQueueByKeyAndValue(ctx, "anklet/jobs/github/queued/veertuinc", "workflow_job.id", "502")
	if err != nil || !strings.Contains(byKey, "502") {
		t.Fatalf("GetJobFromQueueByKeyAndValue() = (%q, %v)", byKey, err)
	}

	// CheckIfJobExistsInHandlerQueues
	exists, err := CheckIfJobExistsInHandlerQueues(ctx, 9001, 501, "veertuinc")
	if err != nil || !exists {
		t.Fatalf("CheckIfJobExistsInHandlerQueues(501) = (%v, %v), want true", exists, err)
	}
	exists, err = CheckIfJobExistsInHandlerQueues(ctx, 9002, 502, "veertuinc")
	if err != nil || exists {
		t.Fatalf("CheckIfJobExistsInHandlerQueues(502) = (%v, %v), want false", exists, err)
	}

	// PopJobOffQueue claims index 1 (502)
	popped, err := PopJobOffQueue(ctx, "anklet/jobs/github/queued/veertuinc", 1)
	if err != nil || !strings.Contains(popped, "502") {
		t.Fatalf("PopJobOffQueue(1) = (%q, %v)", popped, err)
	}

	// PopJobOffQueue race (LRem returns 0) returns empty string
	zero := int64(0)
	fake.lremResult = &zero
	racePopped, err := PopJobOffQueue(ctx, "anklet/jobs/github/queued/veertuinc", 0)
	if err != nil || racePopped != "" {
		t.Fatalf("PopJobOffQueue race = (%q, %v), want empty", racePopped, err)
	}
	fake.lremResult = nil

	// DeleteFromQueue removes 501
	if err := DeleteFromQueue(ctx, 501, "anklet/jobs/github/queued/veertuinc"); err != nil {
		t.Fatalf("DeleteFromQueue() error = %v", err)
	}
	if len(fake.lists["anklet/jobs/github/queued/veertuinc"]) != 0 {
		t.Fatalf("expected queue empty after delete, got %v", fake.lists["anklet/jobs/github/queued/veertuinc"])
	}

	// UpdateJobsWorkflowJobStatus across status transitions
	statusCases := []struct {
		apiStatus      string
		apiConclusion  string
		wantStatus     string
		wantConclusion string
	}{
		{"completed", "success", "completed", "success"},
		{"action_required", "", "completed", "failure"},
		{"timed_out", "", "completed", "failure"},
		{"cancelled", "cancelled", "completed", "cancelled"},
		{"waiting", "", "in_progress", ""},
		{"pending", "", "in_progress", ""},
		{"requested", "", "queued", ""},
	}
	for _, tc := range statusCases {
		t.Run("status_"+tc.apiStatus, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":         501,
					"status":     tc.apiStatus,
					"conclusion": tc.apiConclusion,
				})
			}))
			defer srv.Close()
			ghClient := gogithub.NewClient(srv.Client())
			u, _ := url.Parse(srv.URL + "/")
			ghClient.BaseURL = u

			apiCtx := context.WithValue(ctx, config.ContextKey("githubwrapperclient"), NewGitHubClientWrapper(ghClient))
			copyJob := job1
			updated, err := UpdateJobsWorkflowJobStatus(apiCtx, apiCtx, &copyJob)
			if err != nil {
				t.Fatalf("UpdateJobsWorkflowJobStatus() error = %v", err)
			}
			if updated.WorkflowJob.Status == nil || *updated.WorkflowJob.Status != tc.wantStatus {
				t.Errorf("status = %v, want %q", updated.WorkflowJob.Status, tc.wantStatus)
			}
			if tc.wantConclusion != "" && (updated.WorkflowJob.Conclusion == nil || *updated.WorkflowJob.Conclusion != tc.wantConclusion) {
				t.Errorf("conclusion = %v, want %q", updated.WorkflowJob.Conclusion, tc.wantConclusion)
			}
		})
	}
}
