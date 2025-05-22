package github

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/database"
)

func GetQueueSize(pluginCtx context.Context, queueName string) (int64, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return 0, fmt.Errorf("error getting database client from context: %s", err.Error())
	}
	return databaseContainer.Client.LLen(pluginCtx, queueName).Result()
}

func GetQueuedJob(pluginCtx context.Context, mainQueueName string, pausedQueueName string) (string, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return "", fmt.Errorf("error getting database client from context: %s", err.Error())
	}
	var queuedJobString string
	// get the next job from the resource specific queue; they get priority for this host
	for {
		// Check if there are any paused jobs that can be resumed
		// Check the length of the paused queue
		pausedQueueLength, err := databaseContainer.Client.LLen(pluginCtx, pausedQueueName).Result()
		if err != nil {
			return "", fmt.Errorf("error getting paused queue length: %s", err.Error())
		}
		if pausedQueueLength == 0 {
			break
		}
		pausedJobString, err := databaseContainer.Client.LPop(pluginCtx, pausedQueueName).Result()
		if err == redis.Nil {
			break
		}
		if err != nil {
			return "", fmt.Errorf("error getting paused jobs: %s", err.Error())
		}

		// Process each paused job
		var typeErr error
		pausedJob, err, typeErr := database.Unwrap[QueueJob](pausedJobString)
		if err != nil || typeErr != nil {
			return "", fmt.Errorf("error unmarshalling job: %s", err.Error())
		}
		err = anka.VmHasEnoughHostResources(pluginCtx, pausedJob.AnkaVM)
		if err != nil {
			databaseContainer.Client.RPush(pluginCtx, pausedQueueName, pausedJobString)
			if pausedQueueLength == 1 { // don't go into a forever loop
				break
			}
			continue
		}
		err = anka.VmHasEnoughResources(pluginCtx, pausedJob.AnkaVM)
		if err != nil {
			databaseContainer.Client.RPush(pluginCtx, pausedQueueName, pausedJobString)
			if pausedQueueLength == 1 { // don't go into a forever loop
				break
			}
			continue
		}
		queuedJobString = pausedJobString
		break
	}
	// get the next job from the main queue if nothing in hostResourceQueue
	if queuedJobString == "" {
		queuedJobString, err = databaseContainer.Client.LPop(pluginCtx, mainQueueName).Result()
		if err == redis.Nil {
			return "", nil
		}
		if err != nil {
			databaseContainer.Client.RPush(pluginCtx, mainQueueName, queuedJobString)
			return "", fmt.Errorf("error getting queued jobs: %s", err.Error())
		}
	}
	return queuedJobString, nil
}
