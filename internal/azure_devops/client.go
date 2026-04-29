package azure_devops

import (
	"context"
	"errors"
	"fmt"

	"github.com/microsoft/azure-devops-go-api/azuredevops/v7"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/build"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/core"
	"github.com/microsoft/azure-devops-go-api/azuredevops/v7/taskagent"
)

// Client wraps a configured *azuredevops.Connection plus per-API sub-clients.
// It is constructed once at plugin start and then reused; each method takes
// its own context so callers control cancellation per-operation.
//
// Why a wrapper instead of consumers using the SDK directly?
//   - Hides SDK arg-struct boilerplate (every SDK call wants pointer-to-int /
//     pointer-to-string fields wrapped in a per-method args type).
//   - Lets the plugins lean on a small, intentional surface that pairs with
//     internal/jobqueue helpers.
//   - Centralizes the 404-as-success policy on idempotent cleanup paths.
type Client struct {
	conn      *azuredevops.Connection
	orgURL    string
	project   string
	core      core.Client
	taskAgent taskagent.Client
	build     build.Client
}

// NewClient constructs a Client. orgURL is the dev.azure.com organization URL
// (e.g. "https://dev.azure.com/my-org"), project is the project name or ID,
// pat is the Personal Access Token used for authentication.
func NewClient(ctx context.Context, orgURL, project, pat string) (*Client, error) {
	if orgURL == "" {
		return nil, fmt.Errorf("orgURL is required")
	}
	if project == "" {
		return nil, fmt.Errorf("project is required")
	}
	if pat == "" {
		return nil, fmt.Errorf("pat is required")
	}
	conn := azuredevops.NewPatConnection(orgURL, pat)
	coreClient, err := core.NewClient(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("creating core client: %w", err)
	}
	taskAgentClient, err := taskagent.NewClient(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("creating taskagent client: %w", err)
	}
	buildClient, err := build.NewClient(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("creating build client: %w", err)
	}
	return &Client{
		conn:      conn,
		orgURL:    orgURL,
		project:   project,
		core:      coreClient,
		taskAgent: taskAgentClient,
		build:     buildClient,
	}, nil
}

// Ping issues a single low-cost API call to confirm credentials and project
// access. On success returns nil; on failure returns an error wrapping the
// SDK's response.
func (c *Client) Ping(ctx context.Context) error {
	project := c.project
	_, err := c.core.GetProject(ctx, core.GetProjectArgs{ProjectId: &project})
	if err != nil {
		return fmt.Errorf("ping (GetProject %q): %w", c.project, err)
	}
	return nil
}

// OrgURL returns the configured organization URL.
func (c *Client) OrgURL() string { return c.orgURL }

// Project returns the configured project name / ID.
func (c *Client) Project() string { return c.project }

// is404 reports whether err is an Azure DevOps WrappedError carrying a 404
// response. Used by the cleanup-idempotent helpers (DeleteAgent, CancelRun)
// to swallow "the thing was already gone" responses.
func is404(err error) bool {
	if err == nil {
		return false
	}
	var wrapped azuredevops.WrappedError
	if errors.As(err, &wrapped) {
		if wrapped.StatusCode != nil && *wrapped.StatusCode == 404 {
			return true
		}
	}
	var wrappedPtr *azuredevops.WrappedError
	if errors.As(err, &wrappedPtr) && wrappedPtr != nil {
		if wrappedPtr.StatusCode != nil && *wrappedPtr.StatusCode == 404 {
			return true
		}
	}
	return false
}
