package github

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/google/go-github/v66/github"
	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
)

type PluginGlobals struct {
	FirstCheckForCompletedJobsRan int32         // atomic flag: 0 = false, 1 = true - allows us to do a full check for completed jobs on startup
	RetryChannel                  chan string   // Send a string to this channel to send the job back to the main queue after checking for reason
	CleanupMutex                  *sync.Mutex   // prevent multiple cleanup jobs from running at the same time
	JobChannel                    chan QueueJob // Used in the CheckForCompletedJobs to handle logic specific to the job
	PausedCancellationJobChannel  chan QueueJob // If a job is paused on the host, getting a job in this channel cleans it up so another host can continue to handle it
	ReturnToMainQueue             chan string   // Similar to the RetryChannel, but used to acually send the job back to the main queue
	CheckForCompletedJobsRunCount int32         // atomic counter - Used to track how many times the CheckForCompletedJobs function has run so we can log every 5 runs the "job is still in progress" message
	Unreturnable                  bool          // Used to prevent a job from being returned to the main queue if it's not returnable
}

func (p *PluginGlobals) SetUnreturnable(unreturnable bool) {
	p.Unreturnable = unreturnable
}

func (p *PluginGlobals) IsUnreturnable() bool {
	return p.Unreturnable
}

func (p *PluginGlobals) IncrementCheckForCompletedJobsRunCount() {
	atomic.AddInt32(&p.CheckForCompletedJobsRunCount, 1)
}

func (p *PluginGlobals) GetCheckForCompletedJobsRunCount() int32 {
	return atomic.LoadInt32(&p.CheckForCompletedJobsRunCount)
}

func (p *PluginGlobals) SetFirstCheckForCompletedJobsRan(ran bool) {
	var value int32
	if ran {
		value = 1
	}
	atomic.StoreInt32(&p.FirstCheckForCompletedJobsRan, value)
}

func (p *PluginGlobals) GetFirstCheckForCompletedJobsRan() bool {
	return atomic.LoadInt32(&p.FirstCheckForCompletedJobsRan) == 1
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
	Name       *string `json:"name"`
	Visibility *string `json:"visibility"`
	Private    *bool   `json:"private"`
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
