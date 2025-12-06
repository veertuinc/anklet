package github

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	github_ratelimit "github.com/gofri/go-github-ratelimit/v2/github_ratelimit"
	"github.com/gofri/go-github-ratelimit/v2/github_ratelimit/github_secondary_ratelimit"
	"github.com/google/go-github/v74/github"
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

func GetGitHubClientFromContext(ctx context.Context) (*github.Client, error) {
	wrapper, ok := ctx.Value(config.ContextKey("githubwrapperclient")).(*GitHubClientWrapper)
	if !ok {
		return nil, fmt.Errorf("GetGitHubClientFromContext failed")
	}
	return wrapper.client, nil
}

func GetRateLimitWaiterClientFromContext(ctx context.Context) (*http.Client, error) {
	rateLimiter, ok := ctx.Value(config.ContextKey("rateLimiter")).(*http.Client)
	if rateLimiter != nil && !ok {
		return nil, fmt.Errorf("GetRateLimitWaiterClientFromContext failed")
	}
	return rateLimiter, nil
}

func GetHttpTransportFromContext(ctx context.Context) (*http.Transport, error) {
	httpTransport, ok := ctx.Value(config.ContextKey("httpTransport")).(*http.Transport)
	if httpTransport != nil && !ok {
		return nil, fmt.Errorf("GetHttpTransportFromContext failed")
	}
	return httpTransport, nil
}

func AuthenticateAndReturnGitHubClient(
	ctx context.Context,
	privateKey string,
	appID int64,
	installationID int64,
	token string,
) (*github.Client, error) {

	var client *github.Client
	var err error
	var rateLimiter *http.Client
	rateLimiter, err = GetRateLimitWaiterClientFromContext(ctx)
	if err != nil {
		return nil, err
	}
	var httpTransport *http.Transport
	httpTransport, err = GetHttpTransportFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if httpTransport == nil {
		httpTransport = http.DefaultTransport.(*http.Transport)
	}
	if rateLimiter == nil {
		rateLimiter = github_ratelimit.NewClient(
			httpTransport,
			github_secondary_ratelimit.WithLimitDetectedCallback(func(cbCtx *github_secondary_ratelimit.CallbackContext) {
				logging.Warn(ctx, "GitHub secondary rate limit detected, sleeping until reset",
					"resetTime", cbCtx.ResetTime,
					"totalSleepTime", cbCtx.TotalSleepTime,
				)
			}),
		)
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
				return nil, fmt.Errorf("error creating github app installation token: %s (does the key exist on the filesystem?)", err.Error())
			} else {
				return nil, fmt.Errorf("error creating github app installation token: %s", err.Error())
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
func ExecuteGitHubClientFunction[T any](
	workerCtx context.Context,
	pluginCtx context.Context,
	executeFunc func() (*T, *github.Response, error),
) (context.Context, *T, *github.Response, error) {
	return executeGitHubClientFunctionWithRetry(workerCtx, pluginCtx, executeFunc, 0)
}

// executeGitHubClientFunctionWithRetry handles the actual retry logic with exponential backoff
func executeGitHubClientFunctionWithRetry[T any](
	workerCtx context.Context,
	pluginCtx context.Context,
	executeFunc func() (*T, *github.Response, error),
	retryAttempt int,
) (context.Context, *T, *github.Response, error) {
	executeGitHubClientFunctionCtx, cancel := context.WithCancel(pluginCtx) // Inherit from parent context
	defer cancel()

	result, response, err := executeFunc()

	if response != nil {
		logging.Debug(pluginCtx,
			"GitHub API rate limit",
			"method", response.Request.Method,
			"url", response.Request.URL.String(),
			"remaining", response.Rate.Remaining,
			"reset", response.Rate.Reset.Format(time.RFC3339),
			"limit", response.Rate.Limit,
		)
		if response.Rate.Remaining <= 10 { // handle primary rate limiting
			sleepDuration := time.Until(response.Rate.Reset.Time) + time.Second // Adding a second to ensure we're past the reset time
			logging.Warn(executeGitHubClientFunctionCtx, "GitHub API rate limit exceeded, sleeping until reset")
			metricsData, err := metrics.GetMetricsDataFromContext(pluginCtx)
			if err != nil {
				return pluginCtx, nil, nil, err
			}
			ctxPlugin, err := config.GetPluginFromContext(pluginCtx)
			if err != nil {
				return pluginCtx, nil, nil, err
			}
			err = metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.PluginBase{
				Name:        ctxPlugin.Name,
				Status:      "limit_paused",
				StatusSince: time.Now(),
			})
			if err != nil {
				logging.Error(workerCtx, "error updating plugin metrics", "error", err)
				return pluginCtx, nil, nil, err
			}
			select {
			case <-time.After(sleepDuration):
				err := metricsData.UpdatePlugin(workerCtx, pluginCtx, metrics.PluginBase{
					Name:        ctxPlugin.Name,
					Status:      "running",
					StatusSince: time.Now(),
				})
				if err != nil {
					logging.Error(workerCtx, "error updating plugin metrics", "error", err)
					return pluginCtx, nil, nil, err
				}
				return executeGitHubClientFunctionWithRetry(workerCtx, executeGitHubClientFunctionCtx, executeFunc, retryAttempt) // Retry the function after waiting
			case <-pluginCtx.Done():
				return pluginCtx, nil, nil, pluginCtx.Err()
			}
		}
	}

	if err != nil {
		// Check if this is a 404 error that we should retry
		if is404Error(err) {
			if retryAttempt < 4 { // Retry up to 4 times as requested
				// Before retrying, check if the client is still authenticated
				// This helps distinguish between auth issues and GitHub service issues
				githubClient, clientErr := GetGitHubClientFromContext(pluginCtx)
				if clientErr != nil {
					logging.Error(pluginCtx, "failed to get GitHub client for authentication validation", "error", clientErr)
				} else {
					authErr := validateGitHubClientAuthentication(pluginCtx, githubClient)
					if authErr != nil {
						logging.Error(pluginCtx,
							"GitHub API 404 error appears to be due to authentication failure",
							"authError", authErr.Error(),
							"originalError", err.Error(),
							"attempt", retryAttempt+1,
						)
						// Don't retry if authentication has failed - return the original error
						return pluginCtx, nil, nil, fmt.Errorf("GitHub API 404 error with authentication failure: %v (original: %v)", authErr, err)
					} else {
						logging.Debug(pluginCtx, "GitHub client authentication validated successfully during 404 retry")
					}
				}

				// Calculate exponential backoff with jitter
				// Base delay starts at 30 seconds, doubles each time
				baseDelay := time.Duration(30) * time.Second
				backoffDelay := time.Duration(math.Pow(2, float64(retryAttempt))) * baseDelay

				// Cap the maximum delay at 30 minutes to prevent excessive waits
				maxDelay := 30 * time.Minute
				if backoffDelay > maxDelay {
					backoffDelay = maxDelay
				}

				logging.Warn(pluginCtx,
					"GitHub API returned 404, authentication validated, retrying with exponential backoff",
					"attempt", retryAttempt+1,
					"maxAttempts", 4,
					"backoffDelay", backoffDelay.String(),
					"error", err.Error(),
				)

				// Check for shutdown signal before sleeping
				if workerCtx.Err() != nil || pluginCtx.Err() != nil {
					logging.Warn(pluginCtx, "context canceled while retrying 404 error")
					return pluginCtx, nil, nil, fmt.Errorf("context canceled while retrying 404 error")
				}

				select {
				case <-time.After(backoffDelay):
					return executeGitHubClientFunctionWithRetry(workerCtx, pluginCtx, executeFunc, retryAttempt+1)
				case <-pluginCtx.Done():
					return pluginCtx, nil, nil, pluginCtx.Err()
				case <-workerCtx.Done():
					return pluginCtx, nil, nil, workerCtx.Err()
				}
			} else {
				logging.Error(pluginCtx, "GitHub API 404 error: maximum retry attempts exceeded (4)",
					"attempts", retryAttempt,
					"error", err.Error())
			}
		}

		// Log non-404 errors or final 404 error after all retries
		if err.Error() != "context canceled" {
			if !strings.Contains(err.Error(), "try again later") {
				logging.Error(pluginCtx, "error executing GitHub client function: "+err.Error())
			}
		}
		return pluginCtx, nil, nil, err
	}

	return pluginCtx, result, response, nil
}

// is404Error checks if the error is a 404 Not Found error from GitHub API
func is404Error(err error) bool {
	if err == nil {
		return false
	}
	errorStr := err.Error()
	return strings.Contains(errorStr, "404 Not Found") ||
		strings.Contains(errorStr, "404") && strings.Contains(errorStr, "Not Found")
}

// validateGitHubClientAuthentication checks if the GitHub client is still authenticated
// by making a simple API call to /user endpoint
func validateGitHubClientAuthentication(ctx context.Context, client *github.Client) error {
	if client == nil {
		return fmt.Errorf("github client is nil")
	}

	// Use a short timeout for the authentication check to avoid long waits
	authCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Try to get the authenticated user - this will fail if auth is invalid
	_, response, err := client.Users.Get(authCtx, "")
	if err != nil {
		// Check if it's an authentication error (401/403) vs other errors
		if response != nil {
			switch response.StatusCode {
			case 401:
				return fmt.Errorf("authentication failed: token is invalid or expired")
			case 403:
				return fmt.Errorf("authentication failed: insufficient permissions or rate limited")
			}
		}
		return fmt.Errorf("authentication check failed: %v", err)
	}

	return nil
}
