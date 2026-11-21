package github

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
)

// cleanup pops items from pluginQueueName (newest first) and cleans up Anka VMs
// and job state safely using an independent non-canceled context.
func (h *Handler) cleanup(
	workerCtx context.Context,
	pluginCtx context.Context,
	queues queueNames,
	state *handlerState,
	onStartRun bool,
) {
	cleanupContext := context.Background()
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return
	}
	cleanupContext = context.WithValue(cleanupContext, config.ContextKey("logger"), logger)

	pluginConfig := h.deps.PluginConfig
	workerGlobals := h.deps.Globals
	serviceDatabase := h.deps.Database
	ankaCLI := h.deps.AnkaCLI
	if workerGlobals == nil || serviceDatabase == nil {
		return
	}

	state.cleanupMu.Lock()
	defer func() {
		state.cleanupMu.Unlock()
		if !onStartRun {
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
		}
		if !workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].FinishedInitialRun.Load() {
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].FinishedInitialRun.Store(true)
		}
		if workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Load() {
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(false)
		}
	}()

	// Fast-fail copy of the existing pooled database client (0 retries during cleanup).
	cleanupDbCopy := *serviceDatabase
	cleanupDbCopy.MaxRetries = 0
	databaseContainer := &cleanupDbCopy
	cleanupContext = context.WithValue(cleanupContext, config.ContextKey("database"), databaseContainer)

	originalJobJSON, err := databaseContainer.RetryLIndex(cleanupContext, queues.pluginQueue, 0)
	if err != nil && err != redis.Nil {
		logging.Warn(pluginCtx, "unable to get job from database during cleanup, skipping", "error", err)
		return
	}
	if err == redis.Nil || originalJobJSON == "" {
		logging.Debug(pluginCtx, "cleanup | nothing to clean up (or redis returned nil)")
		return
	}

	logging.Info(pluginCtx, "cleanup | started", "onStartRun", onStartRun)
	originalQueuedJob, err := database.Unwrap[internalGithub.QueueJob](originalJobJSON)
	if err != nil {
		logging.Error(pluginCtx, "error unmarshalling job", "error", err, "originalJobJSON", originalJobJSON)
		return
	}

	if state.pausedTakenByOtherHost.Load() {
		logging.Debug(pluginCtx, "cleanup | paused job picked up by another host")
		if originalQueuedJob.WorkflowJob.ID != nil {
			if delErr := internalGithub.DeleteFromQueue(cleanupContext, *originalQueuedJob.WorkflowJob.ID, queues.paused); delErr != nil {
				logging.Error(pluginCtx, "error deleting from paused queue", "error", delErr)
			}
		}
		return
	}

	if originalQueuedJob.WorkflowJob.Status != nil &&
		(*originalQueuedJob.WorkflowJob.Status == "in_progress" ||
			(workerGlobals.ReturnAllToMainQueue.Load() && originalQueuedJob.Action == "in_progress")) {
		logging.Warn(pluginCtx, "cleanup | job is still running; skipping cleanup")
		return
	}

	cleaningQueue := queues.pluginQueue + "/cleaning"
	for {
		cleaningJobJSON, err := databaseContainer.RetryLIndex(cleanupContext, cleaningQueue, 0)
		if err != nil && err != redis.Nil {
			logging.Error(pluginCtx, "error getting job from the list", "error", err)
			return
		}
		if cleaningJobJSON == "" {
			if onStartRun {
				return
			}
			cleaningJobJSON, err = databaseContainer.RetryRPopLPush(cleanupContext, queues.pluginQueue, cleaningQueue)
			if err == redis.Nil {
				logging.Debug(pluginCtx, "cleanup | no job to clean up from "+queues.pluginQueue)
				return
			}
			if err != nil {
				logging.Error(pluginCtx, "error popping job from the list", "error", err)
				return
			}
		}

		queuedJob, err := database.Unwrap[internalGithub.QueueJob](cleaningJobJSON)
		if err != nil {
			logging.Error(pluginCtx, "error unmarshalling job", "error", err, "cleaningJobJSON", cleaningJobJSON)
			return
		}

		switch queuedJob.Type {
		case "anka.VM":
			logging.Info(pluginCtx, "cleanup | anka.VM | queuedJob", "queuedJob", queuedJob)
			if ankaCLI == nil {
				logging.Error(pluginCtx, "ankaCLI is nil during VM cleanup")
				return
			}
			if level := strings.ToUpper(os.Getenv("LOG_LEVEL")); level == "DEBUG" || level == "DEV" {
				diagDir, tmpErr := os.MkdirTemp("", "anklet-diag-"+queuedJob.AnkaVM.Name+"-*")
				if tmpErr == nil {
					_ = os.Chmod(diagDir, 0o700)
					if copyErr := ankaCLI.AnkaCopyOutOfVM(cleanupContext, queuedJob.AnkaVM.Name, "/Users/anka/actions-runner/_diag", diagDir); copyErr != nil {
						logging.Warn(pluginCtx, "error copying actions runner diag out of vm", "error", copyErr)
						_ = os.RemoveAll(diagDir)
					}
				}
			}
			if delErr := ankaCLI.AnkaDelete(workerCtx, cleanupContext, queuedJob.AnkaVM.Name); delErr != nil {
				logging.Error(pluginCtx, "error deleting vm", "error", delErr)
			}
			if _, delErr := databaseContainer.RetryDel(cleanupContext, cleaningQueue); delErr != nil {
				logging.Error(pluginCtx, "error deleting job from cleaning queue", "error", delErr)
				return
			}
			continue

		case "WorkflowJobPayload":
			logging.Info(pluginCtx, "cleanup | WorkflowJobPayload | queuedJob", "queuedJob", queuedJob)
			if queuedJob.WorkflowJob.ID != nil {
				if delErr := internalGithub.DeleteFromQueue(cleanupContext, *queuedJob.WorkflowJob.ID, queues.mainInProgress); delErr != nil {
					logging.Error(pluginCtx, "error deleting from in_progress queue", "error", delErr)
				}
			}

			if queuedJob.WorkflowJob.Status != nil && *queuedJob.WorkflowJob.Status == "completed" {
				logging.Debug(pluginCtx, "cleanup | removing completed job from database", "queuedJob", queuedJob)
				if queuedJob.WorkflowJob.ID != nil {
					mainCompletedJobJSON, findErr := internalGithub.GetJobJSONFromQueueByID(cleanupContext, *queuedJob.WorkflowJob.ID, queues.mainCompleted)
					if findErr != nil {
						logging.Error(pluginCtx, "error searching in queue", "error", findErr)
					}
					if mainCompletedJobJSON != "" {
						success, remErr := databaseContainer.RetryLRem(cleanupContext, queues.mainCompleted, 1, mainCompletedJobJSON)
						if remErr != nil || success != 1 {
							logging.Error(pluginCtx,
								"error removing completedJob from "+queues.mainCompleted,
								"error", remErr,
								"removed", success,
							)
							return
						}
					}
				}
				if _, delErr := databaseContainer.RetryDel(cleanupContext, queues.pluginCompleted); delErr != nil {
					logging.Error(pluginCtx, "error deleting job from completed queue", "error", delErr)
					return
				}
				if _, delErr := databaseContainer.RetryDel(cleanupContext, cleaningQueue); delErr != nil {
					logging.Error(pluginCtx, "error deleting job from cleaning queue", "error", delErr)
					return
				}
				return
			}

			if state.pausedTakenByOtherHost.Load() {
				if _, delErr := databaseContainer.RetryDel(cleanupContext, cleaningQueue); delErr != nil {
					logging.Error(pluginCtx, "error deleting job from cleaning queue", "error", delErr)
				}
				return
			}

			reason := state.takeReturnReason()
			if reason == "" && workerGlobals.ReturnAllToMainQueue.Load() {
				reason = "return_all_to_main_queue"
			}
			if reason != "" {
				logging.Warn(pluginCtx, "pushing job from "+cleaningQueue+" back to "+queues.mainQueue, "reason", reason)
				poppedJSON, popErr := databaseContainer.RetryRPop(cleanupContext, cleaningQueue)
				if popErr != nil {
					logging.Error(pluginCtx, "error removing job from cleaning queue", "error", popErr)
					return
				}
				cleaningQueuedJob, unwrapErr := database.Unwrap[internalGithub.QueueJob](poppedJSON)
				if unwrapErr != nil {
					logging.Error(pluginCtx, "error unmarshalling job", "error", unwrapErr, "cleaningJobJSON", poppedJSON)
					return
				}
				cleaningQueuedJob.Action = "queued"
				if reason != "not_enough_host_resources" && reason != "pull_already_running_on_host" {
					cleaningQueuedJob.Attempts++
				}
				marshaledJob, marshalErr := json.Marshal(cleaningQueuedJob)
				if marshalErr != nil {
					logging.Error(pluginCtx, "error marshalling job", "error", marshalErr)
					return
				}
				if _, pushErr := databaseContainer.RetryLPush(cleanupContext, queues.mainQueue, marshaledJob); pushErr != nil {
					logging.Error(pluginCtx, "error pushing job back to queued", "error", pushErr)
					return
				}
				if cleaningQueuedJob.WorkflowJob.ID != nil {
					if delErr := internalGithub.DeleteFromQueue(cleanupContext, *cleaningQueuedJob.WorkflowJob.ID, queues.paused); delErr != nil {
						logging.Warn(pluginCtx, "unable to delete from paused queue", "error", delErr)
					}
				}
				if _, delErr := databaseContainer.RetryDel(cleanupContext, cleaningQueue); delErr != nil {
					logging.Error(pluginCtx, "error deleting job from cleaning queue", "error", delErr)
				}
				return
			}

			if watchJobIsTerminal(queuedJob.WorkflowJob.Status) {
				if _, delErr := databaseContainer.RetryDel(cleanupContext, cleaningQueue); delErr != nil {
					logging.Error(pluginCtx, "error deleting job from cleaning queue", "error", delErr)
				}
				return
			}

			logging.Warn(pluginCtx, "pushing job back to "+queues.pluginQueue, "queuedJob", queuedJob)
			if _, pushErr := databaseContainer.RetryRPopLPush(cleanupContext, cleaningQueue, queues.pluginQueue); pushErr != nil {
				logging.Error(pluginCtx, "error pushing job back to queued", "error", pushErr)
				return
			}
			if _, delErr := databaseContainer.RetryDel(cleanupContext, cleaningQueue); delErr != nil {
				logging.Error(pluginCtx, "error deleting job from cleaning queue", "error", delErr)
			}
			return

		default:
			logging.Error(pluginCtx, "unknown job type", "job", queuedJob)
			return
		}
	}
}
