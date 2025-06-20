package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/go-github/v66/github"
	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/logging"
)

func GetQueueSize(pluginCtx context.Context, queueName string) (int64, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return 0, fmt.Errorf("error getting database client from context: %s", err.Error())
	}
	return databaseContainer.Client.LLen(pluginCtx, queueName).Result()
}

func GetJobFromQueue(
	pluginCtx context.Context,
	jobID int64,
	queue string,
) (string, error) {
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return "", err
	}
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logging.Panic(pluginCtx, pluginCtx, "error getting database client from context: "+err.Error())
	}
	localCtx := context.Background() // avoids context cancellation preventing this from running
	queued, err := databaseContainer.Client.LRange(localCtx, queue, 0, -1).Result()
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting list of queued jobs", "err", err)
		return "", err
	}
	for _, queueItem := range queued {
		queueJob, err, typeErr := database.Unwrap[QueueJob](queueItem)
		if err != nil {
			logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err)
			return "", err
		}
		if typeErr != nil { // not the type we want
			continue
		}
		if queueJob.WorkflowJob.ID == nil {
			logger.ErrorContext(pluginCtx, "WorkflowJob.ID is nil", "WorkflowJob", queueJob.WorkflowJob)
			return "", fmt.Errorf("WorkflowJob.ID is nil")
		}
		if *queueJob.WorkflowJob.ID == jobID {
			// logger.WarnContext(pluginCtx, "WorkflowJob.ID already in queue", "WorkflowJob.ID", jobID)
			return queueItem, nil
		}
	}
	return "", nil
}

func DeleteFromQueue(pluginCtx context.Context, logger *slog.Logger, jobID int64, queue string) error {
	innerContext := context.Background() // avoids context cancellation preventing cleanup
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logging.Panic(pluginCtx, pluginCtx, "error getting database client from context: "+err.Error())
	}
	queued, err := databaseContainer.Client.LRange(innerContext, queue, 0, -1).Result()
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting list of queued jobs", "err", err)
		return err
	}
	logger.InfoContext(pluginCtx, "deleting job from queue", "jobID", jobID, "queue", queue, "queued", queued)
	for _, queueItem := range queued {
		queueJob, err, typeErr := database.Unwrap[QueueJob](queueItem)
		if err != nil {
			logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err)
			return err
		}
		if typeErr != nil { // not the type we want
			continue
		}
		fmt.Println("queueJob", queueJob.WorkflowJob.ID)
		fmt.Println("queueItem", queueItem)
		fmt.Println("jobID", jobID)
		fmt.Println("queueJob.WorkflowJob.ID", *queueJob.WorkflowJob.ID)
		fmt.Println("queueJob.WorkflowJob.ID == jobID", *queueJob.WorkflowJob.ID == jobID)
		if *queueJob.WorkflowJob.ID == jobID {
			// logger.WarnContext(pluginCtx, "WorkflowJob.ID already in queue", "WorkflowJob.ID", jobID)
			_, err = databaseContainer.Client.LRem(innerContext, queue, 1, queueItem).Result()
			if err != nil {
				logger.ErrorContext(pluginCtx, "error removing job from queue", "err", err)
				return err
			}
			return nil
		}
	}
	return nil
}

func QueueHasJobs(
	pluginCtx context.Context,
	queueName string,
) (bool, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return false, fmt.Errorf("error getting database client from context: %s", err.Error())
	}
	queueSize, err := databaseContainer.Client.LLen(pluginCtx, queueName).Result()
	if err != nil {
		return false, fmt.Errorf("error getting queue size: %s", err.Error())
	}
	return queueSize > 0, nil
}

func PopJobOffQueue(
	pluginCtx context.Context,
	queueName string,
	queueTargetIndex int64,
) (string, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return "", fmt.Errorf("error getting database client from context: %s", err.Error())
	}
	// logger, err := logging.GetLoggerFromContext(pluginCtx)
	// if err != nil {
	// 	return "", fmt.Errorf("error getting logger from context: %s", err.Error())
	// }
	// logger.WarnContext(pluginCtx, "getting queued job at index", "queueTargetIndex", queueTargetIndex)
	// we use Range here + Rem instead of Pop so we can use QueueTargetIndex.
	// QueueTargetIndex is the index we want to start at, allowing us to push
	// past the jobs we can't run due to host limits but are still in the main queue.
	queuedJobsString, err := databaseContainer.Client.LRange(pluginCtx, queueName, queueTargetIndex, -1).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("error getting queued job: %s", err.Error())
	}
	if len(queuedJobsString) == 0 {
		return "", nil
	}
	targetElement := queuedJobsString[0]
	// we use LRem to target removal of the element. If something else got the element already, we just return nothing and retry
	success, err := databaseContainer.Client.LRem(pluginCtx, queueName, 1, targetElement).Result()
	if err != nil {
		return "", fmt.Errorf("error removing queued job: %s", err.Error())
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
		return "", fmt.Errorf("error getting database client from context: %s", err.Error())
	}
	queuedJobsString, err := databaseContainer.Client.LRange(pluginCtx, queueName, 0, -1).Result()
	if err != nil {
		return "", fmt.Errorf("error getting queued jobs: %s", err.Error())
	}
	for _, job := range queuedJobsString {
		var jobMap map[string]any
		err := json.Unmarshal([]byte(job), &jobMap)
		if err != nil {
			return "", fmt.Errorf("error unmarshalling job to map: %s", err.Error())
		}
		// Split the key by dots to navigate through nested fields
		keyParts := strings.Split(key, ".")
		// Start with the root of the JSON
		var current any = jobMap
		// Navigate through each part of the key
		for _, part := range keyParts {
			// Check if current is a map
			if currentMap, ok := current.(map[string]any); ok {
				current = currentMap[part]
				if current == nil {
					break
				}
			} else {
				// If not a map, we can't go deeper
				current = nil
				break
			}
		}
		// Convert the found value to string for comparison
		var currentStr string
		if current != nil {
			switch v := current.(type) {
			case string:
				currentStr = v
			case float64:
				// JSON numbers are parsed as float64
				currentStr = fmt.Sprintf("%d", int64(v))
			case bool:
				currentStr = fmt.Sprintf("%t", v)
			default:
				// For other types, try JSON marshaling
				bytes, err := json.Marshal(v)
				if err == nil {
					currentStr = string(bytes)
				}
			}
		}
		if currentStr == value {
			return job, nil
		}
	}
	return "", nil
}

func UpdateJobWorkflowJobStatus(
	workerCtx context.Context,
	pluginCtx context.Context,
	queuedJob *QueueJob,
) (QueueJob, error) {
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return *queuedJob, err
	}
	githubClient, err := GetGitHubClientFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting github client from context", "err", err)
		return *queuedJob, err
	}
	pluginConfig, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting plugin from context", "err", err)
		return *queuedJob, err
	}
	pluginCtx, currentWorkflowJob, _, err := ExecuteGitHubClientFunction(workerCtx, pluginCtx, logger, func() (*github.WorkflowJob, *github.Response, error) {
		workflowJob, response, err := githubClient.Actions.GetWorkflowJobByID(pluginCtx, pluginConfig.Owner, *queuedJob.Repository.Name, *queuedJob.WorkflowJob.ID)
		return workflowJob, response, err
	})
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting workflow run", "err", err)
		return *queuedJob, err
	}
	logger.DebugContext(pluginCtx, "workflowJob from API", "workflowJob", currentWorkflowJob)
	// logger.DebugContext(pluginCtx, "response", "response", response)
	// Handle each workflow job status with a log message
	// completed = we clean up everything
	// failed = we clean up everything
	// queued = let it run, don't do anything
	// running = let it run, don't do anything
	if currentWorkflowJob.Status != nil {
		status := *currentWorkflowJob.Status
		switch status {
		case "completed":
			logger.InfoContext(pluginCtx, "workflow job is completed")
			queuedJob.WorkflowJob.Status = github.String("completed")
		case "action_required":
			logger.InfoContext(pluginCtx, "workflow job requires action")
			queuedJob.WorkflowJob.Conclusion = github.String("failure")
			queuedJob.WorkflowJob.Status = github.String("completed")
		case "cancelled":
			logger.InfoContext(pluginCtx, "workflow job was cancelled")
			queuedJob.WorkflowJob.Status = github.String("completed")
		case "failure":
			logger.InfoContext(pluginCtx, "workflow job failed")
			queuedJob.WorkflowJob.Conclusion = github.String("failure")
			queuedJob.WorkflowJob.Status = github.String("completed")
		case "neutral":
			logger.InfoContext(pluginCtx, "workflow job ended with neutral status")
			queuedJob.WorkflowJob.Status = github.String("completed")
		case "skipped":
			logger.InfoContext(pluginCtx, "workflow job was skipped")
			queuedJob.WorkflowJob.Status = github.String("completed")
		case "stale":
			logger.InfoContext(pluginCtx, "workflow job is stale")
			queuedJob.WorkflowJob.Status = github.String("completed")
		case "success":
			logger.InfoContext(pluginCtx, "workflow job succeeded")
			queuedJob.WorkflowJob.Status = github.String("completed")
		case "timed_out":
			logger.InfoContext(pluginCtx, "workflow job timed out")
			queuedJob.WorkflowJob.Conclusion = github.String("failure")
			queuedJob.WorkflowJob.Status = github.String("completed")
		case "in_progress":
			logger.InfoContext(pluginCtx, "workflow job is in progress")
			queuedJob.WorkflowJob.Status = github.String("in_progress")
		case "queued":
			logger.InfoContext(pluginCtx, "workflow job is queued")
			queuedJob.WorkflowJob.Status = github.String("queued")
		case "requested":
			logger.InfoContext(pluginCtx, "workflow job was requested")
			queuedJob.WorkflowJob.Status = github.String("queued")
		case "waiting":
			logger.InfoContext(pluginCtx, "workflow job is waiting")
			queuedJob.WorkflowJob.Status = github.String("in_progress")
		case "pending":
			logger.InfoContext(pluginCtx, "workflow job is pending")
			queuedJob.WorkflowJob.Status = github.String("in_progress")
		default:
			logger.InfoContext(pluginCtx, "workflow job has unknown status", "status", status)
		}
	} else {
		logger.WarnContext(pluginCtx, "workflow job status is nil")
	}
	return *queuedJob, nil
}
