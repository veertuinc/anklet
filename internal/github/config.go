package github

import (
	"github.com/google/go-github/v66/github"
)

type SimplifiedWorkflowJobEvent struct {
	WorkflowJob SimplifiedWorkflowJob `json:"workflow_job"`
	Action      string                `json:"action"`
}

type SimplifiedWorkflowJob struct {
	ID          *int64            `json:"id"`
	Name        *string           `json:"name"`
	RunID       *int64            `json:"run_id"`
	Status      *string           `json:"status"`
	Conclusion  *string           `json:"conclusion"`
	StartedAt   *github.Timestamp `json:"started_at"`
	CompletedAt *github.Timestamp `json:"completed_at"`
	Labels      []string          `json:"labels"`
	HTMLURL     *string           `json:"html_url"`
}
