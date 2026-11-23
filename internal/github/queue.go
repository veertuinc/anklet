package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/go-github/v75/github"
	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/logging"
)

// ListOwnerQueueKeys returns the exact owner queue key (if present) and any per-handler
// sub-queue keys under prefix/owner/* using SCAN so unrelated orgs with a shared prefix
// (e.g. "org" vs "org-staging") are never matched.
func ListOwnerQueueKeys(
	ctx context.Context,
	db *database.Database,
	prefix string,
	owner string,
) ([]string, error) {
	exactKey := strings.TrimSuffix(prefix, "/") + "/" + owner
	var keys []string

	exists, err := db.RetryExists(ctx, exactKey)
	if err != nil {
		return nil, fmt.Errorf("checking queue key %s: %w", exactKey, err)
	}
	if exists > 0 {
		keys = append(keys, exactKey)
	}

	subKeys, err := db.ScanAllKeys(ctx, exactKey+"/*")
	if err != nil {
		return nil, fmt.Errorf("scanning sub-queues for %s: %w", exactKey, err)
	}
	keys = append(keys, subKeys...)
	return keys, nil
}

func GetJobJSONFromQueueByID(
	pluginCtx context.Context,
	jobID int64,
	queue string,
) (string, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return "", fmt.Errorf("error getting database client from context: %w", err)
	}
	queued, err := databaseContainer.RetryLRange(pluginCtx, queue, 0, -1)
	if err != nil {
		logging.Error(pluginCtx, "error getting list of queued jobs", "err", err)
		return "", err
	}
	for _, queueItem := range queued {
		queueJob, unwrapErr := database.Unwrap[QueueJob](queueItem)
		if unwrapErr != nil {
			logging.Error(pluginCtx, "error unmarshalling job", "err", unwrapErr)
			return "", unwrapErr
		}
		if queueJob.WorkflowJob.ID == nil {
			logging.Error(pluginCtx, "WorkflowJob.ID is nil", "WorkflowJob", queueJob.WorkflowJob)
			return "", fmt.Errorf("WorkflowJob.ID is nil")
		}
		if *queueJob.WorkflowJob.ID == jobID {
			return queueItem, nil
		}
	}
	return "", nil
}

func GetJobFromQueue(
	pluginCtx context.Context,
	queue string,
) (QueueJob, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return QueueJob{}, fmt.Errorf("error getting database client from context: %w", err)
	}
	// Use background context preserving logger so deferred cleanup reads survive cancellation.
	localCtx := context.Background()
	if logger, logErr := logging.GetLoggerFromContext(pluginCtx); logErr == nil {
		localCtx = context.WithValue(localCtx, config.ContextKey("logger"), logger)
	}
	queuedJobJSON, err := databaseContainer.RetryLIndex(localCtx, queue, 0)
	if err != nil {
		logging.Error(pluginCtx, "error getting list of queued jobs", "err", err)
		return QueueJob{}, err
	}
	queueJob, err := database.Unwrap[QueueJob](queuedJobJSON)
	if err != nil {
		logging.Error(pluginCtx, "error unmarshalling job", "err", err)
		return QueueJob{}, err
	}
	return queueJob, nil
}

func DeleteFromQueue(ctx context.Context, jobID int64, queue string) error {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		return fmt.Errorf("error getting database client from context: %w", err)
	}
	innerContext := context.Background()
	if logger, logErr := logging.GetLoggerFromContext(ctx); logErr == nil {
		innerContext = context.WithValue(innerContext, config.ContextKey("logger"), logger)
	}
	queued, err := databaseContainer.RetryLRange(innerContext, queue, 0, -1)
	if err != nil {
		logging.Error(ctx, "error getting list of queued jobs", "err", err)
		return err
	}
	if len(queued) == 0 {
		return nil
	}
	logging.Debug(ctx, "deleting job from queue", "jobID", jobID, "queue", queue, "queued", queued)
	for _, queueItem := range queued {
		queueJob, unwrapErr := database.Unwrap[QueueJob](queueItem)
		if unwrapErr != nil {
			logging.Error(ctx, "error unmarshalling job", "err", unwrapErr)
			return unwrapErr
		}
		if queueJob.WorkflowJob.ID == nil {
			continue
		}
		if *queueJob.WorkflowJob.ID == jobID {
			success, remErr := databaseContainer.RetryLRem(innerContext, queue, 1, queueItem)
			if remErr != nil {
				logging.Error(ctx, "error removing job from queue", "err", remErr)
				return remErr
			}
			if success == 1 {
				logging.Debug(ctx, "job removed from queue", "jobID", jobID, "queue", queue)
			} else {
				return fmt.Errorf("job not removed from queue")
			}
			return nil
		}
	}
	return nil
}

func PopJobOffQueue(
	pluginCtx context.Context,
	queueName string,
	queueTargetIndex int64,
) (string, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return "", fmt.Errorf("error getting database client from context: %w", err)
	}
	// Read only the single target index instead of scanning the entire queue list.
	targetElement, err := databaseContainer.RetryLIndex(pluginCtx, queueName, queueTargetIndex)
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("error getting queued job: %w", err)
	}
	if targetElement == "" {
		return "", nil
	}
	// Use LRem to claim this exact element. If another worker claimed it first, return empty.
	success, err := databaseContainer.RetryLRem(pluginCtx, queueName, 1, targetElement)
	if err != nil {
		return "", fmt.Errorf("error removing queued job: %w", err)
	}
	if success == 1 {
		return targetElement, nil
	}
	return "", nil
}

func GetJobFromQueueByKeyAndValue(
	pluginCtx context.Context,
	queueName string,
	key string,
	value string,
) (string, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return "", fmt.Errorf("error getting database client from context: %w", err)
	}
	queuedJobsString, err := databaseContainer.RetryLRange(pluginCtx, queueName, 0, -1)
	if err != nil {
		return "", fmt.Errorf("error getting queued jobs: %w", err)
	}
	for _, job := range queuedJobsString {
		var jobMap map[string]any
		if err := json.Unmarshal([]byte(job), &jobMap); err != nil {
			return "", fmt.Errorf("error unmarshalling job to map: %w", err)
		}
		keyParts := strings.Split(key, ".")
		var current any = jobMap
		for _, part := range keyParts {
			if currentMap, ok := current.(map[string]any); ok {
				current = currentMap[part]
				if current == nil {
					break
				}
			} else {
				current = nil
				break
			}
		}
		var currentStr string
		if current != nil {
			switch v := current.(type) {
			case string:
				currentStr = v
			case float64:
				currentStr = fmt.Sprintf("%d", int64(v))
			case bool:
				currentStr = fmt.Sprintf("%t", v)
			default:
				if encoded, marshalErr := json.Marshal(v); marshalErr == nil {
					currentStr = string(encoded)
				}
			}
		}
		if currentStr == value {
			return job, nil
		}
	}
	return "", nil
}

// UpdateJobsWorkflowJobStatus fetches the latest workflow job state from GitHub's Actions API.
func UpdateJobsWorkflowJobStatus(
	workerCtx context.Context,
	pluginCtx context.Context,
	queuedJob *QueueJob,
) (QueueJob, error) {
	if queuedJob == nil || queuedJob.WorkflowJob.ID == nil {
		return QueueJob{}, fmt.Errorf("workflow job ID is missing")
	}
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
	jobOwner, jobRepo, err := queuedJob.RepositoryOwnerAndName()
	if err != nil {
		logging.Error(pluginCtx, "error getting job repository owner and name", "err", err)
		return *queuedJob, err
	}
	if wrapper, wrapperErr := GetGitHubClientWrapperFromContext(pluginCtx); wrapperErr == nil {
		orgClient, orgErr := wrapper.ClientForOrganization(pluginCtx, jobOwner)
		if orgErr != nil {
			logging.Error(pluginCtx, "error getting GitHub client for job organization", "org", jobOwner, "err", orgErr)
			return *queuedJob, orgErr
		}
		githubClient = orgClient
	}
	pluginCtx, currentWorkflowJob, _, err := ExecuteGitHubClientFunction(workerCtx, pluginCtx, func() (*github.WorkflowJob, *github.Response, error) {
		return githubClient.Actions.GetWorkflowJobByID(pluginCtx, jobOwner, jobRepo, *queuedJob.WorkflowJob.ID)
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
		switch status {
		case "completed":
			logging.Info(pluginCtx, "workflow job is completed")
			queuedJob.WorkflowJob.Status = github.Ptr("completed")
		case "action_required":
			logging.Info(pluginCtx, "workflow job requires action")
			queuedJob.WorkflowJob.Conclusion = github.Ptr("failure")
			queuedJob.WorkflowJob.Status = github.Ptr("completed")
		case "cancelled":
			logging.Info(pluginCtx, "workflow job was cancelled")
			queuedJob.WorkflowJob.Status = github.Ptr("completed")
		case "failure":
			logging.Info(pluginCtx, "workflow job failed")
			queuedJob.WorkflowJob.Conclusion = github.Ptr("failure")
			queuedJob.WorkflowJob.Status = github.Ptr("completed")
		case "neutral":
			logging.Info(pluginCtx, "workflow job ended with neutral status")
			queuedJob.WorkflowJob.Status = github.Ptr("completed")
		case "skipped":
			logging.Info(pluginCtx, "workflow job was skipped")
			queuedJob.WorkflowJob.Status = github.Ptr("completed")
		case "stale":
			logging.Info(pluginCtx, "workflow job is stale")
			queuedJob.WorkflowJob.Status = github.Ptr("completed")
		case "success":
			logging.Info(pluginCtx, "workflow job succeeded")
			queuedJob.WorkflowJob.Status = github.Ptr("completed")
		case "timed_out":
			logging.Info(pluginCtx, "workflow job timed out")
			queuedJob.WorkflowJob.Conclusion = github.Ptr("failure")
			queuedJob.WorkflowJob.Status = github.Ptr("completed")
		case "in_progress":
			logging.Info(pluginCtx, "workflow job is in progress")
			queuedJob.WorkflowJob.Status = github.Ptr("in_progress")
		case "queued":
			logging.Info(pluginCtx, "workflow job is queued")
			queuedJob.WorkflowJob.Status = github.Ptr("queued")
		case "requested":
			logging.Info(pluginCtx, "workflow job was requested")
			queuedJob.WorkflowJob.Status = github.Ptr("queued")
		case "waiting":
			logging.Info(pluginCtx, "workflow job is waiting")
			queuedJob.WorkflowJob.Status = github.Ptr("in_progress")
		case "pending":
			logging.Info(pluginCtx, "workflow job is pending")
			queuedJob.WorkflowJob.Status = github.Ptr("in_progress")
		default:
			logging.Info(pluginCtx, "workflow job has unknown status", "status", status)
		}
	} else {
		logging.Warn(pluginCtx, "workflow job status is nil")
	}
	if currentWorkflowJob.Conclusion != nil {
		newConclusion := *currentWorkflowJob.Conclusion
		if newConclusion != "" {
			if newConclusion != previousConclusion {
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
	}
	return *queuedJob, nil
}

// CheckIfJobExistsInHandlerQueues checks whether a job with matching workflow run ID and job ID
// already exists as the active head item in any handler queue for owner.
func CheckIfJobExistsInHandlerQueues(
	pluginCtx context.Context,
	workflowRunID int64,
	jobID int64,
	owner string,
) (bool, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return false, fmt.Errorf("error getting database client from context: %w", err)
	}

	handlerQueuePattern := "anklet/jobs/github/queued/" + owner + "/*"
	handlerQueueKeys, err := databaseContainer.ScanAllKeys(pluginCtx, handlerQueuePattern)
	if err != nil {
		return false, fmt.Errorf("error scanning handler queue keys: %w", err)
	}
	if len(handlerQueueKeys) == 0 {
		return false, nil
	}

	resultChan := make(chan bool, len(handlerQueueKeys))
	for _, queueKey := range handlerQueueKeys {
		go func(key string) {
			firstJobJSON, idxErr := databaseContainer.RetryLIndex(pluginCtx, key, 0)
			if idxErr != nil || firstJobJSON == "" {
				resultChan <- false
				return
			}
			queueJob, unwrapErr := database.Unwrap[QueueJob](firstJobJSON)
			if unwrapErr != nil {
				resultChan <- false
				return
			}
			if queueJob.WorkflowJob.RunID != nil && *queueJob.WorkflowJob.RunID == workflowRunID &&
				queueJob.WorkflowJob.ID != nil && *queueJob.WorkflowJob.ID == jobID {
				resultChan <- true
				return
			}
			resultChan <- false
		}(queueKey)
	}

	for range len(handlerQueueKeys) {
		if <-resultChan {
			return true, nil
		}
	}
	return false, nil
}
