package run

import (
	"context"
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
	firstPluginStarted chan bool,
	metricsData *metrics.MetricsDataLock,
) {
	ctxPlugin := config.GetPluginFromContext(pluginCtx)
	// fmt.Printf("%+v\n", service)
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("plugin", ctxPlugin.Plugin))
	if ctxPlugin.Plugin == "" {
		panic("plugin is not set in yaml:plugins:" + ctxPlugin.Name + ":plugin")
	}
	if ctxPlugin.Plugin == "github" {
		for {
			select {
			case <-pluginCtx.Done():
				pluginCancel()
				return
			default:
				// notify the main thread that the service has started
				select {
				case <-firstPluginStarted:
				default:
					close(firstPluginStarted)
				}
				github.Run(workerCtx, pluginCtx, pluginCancel, logger, metricsData)
				metricsData.SetStatus(pluginCtx, logger, "idle")
			}
		}
	} else if ctxPlugin.Plugin == "github_receiver" {
		github_receiver.Run(workerCtx, pluginCtx, pluginCancel, logger, firstPluginStarted, metricsData)
	} else {
		panic("plugin not found: " + ctxPlugin.Plugin)
	}
}
