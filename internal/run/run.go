package run

import (
	"context"
	"fmt"

	"github.com/veertuinc/anklet/internal/config"
	azure_devops_handler "github.com/veertuinc/anklet/plugins/handlers/azure_devops"
	"github.com/veertuinc/anklet/plugins/handlers/github"
	azure_devops_receiver "github.com/veertuinc/anklet/plugins/receivers/azure_devops"
	github_receiver "github.com/veertuinc/anklet/plugins/receivers/github"
)

func Plugin(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginCancel context.CancelFunc,
) (context.Context, error) {
	var updatedPluginCtx context.Context
	ctxPlugin, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	if ctxPlugin.Plugin == "" {
		return pluginCtx, fmt.Errorf("plugin is not set in yaml:plugins:%s:plugin", ctxPlugin.Name)
	}
	switch ctxPlugin.Plugin {
	case "github":
		select {
		case <-pluginCtx.Done():
			pluginCancel()
			return pluginCtx, nil
		default:
			// notify the main thread that the service has started
			updatedPluginCtx, err = github.Run(
				workerCtx,
				pluginCtx,
				pluginCancel,
			)
			if err != nil {
				return updatedPluginCtx, fmt.Errorf("error running github plugin: %s", err.Error())
			}
			// metricsData.SetStatus(pluginCtx, logger, "idle")
			return updatedPluginCtx, nil // pass back to the main thread/loop
		}
	case "github_receiver":
		updatedPluginCtx, err = github_receiver.Run(
			workerCtx,
			pluginCtx,
		)
		if err != nil {
			return updatedPluginCtx, err
		}
	case "azure_devops":
		select {
		case <-pluginCtx.Done():
			pluginCancel()
			return pluginCtx, nil
		default:
			updatedPluginCtx, err = azure_devops_handler.Run(
				workerCtx,
				pluginCtx,
				pluginCancel,
			)
			if err != nil {
				return updatedPluginCtx, fmt.Errorf("error running azure_devops plugin: %s", err.Error())
			}
			return updatedPluginCtx, nil
		}
	case "azure_devops_receiver":
		updatedPluginCtx, err = azure_devops_receiver.Run(
			workerCtx,
			pluginCtx,
		)
		if err != nil {
			return updatedPluginCtx, err
		}
	default:
		return pluginCtx, fmt.Errorf("plugin not supported")
	}
	return pluginCtx, nil
}
