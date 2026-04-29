package jobqueue

import "github.com/veertuinc/anklet/internal/anka"

// BaseQueueJob holds the fields every plugin's QueueJob carries regardless of
// the upstream CI provider. Provider-specific QueueJob types embed this struct
// so the jobqueue helpers (and any future shared lifecycle code) can operate on
// the common surface without knowing about WorkflowJob, Job, PipelineRun, etc.
//
// JSON tags match the existing wire format used by the GitHub plugin so the
// embed change does not break any payloads already sitting in Redis.
type BaseQueueJob struct {
	Type     string  `json:"type"`
	Action   string  `json:"action"`
	Attempts int     `json:"attempts"`
	PausedOn string  `json:"paused_on"`
	AnkaVM   anka.VM `json:"anka_vm"`
}

// Normalized status vocabulary written into a queued job's WorkflowJob.Status
// (GitHub) or surfaced by Azure DevOps' MapJobStateToAction. Each provider's
// status mapper translates its native vocabulary into these.
const (
	StatusQueued     = "queued"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusCanceled   = "canceled"
	StatusFailed     = "failed"
)

// Action vocabulary written into BaseQueueJob.Action. Some values are direct
// status mirrors (Queued/InProgress/Completed/Canceled); the rest are internal
// sentinels used by the handler control loop.
const (
	ActionQueued     = "queued"
	ActionInProgress = "in_progress"
	ActionCompleted  = "completed"
	ActionCanceled   = "canceled"
	// ActionCancel is a request the handler emits to itself when it has
	// decided the job must be cancelled (e.g. host lost capacity); the
	// downstream cleanup logic acts on it.
	ActionCancel = "cancel"
	// ActionFinish tells the handler's main loop to exit cleanly without
	// touching the queue further.
	ActionFinish = "finish"
	// ActionPaused marks a job that has been parked on a specific host so
	// another host can pick it up later.
	ActionPaused = "paused"
)
