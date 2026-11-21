package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/v75/github"
	internalAnka "github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/drain"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
	"github.com/veertuinc/anklet/internal/plugin"
)

type queueNames struct {
	owner           string
	mainQueue       string
	mainCompleted   string
	pluginQueue     string
	pluginCompleted string
	mainInProgress  string
	paused          string
}

func buildQueueNames(pluginConfig config.Plugin) queueNames {
	owner := pluginConfig.GetQueueOwner()
	return queueNames{
		owner:           owner,
		mainQueue:       "anklet/jobs/github/queued/" + owner,
		mainCompleted:   "anklet/jobs/github/completed/" + owner,
		pluginQueue:     "anklet/jobs/github/queued/" + owner + "/{" + pluginConfig.Name + "}",
		pluginCompleted: "anklet/jobs/github/completed/" + owner + "/" + pluginConfig.Name,
		mainInProgress:  "anklet/jobs/github/in_progress/" + owner,
		paused:          "anklet/jobs/github/paused/" + owner,
	}
}

// Handler implements plugin.Runner for GitHub Actions macOS VM jobs.
type Handler struct {
	deps plugin.Deps
}

// NewHandler creates a GitHub Actions handler plugin with typed dependencies.
func NewHandler(deps plugin.Deps) *Handler {
	return &Handler{deps: deps}
}

func (h *Handler) Role() plugin.Role {
	return plugin.RoleHandler
}

func extractLabelValue(labels []string, prefix string) string {
	for _, label := range labels {
		if result, found := strings.CutPrefix(label, prefix); found {
			return result
		}
	}
	return ""
}

// cancelJob cancels the workflow run on GitHub and marks the job completed in Redis.
func (h *Handler) cancelJob(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginQueueName string,
	queuedJob *internalGithub.QueueJob,
) error {
	if err := sendCancelWorkflowRun(workerCtx, pluginCtx, h.deps.Metrics, *queuedJob); err != nil {
		logging.Error(pluginCtx, "error sending cancel workflow run", "error", err)
	}
	queuedJob.Action = "canceled"
	queuedJob.WorkflowJob.Status = github.Ptr("completed")
	if err := internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, queuedJob); err != nil && !errors.Is(err, internalGithub.ErrJobAlreadyCompleted) {
		logging.Error(pluginCtx, "error updating job in db", "error", err)
		return err
	}
	return nil
}

func (h *Handler) waitForFirstWatcherPass(workerCtx, pluginCtx context.Context, state *handlerState) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	counter := 0
	for !state.firstCheckRan.Load() {
		select {
		case <-workerCtx.Done():
			return fmt.Errorf("context canceled while waiting for first checkForCompletedJobs to run")
		case <-pluginCtx.Done():
			return fmt.Errorf("context canceled while waiting for first checkForCompletedJobs to run")
		case <-ticker.C:
			counter++
			if counter%25 == 0 {
				logging.Debug(pluginCtx, "waiting for first check for completed jobs to run", "seconds_waited", counter/5)
			}
		}
	}
	return nil
}

func (h *Handler) deleteVMWithBackgroundCtx(workerCtx, pluginCtx context.Context, vmName string) {
	if vmName == "" || h.deps.AnkaCLI == nil {
		return
	}
	deleteCtx := context.Background()
	if logger, err := logging.GetLoggerFromContext(pluginCtx); err == nil {
		deleteCtx = context.WithValue(deleteCtx, config.ContextKey("logger"), logger)
	}
	if err := h.deps.AnkaCLI.AnkaDelete(workerCtx, deleteCtx, vmName); err != nil {
		logging.Error(pluginCtx, "error deleting vm", "error", err)
	}
}

func (h *Handler) Run(
	workerCtx context.Context,
	pluginCtx context.Context,
	_ context.CancelFunc,
) (context.Context, error) {
	if !internalGithub.LogGitHubAPIRateLimitStatusAtRunStart(workerCtx, pluginCtx) {
		logging.Info(pluginCtx, "starting github plugin")
	}

	pluginConfig := h.deps.PluginConfig
	if pluginConfig.SkipCPUAndMemoryResourceChecks {
		logging.Warn(
			pluginCtx,
			"skipping VM CPU and memory resource checks; this plugin may overcommit host resources; use at your own risk",
		)
	}

	metricsData := h.deps.Metrics
	if metricsData == nil {
		var err error
		metricsData, err = metrics.GetMetricsDataFromContext(workerCtx)
		if err != nil {
			return pluginCtx, err
		}
		h.deps.Metrics = metricsData
	}

	workerGlobals := h.deps.Globals
	if workerGlobals == nil {
		var err error
		workerGlobals, err = config.GetWorkerGlobalsFromContext(pluginCtx)
		if err != nil {
			return pluginCtx, err
		}
		h.deps.Globals = workerGlobals
	}

	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(true)

	pluginCtx = logging.AppendCtx(pluginCtx,
		slog.Int("hostCPUCount", workerGlobals.HostCPUCount),
		slog.Uint64("hostMemoryBytes", workerGlobals.HostMemoryBytes),
	)

	configFileName := h.deps.ConfigFileName
	if err := pluginConfig.ValidateGitHubScope(); err != nil {
		return pluginCtx, fmt.Errorf("invalid github scope in %s:plugins:%s: %w", configFileName, pluginConfig.Name, err)
	}
	if err := pluginConfig.ValidateEnterpriseGitHubCredentials(); err != nil {
		return pluginCtx, fmt.Errorf("%s:plugins:%s: %w", configFileName, pluginConfig.Name, err)
	}
	if !pluginConfig.IsEnterpriseScope() && pluginConfig.Token == "" && pluginConfig.PrivateKey == "" {
		return pluginCtx, fmt.Errorf("token or private_key are not set at global level or in %s:plugins:%s<token/private_key>", configFileName, pluginConfig.Name)
	}
	if pluginConfig.PrivateKey != "" && pluginConfig.AppID == 0 {
		return pluginCtx, fmt.Errorf("private_key and app_id must both be set in %s:plugins:%s", configFileName, pluginConfig.Name)
	}
	if strings.HasPrefix(pluginConfig.PrivateKey, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return pluginCtx, fmt.Errorf("unable to get user home directory: %w", err)
		}
		pluginConfig.PrivateKey = filepath.Join(homeDir, pluginConfig.PrivateKey[2:])
	}

	githubClient, githubWrapperClient, err := internalGithub.AuthenticateAndReturnGitHubClient(
		pluginCtx,
		pluginConfig.PrivateKey,
		pluginConfig.AppID,
		pluginConfig.InstallationID,
		pluginConfig.Token,
		pluginConfig.Enterprise,
		pluginConfig.Owner,
	)
	if err != nil {
		return pluginCtx, fmt.Errorf("error authenticating github client: %w", err)
	}
	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("githubwrapperclient"), githubWrapperClient)
	repositoryURL := pluginConfig.GitHubRegistrationURL()

	databaseContainer := h.deps.Database
	if databaseContainer == nil {
		databaseContainer, err = database.GetDatabaseFromContext(pluginCtx)
		if err != nil {
			// Preserve exact error string checked by CLI integration tests.
			return pluginCtx, fmt.Errorf("error getting database from context: %w", err)
		}
		h.deps.Database = databaseContainer
	}

	queues := buildQueueNames(pluginConfig)
	state := newHandlerState()

	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		logging.Warn(pluginCtx, "context canceled before checking for jobs")
		return pluginCtx, nil
	}

	watcherCtx, stopWatcher := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)

	// Always stop the watcher before waiting on wg, guaranteeing no early return can deadlock.
	defer func() {
		stopWatcher()
		wg.Wait()
		h.cleanup(workerCtx, pluginCtx, queues, state, false)
	}()

	go func() {
		defer wg.Done()
		h.checkForCompletedJobs(watcherCtx, workerCtx, pluginCtx, queues, state)
	}()

	if err := h.waitForFirstWatcherPass(workerCtx, pluginCtx, state); err != nil {
		return pluginCtx, err
	}

	select {
	case jobFromWatcher := <-state.jobEvents:
		if watchJobIsTerminal(jobFromWatcher.WorkflowJob.Status) {
			logging.Info(pluginCtx, statusString(jobFromWatcher.WorkflowJob.Status)+" job found at start", "jobFromJobChannel", jobFromWatcher)
			return pluginCtx, nil
		}
		if statusString(jobFromWatcher.WorkflowJob.Status) == "in_progress" {
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
			logging.Info(pluginCtx, "watching for job completion", "jobFromJobChannel", jobFromWatcher)
			pluginCtx, err = h.watchJobStatus(workerCtx, pluginCtx, queues, state)
			if err != nil {
				logging.Error(pluginCtx, "error watching for job completion", "error", err)
				return pluginCtx, err
			}
			return pluginCtx, nil
		}
	default:
		if !workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].FinishedInitialRun.Load() {
			return pluginCtx, nil
		}
	}

	// Finalize any interrupted cleanup from a prior crash.
	h.cleanup(workerCtx, pluginCtx, queues, state, true)
	select {
	case runningJob := <-state.jobEvents:
		if watchJobIsTerminal(runningJob.WorkflowJob.Status) {
			logging.Info(pluginCtx, statusString(runningJob.WorkflowJob.Status)+" job found at start, after first cleanup")
			return pluginCtx, nil
		}
	case <-pluginCtx.Done():
		return pluginCtx, nil
	default:
	}

	if drain.IsDraining() {
		drainFilePath, _ := drain.DrainFilePath()
		logging.Warn(pluginCtx, "drain file present; not picking up new jobs", "path", drainFilePath)
		if err := metricsData.SetStatus(pluginCtx, "draining"); err != nil {
			logging.Error(pluginCtx, "error setting plugin status", "error", err)
		}
		return pluginCtx, nil
	}

	if !internalAnka.HostHasVmCapacity(pluginCtx) {
		logging.Warn(pluginCtx, "host does not have vm capacity")
		return pluginCtx, nil
	}

	var queuedJob internalGithub.QueueJob
	var claimed bool
	pluginCtx, queuedJob, claimed, err = h.claimNextJob(workerCtx, pluginCtx, queues)
	if err != nil || !claimed {
		return pluginCtx, err
	}
	if queuedJob.WorkflowJob.ID == nil {
		logging.Error(pluginCtx, "claimed job missing workflow_job.id; discarding")
		return pluginCtx, nil
	}

	logging.Info(pluginCtx, "queued job found", "queuedJob", queuedJob)

	var runID int64
	if queuedJob.WorkflowJob.RunID != nil {
		runID = *queuedJob.WorkflowJob.RunID
	}
	workflowName := statusString(queuedJob.WorkflowJob.WorkflowName)
	htmlURL := statusString(queuedJob.WorkflowJob.HTMLURL)
	pluginCtx = logging.AppendCtx(pluginCtx,
		slog.Int64("workflowJobID", *queuedJob.WorkflowJob.ID),
		slog.Int64("workflowJobRunID", runID),
		slog.String("workflowName", workflowName),
		slog.String("workflowJobURL", htmlURL),
		slog.Int("attempts", queuedJob.Attempts),
	)

	// Confirm the newly claimed job is not already completed before provisioning a VM.
	state.firstCheckRan.Store(false)
	if err := h.waitForFirstWatcherPass(workerCtx, pluginCtx, state); err != nil {
		logging.Warn(pluginCtx, "context canceled before first check for completed jobs")
		return pluginCtx, nil
	}
	select {
	case job := <-state.jobEvents:
		if watchJobIsTerminal(job.WorkflowJob.Status) {
			logging.Info(pluginCtx, statusString(job.WorkflowJob.Status)+" job found by checkForCompletedJobs (at start of Run)")
		}
		return pluginCtx, nil
	case <-pluginCtx.Done():
		logging.Warn(pluginCtx, "context canceled before vm startup")
		return pluginCtx, nil
	default:
	}

	if queuedJob.Attempts > 0 {
		maxAttempts := pluginConfig.JobRetryAttempts
		if maxAttempts == 0 {
			maxAttempts = 5
		}
		if queuedJob.Attempts > maxAttempts {
			logging.Warn(pluginCtx, "job has attempts > max attempts, cancelling it", "attempts", queuedJob.Attempts, "max_attempts", maxAttempts)
			_ = h.cancelJob(workerCtx, pluginCtx, queues.pluginQueue, &queuedJob)
			return pluginCtx, nil
		}
		logging.Warn(pluginCtx, "job has attempts > 0, checking status from API to see if the job is still even running")
		queuedJob, err = internalGithub.UpdateJobsWorkflowJobStatus(workerCtx, pluginCtx, &queuedJob)
		if err != nil {
			logging.Error(pluginCtx, "error updating job workflow job status", "error", err)
			state.requestReturnToMainQueue("error_updating_job_workflow_job_status")
			return pluginCtx, nil
		}
		if statusString(queuedJob.WorkflowJob.Status) == "completed" {
			queuedJob.Action = "finish"
			if updateErr := internalGithub.UpdateJobInDB(pluginCtx, queues.pluginQueue, &queuedJob); updateErr != nil && !errors.Is(updateErr, internalGithub.ErrJobAlreadyCompleted) {
				logging.Error(pluginCtx, "error updating job in db", "error", updateErr)
			}
			return pluginCtx, nil
		}
		if statusString(queuedJob.WorkflowJob.Status) == "in_progress" {
			logging.Info(pluginCtx, "job is in progress, so we'll wait for it to finish")
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
			pluginCtx, err = h.watchJobStatus(workerCtx, pluginCtx, queues, state)
			if err != nil {
				logging.Error(pluginCtx, "error watching for job completion", "error", err)
				return pluginCtx, err
			}
			return pluginCtx, nil
		}
	}

	ankaTemplateUUID := extractLabelValue(queuedJob.WorkflowJob.Labels, "anka-template:")
	if ankaTemplateUUID == "" {
		logging.Error(pluginCtx, "anka-template label missing from workflow job; cancelling job")
		_ = h.cancelJob(workerCtx, pluginCtx, queues.pluginQueue, &queuedJob)
		return pluginCtx, nil
	}
	ankaTemplateTag := extractLabelValue(queuedJob.WorkflowJob.Labels, "anka-template-tag:")
	if ankaTemplateTag == "" {
		ankaTemplateTag = "(using latest)"
	}
	pluginCtx = logging.AppendCtx(pluginCtx,
		slog.String("ankaTemplateUUID", ankaTemplateUUID),
		slog.String("ankaTemplateTag", ankaTemplateTag),
	)

	ankaCLI := h.deps.AnkaCLI
	if ankaCLI == nil {
		ankaCLI, err = internalAnka.GetAnkaCLIFromContext(pluginCtx)
		if err != nil {
			return pluginCtx, err
		}
		h.deps.AnkaCLI = ankaCLI
	}

	logging.Info(pluginCtx, "handling anka workflow run job")
	if err := metricsData.SetStatus(pluginCtx, "in_progress"); err != nil {
		logging.Error(pluginCtx, "error setting plugin status", "error", err)
	}

	queuedJob.Action = "preparing"
	if updateErr := internalGithub.UpdateJobInDB(pluginCtx, queues.pluginQueue, &queuedJob); updateErr != nil {
		if errors.Is(updateErr, internalGithub.ErrJobAlreadyCompleted) {
			logging.Warn(pluginCtx, "job found completed in DB")
			return pluginCtx, nil
		}
		logging.Error(pluginCtx, "error updating job in db", "error", updateErr)
		state.requestReturnToMainQueue("error_updating_job_in_db")
		return pluginCtx, nil
	}

	if queuedJob.AnkaVM.CPUCount == 0 || queuedJob.AnkaVM.MEMBytes == 0 {
		isRegistryRunning, regErr := ankaCLI.AnkaRegistryRunning(pluginCtx)
		if regErr != nil {
			logging.Error(pluginCtx, "error checking if registry is running", "error", regErr)
			state.requestReturnToMainQueue("anka_registry_not_running")
			return pluginCtx, nil
		}
		if isRegistryRunning {
			ankaRegistryVMInfo, infoErr := internalAnka.GetAnkaRegistryVmInfo(workerCtx, pluginCtx, ankaTemplateUUID, ankaTemplateTag)
			if infoErr != nil {
				if strings.Contains(infoErr.Error(), "not found") {
					logging.Error(pluginCtx, "anka template or tag not found in registry; cancelling job",
						"ankaTemplateUUID", ankaTemplateUUID,
						"ankaTemplateTag", ankaTemplateTag,
						"err", infoErr,
					)
					queuedJob.WorkflowJob.Conclusion = github.Ptr("failure")
					_ = h.cancelJob(workerCtx, pluginCtx, queues.pluginQueue, &queuedJob)
					workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
					return pluginCtx, nil
				}
				logging.Error(pluginCtx, "error getting anka registry vm info", "error", infoErr)
				state.requestReturnToMainQueue("anka_registry_not_running")
				return pluginCtx, nil
			}
			queuedJob.AnkaVM = *ankaRegistryVMInfo
		} else {
			logging.Error(pluginCtx, "anka registry is not running, checking if template is already pulled")
			ankaShowOutput, showErr := ankaCLI.AnkaShow(pluginCtx, ankaTemplateUUID)
			if showErr != nil {
				logging.Error(pluginCtx, "template doesn't exist locally", "error", showErr)
				workerGlobals.IncrementQueueTargetIndex()
				state.requestReturnToMainQueue("anka_template_does_not_exist_locally")
				return pluginCtx, nil
			}
			if ankaShowOutput.Tag != ankaTemplateTag {
				logging.Error(pluginCtx, "current anka template tag on the host doesn't match the tag specified in the labels", "ankaShowOutput.Tag", ankaShowOutput.Tag)
				workerGlobals.IncrementQueueTargetIndex()
				state.requestReturnToMainQueue("anka_template_tag_does_not_match")
				return pluginCtx, nil
			}
			queuedJob.AnkaVM = internalAnka.VM{
				CPUCount: ankaShowOutput.CPU,
				MEMBytes: ankaShowOutput.MEMBytes,
			}
		}

		if updateErr := internalGithub.UpdateJobInDB(pluginCtx, queues.pluginQueue, &queuedJob); updateErr != nil {
			if errors.Is(updateErr, internalGithub.ErrJobAlreadyCompleted) {
				return pluginCtx, nil
			}
			logging.Error(pluginCtx, "error updating job in db", "error", updateErr)
		}
	}

	logging.Debug(pluginCtx, "vmInfo", "queuedJob.AnkaVM", queuedJob.AnkaVM)
	pluginCtx = logging.AppendCtx(pluginCtx, slog.Any("vm", queuedJob.AnkaVM))

	if queuedJob.Action != "paused" && !pluginConfig.SkipCPUAndMemoryResourceChecks {
		if resErr := internalAnka.VmHasEnoughHostResources(pluginCtx, queuedJob.AnkaVM); resErr != nil {
			logging.Warn(pluginCtx, "host does not have enough resources to run vm")
			workerGlobals.IncrementQueueTargetIndex()
			state.requestReturnToMainQueue("not_enough_host_resources")
			return pluginCtx, nil
		}
		if resErr := internalAnka.VmHasEnoughResources(pluginCtx, queuedJob.AnkaVM); resErr != nil {
			canContinue, waitErr := h.waitForAvailableResources(workerCtx, pluginCtx, queues, state, &queuedJob)
			if waitErr != nil || !canContinue {
				return pluginCtx, waitErr
			}
		}
	}

	logging.Info(pluginCtx, "vm has enough resources now to run; starting runner")

	installRunnerPath := filepath.Join(workerGlobals.PluginsPath, "handlers", "github", "install-runner.bash")
	registerRunnerPath := filepath.Join(workerGlobals.PluginsPath, "handlers", "github", "register-runner.bash")
	startRunnerPath := filepath.Join(workerGlobals.PluginsPath, "handlers", "github", "start-runner.bash")
	_, installRunnerErr := os.Stat(installRunnerPath)
	_, registerRunnerErr := os.Stat(registerRunnerPath)
	_, startRunnerErr := os.Stat(startRunnerPath)
	if installRunnerErr != nil || registerRunnerErr != nil || startRunnerErr != nil {
		state.requestReturnToMainQueue("missing_runner_scripts")
		return pluginCtx, fmt.Errorf("must include install-runner.bash, register-runner.bash, and start-runner.bash in %s/handlers/github/", workerGlobals.PluginsPath)
	}

	if !pluginConfig.SkipPull {
		ensureErr := ankaCLI.EnsureVMTemplateExists(workerCtx, pluginCtx, ankaTemplateUUID, ankaTemplateTag)
		if workerCtx.Err() != nil || pluginCtx.Err() != nil {
			logging.Error(pluginCtx, "context canceled while ensuring vm template exists")
			return pluginCtx, nil
		}
		if ensureErr != nil {
			switch {
			case errors.Is(ensureErr, internalAnka.ErrTemplateNotFoundInRegistry):
				logging.Error(pluginCtx, "anka template or tag not found in registry; cancelling job",
					"ankaTemplateUUID", ankaTemplateUUID,
					"ankaTemplateTag", ankaTemplateTag,
					"err", ensureErr,
				)
				_ = h.cancelJob(workerCtx, pluginCtx, queues.pluginQueue, &queuedJob)
				return pluginCtx, nil
			case errors.Is(ensureErr, internalAnka.ErrInsufficientSpace):
				logging.Error(pluginCtx, "insufficient space on host", "error", ensureErr)
				workerGlobals.IncrementQueueTargetIndex()
				state.requestReturnToMainQueue("error_no_space_on_host_for_template")
				return pluginCtx, nil
			default:
				logging.Warn(pluginCtx, "problem ensuring vm template exists on host", "error", ensureErr)
				workerGlobals.IncrementQueueTargetIndex()
				if strings.Contains(ensureErr.Error(), "a pull is already running on this host") {
					state.requestReturnToMainQueue("pull_already_running_on_host")
				} else {
					state.requestReturnToMainQueue("problem_ensuring_vm_template_exists_on_host")
				}
				return pluginCtx, nil
			}
		}
	}

	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		return pluginCtx, fmt.Errorf("context canceled during vm template check")
	}

	var runnerRegistration *github.RegistrationToken
	var response *github.Response
	pluginCtx, runnerRegistration, response, err = h.createRegistrationToken(workerCtx, pluginCtx, githubClient)
	if err != nil {
		if pluginConfig.IsEnterpriseScope() && strings.Contains(err.Error(), "Resource not accessible by integration") {
			err = fmt.Errorf("%w (GitHub Apps cannot call enterprise runner APIs; set plugins.%s.token to a classic PAT with manage_runners:enterprise — App private_key can stay for org Actions)", err, pluginConfig.Name)
		}
		logging.Error(pluginCtx, "error creating registration token", "error", err, "response", response)
		metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx)
		state.requestReturnToMainQueue("error_creating_registration_token")
		return pluginCtx, nil
	}
	if runnerRegistration == nil || runnerRegistration.Token == nil || *runnerRegistration.Token == "" {
		logging.Error(pluginCtx, "registration token is empty; something wrong with github or your service token", "response", response)
		state.requestReturnToMainQueue("registration_token_empty")
		return pluginCtx, nil
	}
	registrationToken := *runnerRegistration.Token

	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		return pluginCtx, fmt.Errorf("context canceled before ObtainAnkaVM")
	}

	// Obtain Anka VM (template is reference-counted in-use during clone inside ObtainAnkaVM).
	vm, obtainErr := ankaCLI.ObtainAnkaVM(workerCtx, pluginCtx, ankaTemplateUUID)
	if vm != nil {
		queuedJob.AnkaVM.Name = vm.Name
	}

	var wrappedVmJSON []byte
	if vm != nil {
		wrappedVM := internalGithub.QueueJob{
			Type:        "anka.VM",
			AnkaVM:      queuedJob.AnkaVM,
			WorkflowJob: queuedJob.WorkflowJob,
		}
		var marshalErr error
		wrappedVmJSON, marshalErr = json.Marshal(wrappedVM)
		if marshalErr != nil {
			h.deleteVMWithBackgroundCtx(workerCtx, pluginCtx, vm.Name)
			state.requestReturnToMainQueue("error_marshalling_vm_to_json")
			return pluginCtx, fmt.Errorf("error marshalling vm to json: %w", marshalErr)
		}
	}

	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		if vm != nil {
			h.deleteVMWithBackgroundCtx(workerCtx, pluginCtx, vm.Name)
		}
		return pluginCtx, fmt.Errorf("context canceled after ObtainAnkaVM")
	}

	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(false)
	pluginCtx = logging.AppendCtx(pluginCtx, slog.Any("vm", queuedJob.AnkaVM))

	if vm != nil {
		if _, dbErr := databaseContainer.RetryRPush(pluginCtx, queues.pluginQueue, wrappedVmJSON); dbErr != nil {
			h.deleteVMWithBackgroundCtx(workerCtx, pluginCtx, vm.Name)
			state.requestReturnToMainQueue("error_pushing_vm_data_to_database")
			return pluginCtx, fmt.Errorf("error pushing vm data to database: %w", dbErr)
		}
	}
	if obtainErr != nil {
		logging.Error(pluginCtx, "error obtaining anka vm", "error", obtainErr)
		state.requestReturnToMainQueue("error_obtaining_anka_vm")
		return pluginCtx, nil
	}
	if vm == nil {
		state.requestReturnToMainQueue("error_obtaining_anka_vm")
		return pluginCtx, nil
	}

	// Copy runner scripts into VM.
	logging.Debug(pluginCtx, "copying install-runner.bash, register-runner.bash, and start-runner.bash to vm")
	if copyErr := ankaCLI.AnkaCopyIntoVM(workerCtx, pluginCtx, queuedJob.AnkaVM.Name, installRunnerPath, registerRunnerPath, startRunnerPath); copyErr != nil {
		metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx)
		state.requestReturnToMainQueue("error_executing_anka_copy")
		return pluginCtx, fmt.Errorf("error executing anka copy: %w", copyErr)
	}

	if h.checkInterruptedBeforeStep(pluginCtx, state, "installing runner") {
		return pluginCtx, nil
	}

	logging.Info(pluginCtx, "installing github runner inside of vm")
	if err := ankaCLI.AnkaRun(pluginCtx, queuedJob.AnkaVM.Name, "./install-runner.bash"); err != nil {
		logging.Error(pluginCtx, "error executing install-runner.bash", "error", err)
		state.requestReturnToMainQueue("error_executing_install_runner_bash")
		return pluginCtx, nil
	}

	if h.checkInterruptedBeforeStep(pluginCtx, state, "registering runner") {
		return pluginCtx, nil
	}

	logging.Info(pluginCtx, "registering github runner inside of vm", "queuedJob", queuedJob)
	registerArgs := []string{
		"./register-runner.bash",
		queuedJob.AnkaVM.Name,
		registrationToken,
		repositoryURL,
		strings.Join(queuedJob.WorkflowJob.Labels, ","),
	}
	if pluginConfig.RunnerGroup != "" {
		registerArgs = append(registerArgs, pluginConfig.RunnerGroup)
	}
	registerErr, registerTimeoutErr := ankaCLI.AnkaRunWithTimeoutRedacted(
		pluginCtx,
		60,
		queuedJob.AnkaVM.Name,
		[]string{registrationToken},
		registerArgs...,
	)
	if registerTimeoutErr != nil {
		logging.Error(pluginCtx, "timeout executing register-runner.bash", "error", registerTimeoutErr)
		state.requestReturnToMainQueue("timeout_executing_register_runner_bash")
		return pluginCtx, nil
	}
	if registerErr != nil {
		state.requestReturnToMainQueue("error_executing_register_runner_bash")
		return pluginCtx, fmt.Errorf("error executing register-runner.bash: %w", registerErr)
	}

	defer h.removeSelfHostedRunner(workerCtx, pluginCtx, queues.pluginQueue)

	queuedJob.Action = "registered"
	if updateErr := internalGithub.UpdateJobInDB(pluginCtx, queues.pluginQueue, &queuedJob); updateErr != nil {
		if errors.Is(updateErr, internalGithub.ErrJobAlreadyCompleted) {
			return pluginCtx, nil
		}
		logging.Error(pluginCtx, "error updating job in db", "error", updateErr)
	}

	if h.checkInterruptedBeforeStep(pluginCtx, state, "starting runner") {
		return pluginCtx, nil
	}

	logging.Info(pluginCtx, "starting github runner inside of vm")
	if err := ankaCLI.AnkaRun(pluginCtx, queuedJob.AnkaVM.Name, "./start-runner.bash"); err != nil {
		state.requestReturnToMainQueue("error_executing_start_runner_bash")
		return pluginCtx, fmt.Errorf("error executing start-runner.bash: %w", err)
	}

	if h.checkInterruptedBeforeStep(pluginCtx, state, "jobCompleted checks") {
		return pluginCtx, nil
	}

	logging.Info(pluginCtx, "finished preparing anka VM with actions runner")
	queuedJob.Action = "in_progress"
	if updateErr := internalGithub.UpdateJobInDB(pluginCtx, queues.pluginQueue, &queuedJob); updateErr != nil {
		if errors.Is(updateErr, internalGithub.ErrJobAlreadyCompleted) {
			return pluginCtx, nil
		}
		logging.Error(pluginCtx, "error updating job in db", "error", updateErr)
	}

	pluginCtx, err = h.watchJobStatus(workerCtx, pluginCtx, queues, state)
	if err != nil {
		logging.Error(pluginCtx, "error watching for job completion", "error", err)
		return pluginCtx, err
	}
	return pluginCtx, nil
}

func (h *Handler) checkInterruptedBeforeStep(pluginCtx context.Context, state *handlerState, stepName string) bool {
	select {
	case job := <-state.jobEvents:
		logging.Info(pluginCtx, "completed job found before "+stepName, "job", job)
		return true
	case <-pluginCtx.Done():
		logging.Warn(pluginCtx, "context canceled before "+stepName)
		return true
	default:
		return false
	}
}
