package github

import (
	"context"
	"fmt"

	"github.com/veertuinc/anklet/internal/jobqueue"
	"github.com/veertuinc/anklet/internal/logging"
)

// UpdateJobInDB updates the queued GitHub QueueJob entry that matches
// upToDateJob. Returns (fatalErr, alreadyCompletedErr) - the second is the
// soft signal callers branch on to decide whether to stop further updates.
//
// Wraps jobqueue.UpdateJobInDB[QueueJob]; the rich "already completed"
// logging that the legacy implementation emitted is preserved here so the
// generic helper stays service-agnostic.
func UpdateJobInDB(pluginCtx context.Context, queue string, upToDateJob *QueueJob) (error, error) {
	completed, existingJob, err := CheckIfJobIsCompleted(pluginCtx, queue)
	if err != nil {
		return fmt.Errorf("error checking if job is completed: %w", err), nil
	}
	if completed {
		logCompletedCollision(pluginCtx, queue, existingJob, upToDateJob)
		return nil, fmt.Errorf("job is already completed")
	}
	return jobqueue.UpdateJobInDB(
		pluginCtx,
		queue,
		upToDateJob,
		QueueJob.Matches,
		QueueJob.IsCompleted,
	)
}

// CheckIfJobIsCompleted reports whether the head-of-queue GitHub job has
// reached the "completed" status. Wraps jobqueue.CheckIfJobIsCompleted with
// QueueJob.IsCompleted as the predicate.
func CheckIfJobIsCompleted(pluginCtx context.Context, pluginQueueName string) (bool, *QueueJob, error) {
	return jobqueue.CheckIfJobIsCompleted(pluginCtx, pluginQueueName, QueueJob.IsCompleted)
}

// logCompletedCollision emits the rich warning the legacy UpdateJobInDB
// produced when an update was attempted against an already-completed job.
// Kept GitHub-specific because the field set (status / conclusion / job_id)
// only makes sense for GitHub QueueJob.
func logCompletedCollision(pluginCtx context.Context, queue string, existing, incoming *QueueJob) {
	var storedStatus, storedConclusion, existingJobID any
	if existing != nil {
		if existing.WorkflowJob.Status != nil {
			storedStatus = *existing.WorkflowJob.Status
		}
		if existing.WorkflowJob.Conclusion != nil {
			storedConclusion = *existing.WorkflowJob.Conclusion
		}
		if existing.WorkflowJob.ID != nil {
			existingJobID = *existing.WorkflowJob.ID
		}
	}
	var incomingStatus, incomingConclusion any
	if incoming != nil {
		if incoming.WorkflowJob.Status != nil {
			incomingStatus = *incoming.WorkflowJob.Status
		}
		if incoming.WorkflowJob.Conclusion != nil {
			incomingConclusion = *incoming.WorkflowJob.Conclusion
		}
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
}
