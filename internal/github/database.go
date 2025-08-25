package github

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/logging"
)

func UpdateJobInDB(pluginCtx context.Context, queue string, upToDateJob *QueueJob) error {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return err
	}
	// Find the job in the database by ID and run_id within the plugin queue
	jobList, err := databaseContainer.RetryLRange(pluginCtx, queue, 0, -1)
	if err != nil {
		return fmt.Errorf("error getting job list: %w", err)
	}
	// logger.DebugContext(pluginCtx, "jobList", "jobList", jobList)
	// Find the matching job
	for i, jobStr := range jobList {
		existingJob, err, typeErr := database.Unwrap[QueueJob](jobStr)
		if err != nil || typeErr != nil {
			continue
		}
		// logger.DebugContext(pluginCtx, "existingJob", "existingJob", existingJob)
		// logger.DebugContext(pluginCtx, "upToDateJob", "upToDateJob", upToDateJob)
		if existingJob.Type == upToDateJob.Type &&
			*existingJob.WorkflowJob.ID == *upToDateJob.WorkflowJob.ID &&
			*existingJob.WorkflowJob.RunID == *upToDateJob.WorkflowJob.RunID {
			// Update the job at this index
			updatedJobJSON, err := json.Marshal(upToDateJob)
			if err != nil {
				return fmt.Errorf("error marshaling updated job: %w", err)
			}
			err = databaseContainer.RetryLSet(pluginCtx, queue, int64(i), updatedJobJSON)
			if err != nil {
				return fmt.Errorf("error updating job in database: %w", err)
			}
			logging.Debug(pluginCtx, "job updated in database", "job", upToDateJob)
			// Find the job in the database by ID and run_id within the plugin queue
			// jobList, err := databaseContainer.Client.LRange(pluginCtx, queue, 0, -1).Result()
			// if err != nil {
			// 	return fmt.Errorf("error getting job list: %w", err)
			// }
			// logger.DebugContext(pluginCtx, "jobList", "jobList", jobList)
			return nil
		}
	}
	return fmt.Errorf("job not found in database")
}
