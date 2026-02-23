package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	return databaseContainer.RetryLLen(pluginCtx, queueName)
}

func GetJobJSONFromQueueByID(pluginCtx context.Context, jobID int64, queueName string) (string, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logging.Panic(pluginCtx, pluginCtx, "error getting database client from context: "+err.Error())
	}
	localCtx := context.Background()
	if logger, err := logging.GetLoggerFromContext(pluginCtx); err == nil {
		localCtx = context.WithValue(localCtx, config.ContextKey("logger"), logger)
	}
	queued, err := databaseContainer.RetryLRange(localCtx, queueName, 0, -1)
	if err != nil {
		logging.Error(pluginCtx, "error getting list of queued jobs", "err", err)
		return "", err
	}
	for _, queueItem := range queued {
		queueJob, err, typeErr := database.Unwrap[QueueJob](queueItem)
		if err != nil {
			logging.Error(pluginCtx, "error unmarshalling job", "err", err)
			return "", err
		}
		if typeErr != nil {
			continue
		}
		if queueJob.Job.ID == nil {
			continue
		}
		if *queueJob.Job.ID == jobID {
			return queueItem, nil
		}
	}
	return "", nil
}

func GetJobFromQueue(pluginCtx context.Context, queueName string) (QueueJob, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logging.Panic(pluginCtx, pluginCtx, "error getting database client from context: "+err.Error())
	}
	localCtx := context.Background()
	if logger, err := logging.GetLoggerFromContext(pluginCtx); err == nil {
		localCtx = context.WithValue(localCtx, config.ContextKey("logger"), logger)
	}
	queuedJobJSON, err := databaseContainer.RetryLIndex(localCtx, queueName, 0)
	if err != nil {
		return QueueJob{}, err
	}
	queueJob, err, typeErr := database.Unwrap[QueueJob](queuedJobJSON)
	if err != nil {
		return QueueJob{}, err
	}
	if typeErr != nil {
		return QueueJob{}, fmt.Errorf("not the type we want")
	}
	return queueJob, nil
}

func DeleteFromQueue(ctx context.Context, jobID int64, queueName string) error {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		return fmt.Errorf("error getting database client from context: %s", err.Error())
	}
	innerContext := context.Background()
	if logger, err := logging.GetLoggerFromContext(ctx); err == nil {
		innerContext = context.WithValue(innerContext, config.ContextKey("logger"), logger)
	}
	queued, err := databaseContainer.RetryLRange(innerContext, queueName, 0, -1)
	if err != nil {
		return err
	}
	for _, queueItem := range queued {
		queueJob, err, typeErr := database.Unwrap[QueueJob](queueItem)
		if err != nil {
			return err
		}
		if typeErr != nil || queueJob.Job.ID == nil {
			continue
		}
		if *queueJob.Job.ID == jobID {
			_, err := databaseContainer.RetryLRem(innerContext, queueName, 1, queueItem)
			return err
		}
	}
	return nil
}

func QueueHasJobs(pluginCtx context.Context, queueName string) (bool, error) {
	queueSize, err := GetQueueSize(pluginCtx, queueName)
	if err != nil {
		return false, err
	}
	return queueSize > 0, nil
}

func PopJobOffQueue(pluginCtx context.Context, queueName string, queueTargetIndex int64) (string, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return "", fmt.Errorf("error getting database client from context: %s", err.Error())
	}
	queuedJobsString, err := databaseContainer.RetryLRange(pluginCtx, queueName, queueTargetIndex, -1)
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
	success, err := databaseContainer.RetryLRem(pluginCtx, queueName, 1, targetElement)
	if err != nil {
		return "", fmt.Errorf("error removing queued job: %s", err.Error())
	}
	if success == 1 {
		return targetElement, nil
	}
	return "", nil
}

func GetJobFromQueueByKeyAndValue(pluginCtx context.Context, queueName string, key string, value string) (string, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return "", fmt.Errorf("error getting database client from context: %s", err.Error())
	}
	queuedJobsString, err := databaseContainer.RetryLRange(pluginCtx, queueName, 0, -1)
	if err != nil {
		return "", fmt.Errorf("error getting queued jobs: %s", err.Error())
	}
	for _, job := range queuedJobsString {
		var jobMap map[string]any
		if err := json.Unmarshal([]byte(job), &jobMap); err != nil {
			return "", err
		}
		keyParts := strings.Split(key, ".")
		var current any = jobMap
		for _, part := range keyParts {
			currentMap, ok := current.(map[string]any)
			if !ok {
				current = nil
				break
			}
			current = currentMap[part]
			if current == nil {
				break
			}
		}
		var currentStr string
		switch v := current.(type) {
		case string:
			currentStr = v
		case float64:
			currentStr = fmt.Sprintf("%d", int64(v))
		case bool:
			currentStr = fmt.Sprintf("%t", v)
		case nil:
			currentStr = ""
		default:
			bytes, err := json.Marshal(v)
			if err == nil {
				currentStr = string(bytes)
			}
		}
		if currentStr == value {
			return job, nil
		}
	}
	return "", nil
}

func CheckIfJobExistsInHandlerQueues(
	pluginCtx context.Context,
	workflowRunID int64,
	jobID int64,
	owner string,
	provider string,
) (bool, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return false, fmt.Errorf("error getting database client from context: %s", err.Error())
	}
	handlerQueuePattern := fmt.Sprintf("anklet/jobs/%s/queued/%s/*", provider, owner)
	handlerQueueKeys, err := databaseContainer.RetryKeys(pluginCtx, handlerQueuePattern)
	if err != nil {
		return false, fmt.Errorf("error getting handler queue keys: %s", err.Error())
	}
	if len(handlerQueueKeys) == 0 {
		return false, nil
	}
	for _, key := range handlerQueueKeys {
		firstJobJSON, err := databaseContainer.RetryLIndex(pluginCtx, key, 0)
		if err != nil || firstJobJSON == "" {
			continue
		}
		queueJob, err, typeErr := database.Unwrap[QueueJob](firstJobJSON)
		if err != nil || typeErr != nil || queueJob.Job.ID == nil || queueJob.Job.RunID == nil {
			continue
		}
		if *queueJob.Job.RunID == workflowRunID && *queueJob.Job.ID == jobID {
			return true, nil
		}
	}
	return false, nil
}
