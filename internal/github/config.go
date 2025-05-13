package github

import (
	"github.com/google/go-github/v66/github"
)

type SimplifiedWorkflowJobEvent struct {
	WorkflowJob SimplifiedWorkflowJob `json:"workflow_job"`
	Action      string                `json:"action"`
	Repository  SimplifiedRepository  `json:"repository"`
}

type SimplifiedRepository struct {
	Name *string `json:"name"`
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
