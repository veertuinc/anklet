package queue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/logging"
)

func UpdateJobInDB(pluginCtx context.Context, queueName string, upToDateJob *QueueJob) (error, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return err, nil
	}
	completed, existingJob, err := CheckIfJobIsCompleted(pluginCtx, queueName)
	if err != nil {
		return fmt.Errorf("error checking if job is completed: %w", err), nil
	}
	if completed {
		var storedStatus any
		if existingJob != nil && existingJob.Job.Status != nil {
			storedStatus = *existingJob.Job.Status
		}
		var incomingStatus any
		if upToDateJob.Job.Status != nil {
			incomingStatus = *upToDateJob.Job.Status
		}
		logging.Warn(pluginCtx, "job in DB is already completed", "stored_status", storedStatus, "incoming_status", incomingStatus, "queue", queueName)
		return nil, fmt.Errorf("job is already completed")
	}
	jobList, err := databaseContainer.RetryLRange(pluginCtx, queueName, 0, -1)
	if err != nil {
		return fmt.Errorf("error getting job list: %w", err), nil
	}
	for i, jobStr := range jobList {
		existingJob, err, typeErr := database.Unwrap[QueueJob](jobStr)
		if err != nil || typeErr != nil {
			continue
		}
		if existingJob.Type == upToDateJob.Type &&
			existingJob.Job.ID != nil && upToDateJob.Job.ID != nil &&
			*existingJob.Job.ID == *upToDateJob.Job.ID &&
			existingJob.Job.RunID != nil && upToDateJob.Job.RunID != nil &&
			*existingJob.Job.RunID == *upToDateJob.Job.RunID {
			updatedJobJSON, err := json.Marshal(upToDateJob)
			if err != nil {
				return fmt.Errorf("error marshaling updated job: %w", err), nil
			}
			if err := databaseContainer.RetryLSet(pluginCtx, queueName, int64(i), updatedJobJSON); err != nil {
				return fmt.Errorf("error updating job in database: %w", err), nil
			}
			return nil, nil
		}
	}
	return fmt.Errorf("job not found in database"), nil
}

func CheckIfJobIsCompleted(pluginCtx context.Context, pluginQueueName string) (bool, *QueueJob, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return false, nil, err
	}
	jobStr, err := databaseContainer.RetryLIndex(pluginCtx, pluginQueueName, 0)
	if err != nil {
		return false, nil, fmt.Errorf("error getting first job from queue: %w", err)
	}
	if jobStr == "" {
		return false, nil, fmt.Errorf("no job found in queue")
	}
	job, err, typeErr := database.Unwrap[QueueJob](jobStr)
	if err != nil || typeErr != nil {
		return false, nil, fmt.Errorf("error unmarshalling job")
	}
	if job.Job.Status != nil && *job.Job.Status == "completed" {
		return true, &job, nil
	}
	return false, &job, nil
}
