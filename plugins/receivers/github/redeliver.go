package github

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	gogithub "github.com/google/go-github/v74/github"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	internalGithub "github.com/veertuinc/anklet/internal/github"
	"github.com/veertuinc/anklet/internal/logging"
)

func hasAnkaTemplateLabel(labels []string) bool {
	return exists_in_array_partial(labels, []string{"anka-template"})
}

func filterFailedOriginalDeliveries(deliveries []*gogithub.HookDelivery) []*gogithub.HookDelivery {
	out := make([]*gogithub.HookDelivery, 0, len(deliveries))
	for _, hookDelivery := range deliveries {
		if hookDelivery == nil || hookDelivery.Action == nil {
			continue
		}
		if hookDelivery.StatusCode == nil || *hookDelivery.StatusCode == 200 {
			continue
		}
		if hookDelivery.Redelivery != nil && *hookDelivery.Redelivery {
			continue
		}
		if *hookDelivery.Action == "in_progress" {
			continue
		}
		out = append(out, hookDelivery)
	}
	return out
}

func shouldSkipQueuedRedelivery(status, conclusion string) bool {
	if conclusion == "cancelled" || conclusion == "canceled" {
		return true
	}
	switch status {
	case "completed", "in_progress":
		return true
	default:
		return false
	}
}

func redeliveryCallEstimate(action string) int {
	if action == "queued" {
		return 3
	}
	return 2
}

func countCandidatesFittingBudget(actionsNewestFirst []string, budgetLeft int) int {
	count := 0
	remaining := budgetLeft
	for i := len(actionsNewestFirst) - 1; i >= 0; i-- {
		est := redeliveryCallEstimate(actionsNewestFirst[i])
		if est > remaining {
			break
		}
		remaining -= est
		count++
	}
	return count
}

func hookAction(hookDelivery *gogithub.HookDelivery) string {
	if hookDelivery == nil || hookDelivery.Action == nil {
		return ""
	}
	return *hookDelivery.Action
}

// runRedeliveryWalk lists failed deliveries and redelivers Anklet jobs that
// still look orphaned. Errors are returned to the caller; they must not stop HTTP.
func runRedeliveryWalk(
	workerCtx context.Context,
	pluginCtx context.Context,
	githubClient *gogithub.Client,
	pluginConfig config.Plugin,
	queueOwner string,
) error {
	if githubClient == nil {
		return fmt.Errorf("github client is nil")
	}
	budget := &internalGithub.RedeliveryBudget{}
	if err := budget.RefreshFromAPI(pluginCtx, githubClient); err != nil {
		logging.Warn(pluginCtx, "unable to read GitHub rate limit before redelivery; continuing with empty window", "error", err)
	}

	limitForHooks := time.Now().Add(-time.Hour * time.Duration(pluginConfig.RedeliverHours))
	opts := &gogithub.ListCursorOptions{PerPage: 100}
	logging.Info(pluginCtx, fmt.Sprintf("listing failed hook deliveries for the last %d hours to see if any need redelivery (may take a while)...", pluginConfig.RedeliverHours))

	var allHookDeliveries []*gogithub.HookDelivery
	reachedLimitTime := false
	apiCallCount := 0
	for !reachedLimitTime {
		if err := budget.Reserve(workerCtx, pluginCtx, githubClient, 1); err != nil {
			return err
		}
		apiCallCount++
		var hookDeliveries []*gogithub.HookDelivery
		var response *gogithub.Response
		var listErr error
		pluginCtx, hookDeliveries, response, listErr = executeListFailedDeliveries(workerCtx, pluginCtx, githubClient, pluginConfig, opts)
		budget.Record(1)
		if response != nil {
			budget.ApplyRate(response.Rate)
		}
		if listErr != nil {
			return fmt.Errorf("error listing hooks: %w", listErr)
		}
		for _, hookDelivery := range hookDeliveries {
			if hookDelivery == nil || hookDelivery.Action == nil {
				continue
			}
			if hookDelivery.DeliveredAt != nil && limitForHooks.After(hookDelivery.DeliveredAt.Time) {
				reachedLimitTime = true
				break
			}
			allHookDeliveries = append(allHookDeliveries, hookDelivery)
		}
		if response == nil || response.Cursor == "" {
			break
		}
		opts.Cursor = response.Cursor
	}

	toRedeliver := filterFailedOriginalDeliveries(allHookDeliveries)
	logging.Info(pluginCtx, "finished fetching hook deliveries",
		"api_calls_made", apiCallCount,
		"total_deliveries_found", len(allHookDeliveries),
		"total_to_redeliver", len(toRedeliver),
	)

	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return fmt.Errorf("error getting database client from context: %s", err.Error())
	}
	allQueuedJobs, err := loadQueueJobsByPrefix(pluginCtx, databaseContainer, "anklet/jobs/github/queued/"+queueOwner+"*")
	if err != nil {
		return err
	}
	allCompletedJobs, err := loadQueueJobsByPrefix(pluginCtx, databaseContainer, "anklet/jobs/github/completed/"+queueOwner+"*")
	if err != nil {
		return err
	}

	logging.Info(pluginCtx, "processing hooks scheduled for redelivery", "total_to_redeliver", len(toRedeliver))
	redeliveryRequested := false

	for i := len(toRedeliver) - 1; i >= 0; i-- {
		hookDelivery := toRedeliver[i]
		estimate := redeliveryCallEstimate(hookAction(hookDelivery))
		if err := budget.Reserve(workerCtx, pluginCtx, githubClient, estimate); err != nil {
			return err
		}

		logging.Debug(pluginCtx, "processing hook for redelivery",
			"hook_id", *hookDelivery.ID,
			"guid", *hookDelivery.GUID,
			"action", *hookDelivery.Action,
			"status_code", *hookDelivery.StatusCode,
		)

		pluginCtx, gottenHookDelivery, response, err := executeGetHookDelivery(workerCtx, pluginCtx, githubClient, pluginConfig, *hookDelivery.ID)
		budget.Record(1)
		if response != nil {
			budget.ApplyRate(response.Rate)
		}
		if err != nil {
			logging.Error(pluginCtx, "error getting hook delivery, skipping", "error", err, "hook_id", *hookDelivery.ID)
			continue
		}
		if gottenHookDelivery == nil || gottenHookDelivery.Request == nil || gottenHookDelivery.Request.RawPayload == nil {
			logging.Warn(pluginCtx, "hook delivery payload missing, skipping", "hook_id", *hookDelivery.ID)
			continue
		}
		var workflowJobEventPayload internalGithub.QueueJob
		if err := json.Unmarshal(*gottenHookDelivery.Request.RawPayload, &workflowJobEventPayload); err != nil {
			logging.Error(pluginCtx, "error unmarshalling hook request raw payload, skipping", "error", err, "hook_id", *hookDelivery.ID)
			continue
		}
		workflowJob := workflowJobEventPayload.WorkflowJob
		if workflowJob.ID == nil {
			logging.Warn(pluginCtx, "WorkflowJob or WorkflowJob.ID is nil")
			continue
		}

		logging.Debug(pluginCtx, "fetched hook delivery details",
			"hook_id", *hookDelivery.ID,
			"workflow_job_id", *workflowJob.ID,
			"workflow_job_name", workflowJob.Name,
			"labels", workflowJob.Labels,
		)

		if !hasAnkaTemplateLabel(workflowJob.Labels) {
			logging.Debug(pluginCtx, "skipping redelivery, job labels do not include anka-template",
				"hook_id", *hookDelivery.ID,
				"workflow_job_id", *workflowJob.ID,
			)
			continue
		}

		if *hookDelivery.Action == "queued" {
			status, conclusion, statusErr := fetchWorkflowJobStatus(workerCtx, pluginCtx, githubClient, workflowJobEventPayload)
			budget.Record(1)
			if statusErr != nil {
				logging.Warn(pluginCtx, "error getting workflow job status, continuing redelivery checks",
					"error", statusErr,
					"workflow_job_id", *workflowJob.ID,
				)
			} else if shouldSkipQueuedRedelivery(status, conclusion) {
				logging.Debug(pluginCtx, "skipping queued redelivery, GitHub job already progressed",
					"hook_id", *hookDelivery.ID,
					"workflow_job_id", *workflowJob.ID,
					"status", status,
					"conclusion", conclusion,
				)
				continue
			}
		}

		inQueued := jobIDInQueueMap(allQueuedJobs, *workflowJob.ID)
		inCompleted := false
		inCompletedListKey := ""
		inCompletedIndex := 0
		if *hookDelivery.Action == "completed" {
			inCompleted, inCompletedListKey, inCompletedIndex = jobIDInCompletedMap(allCompletedJobs, *workflowJob.ID)
		}

		if inQueued && inCompleted {
			logging.Debug(pluginCtx, "job is in both queued and completed, skipping redelivery",
				"workflow_job_id", *workflowJob.ID,
				"hook_id", *hookDelivery.ID,
			)
			continue
		}
		if inCompleted && !inQueued {
			logging.Debug(pluginCtx, "job is in completed but not queued, removing from completed database",
				"workflow_job_id", *workflowJob.ID,
				"hook_id", *hookDelivery.ID,
			)
			_, remErr := databaseContainer.RetryLRem(pluginCtx, inCompletedListKey, 1, allCompletedJobs[inCompletedListKey][inCompletedIndex])
			if remErr != nil {
				logging.Error(pluginCtx, "error removing completed job from database, skipping", "error", remErr)
			}
			continue
		}
		if *hookDelivery.StatusCode == 200 || inCompleted {
			logging.Debug(pluginCtx, "skipping redelivery, hook already succeeded or completed",
				"hook_id", *hookDelivery.ID,
				"workflow_job_id", *workflowJob.ID,
				"status_code", *hookDelivery.StatusCode,
				"in_completed", inCompleted,
			)
			continue
		}

		logging.Info(pluginCtx, "redelivering hook",
			"hook_id", *hookDelivery.ID,
			"workflow_job_id", *workflowJob.ID,
			"action", *hookDelivery.Action,
			"original_status_code", *hookDelivery.StatusCode,
			"in_queued", inQueued,
			"in_completed", inCompleted,
		)

		pluginCtx, redelivery, redResp, _ := executeRedeliverHookDelivery(workerCtx, pluginCtx, githubClient, pluginConfig, *hookDelivery.ID)
		budget.Record(1)
		if redResp != nil {
			budget.ApplyRate(redResp.Rate)
		}
		redeliveryRequested = true
		logging.Info(pluginCtx, "hook redelivery requested successfully",
			"redelivery", redelivery,
			"hookDelivery", map[string]any{
				"guid":       *hookDelivery.GUID,
				"action":     *hookDelivery.Action,
				"statusCode": *hookDelivery.StatusCode,
				"redelivery": *hookDelivery.Redelivery,
			},
			"job", map[string]any{
				"workflowJob": map[string]any{
					"id": *workflowJob.ID,
				},
			},
			"inCompleted", inCompleted,
		)
	}

	logging.Info(pluginCtx, "finished processing hooks for redelivery",
		"total_hooks_checked", len(toRedeliver),
	)

	if redeliveryRequested {
		logging.Info(pluginCtx, "sleeping for 1 minute to allow handlers to process jobs")
		select {
		case <-time.After(1 * time.Minute):
		case <-workerCtx.Done():
			return workerCtx.Err()
		case <-pluginCtx.Done():
			return pluginCtx.Err()
		}
	}
	return nil
}

func executeListFailedDeliveries(
	workerCtx context.Context,
	pluginCtx context.Context,
	githubClient *gogithub.Client,
	pluginConfig config.Plugin,
	opts *gogithub.ListCursorOptions,
) (context.Context, []*gogithub.HookDelivery, *gogithub.Response, error) {
	pluginCtx, result, response, err := internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*[]*gogithub.HookDelivery, *gogithub.Response, error) {
		hookDeliveries, resp, listErr := internalGithub.ListFailedHookDeliveries(pluginCtx, githubClient, pluginConfig.Owner, pluginConfig.Repo, pluginConfig.HookID, opts)
		if listErr != nil {
			return nil, resp, listErr
		}
		return &hookDeliveries, resp, nil
	})
	if result == nil {
		return pluginCtx, nil, response, err
	}
	return pluginCtx, *result, response, err
}

func executeGetHookDelivery(
	workerCtx context.Context,
	pluginCtx context.Context,
	githubClient *gogithub.Client,
	pluginConfig config.Plugin,
	deliveryID int64,
) (context.Context, *gogithub.HookDelivery, *gogithub.Response, error) {
	return internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*gogithub.HookDelivery, *gogithub.Response, error) {
		if pluginConfig.Repo != "" {
			return githubClient.Repositories.GetHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.Repo, pluginConfig.HookID, deliveryID)
		}
		return githubClient.Organizations.GetHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.HookID, deliveryID)
	})
}

func executeRedeliverHookDelivery(
	workerCtx context.Context,
	pluginCtx context.Context,
	githubClient *gogithub.Client,
	pluginConfig config.Plugin,
	deliveryID int64,
) (context.Context, *gogithub.HookDelivery, *gogithub.Response, error) {
	return internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*gogithub.HookDelivery, *gogithub.Response, error) {
		if pluginConfig.Repo != "" {
			return githubClient.Repositories.RedeliverHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.Repo, pluginConfig.HookID, deliveryID)
		}
		return githubClient.Organizations.RedeliverHookDelivery(pluginCtx, pluginConfig.Owner, pluginConfig.HookID, deliveryID)
	})
}

func fetchWorkflowJobStatus(
	workerCtx context.Context,
	pluginCtx context.Context,
	githubClient *gogithub.Client,
	queuedJob internalGithub.QueueJob,
) (string, string, error) {
	jobOwner, jobRepo, err := queuedJob.RepositoryOwnerAndName()
	if err != nil {
		return "", "", err
	}
	if queuedJob.WorkflowJob.ID == nil {
		return "", "", fmt.Errorf("workflow job id is nil")
	}
	client := githubClient
	if wrapper, wrapperErr := internalGithub.GetGitHubClientWrapperFromContext(pluginCtx); wrapperErr == nil {
		orgClient, orgErr := wrapper.ClientForOrganization(pluginCtx, jobOwner)
		if orgErr != nil {
			return "", "", orgErr
		}
		client = orgClient
	}
	_, currentWorkflowJob, _, err := internalGithub.ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*gogithub.WorkflowJob, *gogithub.Response, error) {
		return client.Actions.GetWorkflowJobByID(pluginCtx, jobOwner, jobRepo, *queuedJob.WorkflowJob.ID)
	})
	if err != nil {
		return "", "", err
	}
	status := ""
	conclusion := ""
	if currentWorkflowJob != nil && currentWorkflowJob.Status != nil {
		status = *currentWorkflowJob.Status
	}
	if currentWorkflowJob != nil && currentWorkflowJob.Conclusion != nil {
		conclusion = *currentWorkflowJob.Conclusion
	}
	return status, conclusion, nil
}

func loadQueueJobsByPrefix(
	pluginCtx context.Context,
	databaseContainer *database.Database,
	pattern string,
) (map[string][]string, error) {
	keys, err := databaseContainer.RetryKeys(pluginCtx, pattern)
	if err != nil {
		return nil, fmt.Errorf("error getting list of keys for %s: %s", pattern, err.Error())
	}
	out := make(map[string][]string, len(keys))
	for _, key := range keys {
		jobs, rangeErr := databaseContainer.RetryLRange(pluginCtx, key, 0, -1)
		if rangeErr != nil {
			return nil, fmt.Errorf("error getting list of jobs for key: %s", rangeErr.Error())
		}
		out[key] = jobs
	}
	return out, nil
}

func jobIDInQueueMap(allQueuedJobs map[string][]string, jobID int64) bool {
	for _, queuedJobs := range allQueuedJobs {
		for _, queuedJob := range queuedJobs {
			if queuedJob == "" {
				continue
			}
			wrappedPayload, err, typeErr := database.Unwrap[internalGithub.QueueJob](queuedJob)
			if err != nil || typeErr != nil || wrappedPayload.WorkflowJob.ID == nil {
				continue
			}
			if *wrappedPayload.WorkflowJob.ID == jobID {
				return true
			}
		}
	}
	return false
}

func jobIDInCompletedMap(allCompletedJobs map[string][]string, jobID int64) (bool, string, int) {
	for key, completedJobs := range allCompletedJobs {
		for index, completedJob := range completedJobs {
			wrappedPayload, err, typeErr := database.Unwrap[internalGithub.QueueJob](completedJob)
			if err != nil || typeErr != nil || wrappedPayload.WorkflowJob.ID == nil {
				continue
			}
			if *wrappedPayload.WorkflowJob.ID == jobID {
				return true, key, index
			}
		}
	}
	return false, "", 0
}
