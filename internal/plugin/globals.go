package plugin

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/veertuinc/anklet/internal/config"
	internalModels "github.com/veertuinc/anklet/internal/models"
)

type PluginGlobals struct {
	FirstCheckForCompletedJobsRan int32
	FirstStartCapacityCheckRan    int32
	RetryChannel                  chan string
	CleanupMutex                  *sync.Mutex
	JobChannel                    chan internalModels.QueueJob
	PausedCancellationJobChannel  chan internalModels.QueueJob
	ReturnToMainQueue             chan string
	CheckForCompletedJobsRunCount int32
	Unreturnable                  bool
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

func (p *PluginGlobals) SetFirstStartCapacityCheckRan(ran bool) {
	var value int32
	if ran {
		value = 1
	}
	atomic.StoreInt32(&p.FirstStartCapacityCheckRan, value)
}

func (p *PluginGlobals) GetFirstStartCapacityCheckRan() bool {
	return atomic.LoadInt32(&p.FirstStartCapacityCheckRan) == 1
}

func GetPluginGlobalsFromContext(ctx context.Context) (*PluginGlobals, error) {
	pluginGlobals, ok := ctx.Value(config.ContextKey("pluginglobals")).(*PluginGlobals)
	if !ok {
		return nil, fmt.Errorf("GetPluginGlobalFromContext failed")
	}
	return pluginGlobals, nil
}
