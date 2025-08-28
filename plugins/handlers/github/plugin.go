package github

import (
	"context"

	"github.com/veertuinc/anklet/internal/plugins/plugin"
)

// HandlerPlugin wraps the github handler to implement the Plugin interface
type HandlerPlugin struct{}

// NewHandlerPlugin creates a new GitHub handler plugin
func NewHandlerPlugin() *HandlerPlugin {
	return &HandlerPlugin{}
}

// init automatically registers this plugin when the package is imported
func init() {
	plugin.Register(NewHandlerPlugin())
}

// Run executes the GitHub handler plugin
func (p *HandlerPlugin) Run(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginCancel context.CancelFunc,
) (context.Context, error) {
	return Run(workerCtx, pluginCtx, pluginCancel)
}

// Name returns the plugin name
func (p *HandlerPlugin) Name() string {
	return "github"
}

// Type returns the plugin type
func (p *HandlerPlugin) Type() string {
	return "handler"
}

// Ensure HandlerPlugin implements the Plugin interface
var _ plugin.Plugin = (*HandlerPlugin)(nil)
