//go:build !darwin && !linux

package host

import (
	"context"
)

// GetHostCPUCount returns the number of CPUs on the host
func GetHostCPUCount(pluginCtx context.Context) (int, error) {
	// Return a default value for unsupported platforms
	return 1, nil
}

// GetHostMemoryBytes returns the total memory in bytes on the host
func GetHostMemoryBytes(pluginCtx context.Context) (uint64, error) {
	// Return a default value for unsupported platforms
	return 1024 * 1024 * 1024, nil // 1GB default
}

// GetHostDiskTotalBytes returns the total disk space in bytes
func GetHostDiskTotalBytes(pluginCtx context.Context) (uint64, error) {
	// Return a default value for unsupported platforms
	return 100 * 1024 * 1024 * 1024, nil // 100GB default
}

// GetHostDiskUsedBytes returns the used disk space in bytes
func GetHostDiskSizeBytes(pluginCtx context.Context) (uint64, error) {
	// Return a default value for unsupported platforms
	return 100 * 1024 * 1024 * 1024, nil // 100GB default
}
