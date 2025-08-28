//go:build !darwin

package anka

import (
	"context"
	"fmt"
)

// EnsureSpaceForTemplate is a stub implementation for non-Darwin platforms
// Since Anka only runs on macOS, this method returns an error indicating
// that the functionality is not supported on this platform
func (cli *Cli) EnsureSpaceForTemplate(
	workerCtx context.Context,
	pluginCtx context.Context,
	templateUUID, tag string,
) (error, error) { // ensureSpaceError, genericError
	return nil, fmt.Errorf("EnsureSpaceForTemplate is only supported on Darwin (macOS) platforms")
}
