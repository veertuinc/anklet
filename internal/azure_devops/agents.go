package azure_devops

import (
	"context"
	"fmt"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/taskagent"
)

// Agent is the minimal agent shape Anklet cares about. Mirrors
// taskagent.TaskAgent but unwraps the SDK's pointer-everywhere style.
type Agent struct {
	ID      int
	Name    string
	Enabled bool
}

// ListAgents returns every agent registered into poolID. An empty pool
// returns (nil, nil). Capabilities and assigned-request details are
// intentionally omitted; callers that need them can hit the SDK directly.
func (c *Client) ListAgents(ctx context.Context, poolID int) ([]Agent, error) {
	pid := poolID
	agents, err := c.taskAgent.GetAgents(ctx, taskagent.GetAgentsArgs{
		PoolId: &pid,
	})
	if err != nil {
		return nil, fmt.Errorf("listing agents in pool %d: %w", poolID, err)
	}
	if agents == nil {
		return nil, nil
	}
	out := make([]Agent, 0, len(*agents))
	for _, a := range *agents {
		entry := Agent{}
		if a.Id != nil {
			entry.ID = *a.Id
		}
		if a.Name != nil {
			entry.Name = *a.Name
		}
		if a.Enabled != nil {
			entry.Enabled = *a.Enabled
		}
		out = append(out, entry)
	}
	return out, nil
}

// DeleteAgent removes agentID from poolID. A 404 from Azure DevOps (the
// agent was already gone) is treated as success; any other error is
// returned to the caller. Idempotent so cleanup paths can call it freely.
func (c *Client) DeleteAgent(ctx context.Context, poolID, agentID int) error {
	pid := poolID
	aid := agentID
	err := c.taskAgent.DeleteAgent(ctx, taskagent.DeleteAgentArgs{
		PoolId:  &pid,
		AgentId: &aid,
	})
	if err != nil && !is404(err) {
		return fmt.Errorf("deleting agent %d from pool %d: %w", agentID, poolID, err)
	}
	return nil
}
