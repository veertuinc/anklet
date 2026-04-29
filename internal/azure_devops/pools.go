package azure_devops

import (
	"context"
	"errors"
	"fmt"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/taskagent"
)

// ErrPoolNotFound is returned by GetPoolByName when the named pool is absent.
var ErrPoolNotFound = errors.New("agent pool not found")

// AgentPool is the minimal pool shape Anklet cares about.
// Mirrors taskagent.TaskAgentPool but unwraps the SDK's pointer-everywhere
// style so callers do not have to nil-check on every access.
type AgentPool struct {
	ID   int
	Name string
}

// GetPoolByName returns the pool with the given Name. ErrPoolNotFound is
// returned when no pool matches; any other error is a transport / auth
// failure surfaced from the SDK.
func (c *Client) GetPoolByName(ctx context.Context, name string) (*AgentPool, error) {
	if name == "" {
		return nil, fmt.Errorf("pool name is required")
	}
	pools, err := c.taskAgent.GetAgentPools(ctx, taskagent.GetAgentPoolsArgs{
		PoolName: &name,
	})
	if err != nil {
		return nil, fmt.Errorf("listing agent pools: %w", err)
	}
	if pools == nil || len(*pools) == 0 {
		return nil, ErrPoolNotFound
	}
	for _, p := range *pools {
		if p.Id == nil || p.Name == nil {
			continue
		}
		if *p.Name == name {
			return &AgentPool{ID: *p.Id, Name: *p.Name}, nil
		}
	}
	return nil, ErrPoolNotFound
}
