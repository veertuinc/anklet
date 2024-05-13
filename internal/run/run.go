package run

import (
	"context"
	"log/slog"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/plugins/github"
)

func Plugin(workerCtx context.Context, serviceCtx context.Context, logger *slog.Logger) {
	service := config.GetServiceFromContext(serviceCtx)
	// fmt.Printf("%+v\n", service)
	serviceCtx = logging.AppendCtx(serviceCtx, slog.String("plugin", service.Plugin))
	if service.Plugin == "" {
		panic("plugin is not set in yaml:services:" + service.Name + ":plugin")
	}
	if service.Plugin == "github" {
		github.Run(workerCtx, serviceCtx, logger)
	} else {
		panic("plugin not found: " + service.Plugin)
	}
}
