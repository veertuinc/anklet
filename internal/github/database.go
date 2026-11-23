package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/logging"
)

// ErrJobAlreadyCompleted indicates the queue head job is already marked completed in Redis.
var ErrJobAlreadyCompleted = errors.New("job is already completed")

func int64PtrEqual(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// UpdateJobInDB replaces the matching job in queue unless the head job is already completed
// (in which case ErrJobAlreadyCompleted is returned).
func UpdateJobInDB(pluginCtx context.Context, queue string, upToDateJob *QueueJob) error {
	if upToDateJob == nil || upToDateJob.WorkflowJob.ID == nil {
		return fmt.Errorf("cannot update job in database: workflow job ID is nil")
	}
	databaseContainer, err := database.GetDatabaseFromContext(pluginCtx)
	if err != nil {
		return err
	}

	completed, existingJob, err := CheckIfJobIsCompleted(pluginCtx, queue)
	if err != nil {
		return fmt.Errorf("error checking if job is completed: %w", err)
	}
	if completed {
		var storedStatus, storedConclusion, incomingStatus, incomingConclusion, existingJobID any
		if existingJob != nil {
			if existingJob.WorkflowJob.Status != nil {
				storedStatus = *existingJob.WorkflowJob.Status
			}
			if existingJob.WorkflowJob.Conclusion != nil {
				storedConclusion = *existingJob.WorkflowJob.Conclusion
			}
			if existingJob.WorkflowJob.ID != nil {
				existingJobID = *existingJob.WorkflowJob.ID
			}
		}
		if upToDateJob.WorkflowJob.Status != nil {
			incomingStatus = *upToDateJob.WorkflowJob.Status
		}
		if upToDateJob.WorkflowJob.Conclusion != nil {
			incomingConclusion = *upToDateJob.WorkflowJob.Conclusion
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
		return ErrJobAlreadyCompleted
	}

	jobList, err := databaseContainer.RetryLRange(pluginCtx, queue, 0, -1)
	if err != nil {
		return fmt.Errorf("error getting job list: %w", err)
	}
	for i, jobStr := range jobList {
		existing, unwrapErr := database.Unwrap[QueueJob](jobStr)
		if unwrapErr != nil {
			continue
		}
		if existing.Type == upToDateJob.Type &&
			int64PtrEqual(existing.WorkflowJob.ID, upToDateJob.WorkflowJob.ID) &&
			int64PtrEqual(existing.WorkflowJob.RunID, upToDateJob.WorkflowJob.RunID) {
			updatedJobJSON, marshalErr := json.Marshal(upToDateJob)
			if marshalErr != nil {
				return fmt.Errorf("error marshaling updated job: %w", marshalErr)
			}
			if setErr := databaseContainer.RetryLSet(pluginCtx, queue, int64(i), updatedJobJSON); setErr != nil {
				return fmt.Errorf("error updating job in database: %w", setErr)
			}
			logging.Debug(pluginCtx, "job updated in database", "job", upToDateJob)
			return nil
		}
	}
	return fmt.Errorf("job not found in database")
}

// CheckIfJobIsCompleted checks whether the head job in pluginQueueName is already completed.
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
	job, err := database.Unwrap[QueueJob](jobStr)
	if err != nil {
		return false, nil, fmt.Errorf("error unmarshalling job: %w", err)
	}
	if job.WorkflowJob.Status != nil && *job.WorkflowJob.Status == "completed" {
		return true, &job, nil
	}
	return false, &job, nil
}
