package run

import (
	"context"
	"fmt"

	"github.com/veertuinc/anklet/internal/plugin"
	githubHandler "github.com/veertuinc/anklet/plugins/handlers/github"
	githubReceiver "github.com/veertuinc/anklet/plugins/receivers/github"
)

type registration struct {
	role    plugin.Role
	factory func(deps plugin.Deps) plugin.Runner
}

var registry = map[string]registration{
	"github": {
		role: plugin.RoleHandler,
		factory: func(deps plugin.Deps) plugin.Runner {
			return githubHandler.NewHandler(deps)
		},
	},
	"github_receiver": {
		role: plugin.RoleReceiver,
		factory: func(deps plugin.Deps) plugin.Runner {
			return githubReceiver.NewReceiver(deps)
		},
	},
}

// LookupRole returns the registered role for pluginType.
func LookupRole(pluginType string) (plugin.Role, bool) {
	reg, ok := registry[pluginType]
	if !ok {
		return "", false
	}
	return reg.role, true
}

// IsReceiverType reports whether pluginType is registered as a receiver plugin.
func IsReceiverType(pluginType string) bool {
	role, ok := LookupRole(pluginType)
	return ok && role == plugin.RoleReceiver
}

// Build constructs a typed plugin.Runner for deps.PluginConfig.Plugin.
func Build(deps plugin.Deps) (plugin.Runner, error) {
	if deps.PluginConfig.Plugin == "" {
		return nil, fmt.Errorf("plugin is not set in yaml:plugins:%s:plugin", deps.PluginConfig.Name)
	}
	reg, ok := registry[deps.PluginConfig.Plugin]
	if !ok {
		return nil, fmt.Errorf("plugin not supported")
	}
	return reg.factory(deps), nil
}

// Plugin executes one run of runner while respecting context cancellation.
func Plugin(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginCancel context.CancelFunc,
	runner plugin.Runner,
) (context.Context, error) {
	if runner == nil {
		return pluginCtx, fmt.Errorf("plugin not supported")
	}
	select {
	case <-pluginCtx.Done():
		pluginCancel()
		return pluginCtx, nil
	default:
	}

	updatedPluginCtx, err := runner.Run(workerCtx, pluginCtx, pluginCancel)
	if err != nil {
		return updatedPluginCtx, fmt.Errorf("error running plugin: %w", err)
	}
	return updatedPluginCtx, nil
}
