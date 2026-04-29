// Package azure_devops implements the Anklet handler that pulls Azure DevOps
// pipeline jobs out of Redis, provisions an Anka VM, registers the VM as a
// self-hosted Azure DevOps agent in the configured pool, runs the agent for
// one job, and tears everything down on completion or shutdown.
//
// MVP scope (delivered in Phase 4b):
//   - Single host happy path: one VM per Run() invocation, no inter-host
//     coordination, no resource admission gates, no retry-attempt counting.
//   - Template selection: pipelines declare which Anka template a job
//     needs via pool demands. The handler reads them from the runtime
//     agent job request (Build.Demands is server-side broken for YAML
//     pipelines - https://developercommunity.visualstudio.com/t/Demands-object-missing-from-buildsget-a/11024369;
//     the SDK doesn't expose the job-request endpoint as a typed method,
//     so we make a raw HTTP call - see internal/azure_devops/jobrequests.go).
//
//     Required pipeline YAML:
//
//       pool:
//         name: anklet
//         demands:
//           - anka-template -equals <vm-template-uuid>
//           - anka-template-tag -equals <tag>            # optional
//           - Agent.Name -equals $(Build.BuildId)        # 1:1 build binding
//
//     The Agent.Name demand is Anklet's equivalent of GitHub's per-job
//     runs-on label. The handler registers the agent with name = Build.BuildId
//     so only the VM provisioned for this build can pick up its jobs.
//     Multi-job builds: see docs/azure_devops.md - jobs in the same build
//     could cross-assign to sibling-job VMs because they share Build.BuildId.
//
//     Demands are also how ADO routes jobs to matching agents at queue
//     time, so this is the correctness boundary: a job tagged for one
//     template can never be picked up by a VM of a different one.
//   - Run completion is detected by polling the Azure DevOps Build API.
//
// Deferred (carried over from the GitHub handler; tracked in
// docs/azure_devops_handler_plan.md):
//   - Multi-host paused-queue + Anka resource gating (CPU/RAM admission
//     checks, paused jobs handed off across hosts).
//   - Crash-resume of an in-flight job (today the plugin queue is wiped at
//     the top of Run() if it has stale entries; the run is cancelled in
//     Azure DevOps so it goes back to "queued" for another attempt).
//   - Agent garbage collection across handler restarts (we delete the
//     agent we registered, but orphaned agents from past runs are not
//     swept).
package azure_devops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	internalAnka "github.com/veertuinc/anklet/internal/anka"
	internalADO "github.com/veertuinc/anklet/internal/azure_devops"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/jobqueue"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
)

// Pool-demand names a pipeline must declare so the handler knows which
// Anka template to clone. These are read from the runtime job request,
// not from the build (Build.Demands is server-side broken on YAML
// pipelines, see package doc).
//
// Names use dashes to match the ADO convention for capability-style
// demand names (e.g. "agent.os", "Agent.Name", and the agent capabilities
// the registered VM advertises).
const (
	demandAnkaTemplate    = "anka-template"
	demandAnkaTemplateTag = "anka-template-tag"
)

// QueueJob.BaseQueueJob.Action values written to the plugin queue. These are
// internal to the ADO handler and complement the jobqueue.Action* vocabulary
// used for cross-provider state.
const (
	actionPreparing  = "preparing"
	actionRegistered = "registered"
)

// Watch loop tuning. Independent of GitHub handler timing because Azure
// DevOps jobs commonly take longer to leave the agent-acquired state.
const (
	runStatePollInterval = 10 * time.Second
	registerScriptTimeoutSeconds = 90
	cancelOnShutdownTimeout = 30 * time.Second
)

// Run is the plugin entry point invoked once per main-loop iteration by
// internal/run. It pops a single Azure DevOps job (if any), provisions a VM,
// registers an agent, watches for completion, and returns. Multi-job loops,
// sleep intervals, and re-invocation are managed by main.go.
func Run(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginCancel context.CancelFunc,
) (context.Context, error) {
	logging.Info(pluginCtx, "starting azure_devops handler plugin")

	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	if err := pluginConfig.ValidateAzureDevOps(); err != nil {
		return pluginCtx, fmt.Errorf("invalid azure_devops plugin config: %w", err)
	}

	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
	if err != nil {
		return pluginCtx, err
	}
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, fmt.Errorf("error getting database from context: %w", err)
	}
	ankaCLI, err := internalAnka.GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}

	// PluginGlobals are required by the rest of Anklet's lifecycle even
	// though this MVP handler does not use the multi-channel coordination
	// (RetryChannel / ReturnToMainQueue / PausedCancellationJobChannel).
	// They stay here so future Phase 4 work that ports the GitHub handler's
	// multi-host loop can reuse the wiring without changing the handler
	// shape.
	pluginGlobals := jobqueue.PluginGlobals[internalADO.QueueJob]{
		RetryChannel:                 make(chan string, 1),
		CleanupMutex:                 &sync.Mutex{},
		JobChannel:                   make(chan internalADO.QueueJob, 1),
		ReturnToMainQueue:            make(chan string, 1),
		PausedCancellationJobChannel: make(chan internalADO.QueueJob, 1),
	}
	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("pluginglobals"), &pluginGlobals)

	pluginCtx = logging.AppendCtx(pluginCtx,
		slog.Int("hostCPUCount", workerGlobals.HostCPUCount),
		slog.Uint64("hostMemoryBytes", workerGlobals.HostMemoryBytes),
	)

	// Block sibling plugins of the same type until we finish provisioning,
	// matching the GitHub handler's contract with main.go.
	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(true)
	defer workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)

	adoClient, err := internalADO.NewClient(pluginCtx, pluginConfig.OrganizationURL, pluginConfig.Project, pluginConfig.Token)
	if err != nil {
		return pluginCtx, fmt.Errorf("creating azure devops client: %w", err)
	}
	if err := adoClient.Ping(pluginCtx); err != nil {
		return pluginCtx, fmt.Errorf("azure devops API unreachable: %w", err)
	}
	pool, err := adoClient.GetPoolByName(pluginCtx, pluginConfig.AgentPoolName)
	if err != nil {
		return pluginCtx, fmt.Errorf("resolving agent pool %q: %w", pluginConfig.AgentPoolName, err)
	}
	logging.Info(pluginCtx, "azure devops API reachable",
		"organization_url", pluginConfig.OrganizationURL,
		"project", pluginConfig.Project,
		"agent_pool_name", pluginConfig.AgentPoolName,
		"agent_pool_id", pool.ID,
	)

	queueNS := pluginConfig.AzureDevOpsRedisNamespace()
	mainQueueName := "anklet/jobs/azure_devops/queued/" + queueNS
	pluginQueueName := "anklet/jobs/azure_devops/queued/" + queueNS + "/{" + pluginConfig.Name + "}"

	if !internalAnka.HostHasVmCapacity(pluginCtx) {
		logging.Warn(pluginCtx, "host does not have vm capacity; skipping this run")
		return pluginCtx, nil
	}

	// Discard any stale entry left in the plugin queue from a prior crashed
	// invocation. Resume-from-crash is intentionally out of MVP scope: we
	// cannot trust the previous AnkaVM still exists, the previous agent may
	// already be registered, and the run may already be assigned. The
	// safest move is to scrub local state and let the next webhook delivery
	// re-queue the job.
	if err := scrubStalePluginQueue(pluginCtx, databaseContainer, adoClient, pluginQueueName); err != nil {
		logging.Warn(pluginCtx, "error scrubbing stale plugin queue", "error", err)
	}

	queuedJob, hasJob, err := popNextJob(pluginCtx, databaseContainer, mainQueueName, pluginQueueName)
	if err != nil {
		return pluginCtx, err
	}
	if !hasJob {
		logging.Debug(pluginCtx, "no azure devops jobs in main queue")
		return pluginCtx, nil
	}

	pluginCtx = logging.AppendCtx(pluginCtx,
		slog.String("ado_job_id", queuedJob.Job.ID),
		slog.Int("ado_run_id", queuedJob.Run.ID),
		slog.String("ado_pipeline", queuedJob.Pipeline.Name),
	)
	logging.Info(pluginCtx, "popped azure devops job from main queue")

	ankaTemplate, ankaTemplateTag, err := resolveTemplate(pluginCtx, adoClient, pool.ID, queuedJob.Job.ID)
	if err != nil {
		logging.Error(pluginCtx, "cannot resolve anka template; cancelling run", "error", err)
		if cancelErr := adoClient.CancelRun(pluginCtx, queuedJob.Run.ID); cancelErr != nil {
			logging.Warn(pluginCtx, "error cancelling unrunnable run", "error", cancelErr)
		}
		_ = removeJobFromPluginQueue(pluginCtx, databaseContainer, pluginQueueName, queuedJob)
		return pluginCtx, fmt.Errorf("template resolution failed: %w", err)
	}
	pluginCtx = logging.AppendCtx(pluginCtx,
		slog.String("anka_template", ankaTemplate),
		slog.String("anka_template_tag", ankaTemplateTag),
	)

	scripts, err := resolveScriptPaths(workerGlobals.PluginsPath)
	if err != nil {
		return pluginCtx, err
	}

	if !pluginConfig.SkipPull {
		notFoundErr, spaceErr, genericErr := ankaCLI.EnsureVMTemplateExists(workerCtx, pluginCtx, ankaTemplate, ankaTemplateTag)
		if notFoundErr != nil {
			logging.Error(pluginCtx, "anka template/tag not found in registry; cancelling run", "error", notFoundErr)
			if cancelErr := adoClient.CancelRun(pluginCtx, queuedJob.Run.ID); cancelErr != nil {
				logging.Warn(pluginCtx, "error cancelling run", "error", cancelErr)
			}
			_ = removeJobFromPluginQueue(pluginCtx, databaseContainer, pluginQueueName, queuedJob)
			return pluginCtx, fmt.Errorf("anka template not found: %w", notFoundErr)
		}
		if spaceErr != nil {
			logging.Error(pluginCtx, "insufficient space on host for anka template", "error", spaceErr)
			return pluginCtx, fmt.Errorf("insufficient host disk space: %w", spaceErr)
		}
		if genericErr != nil {
			logging.Warn(pluginCtx, "transient error preparing anka template; retrying next loop", "error", genericErr)
			return pluginCtx, nil
		}
	}

	if err := metricsData.SetStatus(pluginCtx, "in_progress"); err != nil {
		logging.Warn(pluginCtx, "error setting plugin metrics status", "error", err)
	}

	queuedJob.Action = actionPreparing
	if err := updateJobInPluginQueue(pluginCtx, databaseContainer, pluginQueueName, queuedJob); err != nil {
		logging.Warn(pluginCtx, "error persisting preparing state", "error", err)
	}

	vm, err := ankaCLI.ObtainAnkaVM(workerCtx, pluginCtx, ankaTemplate)
	if err != nil {
		if vm != nil {
			deleteCtx := newCleanupContext(pluginCtx, databaseContainer)
			if delErr := ankaCLI.AnkaDelete(workerCtx, deleteCtx, vm.Name); delErr != nil {
				logging.Warn(pluginCtx, "error deleting partially-created VM", "error", delErr, "vm", vm.Name)
			}
		}
		return pluginCtx, fmt.Errorf("error obtaining anka vm: %w", err)
	}
	queuedJob.AnkaVM = *vm
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("vm", vm.Name))

	// Other plugins waiting for our preparation are now unblocked.
	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)

	// runOutcome tracks whether the run reached a terminal state inside the
	// watch loop. If false at deferred cleanup, the deferred function will
	// best-effort cancel the run so it goes back to the ADO queue.
	state := lifecycleState{
		vmName:           vm.Name,
		runID:            queuedJob.Run.ID,
		poolID:           pool.ID,
		agentRegistered:  false,
		runReachedFinal:  false,
		jobAlreadyRemoved: false,
	}

	defer func() {
		cleanupCtx := newCleanupContext(pluginCtx, databaseContainer)
		teardown(workerCtx, cleanupCtx, ankaCLI, adoClient, &state)
		if !state.jobAlreadyRemoved {
			if err := removeJobFromPluginQueue(cleanupCtx, databaseContainer, pluginQueueName, queuedJob); err != nil {
				logging.Warn(pluginCtx, "error removing job from plugin queue during cleanup", "error", err)
			}
		}
	}()

	if err := ankaCLI.AnkaCopyIntoVM(workerCtx, pluginCtx, vm.Name, scripts.install, scripts.register, scripts.start); err != nil {
		return pluginCtx, fmt.Errorf("error copying handler scripts into VM: %w", err)
	}

	logging.Info(pluginCtx, "installing azure devops agent inside vm")
	if err := ankaCLI.AnkaRun(pluginCtx, vm.Name, "./install-agent.bash"); err != nil {
		return pluginCtx, fmt.Errorf("install-agent.bash failed: %w", err)
	}

	// Agent name is set to the build ID so the YAML can pin a job to its
	// build via `Agent.Name -equals $(Build.BuildId)` in pool.demands.
	// This is Anklet's equivalent of GitHub's per-job runs-on label and
	// gives the same 1:1 binding for single-job builds. See
	// docs/azure_devops.md for the multi-job-build caveat.
	agentName := strconv.Itoa(queuedJob.Run.ID)
	state.agentName = agentName
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("agent_name", agentName))

	logging.Info(pluginCtx, "registering azure devops agent")
	registerErr, registerTimeoutErr := ankaCLI.AnkaRunWithTimeout(pluginCtx,
		registerScriptTimeoutSeconds,
		vm.Name,
		"./register-agent.bash",
		pluginConfig.OrganizationURL,
		pluginConfig.AgentPoolName,
		agentName,
		pluginConfig.Token,
	)
	if registerTimeoutErr != nil {
		return pluginCtx, fmt.Errorf("register-agent.bash timed out: %w", registerTimeoutErr)
	}
	if registerErr != nil {
		return pluginCtx, fmt.Errorf("register-agent.bash failed: %w", registerErr)
	}
	state.agentRegistered = true

	queuedJob.Action = actionRegistered
	if err := updateJobInPluginQueue(pluginCtx, databaseContainer, pluginQueueName, queuedJob); err != nil {
		logging.Warn(pluginCtx, "error persisting registered state", "error", err)
	}

	// Pass the resolved template UUID and tag so start-agent.bash can
	// register them as system capabilities on the agent process. ADO's
	// dispatcher matches the job's pool demands against those
	// capabilities, so without them the agent would be online but the
	// job (which demanded a specific template) would never be assigned
	// to it. This is also our correctness boundary - a VM cloned from
	// template A can never accidentally pick up a job that demanded
	// template B because ADO refuses the assignment server-side.
	logging.Info(pluginCtx, "starting azure devops agent inside vm")
	if err := ankaCLI.AnkaRun(pluginCtx, vm.Name, "./start-agent.bash", ankaTemplate, ankaTemplateTag); err != nil {
		return pluginCtx, fmt.Errorf("start-agent.bash failed: %w", err)
	}

	queuedJob.Action = jobqueue.ActionInProgress
	if err := updateJobInPluginQueue(pluginCtx, databaseContainer, pluginQueueName, queuedJob); err != nil {
		logging.Warn(pluginCtx, "error persisting in_progress state", "error", err)
	}

	if err := watchRunCompletion(workerCtx, pluginCtx, adoClient, queuedJob.Run.ID, pluginConfig.Name, metricsData, &state); err != nil {
		return pluginCtx, err
	}

	// We owned the cleanup of the plugin queue entry through the watch
	// loop's normal completion path, so signal the deferred function not
	// to remove it again.
	state.jobAlreadyRemoved = true
	if err := removeJobFromPluginQueue(pluginCtx, databaseContainer, pluginQueueName, queuedJob); err != nil {
		logging.Warn(pluginCtx, "error removing completed job from plugin queue", "error", err)
	}

	// Suppress unused param lint - pluginCancel is part of the handler
	// signature contract even though this MVP does not need to invoke it.
	_ = pluginCancel
	return pluginCtx, nil
}

// lifecycleState bundles the values the deferred teardown needs so the
// cleanup function does not have to re-read them out of the (possibly
// canceled) plugin context.
type lifecycleState struct {
	vmName            string
	agentName         string
	runID             int
	poolID            int
	agentRegistered   bool
	runReachedFinal   bool
	jobAlreadyRemoved bool
}

// scriptPaths is just a typed bundle so we can pre-validate all three
// handler scripts before doing any provisioning work.
type scriptPaths struct {
	install  string
	register string
	start    string
}

// resolveTemplate fetches the runtime job request for jobID from the
// configured agent pool and reads the anka-template / anka-template-tag
// pool demands off it. Returns a non-nil error if the matching job
// request can't be found, the required demand is missing, or the demand
// has an empty value (we can't clone an unnamed template).
func resolveTemplate(ctx context.Context, adoClient *internalADO.Client, poolID int, jobID string) (string, string, error) {
	requests, err := adoClient.ListJobRequests(ctx, poolID)
	if err != nil {
		return "", "", fmt.Errorf("listing job requests for pool %d: %w", poolID, err)
	}
	match := internalADO.FindJobRequest(requests, jobID)
	if match == nil {
		return "", "", fmt.Errorf("no job request found in pool %d for jobId %s "+
			"(demand routing may have steered the job to a different pool)", poolID, jobID)
	}
	template, ok := match.DemandValue(demandAnkaTemplate)
	if !ok || template == "" {
		return "", "", fmt.Errorf("job request %d does not declare a non-empty %q demand; "+
			"add `- %s -equals <vm-template-uuid>` to pool.demands in your pipeline YAML",
			match.RequestID, demandAnkaTemplate, demandAnkaTemplate)
	}
	tag, _ := match.DemandValue(demandAnkaTemplateTag)
	return template, tag, nil
}

// resolveScriptPaths validates that the three handler shell scripts live
// where the worker expects them, returning an early error so we never
// provision a VM only to discover scripts are missing.
func resolveScriptPaths(pluginsPath string) (scriptPaths, error) {
	paths := scriptPaths{
		install:  filepath.Join(pluginsPath, "handlers", "azure_devops", "install-agent.bash"),
		register: filepath.Join(pluginsPath, "handlers", "azure_devops", "register-agent.bash"),
		start:    filepath.Join(pluginsPath, "handlers", "azure_devops", "start-agent.bash"),
	}
	for _, p := range []string{paths.install, paths.register, paths.start} {
		if _, err := os.Stat(p); err != nil {
			return scriptPaths{}, fmt.Errorf("missing handler script %s: %w", p, err)
		}
	}
	return paths, nil
}

// popNextJob pulls a single Azure DevOps QueueJob from the main queue and
// pushes it onto the per-plugin queue with Action set to actionPreparing.
// Returns hasJob=false when the main queue is empty.
//
// Cluster-safety: this is intentionally NOT an RPOPLPUSH. The plugin queue
// is hash-tagged ("anklet/jobs/.../{<plugin-name>}") so per-plugin operations
// stay in one Redis Cluster slot, but the main queue is not. RPOPLPUSH
// requires both keys to hash to the same slot, so we LRange+LRem the head
// of the main queue (via jobqueue.PopJobOffQueue) and RPush to the plugin
// queue as two separate operations. This matches the GitHub handler, which
// comments "do not use RPopLPush due to hash tag issue".
//
// The two ops are not atomic: if anklet crashes between them, the job is
// lost from main and never reaches plugin. We accept this for the MVP; the
// next webhook delivery for the same run will re-queue it.
func popNextJob(
	ctx context.Context,
	db *database.Database,
	mainQueueName, pluginQueueName string,
) (internalADO.QueueJob, bool, error) {
	popped, err := jobqueue.PopJobOffQueue(ctx, mainQueueName, 0)
	if err != nil {
		return internalADO.QueueJob{}, false, fmt.Errorf("popping from %s: %w", mainQueueName, err)
	}
	if popped == "" {
		return internalADO.QueueJob{}, false, nil
	}

	job, unmarshalErr, typeErr := database.Unwrap[internalADO.QueueJob](popped)
	if unmarshalErr != nil || typeErr != nil {
		// Bad payload at the head of main queue would block every future
		// pop; PopJobOffQueue already removed it. Don't push it onto the
		// plugin queue.
		return internalADO.QueueJob{}, false, fmt.Errorf("unparseable job from main queue: unmarshalErr=%v typeErr=%v", unmarshalErr, typeErr)
	}

	if _, err := db.RetryRPush(ctx, pluginQueueName, popped); err != nil {
		// Best-effort: restore to main so the job isn't dropped.
		if _, restoreErr := db.RetryRPush(ctx, mainQueueName, popped); restoreErr != nil {
			return internalADO.QueueJob{}, false, fmt.Errorf("pushing to plugin queue: %v; and restoring to main: %v", err, restoreErr)
		}
		return internalADO.QueueJob{}, false, fmt.Errorf("pushing to plugin queue: %w (restored to main)", err)
	}
	return job, true, nil
}

// scrubStalePluginQueue removes any leftover entries in the plugin queue
// from a previous crashed Run() and best-effort cancels the corresponding
// Azure DevOps run so the job becomes available to other handlers. This
// implements the documented MVP "no resume" behavior.
func scrubStalePluginQueue(
	ctx context.Context,
	db *database.Database,
	adoClient *internalADO.Client,
	pluginQueueName string,
) error {
	entries, err := db.RetryLRange(ctx, pluginQueueName, 0, -1)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}
	logging.Warn(ctx, "found stale entries in plugin queue from previous run; scrubbing", "count", len(entries))
	for _, entry := range entries {
		stale, unmarshalErr, typeErr := database.Unwrap[internalADO.QueueJob](entry)
		if unmarshalErr == nil && typeErr == nil && stale.Run.ID != 0 {
			if cancelErr := adoClient.CancelRun(ctx, stale.Run.ID); cancelErr != nil {
				logging.Warn(ctx, "error cancelling stale run during scrub", "error", cancelErr, "run_id", stale.Run.ID)
			}
		}
	}
	if _, err := db.RetryDel(ctx, pluginQueueName); err != nil {
		return fmt.Errorf("deleting stale plugin queue: %w", err)
	}
	return nil
}

// updateJobInPluginQueue replaces the head entry of the plugin queue with
// the JSON-encoded job. Used to record state transitions
// (preparing -> registered -> in_progress) so an aggregator or operator
// inspecting the queue can see what stage the handler is at.
func updateJobInPluginQueue(
	ctx context.Context,
	db *database.Database,
	pluginQueueName string,
	job internalADO.QueueJob,
) error {
	encoded, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshalling job: %w", err)
	}
	// We only ever have one entry while a job is being handled; index 0
	// holds it (RPOPLPUSH pushes to the head of the destination).
	if err := db.RetryLSet(ctx, pluginQueueName, 0, encoded); err != nil {
		return fmt.Errorf("setting plugin queue head: %w", err)
	}
	return nil
}

// removeJobFromPluginQueue removes any entry in the plugin queue whose
// Job/Run/Attempt match job. Designed to be called both from the success
// path and from cleanup, so it is idempotent: missing entries are not an
// error.
func removeJobFromPluginQueue(
	ctx context.Context,
	db *database.Database,
	pluginQueueName string,
	job internalADO.QueueJob,
) error {
	entries, err := db.RetryLRange(ctx, pluginQueueName, 0, -1)
	if err != nil {
		return fmt.Errorf("listing plugin queue: %w", err)
	}
	for _, entry := range entries {
		queued, unmarshalErr, typeErr := database.Unwrap[internalADO.QueueJob](entry)
		if unmarshalErr != nil || typeErr != nil {
			continue
		}
		if queued.Matches(job) {
			if _, err := db.RetryLRem(ctx, pluginQueueName, 1, entry); err != nil {
				return fmt.Errorf("removing matched entry: %w", err)
			}
		}
	}
	return nil
}

// watchRunCompletion polls the Azure DevOps Build API on a fixed cadence
// until the run reaches a terminal state, emitting metrics on completion
// and returning when the run is final. Honors pluginCtx cancellation by
// returning early; the deferred teardown handles cancelling the run and
// reaping the VM/agent.
func watchRunCompletion(
	workerCtx context.Context,
	pluginCtx context.Context,
	adoClient *internalADO.Client,
	runID int,
	pluginName string,
	metricsData *metrics.MetricsDataLock,
	state *lifecycleState,
) error {
	logging.Info(pluginCtx, "watching azure devops run for completion", "poll_interval", runStatePollInterval)
	ticker := time.NewTicker(runStatePollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pluginCtx.Done():
			logging.Warn(pluginCtx, "context canceled while watching run", "run_id", runID)
			return nil
		case <-ticker.C:
			run, err := adoClient.GetRun(pluginCtx, runID)
			if err != nil {
				logging.Warn(pluginCtx, "error polling run status", "error", err, "run_id", runID)
				continue
			}
			if run.Status != "completed" {
				logging.Debug(pluginCtx, "run still active", "status", run.Status)
				continue
			}
			state.runReachedFinal = true
			logging.Info(pluginCtx, "run completed", "run_id", runID, "result", run.Result)
			recordRunOutcome(workerCtx, pluginCtx, metricsData, pluginName, run.Result)
			return nil
		}
	}
}

// recordRunOutcome bumps the right metrics counter for the terminal state
// the run reached and updates the plugin metrics with the most recent run
// timestamps. Errors are logged but not propagated; metrics failures must
// not prevent successful job teardown.
func recordRunOutcome(
	workerCtx context.Context,
	pluginCtx context.Context,
	metricsData *metrics.MetricsDataLock,
	pluginName string,
	result string,
) {
	now := time.Now()
	switch result {
	case "succeeded", "":
		metricsData.IncrementTotalSuccessfulRunsSinceStart(workerCtx, pluginCtx)
		if err := metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.Plugin{
			PluginBase:        &metrics.PluginBase{Name: pluginName},
			LastSuccessfulRun: now,
		}); err != nil {
			logging.Warn(pluginCtx, "error updating success metrics", "error", err)
		}
	case "failed":
		metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx)
		if err := metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.Plugin{
			PluginBase:    &metrics.PluginBase{Name: pluginName},
			LastFailedRun: now,
		}); err != nil {
			logging.Warn(pluginCtx, "error updating failure metrics", "error", err)
		}
	case "canceled":
		metricsData.IncrementTotalCanceledRunsSinceStart(workerCtx, pluginCtx)
		if err := metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.Plugin{
			PluginBase:      &metrics.PluginBase{Name: pluginName},
			LastCanceledRun: now,
		}); err != nil {
			logging.Warn(pluginCtx, "error updating cancel metrics", "error", err)
		}
	default:
		logging.Warn(pluginCtx, "unknown run result; metrics not incremented", "result", result)
	}
}

// teardown is the deferred cleanup that always runs at the end of Run().
// It cancels the run if we never observed it finishing, deletes the
// registered Azure DevOps agent, and deletes the Anka VM. Every step is
// best-effort; failures are logged so an operator can sweep manually.
//
// workerCtx is required separately from cleanupCtx because internal/anka
// reads its metrics handle from the worker context. cleanupCtx is the
// short-lived context used for cancellation/logging during shutdown; it
// does not carry the metrics value.
func teardown(
	workerCtx context.Context,
	cleanupCtx context.Context,
	ankaCLI *internalAnka.Cli,
	adoClient *internalADO.Client,
	state *lifecycleState,
) {
	if state.runID != 0 && !state.runReachedFinal {
		cancelCtx, cancelFn := context.WithTimeout(cleanupCtx, cancelOnShutdownTimeout)
		defer cancelFn()
		if err := adoClient.CancelRun(cancelCtx, state.runID); err != nil {
			logging.Warn(cleanupCtx, "error cancelling run during teardown", "error", err, "run_id", state.runID)
		} else {
			logging.Info(cleanupCtx, "cancelled run during teardown", "run_id", state.runID)
		}
	}
	if state.agentRegistered && state.poolID != 0 && state.agentName != "" {
		if err := deleteAgentByName(cleanupCtx, adoClient, state.poolID, state.agentName); err != nil {
			logging.Warn(cleanupCtx, "error removing azure devops agent during teardown", "error", err, "agent_name", state.agentName)
		}
	}
	if state.vmName != "" {
		if err := ankaCLI.AnkaDelete(workerCtx, cleanupCtx, state.vmName); err != nil {
			logging.Warn(cleanupCtx, "error deleting anka vm during teardown", "error", err, "vm", state.vmName)
		} else {
			logging.Info(cleanupCtx, "deleted anka vm", "vm", state.vmName)
		}
	}
}

// deleteAgentByName looks up the Azure DevOps agent in pool whose Name
// matches name and deletes it. ADO does not expose a name-keyed delete
// endpoint, so we list-then-delete by ID. Missing agents are not errors.
func deleteAgentByName(
	ctx context.Context,
	adoClient *internalADO.Client,
	poolID int,
	name string,
) error {
	agents, err := adoClient.ListAgents(ctx, poolID)
	if err != nil {
		return fmt.Errorf("listing agents to find %q: %w", name, err)
	}
	for _, a := range agents {
		if a.Name != name {
			continue
		}
		if err := adoClient.DeleteAgent(ctx, poolID, a.ID); err != nil {
			return fmt.Errorf("deleting agent %d (%q): %w", a.ID, name, err)
		}
		logging.Info(ctx, "deleted azure devops agent", "agent_name", name, "agent_id", a.ID)
		return nil
	}
	logging.Debug(ctx, "no matching agent found during teardown", "agent_name", name)
	return nil
}

// newCleanupContext builds a context.Background-rooted context preloaded
// with the logger and database from pluginCtx so deferred cleanup keeps
// working even after pluginCtx has been cancelled. Mirrors the pattern the
// GitHub handler uses for VM deletion on shutdown.
func newCleanupContext(pluginCtx context.Context, db *database.Database) context.Context {
	cleanupCtx := context.Background()
	if logger, err := logging.GetLoggerFromContext(pluginCtx); err == nil {
		cleanupCtx = context.WithValue(cleanupCtx, config.ContextKey("logger"), logger)
	}
	cleanupCtx = context.WithValue(cleanupCtx, config.ContextKey("database"), db)
	return cleanupCtx
}
