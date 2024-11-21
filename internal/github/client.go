package github

import (
	"context"
	"net/http"

	"github.com/google/go-github/v66/github"
	"github.com/veertuinc/anklet/internal/config"
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
	if !ok {
		panic("GetRateLimitWaiterClientFromContext failed")
	}
	return rateLimiter
}
