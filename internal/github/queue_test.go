package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v75/github"
	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/metrics"
)

type fakeRedisQueue struct {
	redis.Cmdable
	keys  map[string][]string
	exist map[string]int64
}

func (f *fakeRedisQueue) Exists(_ context.Context, keys ...string) *redis.IntCmd {
	cmd := redis.NewIntCmd(context.Background())
	var count int64
	for _, k := range keys {
		if _, ok := f.keys[k]; ok {
			count++
		} else if c, ok := f.exist[k]; ok {
			count += c
		}
	}
	cmd.SetVal(count)
	return cmd
}

func (f *fakeRedisQueue) Scan(_ context.Context, cursor uint64, match string, _ int64) *redis.ScanCmd {
	cmd := redis.NewScanCmd(context.Background(), nil)
	if cursor != 0 {
		cmd.SetVal(nil, 0)
		return cmd
	}
	prefix := strings.TrimSuffix(match, "*")
	var out []string
	for k := range f.keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	cmd.SetVal(out, 0)
	return cmd
}

func (f *fakeRedisQueue) LIndex(_ context.Context, key string, index int64) *redis.StringCmd {
	cmd := redis.NewStringCmd(context.Background())
	list := f.keys[key]
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

func (f *fakeRedisQueue) LRange(_ context.Context, key string, _, _ int64) *redis.StringSliceCmd {
	cmd := redis.NewStringSliceCmd(context.Background())
	cmd.SetVal(append([]string(nil), f.keys[key]...))
	return cmd
}

func (f *fakeRedisQueue) LSet(_ context.Context, key string, index int64, value any) *redis.StatusCmd {
	cmd := redis.NewStatusCmd(context.Background())
	if int(index) < len(f.keys[key]) {
		switch v := value.(type) {
		case []byte:
			f.keys[key][index] = string(v)
		case string:
			f.keys[key][index] = v
		}
	}
	cmd.SetVal("OK")
	return cmd
}

func TestValidateGitHubClientAuthenticationUsesRateLimitEndpoint(t *testing.T) {
	t.Parallel()

	calledPath := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calledPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"resources":{"core":{"limit":5000,"remaining":4999,"reset":1893456000}}}`))
	}))
	defer srv.Close()

	client := gogithub.NewClient(srv.Client())
	u, _ := url.Parse(srv.URL + "/")
	client.BaseURL = u

	if err := validateGitHubClientAuthentication(context.Background(), client); err != nil {
		t.Fatalf("validateGitHubClientAuthentication() error = %v", err)
	}
	if calledPath != "/rate_limit" {
		t.Fatalf("called path = %q, want /rate_limit (must not call /user)", calledPath)
	}

	unauthSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer unauthSrv.Close()
	unauthClient := gogithub.NewClient(unauthSrv.Client())
	u2, _ := url.Parse(unauthSrv.URL + "/")
	unauthClient.BaseURL = u2
	if err := validateGitHubClientAuthentication(context.Background(), unauthClient); err == nil {
		t.Fatal("expected error for 401 unauthorized")
	}
}

func TestExecuteGitHubClientFunctionZeroLimitDoesNotPauseAndHandlesNilRequest(t *testing.T) {
	t.Parallel()

	ctx := testContextWithLogger()
	start := time.Now()
	_, val, _, err := ExecuteGitHubClientFunction(ctx, ctx, func() (*string, *gogithub.Response, error) {
		s := "ok"
		// Limit == 0 and Request == nil must not panic or pause.
		resp := &gogithub.Response{Response: &http.Response{StatusCode: 200}}
		return &s, resp, nil
	})
	if err != nil {
		t.Fatalf("ExecuteGitHubClientFunction() error = %v", err)
	}
	if val == nil || *val != "ok" {
		t.Fatalf("val = %v, want ok", val)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("unexpected rate-limit pause when Rate.Limit == 0")
	}
}

func TestWaitForGitHubAPIRateLimitResetUpdatesMetricsStatus(t *testing.T) {
	t.Parallel()

	m := &metrics.MetricsDataLock{}
	if err := m.AddPlugin(metrics.Plugin{
		PluginBase: metrics.PluginBase{
			Name:       "RUNNER1",
			PluginName: "github",
			Status:     "idle",
		},
	}); err != nil {
		t.Fatal(err)
	}
	globals := &config.Globals{Plugins: map[string]map[string]*config.PluginGlobal{}}
	workerCtx := context.WithValue(testContextWithLogger(), config.ContextKey("metrics"), m)
	workerCtx = context.WithValue(workerCtx, config.ContextKey("globals"), globals)
	pluginCtx, cancel := context.WithCancel(testContextWithLogger())
	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("plugin"), config.Plugin{Name: "RUNNER1"})

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_ = waitForGitHubAPIRateLimitReset(workerCtx, pluginCtx, time.Now().Add(10*time.Second))
	snap := m.Snapshot()
	if snap.Plugins[0].Status != "limit_paused" {
		t.Fatalf("plugin status = %q, want limit_paused", snap.Plugins[0].Status)
	}
}

func TestListOwnerQueueKeysPreventsCrossOrgPrefixCollision(t *testing.T) {
	t.Parallel()

	fake := &fakeRedisQueue{
		keys: map[string][]string{
			"anklet/jobs/github/queued/org":            {"{}"},
			"anklet/jobs/github/queued/org/{HANDLER1}": {"{}"},
			"anklet/jobs/github/queued/org-prod":       {"{}"},
			"anklet/jobs/github/queued/org-prod/{H2}":  {"{}"},
		},
	}
	db := &database.Database{Client: fake, MaxRetries: 0}
	keys, err := ListOwnerQueueKeys(context.Background(), db, "anklet/jobs/github/queued", "org")
	if err != nil {
		t.Fatalf("ListOwnerQueueKeys() error = %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("got %v, want exactly 2 keys for org", keys)
	}
	for _, k := range keys {
		if strings.Contains(k, "org-prod") {
			t.Fatalf("cross-org key leaked into result: %s", k)
		}
	}
}

func TestUpdateJobInDBSentinelCompletedAndNilRunID(t *testing.T) {
	t.Parallel()

	jobID := int64(42)
	queuedJob := QueueJob{
		Type: "WorkflowJobPayload",
		WorkflowJob: SimplifiedWorkflowJob{
			ID:     &jobID,
			Status: gogithub.Ptr("queued"),
		},
	}
	raw, _ := json.Marshal(queuedJob)
	fake := &fakeRedisQueue{
		keys: map[string][]string{
			"q": {string(raw)},
		},
	}
	db := &database.Database{Client: fake, MaxRetries: 0}
	ctx := context.WithValue(testContextWithLogger(), config.ContextKey("database"), db)

	// Nil RunID must update without panicking.
	queuedJob.WorkflowJob.Status = gogithub.Ptr("completed")
	if err := UpdateJobInDB(ctx, "q", &queuedJob); err != nil {
		t.Fatalf("UpdateJobInDB() error = %v", err)
	}

	// Second update against completed head must return ErrJobAlreadyCompleted.
	err := UpdateJobInDB(ctx, "q", &queuedJob)
	if !errors.Is(err, ErrJobAlreadyCompleted) {
		t.Fatalf("UpdateJobInDB() error = %v, want ErrJobAlreadyCompleted", err)
	}
}
