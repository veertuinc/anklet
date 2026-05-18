package github

import (
	"context"
	"time"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
)

const githubAPIRateLimitNoticeInterval = 30 * time.Second

func setGitHubAPIRateLimitPaused(workerCtx context.Context, resetAt time.Time) {
	globals, err := config.GetWorkerGlobalsFromContext(workerCtx)
	if err != nil {
		return
	}
	globals.GitHubAPIRateLimitResetUnix.Store(resetAt.Unix())
}

func clearGitHubAPIRateLimitPaused(workerCtx context.Context) {
	globals, err := config.GetWorkerGlobalsFromContext(workerCtx)
	if err != nil {
		return
	}
	globals.GitHubAPIRateLimitResetUnix.Store(0)
}

// logGitHubAPIRateLimitStatusAtRunStart logs a throttled notice when this handler's loop
// runs while another call is waiting out the primary GitHub API rate limit.
// Returns true when the normal "starting github plugin" line should be skipped.
// LogGitHubAPIRateLimitStatusAtRunStart logs a throttled notice when this handler's loop
// runs while waiting out the primary GitHub API rate limit.
// Returns true when the normal "starting github plugin" line should be skipped.
func LogGitHubAPIRateLimitStatusAtRunStart(workerCtx, pluginCtx context.Context) bool {
	globals, err := config.GetWorkerGlobalsFromContext(workerCtx)
	if err != nil {
		return false
	}

	resetUnix := globals.GitHubAPIRateLimitResetUnix.Load()
	if resetUnix == 0 {
		return false
	}

	resetAt := time.Unix(resetUnix, 0)
	remaining := time.Until(resetAt)
	if remaining <= 0 {
		globals.GitHubAPIRateLimitResetUnix.Store(0)
		return false
	}

	nowUnix := time.Now().Unix()
	lastNoticeUnix := globals.LastGitHubAPIRateLimitNoticeUnix.Load()
	if nowUnix-lastNoticeUnix < int64(githubAPIRateLimitNoticeInterval.Seconds()) {
		return true
	}

	globals.LastGitHubAPIRateLimitNoticeUnix.Store(nowUnix)
	logging.Info(
		pluginCtx,
		"github plugin waiting for GitHub API rate limit to reset",
		"resetAt", resetAt.Format(time.RFC3339),
		"remaining", remaining.Round(time.Second).String(),
	)
	return true
}

func waitForGitHubAPIRateLimitReset(workerCtx, pluginCtx context.Context, resetAt time.Time) error {
	sleepDuration := time.Until(resetAt) + time.Second
	setGitHubAPIRateLimitPaused(workerCtx, resetAt)
	defer clearGitHubAPIRateLimitPaused(workerCtx)

	logging.Warn(
		pluginCtx,
		"GitHub API rate limit exceeded, pausing until reset",
		"resetAt", resetAt.Format(time.RFC3339),
		"sleepDuration", sleepDuration.Round(time.Second).String(),
	)

	if metricsData, metricsErr := metrics.GetMetricsDataFromContext(workerCtx); metricsErr == nil {
		if ctxPlugin, pluginErr := config.GetPluginFromContext(pluginCtx); pluginErr == nil {
			if updateErr := metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.PluginBase{
				Name:        ctxPlugin.Name,
				Status:      "limit_paused",
				StatusSince: time.Now(),
			}); updateErr != nil {
				logging.Error(workerCtx, "error updating plugin metrics to limit_paused", "error", updateErr)
			}
		}
	}

	logging.Info(
		pluginCtx,
		"GitHub API rate limit pause started",
		"resetAt", resetAt.Format(time.RFC3339),
		"sleepDuration", sleepDuration.Round(time.Second).String(),
	)

	timer := time.NewTimer(sleepDuration)
	defer timer.Stop()

	noticeTicker := time.NewTicker(githubAPIRateLimitNoticeInterval)
	defer noticeTicker.Stop()

	for {
		select {
		case <-timer.C:
			if metricsData, metricsErr := metrics.GetMetricsDataFromContext(workerCtx); metricsErr == nil {
				if ctxPlugin, pluginErr := config.GetPluginFromContext(pluginCtx); pluginErr == nil {
					if updateErr := metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.PluginBase{
						Name:        ctxPlugin.Name,
						Status:      "running",
						StatusSince: time.Now(),
					}); updateErr != nil {
						logging.Error(workerCtx, "error updating plugin metrics to running", "error", updateErr)
					}
				}
			}
			return nil
		case <-noticeTicker.C:
			remaining := time.Until(resetAt)
			if remaining <= 0 {
				return nil
			}
			logging.Info(
				pluginCtx,
				"still paused for GitHub API rate limit",
				"resetAt", resetAt.Format(time.RFC3339),
				"remaining", remaining.Round(time.Second).String(),
			)
		case <-pluginCtx.Done():
			return pluginCtx.Err()
		}
	}
}
