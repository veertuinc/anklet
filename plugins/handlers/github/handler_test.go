package github

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v75/github"
	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/metrics"
	"github.com/veertuinc/anklet/internal/plugin"
)

type fakeHandlerRedis struct {
	redis.Cmdable
	mu    sync.Mutex
	lists map[string][]string
}

func newFakeHandlerRedis() *fakeHandlerRedis {
	return &fakeHandlerRedis{lists: make(map[string][]string)}
}

func (f *fakeHandlerRedis) LIndex(_ context.Context, key string, index int64) *redis.StringCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewStringCmd(context.Background())
	list := f.lists[key]
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

func (f *fakeHandlerRedis) LLen(_ context.Context, key string) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewIntCmd(context.Background())
	cmd.SetVal(int64(len(f.lists[key])))
	return cmd
}

func (f *fakeHandlerRedis) LRange(_ context.Context, key string, _, _ int64) *redis.StringSliceCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewStringSliceCmd(context.Background())
	cmd.SetVal(append([]string(nil), f.lists[key]...))
	return cmd
}

func (f *fakeHandlerRedis) LPush(_ context.Context, key string, values ...any) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewIntCmd(context.Background())
	for _, v := range values {
		switch tv := v.(type) {
		case []byte:
			f.lists[key] = append([]string{string(tv)}, f.lists[key]...)
		case string:
			f.lists[key] = append([]string{tv}, f.lists[key]...)
		}
	}
	cmd.SetVal(int64(len(f.lists[key])))
	return cmd
}

func (f *fakeHandlerRedis) LRem(_ context.Context, key string, _ int64, value any) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewIntCmd(context.Background())
	target := ""
	switch tv := value.(type) {
	case []byte:
		target = string(tv)
	case string:
		target = tv
	}
	list := f.lists[key]
	for i, item := range list {
		if item == target {
			f.lists[key] = append(list[:i], list[i+1:]...)
			cmd.SetVal(1)
			return cmd
		}
	}
	cmd.SetVal(0)
	return cmd
}

func (f *fakeHandlerRedis) RPush(_ context.Context, key string, values ...any) *redis.IntCmd {
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

func (f *fakeHandlerRedis) RPop(_ context.Context, key string) *redis.StringCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewStringCmd(context.Background())
	list := f.lists[key]
	if len(list) == 0 {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	val := list[len(list)-1]
	f.lists[key] = list[:len(list)-1]
	cmd.SetVal(val)
	return cmd
}

func (f *fakeHandlerRedis) RPopLPush(_ context.Context, source, destination string) *redis.StringCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewStringCmd(context.Background())
	list := f.lists[source]
	if len(list) == 0 {
		cmd.SetErr(redis.Nil)
		return cmd
	}
	val := list[len(list)-1]
	f.lists[source] = list[:len(list)-1]
	f.lists[destination] = append([]string{val}, f.lists[destination]...)
	cmd.SetVal(val)
	return cmd
}

func (f *fakeHandlerRedis) Del(_ context.Context, keys ...string) *redis.IntCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewIntCmd(context.Background())
	var deleted int64
	for _, k := range keys {
		if _, ok := f.lists[k]; ok {
			delete(f.lists, k)
			deleted++
		}
	}
	cmd.SetVal(deleted)
	return cmd
}

func (f *fakeHandlerRedis) LSet(_ context.Context, key string, index int64, value any) *redis.StatusCmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	cmd := redis.NewStatusCmd(context.Background())
	if int(index) < len(f.lists[key]) {
		switch tv := value.(type) {
		case []byte:
			f.lists[key][index] = string(tv)
		case string:
			f.lists[key][index] = tv
		}
	}
	cmd.SetVal("OK")
	return cmd
}

func newTestHandlerAndCtx() (*Handler, context.Context, *fakeHandlerRedis) {
	fake := newFakeHandlerRedis()
	db := &database.Database{Client: fake, MaxRetries: 0}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	globals := &config.Globals{
		Plugins: map[string]map[string]*config.PluginGlobal{
			"github": {
				"H1": {},
			},
		},
	}
	m := &metrics.MetricsDataLock{}
	_ = m.AddPlugin(metrics.Plugin{
		PluginBase: metrics.PluginBase{Name: "H1", PluginName: "github", Status: "idle"},
	})
	deps := plugin.Deps{
		Logger:       logger,
		PluginConfig: config.Plugin{Name: "H1", Plugin: "github", Owner: "veertuinc"},
		Globals:      globals,
		Metrics:      m,
		Database:     db,
	}
	ctx := deps.WithContext(context.Background())
	return NewHandler(deps), ctx, fake
}

func TestCheckForCompletedJobsExitsOnWatcherCancelWithoutDeadlock(t *testing.T) {
	t.Parallel()

	h, ctx, _ := newTestHandlerAndCtx()
	queues := buildQueueNames(h.deps.PluginConfig)
	state := newHandlerState()
	watcherCtx, stopWatcher := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.checkForCompletedJobs(watcherCtx, ctx, ctx, queues, state)
	}()

	if err := h.waitForFirstWatcherPass(ctx, ctx, state); err != nil {
		t.Fatalf("waitForFirstWatcherPass() error = %v", err)
	}
	stopWatcher()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("checkForCompletedJobs did not exit promptly after stopWatcher()")
	}
}

func TestClaimPausedJobRequeuesOnceWithoutDuplicateDeferBug(t *testing.T) {
	t.Parallel()

	h, ctx, fake := newTestHandlerAndCtx()
	queues := buildQueueNames(h.deps.PluginConfig)

	// Two jobs already paused on this same host (H1) -> neither can be claimed,
	// and each must be returned exactly once (no duplicate defer pushes).
	job1, _ := json.Marshal(internalGithub.QueueJob{
		Type:        "WorkflowJobPayload",
		WorkflowJob: internalGithub.SimplifiedWorkflowJob{ID: gogithub.Ptr(int64(101))},
		PausedOn:    "H1",
	})
	job2, _ := json.Marshal(internalGithub.QueueJob{
		Type:        "WorkflowJobPayload",
		WorkflowJob: internalGithub.SimplifiedWorkflowJob{ID: gogithub.Ptr(int64(102))},
		PausedOn:    "H1",
	})
	fake.lists[queues.paused] = []string{string(job1), string(job2)}

	_, _, _, claimed, err := h.claimPausedJob(ctx, ctx, queues)
	if err != nil {
		t.Fatalf("claimPausedJob() error = %v", err)
	}
	if claimed {
		t.Fatal("expected claimed = false when jobs are paused on same host")
	}
	if got := len(fake.lists[queues.paused]); got != 2 {
		t.Fatalf("paused queue len = %d, want 2 (no duplicates or losses)", got)
	}
}

func TestExtractLabelValueAndWatchJobHelpers(t *testing.T) {
	t.Parallel()

	labels := []string{"self-hosted", "anka-template:uuid-1", "anka-template-tag:v2"}
	if got := extractLabelValue(labels, "anka-template:"); got != "uuid-1" {
		t.Errorf("template = %q, want uuid-1", got)
	}
	if got := extractLabelValue(labels, "anka-template-tag:"); got != "v2" {
		t.Errorf("tag = %q, want v2", got)
	}
	if got := extractLabelValue(labels, "missing:"); got != "" {
		t.Errorf("missing = %q, want empty", got)
	}
	if watchJobIsTerminal(nil) {
		t.Error("nil status should not be terminal")
	}
	if !watchJobIsTerminal(gogithub.Ptr("completed")) || !watchJobIsTerminal(gogithub.Ptr("timed_out")) {
		t.Error("completed and timed_out should be terminal")
	}

	// Nil Conclusion / HTMLURL must not panic.
	h, ctx, _ := newTestHandlerAndCtx()
	_, err := handleWatchJobCompleted(ctx, ctx, h.deps.PluginConfig, h.deps.Metrics, internalGithub.QueueJob{
		WorkflowJob: internalGithub.SimplifiedWorkflowJob{
			ID:         gogithub.Ptr(int64(1)),
			Conclusion: gogithub.Ptr("success"),
			HTMLURL:    nil,
		},
	})
	if err != nil {
		t.Fatalf("handleWatchJobCompleted() error = %v", err)
	}
}
