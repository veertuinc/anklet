package metrics

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
)

func testContextWithLogger() context.Context {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return context.WithValue(context.Background(), config.ContextKey("logger"), logger)
}

func TestExportMetricsToDBReturnsWhenContextsAlreadyCancelled(t *testing.T) {
	pluginCtx, pluginCancel := context.WithCancel(testContextWithLogger())
	pluginCancel()
	workerCtx, workerCancel := context.WithCancel(context.Background())
	workerCancel()

	done := make(chan struct{})
	go func() {
		ExportMetricsToDB(workerCtx, pluginCtx, "owner/name")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ExportMetricsToDB blocked after worker and plugin contexts were cancelled")
	}
}

func TestExportMetricsToDBWaitStopsWhenContextCancels(t *testing.T) {
	metricsData := &MetricsDataLock{}
	redisClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	defer func() { _ = redisClient.Close() }()
	db := &database.Database{
		Client:             redisClient,
		MaxRetries:         2,
		RetryDelay:         time.Millisecond,
		RetryBackoffFactor: 1,
	}

	pluginCtx := context.WithValue(testContextWithLogger(), config.ContextKey("database"), db)
	pluginCtx, pluginCancel := context.WithCancel(pluginCtx)
	defer pluginCancel()

	workerCtx := context.WithValue(context.Background(), config.ContextKey("metrics"), metricsData)
	workerCtx, workerCancel := context.WithCancel(workerCtx)
	defer workerCancel()

	done := make(chan struct{})
	go func() {
		ExportMetricsToDB(workerCtx, pluginCtx, "owner/name")
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	workerCancel()
	pluginCancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ExportMetricsToDB wait loop did not return after context cancel")
	}
}
