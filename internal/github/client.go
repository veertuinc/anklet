package github

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	github_ratelimit "github.com/gofri/go-github-ratelimit/v2/github_ratelimit"
	"github.com/gofri/go-github-ratelimit/v2/github_ratelimit/github_secondary_ratelimit"
	"github.com/google/go-github/v74/github"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
)

type GitHubClientWrapper struct {
	client        *github.Client
	appsTransport *ghinstallation.AppsTransport // set when using GitHub App auth; enables per-org clients
	orgClients    sync.Map                      // org login -> *github.Client
}

func NewGitHubClientWrapper(client *github.Client) *GitHubClientWrapper {
	return &GitHubClientWrapper{
		client: client,
	}
}

func GetGitHubClientWrapperFromContext(ctx context.Context) (*GitHubClientWrapper, error) {
	wrapper, ok := ctx.Value(config.ContextKey("githubwrapperclient")).(*GitHubClientWrapper)
	if !ok {
		return nil, fmt.Errorf("GetGitHubClientWrapperFromContext failed")
	}
	return wrapper, nil
}

func GetGitHubClientFromContext(ctx context.Context) (*github.Client, error) {
	wrapper, err := GetGitHubClientWrapperFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetGitHubClientFromContext failed")
	}
	return wrapper.client, nil
}

// newSecondaryRateLimitedClient wraps base with GitHub secondary rate-limit waiting.
// Always create a new client; never mutate a shared rateLimiter.Transport.
func newSecondaryRateLimitedClient(ctx context.Context, base http.RoundTripper) *http.Client {
	if base == nil {
		base = http.DefaultTransport
	}
	return github_ratelimit.NewClient(
		base,
		github_secondary_ratelimit.WithLimitDetectedCallback(func(cbCtx *github_secondary_ratelimit.CallbackContext) {
			logging.Warn(ctx, "GitHub secondary rate limit detected, sleeping until reset",
				"resetTime", cbCtx.ResetTime,
				"totalSleepTime", cbCtx.TotalSleepTime,
			)
		}),
	)
}

// ClientForOrganization returns a client authenticated as the App installation for org.
// When not using App auth (PAT) or appsTransport is unset, returns the default client.
func (w *GitHubClientWrapper) ClientForOrganization(ctx context.Context, org string) (*github.Client, error) {
	if w == nil {
		return nil, fmt.Errorf("github client wrapper is nil")
	}
	if w.appsTransport == nil || org == "" {
		return w.client, nil
	}
	if cached, ok := w.orgClients.Load(org); ok {
		return cached.(*github.Client), nil
	}
	appClient := github.NewClient(&http.Client{Transport: w.appsTransport})
	installation, _, err := appClient.Apps.FindOrganizationInstallation(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("finding GitHub App installation for org %s: %w", org, err)
	}
	if installation.GetID() == 0 {
		return nil, fmt.Errorf("GitHub App is not installed on organization %s", org)
	}
	itr := ghinstallation.NewFromAppsTransport(w.appsTransport, installation.GetID())
	client := github.NewClient(newSecondaryRateLimitedClient(ctx, itr))
	actual, _ := w.orgClients.LoadOrStore(org, client)
	return actual.(*github.Client), nil
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

// AuthenticateAndReturnGitHubClient builds a GitHub API client.
// When privateKey is set, appID is required. installationID may be 0: in that case the
// installation is resolved from enterprise (GET /enterprises/{enterprise}/installation)
// or owner (FindOrganizationInstallation). The returned wrapper can mint per-org clients
// when App auth was used (appsTransport is set).
//
// Enterprise runner APIs (registration/list/remove) are not accessible to GitHub App
// installation tokens ("Resource not accessible by integration"). When enterprise is set
// and a classic PAT (token) is also provided, the default client uses the PAT for those
// APIs while appsTransport remains available for per-org Actions calls via
// ClientForOrganization. In that hybrid mode, installation_id is unused for the default
// client (org installs are resolved per job owner).
func AuthenticateAndReturnGitHubClient(
	ctx context.Context,
	privateKey string,
	appID int64,
	installationID int64,
	token string,
	enterprise string,
	owner string,
) (*github.Client, *GitHubClientWrapper, error) {

	var client *github.Client
	var err error
	var rateLimiter *http.Client
	rateLimiter, err = GetRateLimitWaiterClientFromContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	var httpTransport *http.Transport
	httpTransport, err = GetHttpTransportFromContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	if httpTransport == nil {
		httpTransport = http.DefaultTransport.(*http.Transport)
	}
	if rateLimiter == nil {
		rateLimiter = newSecondaryRateLimitedClient(ctx, httpTransport)
	}
	wrapper := &GitHubClientWrapper{}
	if privateKey != "" {
		if appID == 0 {
			return nil, nil, fmt.Errorf("app_id is required when private_key is set")
		}
		// support private key in a file or as text
		var privateKeyBytes []byte
		privateKeyBytes, err = os.ReadFile(privateKey)
		if err != nil {
			privateKeyBytes = []byte(privateKey)
		}
		appsTransport, err := ghinstallation.NewAppsTransport(httpTransport, appID, privateKeyBytes)
		if err != nil {
			if strings.Contains(err.Error(), "invalid key") {
				return nil, nil, fmt.Errorf("error creating github app transport: %s (does the key exist on the filesystem?)", err.Error())
			}
			return nil, nil, fmt.Errorf("error creating github app transport: %s", err.Error())
		}
		wrapper.appsTransport = appsTransport

		// Enterprise self-hosted runner APIs require a classic PAT; App tokens get 403.
		if enterprise != "" && token != "" {
			logging.Info(ctx, "using classic PAT for enterprise GitHub client (runner APIs); App auth kept for org-scoped Actions",
				"enterprise", enterprise,
			)
			client = github.NewClient(rateLimiter).WithAuthToken(token)
		} else {
			resolvedInstallationID := installationID
			if resolvedInstallationID == 0 {
				resolvedInstallationID, err = resolveInstallationID(ctx, appsTransport, enterprise, owner)
				if err != nil {
					return nil, nil, err
				}
				logging.Info(ctx, "resolved GitHub App installation_id",
					"installationID", resolvedInstallationID,
					"enterprise", enterprise,
					"owner", owner,
				)
			} else {
				logging.Info(ctx, "using configured GitHub App installation_id",
					"installationID", resolvedInstallationID,
					"enterprise", enterprise,
					"owner", owner,
				)
			}

			// Do not mutate the shared context rateLimiter.Transport; wrap the
			// installation transport in a fresh secondary rate-limit client.
			itr := ghinstallation.NewFromAppsTransport(appsTransport, resolvedInstallationID)
			client = github.NewClient(newSecondaryRateLimitedClient(ctx, itr))
		}
	} else {
		client = github.NewClient(rateLimiter).WithAuthToken(token)
	}
	wrapper.client = client
	return client, wrapper, nil
}

func resolveInstallationID(
	ctx context.Context,
	appsTransport *ghinstallation.AppsTransport,
	enterprise string,
	owner string,
) (int64, error) {
	appClient := github.NewClient(&http.Client{Transport: appsTransport})
	if enterprise != "" {
		id, err := findEnterpriseInstallationID(ctx, appClient, enterprise)
		if err != nil {
			return 0, fmt.Errorf("resolving enterprise GitHub App installation for %s: %w", enterprise, err)
		}
		return id, nil
	}
	if owner != "" {
		installation, _, err := appClient.Apps.FindOrganizationInstallation(ctx, owner)
		if err != nil {
			return 0, fmt.Errorf("resolving organization GitHub App installation for %s: %w", owner, err)
		}
		if installation.GetID() == 0 {
			return 0, fmt.Errorf("GitHub App is not installed on organization %s", owner)
		}
		return installation.GetID(), nil
	}
	return 0, fmt.Errorf("installation_id is required when private_key is set unless enterprise or owner is set to resolve it")
}

// findEnterpriseInstallationID resolves the App's installation on an enterprise.
// Tries GET /enterprises/{enterprise}/installation first, then falls back to
// paginating GET /app/installations for TargetType=Enterprise.
func findEnterpriseInstallationID(ctx context.Context, client *github.Client, enterprise string) (int64, error) {
	u := fmt.Sprintf("enterprises/%s/installation", enterprise)
	req, err := client.NewRequest("GET", u, nil)
	if err != nil {
		return 0, err
	}
	installation := new(github.Installation)
	_, err = client.Do(ctx, req, installation)
	if err == nil {
		if installation.GetID() == 0 {
			return 0, fmt.Errorf("GitHub App is not installed on enterprise %s", enterprise)
		}
		return installation.GetID(), nil
	}
	if !is404Error(err) {
		return 0, err
	}

	id, foundInstalls, listErr := findEnterpriseInstallationIDFromList(ctx, client, enterprise)
	if listErr != nil {
		return 0, fmt.Errorf("%w (also failed listing app installations: %v)", err, listErr)
	}
	if id != 0 {
		logging.Info(ctx, "resolved enterprise GitHub App installation via ListInstallations fallback",
			"enterprise", enterprise,
			"installationID", id,
		)
		return id, nil
	}
	return 0, fmt.Errorf(
		"GitHub App is not installed on enterprise %q (GET /enterprises/%s/installation returned 404). Install the App on the enterprise account, or set installation_id explicitly. For enterprise handlers, prefer classic PAT + App hybrid auth (PAT for runner APIs; org App installs for Actions). Current App installations: %s",
		enterprise, enterprise, summarizeInstallations(foundInstalls),
	)
}

func findEnterpriseInstallationIDFromList(
	ctx context.Context,
	client *github.Client,
	enterprise string,
) (int64, []*github.Installation, error) {
	var found []*github.Installation
	opts := &github.ListOptions{PerPage: 100}
	for {
		installations, resp, err := client.Apps.ListInstallations(ctx, opts)
		if err != nil {
			return 0, found, err
		}
		found = append(found, installations...)
		if id := matchEnterpriseInstallation(installations, enterprise); id != 0 {
			return id, found, nil
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return 0, found, nil
}

func matchEnterpriseInstallation(installations []*github.Installation, enterprise string) int64 {
	for _, inst := range installations {
		if inst == nil {
			continue
		}
		if !strings.EqualFold(inst.GetTargetType(), "Enterprise") {
			continue
		}
		account := inst.GetAccount()
		if account == nil {
			continue
		}
		// Enterprise account login is the enterprise slug in installation payloads.
		if strings.EqualFold(account.GetLogin(), enterprise) {
			return inst.GetID()
		}
	}
	return 0
}

func summarizeInstallations(installations []*github.Installation) string {
	if len(installations) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(installations))
	for _, inst := range installations {
		if inst == nil {
			continue
		}
		accountLogin := ""
		if account := inst.GetAccount(); account != nil {
			accountLogin = account.GetLogin()
		}
		parts = append(parts, fmt.Sprintf("%s:%s(id=%d)", inst.GetTargetType(), accountLogin, inst.GetID()))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
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
			if err := waitForGitHubAPIRateLimitReset(workerCtx, executeGitHubClientFunctionCtx, response.Rate.Reset.Time); err != nil {
				return pluginCtx, nil, nil, err
			}
			return executeGitHubClientFunctionWithRetry(workerCtx, executeGitHubClientFunctionCtx, executeFunc, retryAttempt) // Retry the function after waiting
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
