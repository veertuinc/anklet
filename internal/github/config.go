package github

import (
	"encoding/json"
	"fmt"

	"github.com/google/go-github/v75/github"
	"github.com/veertuinc/anklet/internal/anka"
)

type QueueJob struct {
	Type        string                `json:"type"`
	WorkflowJob SimplifiedWorkflowJob `json:"workflow_job"`
	AnkaVM      anka.VM               `json:"anka_vm"`
	Repository  Repository            `json:"repository"`
	Action      string                `json:"action"`
	PausedOn    string                `json:"paused_on"`
	Attempts    int                   `json:"attempts"`
}

// RepositoryOwnerAndName returns the job's repository owner and name for Actions API calls.
func (q QueueJob) RepositoryOwnerAndName() (owner string, name string, err error) {
	if q.Repository.Owner == nil || *q.Repository.Owner == "" {
		return "", "", fmt.Errorf("job repository owner is missing")
	}
	if q.Repository.Name == nil || *q.Repository.Name == "" {
		return "", "", fmt.Errorf("job repository name is missing")
	}
	return *q.Repository.Owner, *q.Repository.Name, nil
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
