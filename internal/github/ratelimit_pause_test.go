package github

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/veertuinc/anklet/internal/config"
)

func testContextWithLogger() context.Context {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return context.WithValue(context.Background(), config.ContextKey("logger"), logger)
}

func TestLogGitHubAPIRateLimitStatusAtRunStart(t *testing.T) {
	t.Parallel()

	globals := &config.Globals{
		Plugins: make(map[string]map[string]*config.PluginGlobal),
	}
	workerCtx := context.WithValue(testContextWithLogger(), config.ContextKey("globals"), globals)
	pluginCtx := testContextWithLogger()

	if LogGitHubAPIRateLimitStatusAtRunStart(workerCtx, pluginCtx) {
		t.Fatal("expected false when rate limit is not active")
	}

	globals.GitHubAPIRateLimitResetUnix.Store(time.Now().Add(2 * time.Minute).Unix())
	if !LogGitHubAPIRateLimitStatusAtRunStart(workerCtx, pluginCtx) {
		t.Fatal("expected true when rate limit is active")
	}
	if !LogGitHubAPIRateLimitStatusAtRunStart(workerCtx, pluginCtx) {
		t.Fatal("expected throttled second call within interval to skip starting log without re-logging")
	}

	globals.GitHubAPIRateLimitResetUnix.Store(time.Now().Add(-time.Minute).Unix())
	if LogGitHubAPIRateLimitStatusAtRunStart(workerCtx, pluginCtx) {
		t.Fatal("expected false after reset time has passed")
	}
	if globals.GitHubAPIRateLimitResetUnix.Load() != 0 {
		t.Fatal("expected stale reset unix to be cleared")
	}
}
