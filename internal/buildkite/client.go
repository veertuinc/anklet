package buildkite

import (
	"context"
	"fmt"

	buildkiteapi "github.com/buildkite/go-buildkite/v4"
	"github.com/veertuinc/anklet/internal/config"
)

type ClientWrapper struct {
	client *buildkiteapi.Client
}

func NewClientWrapper(client *buildkiteapi.Client) *ClientWrapper {
	return &ClientWrapper{client: client}
}

func GetClientFromContext(ctx context.Context) (*buildkiteapi.Client, error) {
	wrapper, ok := ctx.Value(config.ContextKey("buildkiteclient")).(*ClientWrapper)
	if !ok || wrapper == nil || wrapper.client == nil {
		return nil, fmt.Errorf("GetBuildkiteClientFromContext failed")
	}
	return wrapper.client, nil
}

func AuthenticateAndReturnBuildkiteClient(token string) (*buildkiteapi.Client, error) {
	if token == "" {
		return nil, fmt.Errorf("buildkite token is empty")
	}
	client, err := buildkiteapi.NewOpts(buildkiteapi.WithTokenAuth(token))
	if err != nil {
		return nil, err
	}
	return client, nil
}
