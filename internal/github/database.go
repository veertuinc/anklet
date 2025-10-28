package github

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/logging"
)

func UpdateJobInDB(pluginCtx context.Context, queue string, upToDateJob *QueueJob) (error, error) {
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return err, nil
	}

	// do a check to see if the object in the DB is already completed and prevent overwriting it
	completed, existingJob, err := CheckIfJobIsCompleted(pluginCtx, queue)
	if err != nil {
		return fmt.Errorf("error checking if job is completed: %w", err), nil
	}
	if completed {
		var storedStatus any
		if existingJob != nil && existingJob.WorkflowJob.Status != nil {
			storedStatus = *existingJob.WorkflowJob.Status
		}
		var storedConclusion any
		if existingJob != nil && existingJob.WorkflowJob.Conclusion != nil {
			storedConclusion = *existingJob.WorkflowJob.Conclusion
		}
		var incomingStatus any
		if upToDateJob.WorkflowJob.Status != nil {
			incomingStatus = *upToDateJob.WorkflowJob.Status
		}
		var incomingConclusion any
		if upToDateJob.WorkflowJob.Conclusion != nil {
			incomingConclusion = *upToDateJob.WorkflowJob.Conclusion
		}
		var existingJobID any
		if existingJob != nil && existingJob.WorkflowJob.ID != nil {
			existingJobID = *existingJob.WorkflowJob.ID
		}
		logging.Warn(
			pluginCtx,
			"job in DB is already completed",
			"job_id", existingJobID,
			"stored_status", storedStatus,
			"stored_conclusion", storedConclusion,
			"incoming_status", incomingStatus,
			"incoming_conclusion", incomingConclusion,
			"queue", queue,
		)
		return nil, fmt.Errorf("job is already completed")
	}

	// Find the job in the database by ID and run_id within the plugin queue
	jobList, err := databaseContainer.RetryLRange(pluginCtx, queue, 0, -1)
	if err != nil {
		return fmt.Errorf("error getting job list: %w", err), nil
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
				return fmt.Errorf("error marshaling updated job: %w", err), nil
			}
			err = databaseContainer.RetryLSet(pluginCtx, queue, int64(i), updatedJobJSON)
			if err != nil {
				return fmt.Errorf("error updating job in database: %w", err), nil
			}
			logging.Debug(pluginCtx, "job updated in database", "job", upToDateJob)
			// Find the job in the database by ID and run_id within the plugin queue
			// jobList, err := databaseContainer.Client.LRange(pluginCtx, queue, 0, -1).Result()
			// if err != nil {
			// 	return fmt.Errorf("error getting job list: %w", err)
			// }
			// logger.DebugContext(pluginCtx, "jobList", "jobList", jobList)
			return nil, nil
		}
	}
	return fmt.Errorf("job not found in database"), nil
}

// check if the job in the DB is completed status
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
	if job.WorkflowJob.Status != nil && *job.WorkflowJob.Status == "completed" {
		return true, &job, nil
	}
	return false, &job, nil
}
