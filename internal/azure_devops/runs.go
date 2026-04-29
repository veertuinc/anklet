package azure_devops

import (
	"context"
	"fmt"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/build"
)

// Run is the minimal pipeline-run / build shape Anklet cares about.
// Status / Result are kept as their native SDK enum values (lowercased
// strings); use the Job-level state vocabulary in types.go for normalized
// comparisons across services.
type Run struct {
	ID     int
	Status string
	Result string
}

// GetRun returns the run identified by runID inside the configured project.
// Internally hits the build API (the pipelines API is read-mostly and
// lacks cancellation - we share one endpoint family for both reads and
// writes so callers don't get split-brain semantics).
func (c *Client) GetRun(ctx context.Context, runID int) (*Run, error) {
	project := c.project
	bid := runID
	b, err := c.build.GetBuild(ctx, build.GetBuildArgs{
		Project: &project,
		BuildId: &bid,
	})
	if err != nil {
		return nil, fmt.Errorf("getting run %d: %w", runID, err)
	}
	if b == nil {
		return nil, fmt.Errorf("run %d not found", runID)
	}
	out := Run{}
	if b.Id != nil {
		out.ID = *b.Id
	}
	if b.Status != nil {
		out.Status = string(*b.Status)
	}
	if b.Result != nil {
		out.Result = string(*b.Result)
	}
	return &out, nil
}

// CancelRun marks the run as cancelled in Azure DevOps. The SDK requires
// status to be set on a Build when issuing an UpdateBuild - we use it to
// transition the build to "cancelling", which is how ADO models a graceful
// stop request. Idempotent; calling on an already-cancelled run is a noop
// from ADO's perspective.
func (c *Client) CancelRun(ctx context.Context, runID int) error {
	project := c.project
	bid := runID
	cancelling := build.BuildStatusValues.Cancelling
	_, err := c.build.UpdateBuild(ctx, build.UpdateBuildArgs{
		Project: &project,
		BuildId: &bid,
		Build:   &build.Build{Status: &cancelling},
	})
	if err != nil {
		return fmt.Errorf("cancelling run %d: %w", runID, err)
	}
	return nil
}
