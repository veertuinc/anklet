package run

import (
	"context"
	"log/slog"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
	github_controller "github.com/veertuinc/anklet/plugins/controllers/github"
	"github.com/veertuinc/anklet/plugins/services/github"
)

func Plugin(
	workerCtx context.Context,
	serviceCtx context.Context,
	serviceCancel context.CancelFunc,
	logger *slog.Logger,
	firstServiceStarted chan bool,
	metricsData *metrics.MetricsDataLock,
) {
	service := config.GetServiceFromContext(serviceCtx)
	// fmt.Printf("%+v\n", service)
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("plugin", service.Plugin))
	if service.Plugin == "" {
		panic("plugin is not set in yaml:services:" + service.Name + ":plugin")
	}
	if service.Plugin == "github" {
		for {
			select {
			case <-serviceCtx.Done():
				serviceCancel()
				return
			default:
				// notify the main thread that the service has started
				select {
				case <-firstServiceStarted:
				default:
					close(firstServiceStarted)
				}
				github.Run(workerCtx, serviceCtx, serviceCancel, logger, metricsData)
				metricsData.SetStatus(serviceCtx, logger, "idle")
			}
		}
	} else if service.Plugin == "github_controller" {
		github_controller.Run(workerCtx, serviceCtx, serviceCancel, logger, firstServiceStarted, metricsData)
	} else {
		panic("plugin not found: " + service.Plugin)
	}
}
