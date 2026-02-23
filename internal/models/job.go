package models

import (
	"time"

	"github.com/veertuinc/anklet/internal/anka"
)

type QueueJob struct {
	Type       string     `json:"type"`
	Job        Job        `json:"job"`
	AnkaVM     anka.VM    `json:"anka_vm"`
	Repository Repository `json:"repository"`
	Action     string     `json:"action"`
	PausedOn   string     `json:"paused_on"`
	Attempts   int        `json:"attempts"`
}

type Repository struct {
	Name  *string `json:"name"`
	Owner *string `json:"owner"`
}

type Job struct {
	ID           *int64     `json:"id"`
	Name         *string    `json:"name"`
	RunID        *int64     `json:"run_id"`
	Status       *string    `json:"status"`
	Conclusion   *string    `json:"conclusion"`
	StartedAt    *time.Time `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	Labels       []string   `json:"labels"`
	HTMLURL      *string    `json:"html_url"`
	WorkflowName *string    `json:"workflow_name"`
}
