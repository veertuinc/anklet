package github

import (
	"context"

	"github.com/veertuinc/anklet/internal/plugins/plugin"
)

// ReceiverPlugin wraps the github receiver to implement the Plugin interface
type ReceiverPlugin struct{}

// NewReceiverPlugin creates a new GitHub receiver plugin
func NewReceiverPlugin() *ReceiverPlugin {
	return &ReceiverPlugin{}
}

// init automatically registers this plugin when the package is imported
func init() {
	plugin.Register(NewReceiverPlugin())
}

// Run executes the GitHub receiver plugin
func (p *ReceiverPlugin) Run(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginCancel context.CancelFunc,
) (context.Context, error) {
	updatedPluginCtx, err := Run(workerCtx, pluginCtx)
	return updatedPluginCtx, err
}

// Name returns the plugin name
func (p *ReceiverPlugin) Name() string {
	return "github_receiver"
}

// Type returns the plugin type
func (p *ReceiverPlugin) Type() string {
	return "receiver"
}

// Ensure ReceiverPlugin implements the Plugin interface
var _ plugin.Plugin = (*ReceiverPlugin)(nil)
