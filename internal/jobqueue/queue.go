package jobqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/database"
)

// GetQueueSize returns the number of items in the named Redis list.
func GetQueueSize(ctx context.Context, queueName string) (int64, error) {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("error getting database client from context: %w", err)
	}
	return databaseContainer.RetryLLen(ctx, queueName)
}

// QueueHasJobs reports whether the named Redis list has at least one entry.
func QueueHasJobs(ctx context.Context, queueName string) (bool, error) {
	size, err := GetQueueSize(ctx, queueName)
	if err != nil {
		return false, err
	}
	return size > 0, nil
}

// PopJobOffQueue removes and returns the element at queueTargetIndex from a
// Redis list. It uses LRange + LRem (instead of LPop) so callers can skip past
// jobs they cannot run yet by passing a non-zero queueTargetIndex. Returns
// ("", nil) if the queue is empty or the targeted element was already removed
// by another worker between the LRange and LRem.
func PopJobOffQueue(ctx context.Context, queueName string, queueTargetIndex int64) (string, error) {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("error getting database client from context: %w", err)
	}
	queuedJobs, err := databaseContainer.RetryLRange(ctx, queueName, queueTargetIndex, -1)
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("error getting queued job: %w", err)
	}
	if len(queuedJobs) == 0 {
		return "", nil
	}
	target := queuedJobs[0]
	removed, err := databaseContainer.RetryLRem(ctx, queueName, 1, target)
	if err != nil {
		return "", fmt.Errorf("error removing queued job: %w", err)
	}
	if removed == 1 {
		return target, nil
	}
	return "", nil
}

// GetJobJSONByJSONPath returns the raw JSON of the first list entry whose
// dotted JSON path resolves to the given string-formatted value.
//
// path is dot-delimited: "workflow_job.id" walks {"workflow_job": {"id": ...}}.
// Numbers, bools, strings, and JSON-marshalable values are stringified before
// comparison so callers can pass an int formatted with strconv etc.
//
// Replaces the GitHub-named "GetJobFromQueueByKeyAndValue" helper - this
// version is service-agnostic since it works on raw JSON, not typed structs.
func GetJobJSONByJSONPath(ctx context.Context, queueName, path, value string) (string, error) {
	databaseContainer, err := database.GetDatabaseFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("error getting database client from context: %w", err)
	}
	queuedJobsString, err := databaseContainer.RetryLRange(ctx, queueName, 0, -1)
	if err != nil {
		return "", fmt.Errorf("error getting queued jobs: %w", err)
	}
	keyParts := strings.Split(path, ".")
	for _, jobStr := range queuedJobsString {
		var jobMap map[string]any
		if err := json.Unmarshal([]byte(jobStr), &jobMap); err != nil {
			return "", fmt.Errorf("error unmarshalling job to map: %w", err)
		}
		if stringifyJSONPath(jobMap, keyParts) == value {
			return jobStr, nil
		}
	}
	return "", nil
}

// stringifyJSONPath walks dotted keys through a JSON-decoded map and returns
// the leaf value formatted as a string. Returns "" when any segment is missing.
func stringifyJSONPath(root map[string]any, keyParts []string) string {
	var current any = root
	for _, part := range keyParts {
		m, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = m[part]
		if current == nil {
			return ""
		}
	}
	switch v := current.(type) {
	case string:
		return v
	case float64:
		// JSON numbers decode as float64; format ID-like values as integers.
		return fmt.Sprintf("%d", int64(v))
	case bool:
		return fmt.Sprintf("%t", v)
	default:
		bytes, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(bytes)
	}
}
