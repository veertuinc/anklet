package github

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/go-github/v66/github"
	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
)

type PluginGlobals struct {
	FirstCheckForCompletedJobsRan bool
	CheckForCompletedJobsMutex    *sync.Mutex
	RetryChannel                  chan QueueJob
	CleanupMutex                  *sync.Mutex
	JobChannel                    chan QueueJob
	PausedCancellationJobChannel  chan QueueJob
	ReturnToMainQueue             chan string
	CheckForCompletedJobsRunCount int
	Unreturnable                  bool
}

func (p *PluginGlobals) SetUnreturnable(unreturnable bool) {
	p.Unreturnable = unreturnable
}

func (p *PluginGlobals) IsUnreturnable() bool {
	return p.Unreturnable
}

func (p *PluginGlobals) IncrementCheckForCompletedJobsRunCount() {
	p.CheckForCompletedJobsRunCount++
}

func GetPluginGlobalsFromContext(ctx context.Context) (*PluginGlobals, error) {
	pluginGlobals, ok := ctx.Value(config.ContextKey("pluginglobals")).(*PluginGlobals)
	if !ok {
		return nil, fmt.Errorf("GetPluginGlobalFromContext failed")
	}
	return pluginGlobals, nil
}

type QueueJob struct {
	Type        string                `json:"type"`
	WorkflowJob SimplifiedWorkflowJob `json:"workflow_job"`
	AnkaVM      anka.VM               `json:"anka_vm"`
	Repository  Repository            `json:"repository"`
	Action      string                `json:"action"`
	PausedOn    string                `json:"paused_on"`
	Attempts    int                   `json:"attempts"`
}

type Repository struct {
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
