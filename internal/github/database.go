package github

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/logging"
)

func InQueue(pluginCtx context.Context, logger *slog.Logger, jobID int64, queue string) (bool, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		logging.Panic(pluginCtx, pluginCtx, "error getting database client from context: "+err.Error())
	}
	queued, err := databaseContainer.Client.LRange(pluginCtx, queue, 0, -1).Result()
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting list of queued jobs", "err", err)
		return false, err
	}
	for _, queueItem := range queued {
		queueJob, err, typeErr := database.Unwrap[QueueJob](queueItem)
		if err != nil {
			logger.ErrorContext(pluginCtx, "error unmarshalling job", "err", err)
			return false, err
		}
		if typeErr != nil { // not the type we want
			continue
		}
		if queueJob.WorkflowJob.ID == nil {
			logger.ErrorContext(pluginCtx, "WorkflowJob.ID is nil", "WorkflowJob", queueJob.WorkflowJob)
			return false, fmt.Errorf("WorkflowJob.ID is nil")
		}
		if *queueJob.WorkflowJob.ID == jobID {
			// logger.WarnContext(pluginCtx, "WorkflowJob.ID already in queue", "WorkflowJob.ID", jobID)
			return true, nil
		}
	}
	return false, nil
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

func UpdateJobInDB(pluginCtx context.Context, queue string, upToDateJob *QueueJob) error {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return err
	}
	// Find the job in the database by ID and run_id within the plugin queue
	jobList, err := databaseContainer.Client.LRange(pluginCtx, queue, 0, -1).Result()
	if err != nil {
		return fmt.Errorf("error getting job list: %w", err)
	}
	// Find the matching job
	for i, jobStr := range jobList {
		var existingJob QueueJob
		_, err, typeErr := database.Unwrap[QueueJob](jobStr)
		if err != nil || typeErr != nil {
			continue
		}
		if existingJob.WorkflowJob.ID == upToDateJob.WorkflowJob.ID &&
			existingJob.WorkflowJob.RunID == upToDateJob.WorkflowJob.RunID {
			// Update the job at this index
			updatedJob, err := json.Marshal(upToDateJob)
			if err != nil {
				return fmt.Errorf("error marshaling updated job: %w", err)
			}
			err = databaseContainer.Client.LSet(pluginCtx, queue, int64(i), updatedJob).Err()
			if err != nil {
				return fmt.Errorf("error updating job in database: %w", err)
			}
			return nil
		}
	}

	return fmt.Errorf("job not found in database")
}
