package run

import (
	"context"
	"log/slog"
	"time"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
	github_controller "github.com/veertuinc/anklet/plugins/controllers"
	"github.com/veertuinc/anklet/plugins/github"
)

func Plugin(workerCtx context.Context, serviceCtx context.Context, serviceCancel context.CancelFunc, logger *slog.Logger, firstServiceStarted chan bool) {
	service := config.GetServiceFromContext(serviceCtx)
	// fmt.Printf("%+v\n", service)
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("plugin", service.Plugin))
	if service.Plugin == "" {
		panic("plugin is not set in yaml:services:" + service.Name + ":plugin")
	}
	if service.Plugin == "github" {
		for {
			select {
			case <-firstServiceStarted:
				github.Run(workerCtx, serviceCtx, serviceCancel, logger)
				return
			case <-serviceCtx.Done():
				logger.InfoContext(serviceCtx, "context cancelled before service started")
				serviceCancel()
				return
			default:
				time.Sleep(1 * time.Second)
			}
		}
	} else if service.Plugin == "github_controller" {
		github_controller.Run(workerCtx, serviceCtx, serviceCancel, logger, firstServiceStarted)
	} else {
		panic("plugin not found: " + service.Plugin)
	}
}
