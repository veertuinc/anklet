package github

import (
	"context"

	"github.com/google/go-github/v74/github"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
)

// UpdateJobsJobStatus updates a queued job status from the GitHub API.
func UpdateJobsJobStatus(
	workerCtx context.Context,
	pluginCtx context.Context,
	queuedJob *QueueJob,
) (QueueJob, error) {
	var previousStatus string
	if queuedJob.Job.Status != nil {
		previousStatus = *queuedJob.Job.Status
	}
	var previousConclusion string
	if queuedJob.Job.Conclusion != nil {
		previousConclusion = *queuedJob.Job.Conclusion
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
		workflowJob, response, err := githubClient.Actions.GetWorkflowJobByID(pluginCtx, pluginConfig.Owner, *queuedJob.Repository.Name, *queuedJob.Job.ID)
		return workflowJob, response, err
	})
	if err != nil {
		logging.Error(pluginCtx, "error getting workflow run", "err", err)
		return *queuedJob, err
	}

	if currentWorkflowJob.Status != nil {
		status := *currentWorkflowJob.Status
		if status != previousStatus {
			logging.Debug(
				pluginCtx,
				"workflow job status transition observed",
				"job_id", queuedJob.Job.ID,
				"attempts", queuedJob.Attempts,
				"previous_status", previousStatus,
				"new_status", status,
				"raw_response", currentWorkflowJob,
			)
		}
		switch status {
		case "completed":
			queuedJob.Job.Status = github.Ptr("completed")
		case "action_required":
			queuedJob.Job.Conclusion = github.Ptr("failure")
			queuedJob.Job.Status = github.Ptr("completed")
		case "cancelled":
			queuedJob.Job.Status = github.Ptr("completed")
		case "failure":
			queuedJob.Job.Conclusion = github.Ptr("failure")
			queuedJob.Job.Status = github.Ptr("completed")
		case "neutral":
			queuedJob.Job.Status = github.Ptr("completed")
		case "skipped":
			queuedJob.Job.Status = github.Ptr("completed")
		case "stale":
			queuedJob.Job.Status = github.Ptr("completed")
		case "success":
			queuedJob.Job.Status = github.Ptr("completed")
		case "timed_out":
			queuedJob.Job.Conclusion = github.Ptr("failure")
			queuedJob.Job.Status = github.Ptr("completed")
		case "in_progress":
			queuedJob.Job.Status = github.Ptr("in_progress")
		case "queued":
			queuedJob.Job.Status = github.Ptr("queued")
		case "requested":
			queuedJob.Job.Status = github.Ptr("queued")
		case "waiting":
			queuedJob.Job.Status = github.Ptr("in_progress")
		case "pending":
			queuedJob.Job.Status = github.Ptr("in_progress")
		}
	}
	if currentWorkflowJob.Conclusion != nil {
		newConclusion := *currentWorkflowJob.Conclusion
		if newConclusion != "" && newConclusion != previousConclusion {
			logging.Debug(
				pluginCtx,
				"workflow job conclusion transition observed",
				"job_id", queuedJob.Job.ID,
				"attempts", queuedJob.Attempts,
				"previous_conclusion", previousConclusion,
				"new_conclusion", newConclusion,
				"raw_response", currentWorkflowJob,
			)
		}
		queuedJob.Job.Conclusion = github.Ptr(newConclusion)
	}
	return *queuedJob, nil
}
