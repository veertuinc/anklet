//go:build darwin || linux

package host

import (
	"context"
	"fmt"
	"runtime"
	"syscall"

	"github.com/shirou/gopsutil/v4/mem"
)

// GetHostCPUCount returns the number of logical CPUs available on the host.
func GetHostCPUCount(_ context.Context) (int, error) {
	return runtime.NumCPU(), nil
}

// GetHostMemoryBytes returns the total physical memory in bytes on the host.
func GetHostMemoryBytes(ctx context.Context) (uint64, error) {
	vmStat, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("getting host memory: %w", err)
	}
	return vmStat.Total, nil
}

// GetHostDiskSizeBytes returns the total root filesystem size in bytes.
func GetHostDiskSizeBytes(_ context.Context) (uint64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return 0, err
	}
	return uint64(stat.Blocks) * uint64(stat.Bsize), nil
}
