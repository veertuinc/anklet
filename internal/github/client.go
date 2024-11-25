package github

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/gofri/go-github-ratelimit/github_ratelimit"
	"github.com/google/go-github/v66/github"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
)

type GitHubClientWrapper struct {
	client *github.Client
}

func NewGitHubClientWrapper(client *github.Client) *GitHubClientWrapper {
	return &GitHubClientWrapper{
		client: client,
	}
}

func GetGitHubClientFromContext(ctx context.Context) *github.Client {
	wrapper, ok := ctx.Value(config.ContextKey("githubwrapperclient")).(*GitHubClientWrapper)
	if !ok {
		panic("GetGitHubClientFromContext failed")
	}
	return wrapper.client
}

func GetRateLimitWaiterClientFromContext(ctx context.Context) *http.Client {
	rateLimiter, ok := ctx.Value(config.ContextKey("rateLimiter")).(*http.Client)
	if rateLimiter != nil && !ok {
		panic("GetRateLimitWaiterClientFromContext failed")
	}
	return rateLimiter
}

func GetHttpTransportFromContext(ctx context.Context) *http.Transport {
	httpTransport, ok := ctx.Value(config.ContextKey("httpTransport")).(*http.Transport)
	if httpTransport != nil && !ok {
		panic("GetHttpTransportFromContext failed")
	}
	return httpTransport
}

func AuthenticateAndReturnGitHubClient(
	ctx context.Context,
	logger *slog.Logger,
	privateKey string,
	appID int64,
	installationID int64,
	token string,
) (*github.Client, error) {

	var client *github.Client
	var err error
	var rateLimiter *http.Client
	rateLimiter = GetRateLimitWaiterClientFromContext(ctx)
	var httpTransport *http.Transport
	httpTransport = GetHttpTransportFromContext(ctx)
	if httpTransport == nil {
		httpTransport = http.DefaultTransport.(*http.Transport)
	}
	if rateLimiter == nil {
		rateLimiter, err = github_ratelimit.NewRateLimitWaiterClient(httpTransport)
		if err != nil {
			logger.ErrorContext(ctx, "error creating github_ratelimit.NewRateLimitWaiterClient", "err", err)
			return nil, err
		}
	}
	if privateKey != "" {
		// support private key in a file or as text
		var privateKeyBytes []byte
		privateKeyBytes, err = os.ReadFile(privateKey)
		if err != nil {
			privateKeyBytes = []byte(privateKey)
		}
		itr, err := ghinstallation.New(httpTransport, appID, installationID, privateKeyBytes)
		if err != nil {
			if strings.Contains(err.Error(), "invalid key") {
				panic("error creating github app installation token: " + err.Error() + " (does the key exist on the filesystem?)")
			} else {
				panic("error creating github app installation token: " + err.Error())
			}
		}
		rateLimiter.Transport = itr
		client = github.NewClient(rateLimiter)
	} else {
		client = github.NewClient(rateLimiter).WithAuthToken(token)
	}
	return client, nil

}

// https://github.com/gofri/go-github-ratelimit has yet to support primary rate limits, so we have to do it ourselves.
func ExecuteGitHubClientFunction[T any](pluginCtx context.Context, logger *slog.Logger, executeFunc func() (*T, *github.Response, error)) (context.Context, *T, *github.Response, error) {
	innerPluginCtx, cancel := context.WithCancel(pluginCtx) // Inherit from parent context
	defer cancel()
	result, response, err := executeFunc()
	if response != nil {
		innerPluginCtx = logging.AppendCtx(innerPluginCtx, slog.Int("api_limit_remaining", response.Rate.Remaining))
		innerPluginCtx = logging.AppendCtx(innerPluginCtx, slog.String("api_limit_reset_time", response.Rate.Reset.Time.Format(time.RFC3339)))
		innerPluginCtx = logging.AppendCtx(innerPluginCtx, slog.Int("api_limit", response.Rate.Limit))
		if response.Rate.Remaining <= 10 { // handle primary rate limiting
			sleepDuration := time.Until(response.Rate.Reset.Time) + time.Second // Adding a second to ensure we're past the reset time
			logger.WarnContext(innerPluginCtx, "GitHub API rate limit exceeded, sleeping until reset")
			metricsData := metrics.GetMetricsDataFromContext(pluginCtx)
			ctxPlugin := config.GetPluginFromContext(pluginCtx)
			metricsData.UpdatePlugin(pluginCtx, logger, metrics.PluginBase{
				Name:   ctxPlugin.Name,
				Status: "limit_paused",
			})
			select {
			case <-time.After(sleepDuration):
				metricsData.UpdatePlugin(pluginCtx, logger, metrics.PluginBase{
					Name:   ctxPlugin.Name,
					Status: "running",
				})
				return ExecuteGitHubClientFunction(pluginCtx, logger, executeFunc) // Retry the function after waiting
			case <-pluginCtx.Done():
				return pluginCtx, nil, nil, pluginCtx.Err()
			}
		}
	}
	if err != nil {
		if err.Error() != "context canceled" {
			if !strings.Contains(err.Error(), "try again later") {
				logger.Error("error executing GitHub client function: " + err.Error())
			}
		}
		return pluginCtx, nil, nil, err
	}
	return pluginCtx, result, response, nil
}
