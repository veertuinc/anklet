//go:build !darwin

package anka

import (
	"context"

	"github.com/veertuinc/anklet/internal/logging"
)

// EnsureSpaceForTemplateOnDarwin is a stub for non-Darwin platforms where Anka VMs don't run
func (cli *Cli) EnsureSpaceForTemplateOnDarwin(
	workerCtx context.Context,
	pluginCtx context.Context,
	template, tag string,
	requiredSizeBytes uint64,
) error {
	logging.Debug(pluginCtx, "disk space checking not available on non-Darwin platforms, skipping space check")
	return nil // Continue without space check on non-Darwin platforms since Anka only runs on macOS
}
