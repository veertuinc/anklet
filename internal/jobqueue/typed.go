package jobqueue

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/veertuinc/anklet/internal/database"
)

// IDExtractor returns the unique identifier for a queued job of type T.
// Used by GetJobJSONByID and DeleteByID.
type IDExtractor[T any, ID comparable] func(T) ID

// Matcher reports whether two queued jobs refer to the same logical work
// (e.g. same workflow_run + workflow_job for GitHub, same Job.ID for ADO).
// Used by UpdateJobInDB to find the entry that needs updating.
type Matcher[T any] func(existing, target T) bool

// CompletedPredicate reports whether a queued job has reached a terminal
// state. Used by CheckIfJobIsCompleted and UpdateJobInDB.
type CompletedPredicate[T any] func(T) bool

// GetJobJSONByID returns the raw JSON of the first list entry whose ID
// (as returned by extractID) matches target. Returns ("", nil) if not found.
// JSON entries that fail to decode into T are skipped (treated as not-a-match)
// so a poison entry never blocks lookup.
func GetJobJSONByID[T any, ID comparable](
	ctx context.Context,
	queue string,
	target ID,
	extractID IDExtractor[T, ID],
) (string, error) {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("error getting database client from context: %w", err)
	}
	queued, err := databaseContainer.RetryLRange(ctx, queue, 0, -1)
	if err != nil {
		return "", fmt.Errorf("error reading queue %q: %w", queue, err)
	}
	for _, item := range queued {
		job, parseErr, _ := database.Unwrap[T](item)
		if parseErr != nil {
			continue
		}
		if extractID(job) == target {
			return item, nil
		}
	}
	return "", nil
}

// DeleteByID removes the first list entry whose ID matches target.
// Returns nil whether or not anything was found (idempotent).
func DeleteByID[T any, ID comparable](
	ctx context.Context,
	queue string,
	target ID,
	extractID IDExtractor[T, ID],
) error {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		return fmt.Errorf("error getting database client from context: %w", err)
	}
	queued, err := databaseContainer.RetryLRange(ctx, queue, 0, -1)
	if err != nil {
		return fmt.Errorf("error reading queue %q: %w", queue, err)
	}
	if len(queued) == 0 {
		return nil
	}
	for _, item := range queued {
		job, parseErr, _ := database.Unwrap[T](item)
		if parseErr != nil {
			continue
		}
		if extractID(job) != target {
			continue
		}
		removed, err := databaseContainer.RetryLRem(ctx, queue, 1, item)
		if err != nil {
			return fmt.Errorf("error removing job from queue %q: %w", queue, err)
		}
		if removed != 1 {
			return fmt.Errorf("job %v not removed from queue %q", target, queue)
		}
		return nil
	}
	return nil
}

// CheckIfJobIsCompleted inspects the first job in pluginQueueName and reports
// whether it is in a terminal state per isCompleted. Returns the parsed job
// (nil if the queue is empty or the entry could not be decoded).
//
// Mirrors the existing internal/github.CheckIfJobIsCompleted contract: an
// empty queue surfaces as an error so callers can distinguish "nothing here"
// from "job exists and is in progress".
func CheckIfJobIsCompleted[T any](
	ctx context.Context,
	pluginQueueName string,
	isCompleted CompletedPredicate[T],
) (bool, *T, error) {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		return false, nil, err
	}
	jobStr, err := databaseContainer.RetryLIndex(ctx, pluginQueueName, 0)
	if err != nil {
		return false, nil, fmt.Errorf("error getting first job from queue: %w", err)
	}
	if jobStr == "" {
		return false, nil, fmt.Errorf("no job found in queue")
	}
	job, parseErr, _ := database.Unwrap[T](jobStr)
	if parseErr != nil {
		return false, nil, fmt.Errorf("error unmarshalling job: %w", parseErr)
	}
	return isCompleted(job), &job, nil
}

// UpdateJobInDB replaces the first list entry that matches upToDate (per
// matches) with a marshalled copy of upToDate. Refuses to overwrite when the
// existing head-of-queue job is already in a terminal state per isCompleted -
// matches the existing GitHub helper's "don't clobber a completed job" guard.
//
// Two-error return preserves the existing internal/github contract: the first
// error is fatal (DB / marshal / not-found); the second is the soft "already
// completed" signal callers branch on.
func UpdateJobInDB[T any](
	ctx context.Context,
	queue string,
	upToDate *T,
	matches Matcher[T],
	isCompleted CompletedPredicate[T],
) (error, error) {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		return err, nil
	}
	completed, _, err := CheckIfJobIsCompleted[T](ctx, queue, isCompleted)
	if err != nil {
		return fmt.Errorf("error checking if job is completed: %w", err), nil
	}
	if completed {
		return nil, fmt.Errorf("job is already completed")
	}
	jobList, err := databaseContainer.RetryLRange(ctx, queue, 0, -1)
	if err != nil {
		return fmt.Errorf("error getting job list: %w", err), nil
	}
	for i, jobStr := range jobList {
		existing, parseErr, _ := database.Unwrap[T](jobStr)
		if parseErr != nil {
			continue
		}
		if !matches(existing, *upToDate) {
			continue
		}
		updatedJSON, err := json.Marshal(upToDate)
		if err != nil {
			return fmt.Errorf("error marshaling updated job: %w", err), nil
		}
		if err := databaseContainer.RetryLSet(ctx, queue, int64(i), updatedJSON); err != nil {
			return fmt.Errorf("error updating job in database: %w", err), nil
		}
		return nil, nil
	}
	return fmt.Errorf("job not found in database"), nil
}

// CheckIfJobExistsInHandlerQueues fans out across every Redis key matching
// queuePattern and reports whether any of them has a head-of-queue entry
// matching predicate. Used by the receiver to short-circuit re-enqueuing a
// job a handler is already actively processing.
//
// Lookup runs in parallel across keys for latency; the function returns at
// the first match without waiting for the rest.
func CheckIfJobExistsInHandlerQueues[T any](
	ctx context.Context,
	queuePattern string,
	predicate func(T) bool,
) (bool, error) {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		return false, fmt.Errorf("error getting database client from context: %w", err)
	}
	keys, err := databaseContainer.RetryKeys(ctx, queuePattern)
	if err != nil {
		return false, fmt.Errorf("error getting handler queue keys: %w", err)
	}
	if len(keys) == 0 {
		return false, nil
	}
	resultChan := make(chan bool, len(keys))
	for _, key := range keys {
		go func(k string) {
			firstJob, err := databaseContainer.RetryLIndex(ctx, k, 0)
			if err != nil || firstJob == "" {
				resultChan <- false
				return
			}
			job, parseErr, _ := database.Unwrap[T](firstJob)
			if parseErr != nil {
				resultChan <- false
				return
			}
			resultChan <- predicate(job)
		}(key)
	}
	for range len(keys) {
		if <-resultChan {
			return true, nil
		}
	}
	return false, nil
}
