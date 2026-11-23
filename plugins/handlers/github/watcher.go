package github

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-github/v75/github"
	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
)

// handlerState coordinates a single Handler.Run execution with its background
// completion watcher and deferred cleanup without bidirectional channel deadlocks.
type handlerState struct {
	firstCheckRan            atomic.Bool
	checkRunCount            atomic.Int32
	stillInProgressPollCount atomic.Int32
	pausedTakenByOtherHost   atomic.Bool

	cleanupMu sync.Mutex

	// jobEvents is a single-producer (watcher) -> single-consumer (Run/watchJobStatus) channel.
	jobEvents chan internalGithub.QueueJob

	returnMu     sync.Mutex
	returnReason string
}

func newHandlerState() *handlerState {
	return &handlerState{
		jobEvents: make(chan internalGithub.QueueJob, 1),
	}
}

func (s *handlerState) publishEvent(job internalGithub.QueueJob) {
	select {
	case <-s.jobEvents:
	default:
	}
	select {
	case s.jobEvents <- job:
	default:
	}
}

func (s *handlerState) requestReturnToMainQueue(reason string) {
	s.returnMu.Lock()
	defer s.returnMu.Unlock()
	if s.returnReason == "" {
		s.returnReason = reason
	}
}

func (s *handlerState) takeReturnReason() string {
	s.returnMu.Lock()
	defer s.returnMu.Unlock()
	reason := s.returnReason
	s.returnReason = ""
	return reason
}

func watchJobIsTerminal(status *string) bool {
	return status != nil && (*status == "completed" || *status == "failed" || *status == "timed_out")
}

func statusString(status *string) string {
	if status == nil {
		return ""
	}
	return *status
}

func handleWatchJobCompleted(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginConfig config.Plugin,
	metricsData *metrics.MetricsDataLock,
	queuedJob internalGithub.QueueJob,
) (context.Context, error) {
	logging.Info(pluginCtx, "job completed",
		"job_id", queuedJob.WorkflowJob.ID,
		"conclusion", queuedJob.WorkflowJob.Conclusion,
	)
	if metricsData == nil {
		var err error
		metricsData, err = metrics.GetMetricsDataFromContext(workerCtx)
		if err != nil {
			return pluginCtx, err
		}
	}
	if queuedJob.WorkflowJob.Conclusion == nil {
		return pluginCtx, nil
	}
	switch *queuedJob.WorkflowJob.Conclusion {
	case "success", "":
		metricsData.IncrementTotalSuccessfulRunsSinceStart(workerCtx, pluginCtx)
		if queuedJob.WorkflowJob.HTMLURL == nil {
			return pluginCtx, nil
		}
		if err := metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.Plugin{
			PluginBase: metrics.PluginBase{
				Name: pluginConfig.Name,
			},
			LastSuccessfulRun:       time.Now(),
			LastSuccessfulRunJobUrl: *queuedJob.WorkflowJob.HTMLURL,
		}); err != nil {
			return pluginCtx, err
		}
	case "failure":
		metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx)
		if queuedJob.WorkflowJob.HTMLURL == nil {
			return pluginCtx, nil
		}
		if err := metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.Plugin{
			PluginBase: metrics.PluginBase{
				Name: pluginConfig.Name,
			},
			LastFailedRun:       time.Now(),
			LastFailedRunJobUrl: *queuedJob.WorkflowJob.HTMLURL,
		}); err != nil {
			return pluginCtx, err
		}
	}
	return pluginCtx, nil
}

// syncJobInDBBeforeWatchComplete persists terminal job state when watchJobStatus exits.
// ErrJobAlreadyCompleted means checkForCompletedJobs updated it first and is treated as success.
func syncJobInDBBeforeWatchComplete(
	pluginCtx context.Context,
	pluginQueueName string,
	queuedJob *internalGithub.QueueJob,
) error {
	err := internalGithub.UpdateJobInDB(pluginCtx, pluginQueueName, queuedJob)
	if errors.Is(err, internalGithub.ErrJobAlreadyCompleted) {
		logging.Debug(
			pluginCtx,
			"job already completed in DB before watchJobStatus exit",
			"job_id", queuedJob.WorkflowJob.ID,
		)
		return nil
	}
	if err != nil {
		logging.Error(pluginCtx, "error updating job in db before watch completion", "error", err)
		return err
	}
	return nil
}

func (h *Handler) watchJobStatus(
	workerCtx context.Context,
	pluginCtx context.Context,
	queues queueNames,
	state *handlerState,
) (context.Context, error) {
	logging.Debug(pluginCtx, "starting watchJobStatus")
	pluginConfig := h.deps.PluginConfig
	databaseContainer := h.deps.Database

	logCounter := 0
	alreadyLogged := false
	const jobStatusCheckIntervalSeconds = 3
	previousStatus := ""
	previousConclusion := ""
	previousRegistrationState := ""
	pollInterval := time.Duration(jobStatusCheckIntervalSeconds) * time.Second
	pollTimer := time.NewTimer(0)
	defer pollTimer.Stop()

	for {
		logging.Debug(pluginCtx, "watchJobStatus loop iteration", "logCounter", logCounter)
		if workerCtx.Err() != nil || pluginCtx.Err() != nil {
			logging.Warn(pluginCtx, "context canceled while watching for job completion")
			return pluginCtx, nil
		}

		select {
		case <-workerCtx.Done():
			logging.Warn(pluginCtx, "context canceled while watching for job completion")
			return pluginCtx, nil
		case <-pluginCtx.Done():
			logging.Warn(pluginCtx, "context canceled while watching for job completion")
			return pluginCtx, nil
		case chJob := <-state.jobEvents:
			if chJob.Action == "finish" {
				return pluginCtx, nil
			}
			if chJob.Action == "timed_out" {
				logging.Error(pluginCtx, "job timed out", "job_id", chJob.WorkflowJob.ID)
				return pluginCtx, nil
			}
			if watchJobIsTerminal(chJob.WorkflowJob.Status) {
				if err := syncJobInDBBeforeWatchComplete(pluginCtx, queues.pluginQueue, &chJob); err != nil {
					return pluginCtx, err
				}
				return handleWatchJobCompleted(workerCtx, pluginCtx, pluginConfig, h.deps.Metrics, chJob)
			}
			if !pollTimer.Stop() {
				select {
				case <-pollTimer.C:
				default:
				}
			}
			pollTimer.Reset(pollInterval)
		case <-pollTimer.C:
			queuedJobJSON, err := databaseContainer.RetryLIndex(pluginCtx, queues.pluginQueue, 0)
			if err != nil {
				logging.Error(pluginCtx, "error searching in queue", "error", err)
				return pluginCtx, err
			}
			queuedJob, err := database.Unwrap[internalGithub.QueueJob](queuedJobJSON)
			if err != nil {
				logging.Error(pluginCtx, "error unmarshalling job", "error", err, "queuedJobJSON", queuedJobJSON)
				return pluginCtx, err
			}
			if queuedJob.WorkflowJob.ID == nil {
				logging.Warn(pluginCtx, "watchJobStatus -> queued job missing ID", "raw_payload", queuedJobJSON)
				pollTimer.Reset(pollInterval)
				continue
			}
			if queuedJob.Action == "timed_out" {
				logging.Error(pluginCtx, "job timed out", "job_id", *queuedJob.WorkflowJob.ID)
				return pluginCtx, nil
			}

			currentStatus := statusString(queuedJob.WorkflowJob.Status)
			if currentStatus != previousStatus {
				logging.Debug(
					pluginCtx,
					"watchJobStatus -> observed status transition from queue snapshot",
					"job_id", *queuedJob.WorkflowJob.ID,
					"attempts", queuedJob.Attempts,
					"previous_status", previousStatus,
					"new_status", currentStatus,
					"raw_payload", queuedJobJSON,
				)
				previousStatus = currentStatus
			}
			currentConclusion := statusString(queuedJob.WorkflowJob.Conclusion)
			if currentConclusion != "" && currentConclusion != previousConclusion {
				logging.Debug(
					pluginCtx,
					"watchJobStatus -> observed conclusion transition from queue snapshot",
					"job_id", *queuedJob.WorkflowJob.ID,
					"attempts", queuedJob.Attempts,
					"previous_conclusion", previousConclusion,
					"new_conclusion", currentConclusion,
					"raw_payload", queuedJobJSON,
				)
			}
			previousConclusion = currentConclusion
			if watchJobIsTerminal(queuedJob.WorkflowJob.Status) {
				return handleWatchJobCompleted(workerCtx, pluginCtx, pluginConfig, h.deps.Metrics, queuedJob)
			}

			mainInProgressQueueJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *queuedJob.WorkflowJob.ID, queues.mainInProgress)
			if err != nil {
				logging.Error(pluginCtx, "error searching in queue", "error", err)
				return pluginCtx, err
			}
			if mainInProgressQueueJobJSON != "" {
				if previousRegistrationState != "in_progress" {
					logging.Debug(
						pluginCtx,
						"watchJobStatus -> job present in mainInProgressQueue",
						"job_id", *queuedJob.WorkflowJob.ID,
						"previous_registration_state", previousRegistrationState,
						"new_registration_state", "in_progress",
						"queue_payload", mainInProgressQueueJobJSON,
					)
					previousRegistrationState = "in_progress"
					logging.Info(pluginCtx, "job found registered runner and is now in progress", "job_id", *queuedJob.WorkflowJob.ID)
				} else if logCounter%2 == 0 {
					logging.Info(pluginCtx, "job found registered runner and is now in progress", "job_id", *queuedJob.WorkflowJob.ID)
				}
			} else {
				if previousRegistrationState != "waiting" {
					logging.Debug(
						pluginCtx,
						"watchJobStatus -> job not present in mainInProgressQueue",
						"job_id", *queuedJob.WorkflowJob.ID,
						"previous_registration_state", previousRegistrationState,
						"new_registration_state", "waiting",
					)
					previousRegistrationState = "waiting"
				}
				if !alreadyLogged {
					logging.Info(pluginCtx, "job is waiting for registered runner", "job_id", *queuedJob.WorkflowJob.ID)
					alreadyLogged = true
				}
			}

			registrationTimeoutSeconds := pluginConfig.RegistrationTimeoutSeconds
			if registrationTimeoutSeconds <= 0 {
				registrationTimeoutSeconds = 80
			}
			if logCounter == registrationTimeoutSeconds/jobStatusCheckIntervalSeconds && mainInProgressQueueJobJSON == "" {
				if watchJobIsTerminal(queuedJob.WorkflowJob.Status) {
					if err := syncJobInDBBeforeWatchComplete(pluginCtx, queues.pluginQueue, &queuedJob); err != nil {
						return pluginCtx, err
					}
					return handleWatchJobCompleted(workerCtx, pluginCtx, pluginConfig, h.deps.Metrics, queuedJob)
				}
				logging.Error(pluginCtx, "waiting for runner registration timed out, will retry")
				queuedJob.Action = "remove_self_hosted_runner"
				if err := syncJobInDBBeforeWatchComplete(pluginCtx, queues.pluginQueue, &queuedJob); err != nil {
					return pluginCtx, err
				}
				state.requestReturnToMainQueue("waiting_for_runner_registration_timed_out")
				return pluginCtx, nil
			}
			logCounter++
			pollTimer.Reset(pollInterval)
		}
	}
}

// checkForCompletedJobs monitors Redis queues for completion or in_progress updates
// on the active job and exits promptly whenever watcherCtx, workerCtx, or pluginCtx cancels.
func (h *Handler) checkForCompletedJobs(
	watcherCtx context.Context,
	workerCtx context.Context,
	pluginCtx context.Context,
	queues queueNames,
	state *handlerState,
) {
	logging.Debug(pluginCtx, "starting checkForCompletedJobs")

	baseCtx := logging.AppendCtx(pluginCtx, slog.Int("check_id", rand.Intn(100)))
	startTime := time.Now()
	const timeoutDuration = 6 * time.Hour

	workerGlobals := h.deps.Globals
	pluginConfig := h.deps.PluginConfig
	databaseContainer := h.deps.Database
	if workerGlobals == nil || databaseContainer == nil {
		logging.Error(baseCtx, "checkForCompletedJobs missing required dependencies")
		state.firstCheckRan.Store(true)
		return
	}

	// Non-canceled context carrying logger so completed-job queue handoff finishes cleanly.
	dbWriteCtx := context.Background()
	if logger, err := logging.GetLoggerFromContext(baseCtx); err == nil {
		dbWriteCtx = context.WithValue(dbWriteCtx, config.ContextKey("logger"), logger)
	}

	defer func() {
		logging.Debug(baseCtx, "ending checkForCompletedJobs")
		state.firstCheckRan.Store(true)
	}()

	sleepTimer := time.NewTimer(0)
	if !sleepTimer.Stop() {
		<-sleepTimer.C
	}
	defer sleepTimer.Stop()

	for {
		if watcherCtx.Err() != nil || workerCtx.Err() != nil || pluginCtx.Err() != nil {
			logging.Debug(baseCtx, "context canceled in checkForCompletedJobs loop")
			return
		}

		checkCtx := baseCtx
		updateDB := false
		hasJob := false
		state.checkRunCount.Add(1)

		if workerGlobals.ReturnAllToMainQueue.Load() {
			logging.Warn(checkCtx, "main worker is attempting to return all jobs to main queue")
			state.requestReturnToMainQueue("return_all_to_main_queue")
			state.publishEvent(internalGithub.QueueJob{Action: "finish"})
			return
		}

		var queuedJob internalGithub.QueueJob
		existingJobString, err := databaseContainer.RetryLIndex(checkCtx, queues.pluginQueue, 0)
		if err == redis.Nil || existingJobString == "" {
			state.stillInProgressPollCount.Store(0)
		} else if err != nil {
			logging.Error(checkCtx, "error reading plugin queue head", "error", err)
			return
		} else {
			hasJob = true
			queuedJob, err = database.Unwrap[internalGithub.QueueJob](existingJobString)
			if err != nil {
				logging.Error(checkCtx, "error unmarshalling job", "error", err, "queuedJob", queuedJob)
				return
			}
			if queuedJob.WorkflowJob.ID == nil {
				logging.Warn(checkCtx, "checkForCompletedJobs -> queued job missing ID", "queue", queues.pluginQueue, "raw_payload", existingJobString)
				return
			}
			logging.Debug(checkCtx, "checkForCompletedJobs -> queued job found in pluginQueue", "queuedJob", queuedJob)

			jobAttrs := []slog.Attr{
				slog.Int64("workflowJobID", *queuedJob.WorkflowJob.ID),
			}
			if queuedJob.WorkflowJob.RunID != nil {
				jobAttrs = append(jobAttrs, slog.Int64("workflowJobRunID", *queuedJob.WorkflowJob.RunID))
			}
			if queuedJob.WorkflowJob.WorkflowName != nil {
				jobAttrs = append(jobAttrs, slog.String("workflowName", *queuedJob.WorkflowJob.WorkflowName))
			}
			if queuedJob.WorkflowJob.HTMLURL != nil {
				jobAttrs = append(jobAttrs, slog.String("workflowJobURL", *queuedJob.WorkflowJob.HTMLURL))
			}
			if queuedJob.WorkflowJob.Status != nil {
				jobAttrs = append(jobAttrs, slog.String("workflowJobStatus", *queuedJob.WorkflowJob.Status))
			}
			checkCtx = logging.AppendCtx(checkCtx, jobAttrs...)
			logging.Debug(checkCtx, "context updated with job info")

			if state.checkRunCount.Load()%10 == 0 {
				logging.Info(checkCtx, "checking for completed jobs")
			}

			logging.Debug(
				checkCtx,
				"checkForCompletedJobs -> evaluating job from plugin queue",
				"queue", queues.pluginQueue,
				"raw_payload", existingJobString,
			)

			mainInProgressQueueJobJSON, err := internalGithub.GetJobJSONFromQueueByID(checkCtx, *queuedJob.WorkflowJob.ID, queues.mainInProgress)
			if err != nil {
				logging.Error(checkCtx, "error searching in queue", "error", err)
				return
			}
			if mainInProgressQueueJobJSON != "" {
				logging.Debug(
					checkCtx,
					"checkForCompletedJobs -> matched job in mainInProgressQueue",
					"queue", queues.mainInProgress,
					"run_count", state.checkRunCount.Load(),
					"raw_payload", mainInProgressQueueJobJSON,
				)
				inProgressPollCount := state.stillInProgressPollCount.Add(1)
				if inProgressPollCount%5 == 0 || inProgressPollCount == 1 {
					logging.Info(checkCtx, "job is still in progress",
						"run_count", state.checkRunCount.Load(),
						"in_progress_poll_count", inProgressPollCount,
					)
				}
				mainInProgressQueueJob, unwrapErr := database.Unwrap[internalGithub.QueueJob](mainInProgressQueueJobJSON)
				if unwrapErr != nil {
					logging.Error(checkCtx, "error unmarshalling job", "error", unwrapErr, "mainInProgressQueueJobJSON", mainInProgressQueueJobJSON)
					return
				}
				if statusString(queuedJob.WorkflowJob.Status) != statusString(mainInProgressQueueJob.WorkflowJob.Status) {
					updateDB = true
				}
				queuedJob.Action = "in_progress"
				queuedJob.WorkflowJob.Status = github.Ptr("in_progress")
			} else {
				state.stillInProgressPollCount.Store(0)
			}

			var mainCompletedQueueJobJSON string
			pluginCompletedQueueJobJSON, err := internalGithub.GetJobJSONFromQueueByID(checkCtx, *queuedJob.WorkflowJob.ID, queues.pluginCompleted)
			if err != nil {
				logging.Error(checkCtx, "error searching in queue", "error", err)
				return
			}
			if pluginCompletedQueueJobJSON == "" {
				mainCompletedQueueJobJSON, err = internalGithub.GetJobJSONFromQueueByID(checkCtx, *queuedJob.WorkflowJob.ID, queues.mainCompleted)
				if err != nil {
					logging.Error(checkCtx, "error searching in queue", "error", err)
					return
				}
			}

			if pluginCompletedQueueJobJSON != "" {
				logging.Info(checkCtx, "checkForCompletedJobs -> found completed job in pluginCompletedQueue")
				logging.Debug(
					checkCtx,
					"checkForCompletedJobs -> pluginCompletedQueue payload",
					"queue", queues.pluginCompleted,
					"raw_payload", pluginCompletedQueueJobJSON,
				)
				queuedJob.WorkflowJob.Status = github.Ptr("completed")
				queuedJob.Action = "finish"
				state.publishEvent(queuedJob)
				updateDB = true
			}

			if mainCompletedQueueJobJSON != "" {
				mainCompletedQueueLength, lenErr := databaseContainer.RetryLLen(checkCtx, queues.mainCompleted)
				if lenErr != nil {
					logging.Error(checkCtx, "error checking mainCompletedQueue length", "error", lenErr)
					return
				}
				logging.Debug(checkCtx, "checkForCompletedJobs -> job is in mainCompletedQueue")
				logging.Debug(
					checkCtx,
					"checkForCompletedJobs -> mainCompletedQueue payload",
					"queue", queues.mainCompleted,
					"queue_length", mainCompletedQueueLength,
					"raw_payload", mainCompletedQueueJobJSON,
				)

				success, remErr := databaseContainer.RetryLRem(dbWriteCtx, queues.mainCompleted, 1, mainCompletedQueueJobJSON)
				if remErr != nil || success != 1 {
					logging.Error(checkCtx,
						"error removing completedJob from "+queues.mainCompleted,
						"error", remErr,
						"removed", success,
						"mainCompletedQueueJobJSON", mainCompletedQueueJobJSON,
					)
					return
				}

				completedQueuedJob, unwrapErr := database.Unwrap[internalGithub.QueueJob](mainCompletedQueueJobJSON)
				if unwrapErr != nil {
					logging.Error(checkCtx, "error unmarshalling job", "error", unwrapErr, "queuedJob", queuedJob)
					return
				}
				if completedQueuedJob.WorkflowJob.ID != nil && *completedQueuedJob.WorkflowJob.ID != *queuedJob.WorkflowJob.ID {
					logging.Warn(
						checkCtx,
						"checkForCompletedJobs -> completed job id mismatch",
						"expected_job_id", *queuedJob.WorkflowJob.ID,
						"received_job_id", *completedQueuedJob.WorkflowJob.ID,
					)
				}
				queuedJob.WorkflowJob.Status = completedQueuedJob.WorkflowJob.Status
				queuedJob.WorkflowJob.Conclusion = completedQueuedJob.WorkflowJob.Conclusion
				logging.Info(checkCtx, "checkForCompletedJobs -> found completed job in mainCompletedQueue, adding to pluginCompletedQueue", "completedQueuedJob", completedQueuedJob)
				if _, pushErr := databaseContainer.RetryLPush(dbWriteCtx, queues.pluginCompleted, mainCompletedQueueJobJSON); pushErr != nil {
					logging.Error(checkCtx, "error inserting completed job into list", "error", pushErr)
					return
				}
				if workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].PluginRunCount.Load()%2 == 0 {
					workerGlobals.ResetQueueTargetIndex()
				}
				state.publishEvent(queuedJob)
				updateDB = true
			}

			// On first check, if job is in_progress without a completed event, confirm status via GitHub API.
			if !state.firstCheckRan.Load() &&
				mainCompletedQueueJobJSON == "" && pluginCompletedQueueJobJSON == "" &&
				mainInProgressQueueJobJSON != "" {
				logging.Debug(checkCtx, "checkForCompletedJobs -> job is in mainInProgressQueue, checking status from API")
				queuedJob, err = internalGithub.UpdateJobsWorkflowJobStatus(workerCtx, checkCtx, &queuedJob)
				if err != nil {
					logging.Error(checkCtx, "error checking workflow job status", "error", err)
					return
				}
				if queuedJob.WorkflowJob.Status != nil &&
					(*queuedJob.WorkflowJob.Status == "completed" ||
						*queuedJob.WorkflowJob.Status == "failed" ||
						*queuedJob.WorkflowJob.Status == "cancelled") {
					queuedJobJSON, marshalErr := json.Marshal(queuedJob)
					if marshalErr != nil {
						logging.Error(checkCtx, "error marshalling queued job", "error", marshalErr)
						return
					}
					if _, pushErr := databaseContainer.RetryLPush(dbWriteCtx, queues.pluginCompleted, queuedJobJSON); pushErr != nil {
						logging.Error(checkCtx, "error inserting completed job into list", "error", pushErr)
						return
					}
				}
				if queuedJob.WorkflowJob.Status != nil && *queuedJob.WorkflowJob.Status == "in_progress" {
					queuedJob.Action = "in_progress"
				}
				state.publishEvent(queuedJob)
				updateDB = true
			}

			if updateDB {
				updateErr := internalGithub.UpdateJobInDB(checkCtx, queues.pluginQueue, &queuedJob)
				if errors.Is(updateErr, internalGithub.ErrJobAlreadyCompleted) {
					return
				}
				if updateErr != nil {
					logging.Error(checkCtx, "error updating job in db", "error", updateErr)
				}
			}
		}

		elapsed := time.Since(startTime)
		if elapsed >= timeoutDuration && hasJob {
			logging.Warn(
				checkCtx,
				"checkForCompletedJobs timeout exceeded, cleaning up potentially orphaned job",
				"elapsed_hours", elapsed.Hours(),
				"timeout_hours", timeoutDuration.Hours(),
			)
			queuedJob.Action = "timed_out"
			queuedJob.WorkflowJob.Conclusion = github.Ptr("timed_out")
			queuedJob.WorkflowJob.Status = github.Ptr("timed_out")
			if updateErr := internalGithub.UpdateJobInDB(checkCtx, queues.pluginQueue, &queuedJob); updateErr != nil && !errors.Is(updateErr, internalGithub.ErrJobAlreadyCompleted) {
				logging.Error(checkCtx, "error updating timed out job in db", "error", updateErr)
			}
			state.publishEvent(queuedJob)
			return
		}

		state.firstCheckRan.Store(true)

		sleepTimer.Reset(3 * time.Second)
		select {
		case <-watcherCtx.Done():
			return
		case <-workerCtx.Done():
			return
		case <-pluginCtx.Done():
			return
		case <-sleepTimer.C:
		}
	}
}
