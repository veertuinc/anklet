package github

import (
	"context"
	"fmt"

	"github.com/google/go-github/v74/github"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/jobqueue"
	"github.com/veertuinc/anklet/internal/logging"
)

// queueJobID extracts the WorkflowJob.ID from a GitHub QueueJob for use as the
// generic IDExtractor passed to jobqueue helpers. Returns 0 when the pointer
// is nil so the helpers' equality check naturally excludes malformed entries.
func queueJobID(j QueueJob) int64 {
	if j.WorkflowJob.ID == nil {
		return 0
	}
	return *j.WorkflowJob.ID
}

// pluginQueueLocalCtx mirrors the legacy "use a fresh background context but
// preserve the logger" pattern several callers inside this package relied on
// so that Redis ops would not abort halfway through cleanup if the plugin
// context was already cancelled.
func pluginQueueLocalCtx(pluginCtx context.Context) context.Context {
	localCtx := context.Background()
	if logger, err := logging.GetLoggerFromContext(pluginCtx); err == nil {
		localCtx = context.WithValue(localCtx, config.ContextKey("logger"), logger)
	}
	return localCtx
}

// GetQueueSize delegates to jobqueue.GetQueueSize. Kept as a thin wrapper to
// preserve callsite ergonomics for existing GitHub code.
func GetQueueSize(pluginCtx context.Context, queueName string) (int64, error) {
	return jobqueue.GetQueueSize(pluginCtx, queueName)
}

// QueueHasJobs delegates to jobqueue.QueueHasJobs.
func QueueHasJobs(pluginCtx context.Context, queueName string) (bool, error) {
	return jobqueue.QueueHasJobs(pluginCtx, queueName)
}

// PopJobOffQueue delegates to jobqueue.PopJobOffQueue.
func PopJobOffQueue(pluginCtx context.Context, queueName string, queueTargetIndex int64) (string, error) {
	return jobqueue.PopJobOffQueue(pluginCtx, queueName, queueTargetIndex)
}

// GetJobJSONFromQueueByID returns the raw JSON of a queued job by workflow job
// ID. Wraps jobqueue.GetJobJSONByID with the GitHub ID extractor and the
// legacy "fresh background context with preserved logger" pattern.
func GetJobJSONFromQueueByID(pluginCtx context.Context, jobID int64, queue string) (string, error) {
	return jobqueue.GetJobJSONByID[QueueJob, int64](pluginQueueLocalCtx(pluginCtx), queue, jobID, queueJobID)
}

// GetJobFromQueue returns the head-of-queue job decoded as a GitHub QueueJob.
// Preserved here (not in jobqueue) because its strict "head only" + "must
// decode" contract is GitHub-specific.
func GetJobFromQueue(pluginCtx context.Context, queue string) (QueueJob, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logging.Panic(pluginCtx, pluginCtx, "error getting database client from context: "+err.Error())
	}
	queuedJobJSON, err := databaseContainer.RetryLIndex(pluginQueueLocalCtx(pluginCtx), queue, 0)
	if err != nil {
		logging.Error(pluginCtx, "error getting list of queued jobs", "err", err)
		return QueueJob{}, err
	}
	queueJob, err, typeErr := database.Unwrap[QueueJob](queuedJobJSON)
	if err != nil {
		logging.Error(pluginCtx, "error unmarshalling job", "err", err)
		return QueueJob{}, err
	}
	if typeErr != nil {
		return QueueJob{}, fmt.Errorf("not the type we want")
	}
	return queueJob, nil
}

// DeleteFromQueue removes a queued job by workflow job ID. Wraps
// jobqueue.DeleteByID with the GitHub ID extractor.
func DeleteFromQueue(ctx context.Context, jobID int64, queue string) error {
	return jobqueue.DeleteByID[QueueJob, int64](pluginQueueLocalCtx(ctx), queue, jobID, queueJobID)
}

// GetJobFromQueueByKeyAndValue is the legacy GitHub spelling of
// jobqueue.GetJobJSONByJSONPath. Kept for callsite stability.
func GetJobFromQueueByKeyAndValue(pluginCtx context.Context, queueName, key, value string) (string, error) {
	return jobqueue.GetJobJSONByJSONPath(pluginCtx, queueName, key, value)
}

// CheckIfJobExistsInHandlerQueues reports whether a job matching the given
// workflow run + workflow job is sitting at the head of any handler queue.
// Wraps jobqueue.CheckIfJobExistsInHandlerQueues with a GitHub-specific
// matcher.
func CheckIfJobExistsInHandlerQueues(pluginCtx context.Context, workflowRunID, jobID int64, owner string) (bool, error) {
	pattern := "anklet/jobs/github/queued/" + owner + "/*"
	return jobqueue.CheckIfJobExistsInHandlerQueues[QueueJob](pluginCtx, pattern, func(qj QueueJob) bool {
		return qj.WorkflowJob.RunID != nil && *qj.WorkflowJob.RunID == workflowRunID &&
			qj.WorkflowJob.ID != nil && *qj.WorkflowJob.ID == jobID
	})
}

// UpdateJobsWorkflowJobStatus updates the workflow job status of a queued
// job by hitting the GitHub Actions API and normalizing the response into the
// jobqueue.Status* vocabulary. Stays here (not in jobqueue) because the API
// call and 13-status mapping are GitHub-specific.
func UpdateJobsWorkflowJobStatus(
	workerCtx context.Context,
	pluginCtx context.Context,
	queuedJob *QueueJob,
) (QueueJob, error) {
	var previousStatus string
	if queuedJob.WorkflowJob.Status != nil {
		previousStatus = *queuedJob.WorkflowJob.Status
	}
	var previousConclusion string
	if queuedJob.WorkflowJob.Conclusion != nil {
		previousConclusion = *queuedJob.WorkflowJob.Conclusion
	}
	githubClient, err := GetGitHubClientFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting github client from context", "err", err)
		return *queuedJob, err
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting plugin from context", "err", err)
		return *queuedJob, err
	}
	pluginCtx, currentWorkflowJob, _, err := ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.WorkflowJob, *github.Response, error) {
		workflowJob, response, err := githubClient.Actions.GetWorkflowJobByID(pluginCtx, pluginConfig.Owner, *queuedJob.Repository.Name, *queuedJob.WorkflowJob.ID)
		return workflowJob, response, err
	})
	if err != nil {
		logging.Error(pluginCtx, "error getting workflow run", "err", err)
		return *queuedJob, err
	}
	logging.Debug(pluginCtx, "workflowJob from API", "workflowJob", currentWorkflowJob)
	if currentWorkflowJob.Status != nil {
		status := *currentWorkflowJob.Status
		if status != previousStatus {
			logging.Debug(
				pluginCtx,
				"workflow job status transition observed",
				"job_id", queuedJob.WorkflowJob.ID,
				"attempts", queuedJob.Attempts,
				"previous_status", previousStatus,
				"new_status", status,
				"raw_response", currentWorkflowJob,
			)
		}
		queuedJob.WorkflowJob.Status, queuedJob.WorkflowJob.Conclusion = mapGitHubAPIStatus(
			pluginCtx, status, queuedJob.WorkflowJob.Status, queuedJob.WorkflowJob.Conclusion,
		)
	} else {
		logging.Warn(pluginCtx, "workflow job status is nil")
	}
	if currentWorkflowJob.Conclusion != nil {
		newConclusion := *currentWorkflowJob.Conclusion
		if newConclusion != "" && newConclusion != previousConclusion {
			logging.Debug(
				pluginCtx,
				"workflow job conclusion transition observed",
				"job_id", queuedJob.WorkflowJob.ID,
				"attempts", queuedJob.Attempts,
				"previous_conclusion", previousConclusion,
				"new_conclusion", newConclusion,
				"raw_response", currentWorkflowJob,
			)
		}
		queuedJob.WorkflowJob.Conclusion = github.Ptr(newConclusion)
	}
	return *queuedJob, nil
}

// mapGitHubAPIStatus normalizes the 13-valued GitHub Actions API status to
// the jobqueue.Status* vocabulary plus, where applicable, sets a failure
// conclusion. The returned (status, conclusion) pointers replace the matching
// fields on the QueueJob. Status / conclusion are left untouched (returned as
// the passed-in current pointer) when the API status doesn't imply a change -
// in particular, the "default" / unknown-status branch preserves the previous
// values, matching the legacy behavior.
func mapGitHubAPIStatus(
	pluginCtx context.Context,
	status string,
	currentStatus *string,
	currentConclusion *string,
) (statusPtr *string, conclusionPtr *string) {
	statusPtr = currentStatus
	conclusionPtr = currentConclusion
	switch status {
	case "completed":
		logging.Info(pluginCtx, "workflow job is completed")
		statusPtr = github.Ptr(jobqueue.StatusCompleted)
	case "action_required":
		logging.Info(pluginCtx, "workflow job requires action")
		conclusionPtr = github.Ptr("failure")
		statusPtr = github.Ptr(jobqueue.StatusCompleted)
	case "cancelled":
		logging.Info(pluginCtx, "workflow job was cancelled")
		statusPtr = github.Ptr(jobqueue.StatusCompleted)
	case "failure":
		logging.Info(pluginCtx, "workflow job failed")
		conclusionPtr = github.Ptr("failure")
		statusPtr = github.Ptr(jobqueue.StatusCompleted)
	case "neutral":
		logging.Info(pluginCtx, "workflow job ended with neutral status")
		statusPtr = github.Ptr(jobqueue.StatusCompleted)
	case "skipped":
		logging.Info(pluginCtx, "workflow job was skipped")
		statusPtr = github.Ptr(jobqueue.StatusCompleted)
	case "stale":
		logging.Info(pluginCtx, "workflow job is stale")
		statusPtr = github.Ptr(jobqueue.StatusCompleted)
	case "success":
		logging.Info(pluginCtx, "workflow job succeeded")
		statusPtr = github.Ptr(jobqueue.StatusCompleted)
	case "timed_out":
		logging.Info(pluginCtx, "workflow job timed out")
		conclusionPtr = github.Ptr("failure")
		statusPtr = github.Ptr(jobqueue.StatusCompleted)
	case "in_progress":
		logging.Info(pluginCtx, "workflow job is in progress")
		statusPtr = github.Ptr(jobqueue.StatusInProgress)
	case "queued":
		logging.Info(pluginCtx, "workflow job is queued")
		statusPtr = github.Ptr(jobqueue.StatusQueued)
	case "requested":
		logging.Info(pluginCtx, "workflow job was requested")
		statusPtr = github.Ptr(jobqueue.StatusQueued)
	case "waiting":
		logging.Info(pluginCtx, "workflow job is waiting")
		statusPtr = github.Ptr(jobqueue.StatusInProgress)
	case "pending":
		logging.Info(pluginCtx, "workflow job is pending")
		statusPtr = github.Ptr(jobqueue.StatusInProgress)
	default:
		logging.Info(pluginCtx, "workflow job has unknown status", "status", status)
	}
	return statusPtr, conclusionPtr
}
