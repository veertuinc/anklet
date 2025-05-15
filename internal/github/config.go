package github

import (
	"github.com/google/go-github/v66/github"
	"github.com/veertuinc/anklet/internal/anka"
)

type QueueJob struct {
	Type              string                `json:"type"`
	WorkflowJob       SimplifiedWorkflowJob `json:"workflow_job"`
	AnkaVM            anka.VM               `json:"anka_vm"`
	RequiredResources RequiredResources     `json:"required_resources"`
	Repository        Repository            `json:"repository"`
	Action            string                `json:"action"`
}

type Repository struct {
	Name *string `json:"name"`
}

type RequiredResources struct {
	CPU float64 `json:"CPU"`
	MEM float64 `json:"MEM"`
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
