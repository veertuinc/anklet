//go:build !darwin

package anka

import (
	"context"
	"fmt"
)

// EnsureSpaceForTemplate is a stub implementation for non-Darwin platforms.
func (cli *Cli) EnsureSpaceForTemplate(
	_ context.Context,
	_ context.Context,
	_, _ string,
) error {
	return fmt.Errorf("EnsureSpaceForTemplate is only supported on Darwin (macOS) platforms")
}
