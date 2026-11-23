package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	gogithub "github.com/google/go-github/v75/github"
	internalAnka "github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	internalGithub "github.com/veertuinc/anklet/internal/github"
)

func TestCheckForCompletedJobsMovesCompletedAndUpdatesInProgress(t *testing.T) {
	t.Parallel()

	h, ctx, fake := newTestHandlerAndCtx()
	queues := buildQueueNames(h.deps.PluginConfig)
	state := newHandlerState()

	queued := internalGithub.QueueJob{
		Type: "WorkflowJobPayload",
		WorkflowJob: internalGithub.SimplifiedWorkflowJob{
			ID:      gogithub.Ptr(int64(801)),
			RunID:   gogithub.Ptr(int64(901)),
			Status:  gogithub.Ptr("queued"),
			HTMLURL: gogithub.Ptr("https://github.com/veertuinc/anklet/job/801"),
		},
	}
	completed := queued
	completed.WorkflowJob.Status = gogithub.Ptr("completed")
	completed.WorkflowJob.Conclusion = gogithub.Ptr("success")

	qRaw, _ := json.Marshal(queued)
	cRaw, _ := json.Marshal(completed)
	fake.lists[queues.pluginQueue] = []string{string(qRaw)}
	fake.lists[queues.mainCompleted] = []string{string(cRaw)}

	watcherCtx, stopWatcher := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.checkForCompletedJobs(watcherCtx, ctx, ctx, queues, state)
	}()

	select {
	case ev := <-state.jobEvents:
		if statusString(ev.WorkflowJob.Status) != "completed" {
			t.Fatalf("event status = %q, want completed", statusString(ev.WorkflowJob.Status))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for completed event from checkForCompletedJobs")
	}

	stopWatcher()
	<-done

	if len(fake.lists[queues.mainCompleted]) != 0 || len(fake.lists[queues.pluginCompleted]) != 1 {
		t.Fatalf("expected completed moved from mainCompleted to pluginCompleted: %+v", fake.lists)
	}
}

func TestWatchJobStatusHandlesCompletionAndRegistrationTimeout(t *testing.T) {
	t.Parallel()

	t.Run("terminal event completes watch and updates metrics", func(t *testing.T) {
		h, ctx, fake := newTestHandlerAndCtx()
		queues := buildQueueNames(h.deps.PluginConfig)
		state := newHandlerState()

		job := internalGithub.QueueJob{
			Type: "WorkflowJobPayload",
			WorkflowJob: internalGithub.SimplifiedWorkflowJob{
				ID:         gogithub.Ptr(int64(810)),
				RunID:      gogithub.Ptr(int64(910)),
				Status:     gogithub.Ptr("queued"),
				Conclusion: gogithub.Ptr("success"),
				HTMLURL:    gogithub.Ptr("https://github.com/veertuinc/anklet/job/810"),
			},
		}
		raw, _ := json.Marshal(job)
		fake.lists[queues.pluginQueue] = []string{string(raw)}

		doneJob := job
		doneJob.WorkflowJob.Status = gogithub.Ptr("completed")
		state.publishEvent(doneJob)

		if _, err := h.watchJobStatus(ctx, ctx, queues, state); err != nil {
			t.Fatalf("watchJobStatus() error = %v", err)
		}
		snap := h.deps.Metrics.Snapshot()
		if snap.TotalSuccessfulRunsSinceStart != 1 || snap.Plugins[0].LastSuccessfulRunJobUrl != "https://github.com/veertuinc/anklet/job/810" {
			t.Fatalf("metrics not updated on completion: %+v", snap)
		}
	})

	t.Run("registration timeout triggers remove_self_hosted_runner and retry", func(t *testing.T) {
		h, ctx, fake := newTestHandlerAndCtx()
		// RegistrationTimeoutSeconds=1 => 1/3 == 0 so timeout fires on first poll (logCounter == 0).
		h.deps.PluginConfig.RegistrationTimeoutSeconds = 1
		queues := buildQueueNames(h.deps.PluginConfig)
		state := newHandlerState()

		job := internalGithub.QueueJob{
			Type: "WorkflowJobPayload",
			WorkflowJob: internalGithub.SimplifiedWorkflowJob{
				ID:     gogithub.Ptr(int64(811)),
				RunID:  gogithub.Ptr(int64(911)),
				Status: gogithub.Ptr("queued"),
			},
		}
		raw, _ := json.Marshal(job)
		fake.lists[queues.pluginQueue] = []string{string(raw)}

		if _, err := h.watchJobStatus(ctx, ctx, queues, state); err != nil {
			t.Fatalf("watchJobStatus() error = %v", err)
		}
		if reason := state.takeReturnReason(); reason != "waiting_for_runner_registration_timed_out" {
			t.Fatalf("returnReason = %q, want waiting_for_runner_registration_timed_out", reason)
		}
		var updated internalGithub.QueueJob
		_ = json.Unmarshal([]byte(fake.lists[queues.pluginQueue][0]), &updated)
		if updated.Action != "remove_self_hosted_runner" {
			t.Fatalf("updated job Action = %q, want remove_self_hosted_runner", updated.Action)
		}
	})
}

func TestClaimNextJobOrgFilteringAndPausedTakeover(t *testing.T) {
	t.Parallel()

	t.Run("filters out foreign org job on shared queue_name", func(t *testing.T) {
		h, ctx, fake := newTestHandlerAndCtx()
		h.deps.PluginConfig.QueueName = "shared"
		queues := buildQueueNames(h.deps.PluginConfig)

		foreignJob := internalGithub.QueueJob{
			Type:        "WorkflowJobPayload",
			WorkflowJob: internalGithub.SimplifiedWorkflowJob{ID: gogithub.Ptr(int64(820))},
			Repository:  internalGithub.Repository{Owner: gogithub.Ptr("other-org"), Name: gogithub.Ptr("repo")},
		}
		raw, _ := json.Marshal(foreignJob)
		fake.lists[queues.mainQueue] = []string{string(raw)}

		_, _, claimed, err := h.claimNextJob(ctx, ctx, queues)
		if err != nil || claimed {
			t.Fatalf("claimNextJob(foreign org) = (claimed=%v, err=%v), want (false, nil)", claimed, err)
		}
		if h.deps.Globals.GetQueueTargetIndex() != 1 || len(fake.lists[queues.mainQueue]) != 1 {
			t.Fatalf("foreign org job should be pushed back and QueueTargetIndex incremented")
		}
	})

	t.Run("claims matching job from mainQueue and resumes from pluginQueue", func(t *testing.T) {
		h, ctx, fake := newTestHandlerAndCtx()
		queues := buildQueueNames(h.deps.PluginConfig)

		job := internalGithub.QueueJob{
			Type:        "WorkflowJobPayload",
			WorkflowJob: internalGithub.SimplifiedWorkflowJob{ID: gogithub.Ptr(int64(821))},
			Repository:  internalGithub.Repository{Owner: gogithub.Ptr("veertuinc"), Name: gogithub.Ptr("anklet")},
		}
		raw, _ := json.Marshal(job)
		fake.lists[queues.mainQueue] = []string{string(raw)}

		_, got, claimed, err := h.claimNextJob(ctx, ctx, queues)
		if err != nil || !claimed || got.WorkflowJob.ID == nil || *got.WorkflowJob.ID != 821 {
			t.Fatalf("claimNextJob() = (%+v, claimed=%v, err=%v)", got, claimed, err)
		}
		if len(fake.lists[queues.pluginQueue]) != 1 {
			t.Fatalf("expected job pushed into pluginQueue")
		}
	})

	t.Run("tryTakeOverPausedJob claims paused job from other host and rejects non-paused", func(t *testing.T) {
		h, ctx, fake := newTestHandlerAndCtx()
		h.deps.PluginConfig.SkipCPUAndMemoryResourceChecks = true
		queues := buildQueueNames(h.deps.PluginConfig)

		otherQueue := "anklet/jobs/github/queued/" + queues.owner + "/{OTHER_HOST}"
		pausedJob := internalGithub.QueueJob{
			Type:        "WorkflowJobPayload",
			WorkflowJob: internalGithub.SimplifiedWorkflowJob{ID: gogithub.Ptr(int64(822))},
			PausedOn:    "OTHER_HOST",
			Action:      "paused",
		}
		raw, _ := json.Marshal(pausedJob)
		fake.lists[otherQueue] = []string{string(raw)}

		claimed, _, _, got, err := h.tryTakeOverPausedJob(ctx, queues, string(raw))
		if err != nil || !claimed || *got.WorkflowJob.ID != 822 {
			t.Fatalf("tryTakeOverPausedJob() = (claimed=%v, got=%+v, err=%v)", claimed, got, err)
		}
		if len(fake.lists[otherQueue]) != 0 {
			t.Fatalf("expected job removed from other host queue")
		}

		// If original host already transitioned to in_progress, takeover must be rejected.
		runningJob := pausedJob
		runningJob.Action = "in_progress"
		runningRaw, _ := json.Marshal(runningJob)
		fake.lists[otherQueue] = []string{string(runningRaw)}
		claimed, _, _, _, err = h.tryTakeOverPausedJob(ctx, queues, string(raw))
		if err != nil || claimed {
			t.Fatalf("expected takeover rejected when original host is already running: claimed=%v err=%v", claimed, err)
		}
	})

	t.Run("waitForAvailableResources exits when another host claims paused job", func(t *testing.T) {
		h, ctx, fake := newTestHandlerAndCtx()
		queues := buildQueueNames(h.deps.PluginConfig)
		state := newHandlerState()
		job := internalGithub.QueueJob{
			Type:        "WorkflowJobPayload",
			WorkflowJob: internalGithub.SimplifiedWorkflowJob{ID: gogithub.Ptr(int64(823)), Status: gogithub.Ptr("queued")},
		}
		raw, _ := json.Marshal(job)
		fake.lists[queues.pluginQueue] = []string{string(raw)}

		go func() {
			time.Sleep(100 * time.Millisecond)
			fake.mu.Lock()
			fake.lists[queues.pluginQueue] = nil
			fake.mu.Unlock()
		}()

		canContinue, err := h.waitForAvailableResources(ctx, ctx, queues, state, &job)
		if err != nil || canContinue || !state.pausedTakenByOtherHost.Load() {
			t.Fatalf("waitForAvailableResources() = (%v, %v), taken=%v", canContinue, err, state.pausedTakenByOtherHost.Load())
		}
	})
}

func TestRunnerRegistrationCancelAndOrphanRemoval(t *testing.T) {
	t.Parallel()

	t.Run("createRegistrationToken routes by repo, org, and enterprise scope", func(t *testing.T) {
		scopes := []struct {
			name     string
			cfg      config.Plugin
			wantPath string
		}{
			{"repo", config.Plugin{Name: "H1", Owner: "veertuinc", Repo: "anklet"}, "/repos/veertuinc/anklet/actions/runners/registration-token"},
			{"org", config.Plugin{Name: "H1", Owner: "veertuinc"}, "/orgs/veertuinc/actions/runners/registration-token"},
			{"enterprise", config.Plugin{Name: "H1", Enterprise: "veertu-inc"}, "/enterprises/veertu-inc/actions/runners/registration-token"},
		}
		for _, sc := range scopes {
			t.Run(sc.name, func(t *testing.T) {
				gotPath := ""
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					gotPath = r.URL.Path
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"token":"REG_TOK_123"}`))
				}))
				defer srv.Close()
				ghClient := gogithub.NewClient(srv.Client())
				u, _ := url.Parse(srv.URL + "/")
				ghClient.BaseURL = u

				h, ctx, _ := newTestHandlerAndCtx()
				h.deps.PluginConfig = sc.cfg
				_, tok, _, err := h.createRegistrationToken(ctx, ctx, ghClient)
				if err != nil || tok == nil || tok.GetToken() != "REG_TOK_123" || gotPath != sc.wantPath {
					t.Fatalf("got (tok=%v, path=%q, err=%v), want (REG_TOK_123, %q)", tok, gotPath, err, sc.wantPath)
				}
			})
		}
	})

	t.Run("removeSelfHostedRunner cancels busy runner and retries deletion", func(t *testing.T) {
		var deleteCalls atomic.Int32
		var cancelCalled atomic.Bool

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.URL.Path == "/orgs/veertuinc/actions/runners" && r.Method == http.MethodGet:
				_, _ = w.Write([]byte(`{"total_count":1,"runners":[{"id":99,"name":"anklet-vm-orphan"}]}`))
			case r.URL.Path == "/orgs/veertuinc/actions/runners/99" && r.Method == http.MethodDelete:
				if deleteCalls.Add(1) == 1 {
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = w.Write([]byte(`{"message":"Runner is currently running a job and cannot be deleted."}`))
					return
				}
				w.WriteHeader(http.StatusNoContent)
			case r.URL.Path == "/repos/veertuinc/anklet/actions/runs/555" && r.Method == http.MethodGet:
				if cancelCalled.Load() {
					_, _ = w.Write([]byte(`{"id":555,"status":"completed","conclusion":"cancelled"}`))
					return
				}
				_, _ = w.Write([]byte(`{"id":555,"status":"in_progress"}`))
			case r.URL.Path == "/repos/veertuinc/anklet/actions/runs/555/cancel" && r.Method == http.MethodPost:
				cancelCalled.Store(true)
				w.WriteHeader(http.StatusAccepted)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer srv.Close()
		ghClient := gogithub.NewClient(srv.Client())
		u, _ := url.Parse(srv.URL + "/")
		ghClient.BaseURL = u

		h, ctx, fake := newTestHandlerAndCtx()
		queues := buildQueueNames(h.deps.PluginConfig)
		job := internalGithub.QueueJob{
			Type:   "WorkflowJobPayload",
			Action: "remove_self_hosted_runner",
			AnkaVM: internalAnka.VM{Name: "anklet-vm-orphan"},
			WorkflowJob: internalGithub.SimplifiedWorkflowJob{
				ID:      gogithub.Ptr(int64(830)),
				RunID:   gogithub.Ptr(int64(555)),
				HTMLURL: gogithub.Ptr("https://github.com/veertuinc/anklet/job/830"),
			},
			Repository: internalGithub.Repository{Owner: gogithub.Ptr("veertuinc"), Name: gogithub.Ptr("anklet")},
		}
		raw, _ := json.Marshal(job)
		fake.lists[queues.pluginQueue] = []string{string(raw)}

		ctx = context.WithValue(ctx, config.ContextKey("githubwrapperclient"), internalGithub.NewGitHubClientWrapper(ghClient))
		h.removeSelfHostedRunner(ctx, ctx, queues.pluginQueue)

		if deleteCalls.Load() != 2 || !cancelCalled.Load() {
			t.Fatalf("expected cancel + 2 delete attempts, got cancel=%v deleteCalls=%d", cancelCalled.Load(), deleteCalls.Load())
		}
	})
}
