package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/go-github/v75/github"
	"github.com/redis/go-redis/v9"
	internalAnka "github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/database"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
)

func (h *Handler) requeuePausedJobIfActive(
	pluginCtx context.Context,
	queues queueNames,
	pausedQueuedJobString string,
) error {
	if pausedQueuedJobString == "" {
		return nil
	}
	databaseContainer := h.deps.Database
	pausedJob, parseErr := database.Unwrap[internalGithub.QueueJob](pausedQueuedJobString)
	if parseErr == nil && pausedJob.WorkflowJob.ID != nil && pausedJob.PausedOn != "" {
		origQueue := "anklet/jobs/github/queued/" + queues.owner + "/{" + pausedJob.PausedOn + "}"
		currentJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *pausedJob.WorkflowJob.ID, origQueue)
		if err == nil && currentJobJSON != "" {
			if currentJob, curErr := database.Unwrap[internalGithub.QueueJob](currentJobJSON); curErr == nil && watchJobIsTerminal(currentJob.WorkflowJob.Status) {
				logging.Info(pluginCtx, "not putting job back in paused queue because it's "+statusString(currentJob.WorkflowJob.Status), "workflowJobID", *pausedJob.WorkflowJob.ID)
				return nil
			}
		}
	}
	if _, err := databaseContainer.RetryLPush(pluginCtx, queues.paused, pausedQueuedJobString); err != nil {
		logging.Error(pluginCtx, "error pushing job to paused queue", "error", err)
		return fmt.Errorf("error pushing job to paused queue: %w", err)
	}
	logging.Debug(pluginCtx, "pushed job back to paused queue", "pausedQueuedJobString", pausedQueuedJobString)
	return nil
}

// claimPausedJob checks the shared paused queue for a job that another host paused
// due to resource contention and that this host can run immediately.
func (h *Handler) claimPausedJob(
	workerCtx context.Context,
	pluginCtx context.Context,
	queues queueNames,
) (context.Context, string, internalGithub.QueueJob, bool, error) {
	databaseContainer := h.deps.Database

	var pausedQueueTargetIndex int64
	for {
		if workerCtx.Err() != nil || pluginCtx.Err() != nil {
			logging.Warn(pluginCtx, "context canceled before check for paused jobs")
			return pluginCtx, "", internalGithub.QueueJob{}, false, nil
		}

		pausedQueueLength, err := databaseContainer.RetryLLen(pluginCtx, queues.paused)
		if err != nil {
			return pluginCtx, "", internalGithub.QueueJob{}, false, fmt.Errorf("error getting paused queue length: %w", err)
		}
		if pausedQueueLength == 0 || pausedQueueTargetIndex >= pausedQueueLength {
			return pluginCtx, "", internalGithub.QueueJob{}, false, nil
		}

		pausedQueuedJobString, err := internalGithub.PopJobOffQueue(pluginCtx, queues.paused, pausedQueueTargetIndex)
		if err != nil {
			return pluginCtx, "", internalGithub.QueueJob{}, false, fmt.Errorf("error getting paused jobs: %w", err)
		}
		if pausedQueuedJobString == "" {
			return pluginCtx, "", internalGithub.QueueJob{}, false, nil
		}

		claimed, nextCtx, jobJSON, job, claimErr := h.tryTakeOverPausedJob(pluginCtx, queues, pausedQueuedJobString)
		if claimErr != nil {
			_ = h.requeuePausedJobIfActive(pluginCtx, queues, pausedQueuedJobString)
			return nextCtx, "", internalGithub.QueueJob{}, false, claimErr
		}
		if claimed {
			return nextCtx, jobJSON, job, true, nil
		}

		// Not claimed by this host: return it to paused queue safely (never stacked in a defer-in-loop).
		if err := h.requeuePausedJobIfActive(pluginCtx, queues, pausedQueuedJobString); err != nil {
			return nextCtx, "", internalGithub.QueueJob{}, false, err
		}
		pausedQueueTargetIndex++
		if pausedQueueLength == 1 {
			return nextCtx, "", internalGithub.QueueJob{}, false, nil
		}
	}
}

func (h *Handler) tryTakeOverPausedJob(
	pluginCtx context.Context,
	queues queueNames,
	pausedQueuedJobString string,
) (bool, context.Context, string, internalGithub.QueueJob, error) {
	pluginConfig := h.deps.PluginConfig
	workerGlobals := h.deps.Globals
	databaseContainer := h.deps.Database

	pausedQueuedJob, err := database.Unwrap[internalGithub.QueueJob](pausedQueuedJobString)
	if err != nil {
		return false, pluginCtx, "", internalGithub.QueueJob{}, fmt.Errorf("error unmarshalling job: %w", err)
	}
	if pausedQueuedJob.WorkflowJob.ID == nil {
		logging.Error(pluginCtx, "paused job missing workflow job ID; discarding from paused queue")
		return false, pluginCtx, "", internalGithub.QueueJob{}, nil
	}

	if workerGlobals.Plugins[pluginConfig.Plugin][pausedQueuedJob.PausedOn] != nil {
		logging.Info(pluginCtx, "job is already paused on this host by another plugin, skipping")
		return false, pluginCtx, "", internalGithub.QueueJob{}, nil
	}
	if !pluginConfig.SkipCPUAndMemoryResourceChecks {
		if err := internalAnka.VmHasEnoughHostResources(pluginCtx, pausedQueuedJob.AnkaVM); err != nil {
			return false, pluginCtx, "", internalGithub.QueueJob{}, nil
		}
		if err := internalAnka.VmHasEnoughResources(pluginCtx, pausedQueuedJob.AnkaVM); err != nil {
			return false, pluginCtx, "", internalGithub.QueueJob{}, nil
		}
	}

	logging.Info(pluginCtx, "paused job found to run", "pausedQueuedJob", pausedQueuedJob)
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("wasPausedOn", pausedQueuedJob.PausedOn))

	origHostQueue := "anklet/jobs/github/queued/" + queues.owner + "/{" + pausedQueuedJob.PausedOn + "}"
	originalHostJob, err := internalGithub.GetJobFromQueueByKeyAndValue(
		pluginCtx,
		origHostQueue,
		"workflow_job.id",
		strconv.FormatInt(*pausedQueuedJob.WorkflowJob.ID, 10),
	)
	if err != nil {
		return false, pluginCtx, "", internalGithub.QueueJob{}, fmt.Errorf("error getting job from paused host's queue: %w", err)
	}
	if originalHostJob == "" {
		logging.Error(pluginCtx, "problem getting job from paused host's queue (empty string); leaving orphaned paused item removed")
		return false, pluginCtx, "", internalGithub.QueueJob{}, nil
	}

	queuedJob, err := database.Unwrap[internalGithub.QueueJob](originalHostJob)
	if err != nil {
		return false, pluginCtx, "", internalGithub.QueueJob{}, fmt.Errorf("error unmarshalling job: %w", err)
	}
	if queuedJob.Action != "paused" {
		logging.Info(pluginCtx, "job is running on the other host, so we can't run it anymore")
		return false, pluginCtx, "", internalGithub.QueueJob{}, nil
	}

	if _, err := databaseContainer.RetryLRem(pluginCtx, origHostQueue, 1, originalHostJob); err != nil {
		logging.Error(pluginCtx, "error removing job from old host's queue", "error", err)
		return false, pluginCtx, "", internalGithub.QueueJob{}, fmt.Errorf("error removing job from old host's queue: %w", err)
	}
	return true, pluginCtx, originalHostJob, queuedJob, nil
}

// claimNextJob returns an existing job from pluginQueueName or claims one from paused/main queues.
func (h *Handler) claimNextJob(
	workerCtx context.Context,
	pluginCtx context.Context,
	queues queueNames,
) (context.Context, internalGithub.QueueJob, bool, error) {
	pluginConfig := h.deps.PluginConfig
	workerGlobals := h.deps.Globals
	databaseContainer := h.deps.Database
	metricsData := h.deps.Metrics

	// Resume any existing job in this plugin's queue first (eldest at index -1).
	queuedJobString, err := databaseContainer.RetryLIndex(pluginCtx, queues.pluginQueue, -1)
	if err != nil && err != redis.Nil {
		return pluginCtx, internalGithub.QueueJob{}, false, fmt.Errorf("error getting last object from %s: %w", queues.pluginQueue, err)
	}
	if queuedJobString != "" {
		logging.Info(pluginCtx, "found job in plugin queue", "queuedJobString", queuedJobString)
		queuedJob, unwrapErr := database.Unwrap[internalGithub.QueueJob](queuedJobString)
		if unwrapErr != nil {
			return pluginCtx, internalGithub.QueueJob{}, false, fmt.Errorf("error unmarshalling job: %w", unwrapErr)
		}
		if queuedJob.Action == "anka.VM" {
			logging.Error(pluginCtx, "found anka.VM job in plugin queue (critical error)", "queuedJob", queuedJob)
			queuedJob.Action = "cancel"
			queuedJob.WorkflowJob.Status = github.Ptr("completed")
			if err := h.cancelJob(workerCtx, pluginCtx, queues.pluginQueue, &queuedJob); err != nil {
				logging.Error(pluginCtx, "error canceling unexpected anka.VM job", "error", err)
			}
			return pluginCtx, internalGithub.QueueJob{}, false, nil
		}
		return pluginCtx, queuedJob, true, nil
	}

	// Check paused jobs first (higher priority).
	var queuedJob internalGithub.QueueJob
	var claimedPaused bool
	pluginCtx, queuedJobString, queuedJob, claimedPaused, err = h.claimPausedJob(workerCtx, pluginCtx, queues)
	if err != nil {
		return pluginCtx, internalGithub.QueueJob{}, false, err
	}

	if !claimedPaused {
		mainQueueLength, lenErr := databaseContainer.RetryLLen(pluginCtx, queues.mainQueue)
		if lenErr != nil {
			return pluginCtx, internalGithub.QueueJob{}, false, fmt.Errorf("error getting main queue length: %w", lenErr)
		}
		if workerGlobals.GetQueueTargetIndex() > mainQueueLength {
			workerGlobals.ResetQueueTargetIndex()
		}
		queuedJobString, err = internalGithub.PopJobOffQueue(pluginCtx, queues.mainQueue, workerGlobals.GetQueueTargetIndex())
		if err != nil {
			if metricsData != nil {
				metricsData.IncrementTotalFailedRunsSinceStart(workerCtx, pluginCtx)
			}
			return pluginCtx, internalGithub.QueueJob{}, false, fmt.Errorf("error getting queued jobs: %w", err)
		}
		if queuedJobString == "" {
			logging.Debug(pluginCtx, "no queued jobs found")
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
			workerGlobals.ResetQueueTargetIndex()
			return pluginCtx, internalGithub.QueueJob{}, false, nil
		}
		queuedJob, err = database.Unwrap[internalGithub.QueueJob](queuedJobString)
		if err != nil {
			return pluginCtx, internalGithub.QueueJob{}, false, fmt.Errorf("error unmarshalling job: %w", err)
		}

		// Filter jobs by organization when using shared queue_name.
		if !pluginConfig.IsEnterpriseScope() && queuedJob.Repository.Owner != nil && *queuedJob.Repository.Owner != pluginConfig.Owner {
			logging.Debug(pluginCtx, "skipping job from different organization",
				"jobOwner", *queuedJob.Repository.Owner,
				"handlerOwner", pluginConfig.Owner,
			)
			if _, pushErr := databaseContainer.RetryRPush(pluginCtx, queues.mainQueue, queuedJobString); pushErr != nil {
				logging.Error(pluginCtx, "error pushing job back to main queue", "error", pushErr)
			}
			workerGlobals.IncrementQueueTargetIndex()
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Preparing.Store(false)
			return pluginCtx, internalGithub.QueueJob{}, false, nil
		}
	}

	if _, err := databaseContainer.RetryRPush(pluginCtx, queues.pluginQueue, queuedJobString); err != nil {
		logging.Error(pluginCtx, "error pushing job to plugin queue", "error", err)
		return pluginCtx, internalGithub.QueueJob{}, false, fmt.Errorf("error pushing job to plugin queue: %w", err)
	}
	return pluginCtx, queuedJob, true, nil
}

// waitForAvailableResources pauses this plugin and pushes the job to pausedQueueName
// until either enough resources free up on this host or another host picks up the job.
func (h *Handler) waitForAvailableResources(
	workerCtx context.Context,
	pluginCtx context.Context,
	queues queueNames,
	state *handlerState,
	queuedJob *internalGithub.QueueJob,
) (bool, error) {
	pluginConfig := h.deps.PluginConfig
	workerGlobals := h.deps.Globals
	databaseContainer := h.deps.Database

	workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(true)

	queuedJob.PausedOn = pluginConfig.Name
	queuedJob.Action = "paused"
	queuedJobJSON, err := json.Marshal(queuedJob)
	if err != nil {
		logging.Error(pluginCtx, "error marshalling queued job", "error", err)
		state.requestReturnToMainQueue("error_marshalling_queued_job")
		return false, nil
	}
	updateErr := internalGithub.UpdateJobInDB(pluginCtx, queues.pluginQueue, queuedJob)
	if errors.Is(updateErr, internalGithub.ErrJobAlreadyCompleted) {
		return false, nil
	}
	if updateErr != nil {
		logging.Error(pluginCtx, "error updating job in db", "error", updateErr)
	}

	pausedQueuedJobJSON, err := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *queuedJob.WorkflowJob.ID, queues.paused)
	if err != nil {
		logging.Error(pluginCtx, "error checking if job is in paused queue", "error", err)
		state.requestReturnToMainQueue("error_checking_if_job_is_in_paused_queue")
		return false, nil
	}
	if pausedQueuedJobJSON == "" {
		success, pushErr := databaseContainer.RetryRPush(pluginCtx, queues.paused, queuedJobJSON)
		if pushErr != nil || success == 0 {
			logging.Error(pluginCtx, "error pushing job to paused queue", "error", pushErr)
			state.requestReturnToMainQueue("error_pushing_job_to_paused_queue")
			return false, nil
		}
		logging.Info(pluginCtx, "pushed job to paused queue", "queuedJob.WorkflowJob.ID", *queuedJob.WorkflowJob.ID)
	}

	enoughResourcesLoopCount := 0
	logging.Warn(pluginCtx, "cannot run vm yet, waiting for enough resources to be available...")

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case job := <-state.jobEvents:
			if watchJobIsTerminal(job.WorkflowJob.Status) {
				logging.Info(pluginCtx, statusString(job.WorkflowJob.Status)+" job found while waiting for resources")
				if delErr := internalGithub.DeleteFromQueue(pluginCtx, *queuedJob.WorkflowJob.ID, queues.paused); delErr != nil {
					logging.Warn(pluginCtx, "error deleting from paused queue", "error", delErr)
				}
				workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(false)
				return false, nil
			}
		case <-workerCtx.Done():
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(false)
			return false, fmt.Errorf("context canceled while waiting for resources")
		case <-pluginCtx.Done():
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(false)
			return false, fmt.Errorf("context canceled while waiting for resources")
		case <-ticker.C:
		}

		if enoughResourcesLoopCount%10 == 0 {
			logging.Warn(pluginCtx, "waiting for enough resources to be available...")
		}
		enoughResourcesLoopCount++

		existingJobJSON, lookupErr := internalGithub.GetJobJSONFromQueueByID(pluginCtx, *queuedJob.WorkflowJob.ID, queues.pluginQueue)
		if lookupErr != nil {
			state.requestReturnToMainQueue("error_checking_if_queue_has_job")
			if delErr := internalGithub.DeleteFromQueue(pluginCtx, *queuedJob.WorkflowJob.ID, queues.paused); delErr != nil {
				logging.Error(pluginCtx, "error deleting from paused queue", "error", delErr)
			}
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(false)
			return false, nil
		}
		if existingJobJSON == "" {
			logging.Warn(pluginCtx, "job was picked up by another host")
			workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name].Paused.Store(false)
			state.pausedTakenByOtherHost.Store(true)
			return false, nil
		}

		if resErr := internalAnka.VmHasEnoughResources(pluginCtx, queuedJob.AnkaVM); resErr != nil {
			if enoughResourcesLoopCount%10 == 0 {
				logging.Warn(pluginCtx, "error from vm has enough resources check", "error", resErr)
			}
			continue
		}

		if delErr := internalGithub.DeleteFromQueue(pluginCtx, *queuedJob.WorkflowJob.ID, queues.paused); delErr != nil {
			logging.Error(pluginCtx, "error deleting from paused queue", "error", delErr)
		}
		queuedJob.Action = "in_progress"
		updateErr = internalGithub.UpdateJobInDB(pluginCtx, queues.pluginQueue, queuedJob)
		if errors.Is(updateErr, internalGithub.ErrJobAlreadyCompleted) {
			return false, nil
		}
		if updateErr != nil {
			logging.Error(pluginCtx, "error updating job in db", "error", updateErr)
		}
		return true, nil
	}
}
