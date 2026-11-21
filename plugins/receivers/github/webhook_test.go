package github

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v75/github"
	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	internalGithub "github.com/veertuinc/anklet/internal/github"
)

type fakeReceiverRedis struct {
	redis.Cmdable
	mu    sync.Mutex
	lists map[string][]string
}

func newFakeReceiverRedis() *fakeReceiverRedis {
	return &fakeReceiverRedis{lists: make(map[string][]string)}
}

func (f *fakeReceiverRedis) Exists(_ context.Context, keys ...string) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewIntCmd(context.Background())
	var n int64
	for _, k := range keys {
		if len(f.lists[k]) > 0 {
			n++
		}
	}
	cmd.SetVal(n)
	return cmd
}

func (f *fakeReceiverRedis) Scan(_ context.Context, cursor uint64, match string, _ int64) *redis.ScanCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewScanCmd(context.Background(), nil)
	if cursor != 0 {
		cmd.SetVal(nil, 0)
		return cmd
	}
	prefix := strings.TrimSuffix(match, "*")
	var out []string
	for k := range f.lists {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	cmd.SetVal(out, 0)
	return cmd
}

func (f *fakeReceiverRedis) LRange(_ context.Context, key string, _, _ int64) *redis.StringSliceCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewStringSliceCmd(context.Background())
	cmd.SetVal(append([]string(nil), f.lists[key]...))
	return cmd
}

func (f *fakeReceiverRedis) LIndex(_ context.Context, key string, index int64) *redis.StringCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewStringCmd(context.Background())
	list := f.lists[key]
	if index < 0 || index >= int64(len(list)) {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	cmd.SetVal(list[index])
	return cmd
}

func (f *fakeReceiverRedis) RPush(_ context.Context, key string, values ...any) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewIntCmd(context.Background())
	for _, v := range values {
		switch tv := v.(type) {
		case []byte:
			f.lists[key] = append(f.lists[key], string(tv))
		case string:
			f.lists[key] = append(f.lists[key], tv)
		}
	}
	cmd.SetVal(int64(len(f.lists[key])))
	return cmd
}

func (f *fakeReceiverRedis) LRem(_ context.Context, key string, _ int64, value any) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewIntCmd(context.Background())
	target := value.(string)
	for i, item := range f.lists[key] {
		if item == target {
			f.lists[key] = append(f.lists[key][:i], f.lists[key][i+1:]...)
			cmd.SetVal(1)
			return cmd
		}
	}
	cmd.SetVal(0)
	return cmd
}

func TestReceiverWebhookAllActionsAndCrossOrgIsolation(t *testing.T) {
	t.Parallel()

	fake := newFakeReceiverRedis()
	// Pre-populate a sibling org queue that shares the "org" prefix.
	fake.lists["anklet/jobs/github/queued/org-prod"] = []string{
		`{"type":"WorkflowJobPayload","workflow_job":{"id":777,"run_id":1,"labels":["anka-template:mac14"]}}`,
	}
	// Pre-populate a handler sub-queue with job 202.
	fake.lists["anklet/jobs/github/queued/org/{H1}"] = []string{
		`{"type":"WorkflowJobPayload","workflow_job":{"id":202,"run_id":2,"labels":["anka-template:mac14"]}}`,
	}

	db := &database.Database{Client: fake, MaxRetries: 0}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.WithValue(context.Background(), config.ContextKey("logger"), logger)
	ctx = context.WithValue(ctx, config.ContextKey("database"), db)
	mux := newReceiverMux(ctx, config.Plugin{Secret: "secret"}, db, "org")

	post := func(body string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, signedReceiverRequest(t, body))
		return rec.Code
	}

	// 1. New queued job -> pushed.
	q1 := `{"action":"queued","workflow_job":{"id":101,"run_id":10,"labels":["anka-template:mac14"]},"repository":{"name":"r","owner":{"login":"org"}}}`
	if code := post(q1); code != http.StatusOK {
		t.Fatalf("queued 101 status = %d", code)
	}
	if len(fake.lists["anklet/jobs/github/queued/org"]) != 1 {
		t.Fatalf("expected 1 item in queued/org, got %d", len(fake.lists["anklet/jobs/github/queued/org"]))
	}

	// 2. Duplicate queued job in main queue -> skipped.
	if code := post(q1); code != http.StatusOK || len(fake.lists["anklet/jobs/github/queued/org"]) != 1 {
		t.Fatalf("duplicate main queued job was not skipped")
	}

	// 3. Duplicate queued job already in handler sub-queue (202) -> skipped.
	q202 := `{"action":"queued","workflow_job":{"id":202,"run_id":2,"labels":["anka-template:mac14"]},"repository":{"name":"r","owner":{"login":"org"}}}`
	if code := post(q202); code != http.StatusOK || len(fake.lists["anklet/jobs/github/queued/org"]) != 1 {
		t.Fatalf("duplicate handler-queued job was not skipped")
	}

	// 4. in_progress job stored; cancelled in_progress skipped.
	ip1 := `{"action":"in_progress","workflow_job":{"id":101,"run_id":10,"labels":["anka-template:mac14"]}}`
	ipCancelled := `{"action":"in_progress","workflow_job":{"id":105,"run_id":11,"conclusion":"cancelled","labels":["anka-template:mac14"]}}`
	if post(ip1) != http.StatusOK {
		t.Fatal("first in_progress webhook failed")
	}
	if post(ip1) != http.StatusOK {
		t.Fatal("duplicate in_progress webhook failed")
	}
	if post(ipCancelled) != http.StatusOK {
		t.Fatal("cancelled in_progress webhook failed")
	}
	if len(fake.lists["anklet/jobs/github/in_progress/org"]) != 1 {
		t.Fatalf("expected 1 item in in_progress/org, got %d", len(fake.lists["anklet/jobs/github/in_progress/org"]))
	}

	// 5. completed for job 101 (present in queued/org) -> stored in completed/org.
	comp101 := `{"action":"completed","workflow_job":{"id":101,"run_id":10,"labels":["anka-template:mac14"]}}`
	if post(comp101) != http.StatusOK || len(fake.lists["anklet/jobs/github/completed/org"]) != 1 {
		t.Fatalf("completed 101 was not enqueued")
	}

	// 6. completed for job 777 (ONLY in sibling org-prod) -> MUST NOT match org!
	comp777 := `{"action":"completed","workflow_job":{"id":777,"run_id":1,"labels":["anka-template:mac14"]}}`
	if post(comp777) != http.StatusOK || len(fake.lists["anklet/jobs/github/completed/org"]) != 1 {
		t.Fatalf("cross-org job 777 from org-prod leaked into org completed queue")
	}
}

func TestRunRedeliveryWalkEndToEnd(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	oldTime := now.Add(-48 * time.Hour)

	var redeliveredIDs []string
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4900")

		switch {
		case r.URL.Path == "/rate_limit":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"resources": map[string]any{
					"core": map[string]any{"limit": 5000, "remaining": 4000, "reset": now.Add(time.Hour).Unix()},
				},
			})
		case r.URL.Path == "/orgs/veertuinc/hooks/55/deliveries" && r.Method == http.MethodGet:
			// Deliveries:
			// 1001: queued, orphaned, should redeliver (will succeed)
			// 1002: queued, already completed on GitHub -> skip
			// 1003: queued, no anka-template label -> skip
			// 1004: completed, stale in completed queue only -> remove from Redis, no redeliver
			// 1005: queued, failed redeliver POST -> logged and skipped cleanly
			// 1006: older than RedeliverHours (48h) -> stops walk
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1005, "guid": "g5", "action": "queued", "status_code": 502, "redelivery": false, "delivered_at": now.Format(time.RFC3339)},
				{"id": 1004, "guid": "g4", "action": "completed", "status_code": 502, "redelivery": false, "delivered_at": now.Format(time.RFC3339)},
				{"id": 1003, "guid": "g3", "action": "queued", "status_code": 500, "redelivery": false, "delivered_at": now.Format(time.RFC3339)},
				{"id": 1002, "guid": "g2", "action": "queued", "status_code": 500, "redelivery": false, "delivered_at": now.Format(time.RFC3339)},
				{"id": 1001, "guid": "g1", "action": "queued", "status_code": 500, "redelivery": false, "delivered_at": now.Format(time.RFC3339)},
				{"id": 1006, "guid": "g6", "action": "queued", "status_code": 500, "redelivery": false, "delivered_at": oldTime.Format(time.RFC3339)},
			})
		case r.URL.Path == "/orgs/veertuinc/hooks/55/deliveries/1001":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 1001, "request": map[string]any{
					"payload": map[string]any{
						"action": "queued",
						"workflow_job": map[string]any{
							"id": 11, "run_id": 1, "labels": []string{"anka-template:mac14"},
						},
						"repository": map[string]any{"name": "anklet", "owner": map[string]any{"login": "veertuinc"}},
					},
				},
			})
		case r.URL.Path == "/orgs/veertuinc/hooks/55/deliveries/1002":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 1002, "request": map[string]any{
					"payload": map[string]any{
						"action": "queued",
						"workflow_job": map[string]any{
							"id": 12, "run_id": 2, "labels": []string{"anka-template:mac14"},
						},
						"repository": map[string]any{"name": "anklet", "owner": map[string]any{"login": "veertuinc"}},
					},
				},
			})
		case r.URL.Path == "/orgs/veertuinc/hooks/55/deliveries/1003":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 1003, "request": map[string]any{
					"payload": map[string]any{
						"action": "queued",
						"workflow_job": map[string]any{
							"id": 13, "run_id": 3, "labels": []string{"ubuntu-latest"},
						},
						"repository": map[string]any{"name": "anklet", "owner": map[string]any{"login": "veertuinc"}},
					},
				},
			})
		case r.URL.Path == "/orgs/veertuinc/hooks/55/deliveries/1004":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 1004, "request": map[string]any{
					"payload": map[string]any{
						"action": "completed",
						"workflow_job": map[string]any{
							"id": 14, "run_id": 4, "labels": []string{"anka-template:mac14"},
						},
						"repository": map[string]any{"name": "anklet", "owner": map[string]any{"login": "veertuinc"}},
					},
				},
			})
		case r.URL.Path == "/orgs/veertuinc/hooks/55/deliveries/1005":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": 1005, "request": map[string]any{
					"payload": map[string]any{
						"action": "queued",
						"workflow_job": map[string]any{
							"id": 15, "run_id": 5, "labels": []string{"anka-template:mac14"},
						},
						"repository": map[string]any{"name": "anklet", "owner": map[string]any{"login": "veertuinc"}},
					},
				},
			})
		case r.URL.Path == "/repos/veertuinc/anklet/actions/jobs/11":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 11, "status": "queued"})
		case r.URL.Path == "/repos/veertuinc/anklet/actions/jobs/12":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 12, "status": "completed", "conclusion": "success"})
		case r.URL.Path == "/repos/veertuinc/anklet/actions/jobs/15":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 15, "status": "queued"})
		case r.URL.Path == "/orgs/veertuinc/hooks/55/deliveries/1001/attempts" && r.Method == http.MethodPost:
			mu.Lock()
			redeliveredIDs = append(redeliveredIDs, "1001")
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/orgs/veertuinc/hooks/55/deliveries/1005/attempts" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"server error"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	ghClient := gogithub.NewClient(srv.Client())
	u, _ := url.Parse(srv.URL + "/")
	ghClient.BaseURL = u

	fake := newFakeReceiverRedis()
	// Stale completed job 14 with no queued entry -> walk must remove it.
	fake.lists["anklet/jobs/github/completed/veertuinc"] = []string{
		`{"type":"WorkflowJobPayload","workflow_job":{"id":14,"run_id":4,"labels":["anka-template:mac14"]}}`,
	}
	db := &database.Database{Client: fake, MaxRetries: 0}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	ctx = context.WithValue(ctx, config.ContextKey("logger"), logger)
	ctx = context.WithValue(ctx, config.ContextKey("database"), db)
	ctx = context.WithValue(ctx, config.ContextKey("githubwrapperclient"), internalGithub.NewGitHubClientWrapper(ghClient))

	// Cancel right after walk enters the post-redelivery 1-minute sleep so the test finishes fast.
	go func() {
		for range 50 {
			mu.Lock()
			done := len(redeliveredIDs) > 0
			mu.Unlock()
			if done {
				time.Sleep(100 * time.Millisecond)
				cancel()
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	err := runRedeliveryWalk(ctx, ctx, ghClient, config.Plugin{
		Owner:          "veertuinc",
		HookID:         55,
		RedeliverHours: 24,
	}, "veertuinc")
	if err != nil && err != context.Canceled {
		t.Fatalf("runRedeliveryWalk() error = %v", err)
	}

	mu.Lock()
	gotRedelivered := append([]string(nil), redeliveredIDs...)
	mu.Unlock()
	if len(gotRedelivered) != 1 || gotRedelivered[0] != "1001" {
		t.Fatalf("redelivered = %v, want [1001]", gotRedelivered)
	}
	if len(fake.lists["anklet/jobs/github/completed/veertuinc"]) != 0 {
		t.Fatalf("expected stale completed job 14 to be removed from Redis")
	}
}
