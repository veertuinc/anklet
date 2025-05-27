package github

import (
	"context"
	"fmt"
	"reflect"

	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/database"
)

func GetQueueSize(pluginCtx context.Context, queueName string) (int64, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return 0, fmt.Errorf("error getting database client from context: %s", err.Error())
	}
	return databaseContainer.Client.LLen(pluginCtx, queueName).Result()
}

func GetQueuedJob(
	pluginCtx context.Context,
	queueName string,
	queueTargetIndex int64,
) (string, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return "", fmt.Errorf("error getting database client from context: %s", err.Error())
	}
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
) (QueueJob, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return QueueJob{}, fmt.Errorf("error getting database client from context: %s", err.Error())
	}
	queuedJobsString, err := databaseContainer.Client.LRange(pluginCtx, queueName, 0, -1).Result()
	if err != nil {
		return QueueJob{}, fmt.Errorf("error getting queued jobs: %s", err.Error())
	}
	for _, job := range queuedJobsString {
		queuedJob, err, typeErr := database.Unwrap[QueueJob](job)
		if err != nil || typeErr != nil {
			return QueueJob{}, fmt.Errorf("error unmarshalling job: %s", err.Error())
		}
		// Dynamically access the field using reflection
		val := reflect.ValueOf(queuedJob)
		field := val.FieldByName(key)
		if field.IsValid() && field.Kind() == reflect.String && field.String() == value {
			return queuedJob, nil
		}
	}
	return QueueJob{}, nil
}
