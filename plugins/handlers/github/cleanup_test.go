package github

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogithub "github.com/google/go-github/v75/github"
	internalAnka "github.com/veertuinc/anklet/internal/anka"
	internalGithub "github.com/veertuinc/anklet/internal/github"
)

func installFakeAnkaOK(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	callLog := filepath.Join(dir, "anka.calls")
	script := "#!/usr/bin/env bash\necho \"$*\" >> \"" + callLog + "\"\necho '{\"status\":\"OK\",\"code\":0,\"body\":{},\"message\":\"ok\"}'\n"
	if err := os.WriteFile(filepath.Join(dir, "anka"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return callLog
}

func TestCleanupDeletesVMAndCompletedJob(t *testing.T) {
	callLog := installFakeAnkaOK(t)
	h, ctx, fake := newTestHandlerAndCtx()
	h.deps.AnkaCLI = &internalAnka.Cli{Version: "3.9.0"}
	h.deps.Metrics.IncrementTotalRunningVMs(ctx, ctx)

	queues := buildQueueNames(h.deps.PluginConfig)
	state := newHandlerState()

	completedJob := internalGithub.QueueJob{
		Type: "WorkflowJobPayload",
		WorkflowJob: internalGithub.SimplifiedWorkflowJob{
			ID:     gogithub.Ptr(int64(701)),
			RunID:  gogithub.Ptr(int64(801)),
			Status: gogithub.Ptr("completed"),
		},
	}
	vmEntry := internalGithub.QueueJob{
		Type:        "anka.VM",
		AnkaVM:      internalAnka.VM{Name: "anklet-vm-test-1"},
		WorkflowJob: completedJob.WorkflowJob,
	}
	jobRaw, _ := json.Marshal(completedJob)
	vmRaw, _ := json.Marshal(vmEntry)

	fake.lists[queues.pluginQueue] = []string{string(jobRaw), string(vmRaw)}
	fake.lists[queues.mainInProgress] = []string{string(jobRaw)}
	fake.lists[queues.mainCompleted] = []string{string(jobRaw)}
	fake.lists[queues.pluginCompleted] = []string{string(jobRaw)}

	h.cleanup(ctx, ctx, queues, state, false)

	calls, _ := os.ReadFile(callLog)
	if !strings.Contains(string(calls), "delete --yes anklet-vm-test-1") {
		t.Fatalf("expected VM delete call, got:\n%s", string(calls))
	}
	if snap := h.deps.Metrics.Snapshot(); snap.TotalRunningVMs != 0 {
		t.Fatalf("TotalRunningVMs = %d, want 0 after cleanup", snap.TotalRunningVMs)
	}
	if len(fake.lists[queues.pluginQueue]) != 0 ||
		len(fake.lists[queues.mainInProgress]) != 0 ||
		len(fake.lists[queues.mainCompleted]) != 0 ||
		len(fake.lists[queues.pluginCompleted]) != 0 {
		t.Fatalf("expected all queues cleared after completed cleanup, got %+v", fake.lists)
	}
}

func TestCleanupOnStartRunRecoversStuckCleaningVM(t *testing.T) {
	callLog := installFakeAnkaOK(t)
	h, ctx, fake := newTestHandlerAndCtx()
	h.deps.AnkaCLI = &internalAnka.Cli{Version: "3.9.0"}
	queues := buildQueueNames(h.deps.PluginConfig)
	state := newHandlerState()

	queuedJob := internalGithub.QueueJob{
		Type:        "WorkflowJobPayload",
		WorkflowJob: internalGithub.SimplifiedWorkflowJob{ID: gogithub.Ptr(int64(702)), Status: gogithub.Ptr("queued")},
	}
	vmEntry := internalGithub.QueueJob{
		Type:   "anka.VM",
		AnkaVM: internalAnka.VM{Name: "anklet-vm-orphan"},
	}
	jobRaw, _ := json.Marshal(queuedJob)
	vmRaw, _ := json.Marshal(vmEntry)

	fake.lists[queues.pluginQueue] = []string{string(jobRaw)}
	fake.lists[queues.pluginQueue+"/cleaning"] = []string{string(vmRaw)}

	h.cleanup(ctx, ctx, queues, state, true)

	calls, _ := os.ReadFile(callLog)
	if !strings.Contains(string(calls), "delete --yes anklet-vm-orphan") {
		t.Fatalf("expected orphaned /cleaning VM to be deleted, calls:\n%s", string(calls))
	}
	if len(fake.lists[queues.pluginQueue+"/cleaning"]) != 0 {
		t.Fatalf("expected /cleaning cleared")
	}
	// onStartRun must stop once /cleaning is drained without popping pluginQueue.
	if len(fake.lists[queues.pluginQueue]) != 1 {
		t.Fatalf("onStartRun should preserve active pluginQueue item, got %d", len(fake.lists[queues.pluginQueue]))
	}
}

func TestCleanupReturnToMainQueueAttemptRules(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		reason       string
		wantAttempts int
	}{
		{name: "transient error increments attempts", reason: "error_executing_install_runner_bash", wantAttempts: 1},
		{name: "not_enough_host_resources does not increment attempts", reason: "not_enough_host_resources", wantAttempts: 0},
		{name: "pull_already_running_on_host does not increment attempts", reason: "pull_already_running_on_host", wantAttempts: 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, ctx, fake := newTestHandlerAndCtx()
			queues := buildQueueNames(h.deps.PluginConfig)
			state := newHandlerState()
			state.requestReturnToMainQueue(tc.reason)

			job := internalGithub.QueueJob{
				Type:        "WorkflowJobPayload",
				WorkflowJob: internalGithub.SimplifiedWorkflowJob{ID: gogithub.Ptr(int64(703)), Status: gogithub.Ptr("queued")},
				Action:      "preparing",
				Attempts:    0,
			}
			raw, _ := json.Marshal(job)
			fake.lists[queues.pluginQueue] = []string{string(raw)}

			h.cleanup(ctx, ctx, queues, state, false)

			if len(fake.lists[queues.mainQueue]) != 1 {
				t.Fatalf("expected job pushed back to mainQueue, got %+v", fake.lists)
			}
			var returned internalGithub.QueueJob
			if err := json.Unmarshal([]byte(fake.lists[queues.mainQueue][0]), &returned); err != nil {
				t.Fatal(err)
			}
			if returned.Action != "queued" || returned.Attempts != tc.wantAttempts {
				t.Fatalf("returned job = (action=%q, attempts=%d), want (queued, %d)", returned.Action, returned.Attempts, tc.wantAttempts)
			}
		})
	}
}

func TestCleanupSkipsInProgressAndHandlesPausedTakenByOtherHost(t *testing.T) {
	t.Parallel()

	t.Run("skips in_progress job", func(t *testing.T) {
		h, ctx, fake := newTestHandlerAndCtx()
		queues := buildQueueNames(h.deps.PluginConfig)
		state := newHandlerState()
		job := internalGithub.QueueJob{
			Type:        "WorkflowJobPayload",
			WorkflowJob: internalGithub.SimplifiedWorkflowJob{ID: gogithub.Ptr(int64(704)), Status: gogithub.Ptr("in_progress")},
		}
		raw, _ := json.Marshal(job)
		fake.lists[queues.pluginQueue] = []string{string(raw)}

		h.cleanup(ctx, ctx, queues, state, false)
		if len(fake.lists[queues.pluginQueue]) != 1 {
			t.Fatalf("in_progress job must not be removed during cleanup")
		}
	})

	t.Run("paused job taken by other host removes paused entry only", func(t *testing.T) {
		h, ctx, fake := newTestHandlerAndCtx()
		queues := buildQueueNames(h.deps.PluginConfig)
		state := newHandlerState()
		state.pausedTakenByOtherHost.Store(true)
		job := internalGithub.QueueJob{
			Type:        "WorkflowJobPayload",
			WorkflowJob: internalGithub.SimplifiedWorkflowJob{ID: gogithub.Ptr(int64(705)), Status: gogithub.Ptr("queued")},
		}
		raw, _ := json.Marshal(job)
		fake.lists[queues.pluginQueue] = []string{string(raw)}
		fake.lists[queues.paused] = []string{string(raw)}

		h.cleanup(ctx, ctx, queues, state, false)
		if len(fake.lists[queues.paused]) != 0 || len(fake.lists[queues.mainQueue]) != 0 {
			t.Fatalf("expected paused removed and mainQueue empty, got %+v", fake.lists)
		}
	})
}
