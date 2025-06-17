package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/redis/go-redis/v9"
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
	for _, queueItem := range queued {
		queueJob, err, typeErr := database.Unwrap[QueueJob](queueItem)
		if err != nil {
			logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err)
			return err
		}
		if typeErr != nil { // not the type we want
			continue
		}
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
