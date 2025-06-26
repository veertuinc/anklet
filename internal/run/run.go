package run

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
	"github.com/veertuinc/anklet/plugins/handlers/github"
	github_receiver "github.com/veertuinc/anklet/plugins/receivers/github"
)

func Plugin(
	workerCtx context.Context,
	pluginCtx context.Context,
	pluginCancel context.CancelFunc,
	logger *slog.Logger,
	metricsData *metrics.MetricsDataLock,
) (context.Context, error) {
	var updatedPluginCtx context.Context
	ctxPlugin, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, err
	}
	// fmt.Printf("%+v\n", service)
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("plugin", ctxPlugin.Plugin))
	if ctxPlugin.Plugin == "" {
		return pluginCtx, fmt.Errorf("plugin is not set in yaml:plugins:%s:plugin", ctxPlugin.Name)
	}
	workerGlobals, err := config.GetWorkerGlobalsFromContext(workerCtx)
	if err != nil {
		return pluginCtx, err
	}
	switch ctxPlugin.Plugin {
	case "github":
		select {
		case <-pluginCtx.Done():
			pluginCancel()
			return pluginCtx, nil
		default:
			// notify the main thread that the service has started
			select {
			case <-workerGlobals.FirstPluginStarted:
			default:
				close(workerGlobals.FirstPluginStarted)
			}
			updatedPluginCtx, err = github.Run(
				workerCtx,
				pluginCtx,
				pluginCancel,
				logger,
				metricsData,
			)
			if err != nil {
				return updatedPluginCtx, err
			}
			// metricsData.SetStatus(pluginCtx, logger, "idle")
			return updatedPluginCtx, nil // pass back to the main thread/loop
		}
	case "github_receiver":
		updatedPluginCtx, err = github_receiver.Run(
			workerCtx,
			pluginCtx,
			pluginCancel,
			logger,
			metricsData,
		)
		if err != nil {
			return updatedPluginCtx, err
		}
	default:
		return pluginCtx, fmt.Errorf("plugin not found")
	}
	return pluginCtx, nil
}
