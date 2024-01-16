package github

import (
	"context"

	"github.com/google/go-github/v58/github"
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
