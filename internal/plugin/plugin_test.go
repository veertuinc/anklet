package plugin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/metrics"
)

func TestDepsWithContextPopulatesAllDependencies(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{WorkDir: "/tmp"}
	globals := &config.Globals{PluginsPath: "/plugins"}
	m := &metrics.MetricsDataLock{}
	db := &database.Database{}
	cli := &anka.Cli{Version: "3.9.0"}
	tr := &http.Transport{}

	deps := Deps{
		Logger:         logger,
		Config:         cfg,
		PluginConfig:   config.Plugin{Name: "P1", Plugin: "github"},
		ConfigFileName: "config.yml",
		Globals:        globals,
		Metrics:        m,
		Database:       db,
		AnkaCLI:        cli,
		HTTPTransport:  tr,
	}

	ctx := deps.WithContext(context.Background())

	if got, err := config.GetLoadedConfigFromContext(ctx); err != nil || got != cfg {
		t.Fatalf("config = (%v, %v), want %v", got, err, cfg)
	}
	if got, err := config.GetWorkerGlobalsFromContext(ctx); err != nil || got != globals {
		t.Fatalf("globals = (%v, %v), want %v", got, err, globals)
	}
	if got, err := config.GetPluginFromContext(ctx); err != nil || got.Name != "P1" {
		t.Fatalf("plugin = (%v, %v), want P1", got, err)
	}
	if got, err := config.GetConfigFileNameFromContext(ctx); err != nil || got != "config.yml" {
		t.Fatalf("configFileName = (%q, %v), want config.yml", got, err)
	}
	if got, err := database.GetDatabaseFromContext(ctx); err != nil || got != db {
		t.Fatalf("database = (%v, %v), want %v", got, err, db)
	}
	if got, err := metrics.GetMetricsDataFromContext(ctx); err != nil || got != m {
		t.Fatalf("metrics = (%v, %v), want %v", got, err, m)
	}
	if got, err := anka.GetAnkaCLIFromContext(ctx); err != nil || got != cli {
		t.Fatalf("ankaCLI = (%v, %v), want %v", got, err, cli)
	}
}
