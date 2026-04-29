package jobqueue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/veertuinc/anklet/internal/config"
)

// PluginGlobals holds per-handler-instance lifecycle state shared across the
// goroutines a single plugin invocation spawns. Generic over J so the channels
// carry the provider's own QueueJob type without unsafe casts.
//
// The atomic flags / counter are kept as int32 (rather than atomic.Bool /
// atomic.Int32) for binary compatibility with code that reads/writes them via
// atomic.* directly; do not switch to the typed atomic wrappers without
// auditing every call site.
type PluginGlobals[J any] struct {
	// Atomic flags: 0 = false, 1 = true.
	// FirstCheckForCompletedJobsRan flips after the very first run of the
	// background completion poller, so the foreground loop can wait for one
	// pass before it does anything.
	FirstCheckForCompletedJobsRan int32
	// FirstStartCapacityCheckRan flips after the first VM-capacity check on
	// plugin start, so we never charge a handler twice for the same warmup.
	FirstStartCapacityCheckRan int32

	// RetryChannel is fed a queue-job JSON string when the handler decides
	// the job needs to bounce back to the main queue after a soft failure.
	RetryChannel chan string

	// CleanupMutex serializes cleanup so two paths cannot tear down the
	// same job at the same time.
	CleanupMutex *sync.Mutex

	// JobChannel carries the active job to consumers that need to react
	// (start a VM, watch for completion, signal finish).
	JobChannel chan J

	// PausedCancellationJobChannel carries a job that was paused on this
	// host but cancelled before another host could take it; the cleanup
	// path drains it so the paused entry is removed.
	PausedCancellationJobChannel chan J

	// ReturnToMainQueue is similar to RetryChannel but signals an
	// authoritative "send this back to the main queue" decision.
	ReturnToMainQueue chan string

	// CheckForCompletedJobsRunCount is incremented every time the
	// background poller runs; used for periodic logging.
	CheckForCompletedJobsRunCount int32

	// Unreturnable, when true, prevents the cleanup path from attempting
	// to put the current job back in the main queue (because it has
	// already been handed off).
	Unreturnable bool
}

func (p *PluginGlobals[J]) SetUnreturnable(v bool) {
	p.Unreturnable = v
}

func (p *PluginGlobals[J]) IsUnreturnable() bool {
	return p.Unreturnable
}

func (p *PluginGlobals[J]) IncrementCheckForCompletedJobsRunCount() {
	atomic.AddInt32(&p.CheckForCompletedJobsRunCount, 1)
}

func (p *PluginGlobals[J]) GetCheckForCompletedJobsRunCount() int32 {
	return atomic.LoadInt32(&p.CheckForCompletedJobsRunCount)
}

func (p *PluginGlobals[J]) SetFirstCheckForCompletedJobsRan(ran bool) {
	var v int32
	if ran {
		v = 1
	}
	atomic.StoreInt32(&p.FirstCheckForCompletedJobsRan, v)
}

func (p *PluginGlobals[J]) GetFirstCheckForCompletedJobsRan() bool {
	return atomic.LoadInt32(&p.FirstCheckForCompletedJobsRan) == 1
}

func (p *PluginGlobals[J]) SetFirstStartCapacityCheckRan(ran bool) {
	var v int32
	if ran {
		v = 1
	}
	atomic.StoreInt32(&p.FirstStartCapacityCheckRan, v)
}

func (p *PluginGlobals[J]) GetFirstStartCapacityCheckRan() bool {
	return atomic.LoadInt32(&p.FirstStartCapacityCheckRan) == 1
}

// GetPluginGlobalsFromContext fetches the PluginGlobals stored on the context
// under the conventional "pluginglobals" key. Generic over J - callers must
// supply the same J they constructed the value with, or the type assertion
// will fail. (This is the same shape that internal/github used; the type
// parameter just documents the requirement that previously lived in a comment.)
func GetPluginGlobalsFromContext[J any](ctx context.Context) (*PluginGlobals[J], error) {
	pluginGlobals, ok := ctx.Value(config.ContextKey("pluginglobals")).(*PluginGlobals[J])
	if !ok {
		return nil, fmt.Errorf("GetPluginGlobalsFromContext failed")
	}
	return pluginGlobals, nil
}
