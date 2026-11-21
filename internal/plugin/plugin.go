package plugin

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/metrics"
)

// Role identifies whether a plugin receives events or handles queued jobs.
type Role string

const (
	RoleHandler  Role = "handler"
	RoleReceiver Role = "receiver"
)

// Deps holds all typed runtime dependencies injected into a plugin instance.
type Deps struct {
	Logger         *slog.Logger
	Config         *config.Config
	PluginConfig   config.Plugin
	ConfigFileName string
	Globals        *config.Globals
	Metrics        *metrics.MetricsDataLock
	Database       *database.Database
	AnkaCLI        *anka.Cli
	HTTPTransport  *http.Transport
}

// WithContext populates a context with runtime dependencies for internal helpers
// that accept context-bound loggers, configs, or clients.
func (d Deps) WithContext(base context.Context) context.Context {
	ctx := base
	if d.Logger != nil {
		ctx = context.WithValue(ctx, config.ContextKey("logger"), d.Logger)
	}
	if d.Config != nil {
		ctx = context.WithValue(ctx, config.ContextKey("config"), d.Config)
	}
	if d.Globals != nil {
		ctx = context.WithValue(ctx, config.ContextKey("globals"), d.Globals)
	}
	if d.HTTPTransport != nil {
		ctx = context.WithValue(ctx, config.ContextKey("httpTransport"), d.HTTPTransport)
	}
	ctx = context.WithValue(ctx, config.ContextKey("configFileName"), d.ConfigFileName)
	ctx = context.WithValue(ctx, config.ContextKey("plugin"), d.PluginConfig)
	if d.AnkaCLI != nil {
		ctx = context.WithValue(ctx, config.ContextKey("ankacli"), d.AnkaCLI)
	}
	if d.Database != nil {
		ctx = context.WithValue(ctx, config.ContextKey("database"), d.Database)
	}
	if d.Metrics != nil {
		ctx = context.WithValue(ctx, config.ContextKey("metrics"), d.Metrics)
	}
	return ctx
}

// Runner is the common interface implemented by every Anklet plugin.
type Runner interface {
	Role() Role
	Run(workerCtx context.Context, pluginCtx context.Context, pluginCancel context.CancelFunc) (context.Context, error)
}
