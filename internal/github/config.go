package github

import (
	"encoding/json"
	"fmt"

	"github.com/google/go-github/v74/github"
	"github.com/veertuinc/anklet/internal/jobqueue"
)

// QueueJob is the GitHub-specific Redis queue payload. It embeds
// jobqueue.BaseQueueJob to share the Type/Action/Attempts/PausedOn/AnkaVM
// fields with the Azure DevOps plugin.
//
// JSON wire format is preserved bit-for-bit from the pre-Phase-0 layout:
// embedded field tags flatten into the outer object, so the Redis payload
// looks identical to what the GitHub plugin already produces.
type QueueJob struct {
	jobqueue.BaseQueueJob
	WorkflowJob SimplifiedWorkflowJob `json:"workflow_job"`
	Repository  Repository            `json:"repository"`
}

// Matches reports whether other refers to the same workflow run + workflow
// job as the receiver. Used as the Matcher passed to jobqueue.UpdateJobInDB.
func (q QueueJob) Matches(other QueueJob) bool {
	if q.Type != other.Type {
		return false
	}
	if q.WorkflowJob.ID == nil || other.WorkflowJob.ID == nil {
		return false
	}
	if q.WorkflowJob.RunID == nil || other.WorkflowJob.RunID == nil {
		return false
	}
	return *q.WorkflowJob.ID == *other.WorkflowJob.ID &&
		*q.WorkflowJob.RunID == *other.WorkflowJob.RunID
}

// IsCompleted reports whether the queued job is in a terminal state.
// Used as the CompletedPredicate passed to jobqueue.CheckIfJobIsCompleted
// and jobqueue.UpdateJobInDB.
func (q QueueJob) IsCompleted() bool {
	return q.WorkflowJob.Status != nil && *q.WorkflowJob.Status == jobqueue.StatusCompleted
}

// FinishSignal returns a sentinel QueueJob carrying jobqueue.ActionFinish,
// used to tell the handler's main loop to exit. Provided as a helper so
// callsites do not need to spell out the embedded BaseQueueJob initializer.
func FinishSignal() QueueJob {
	return QueueJob{BaseQueueJob: jobqueue.BaseQueueJob{Action: jobqueue.ActionFinish}}
}

type Repository struct {
	Name       *string `json:"name"`
	Owner      *string `json:"owner"`
	Visibility *string `json:"visibility"`
	Private    *bool   `json:"private"`
}

// UnmarshalJSON handles both string and object formats for the Owner field.
// GitHub webhook payloads send owner as an object ({"login": "...", ...}),
// while the Redis queue stores it as a plain string.
func (r *Repository) UnmarshalJSON(data []byte) error {
	type repositoryRaw struct {
		Name       *string         `json:"name"`
		Owner      json.RawMessage `json:"owner"`
		Visibility *string         `json:"visibility"`
		Private    *bool           `json:"private"`
	}
	var raw repositoryRaw
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.Name = raw.Name
	r.Visibility = raw.Visibility
	r.Private = raw.Private
	if raw.Owner != nil && string(raw.Owner) != "null" {
		var s string
		if err := json.Unmarshal(raw.Owner, &s); err == nil {
			r.Owner = &s
			return nil
		}
		var obj struct {
			Login *string `json:"login"`
		}
		if err := json.Unmarshal(raw.Owner, &obj); err != nil {
			return fmt.Errorf("cannot unmarshal repository owner: %w", err)
		}
		r.Owner = obj.Login
	}
	return nil
}

type SimplifiedWorkflowJob struct {
	ID           *int64            `json:"id"`
	Name         *string           `json:"name"`
	RunID        *int64            `json:"run_id"`
	Status       *string           `json:"status"`
	Conclusion   *string           `json:"conclusion"`
	StartedAt    *github.Timestamp `json:"started_at"`
	CompletedAt  *github.Timestamp `json:"completed_at"`
	Labels       []string          `json:"labels"`
	HTMLURL      *string           `json:"html_url"`
	WorkflowName *string           `json:"workflow_name"`
}

type WorkflowRunJobDetail struct {
	JobID           int64
	JobName         string
	JobURL          string
	WorkflowName    string
	AnkaTemplate    string
	AnkaTemplateTag string
	RunID           int64
	Labels          []string
	Repo            string
	Conclusion      string
}
