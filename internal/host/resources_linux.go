//go:build linux

package host

// #include <unistd.h>
import "C"
import (
	"context"
	"syscall"
)

// GetHostCPUCount returns the number of CPUs on the host
func GetHostCPUCount(pluginCtx context.Context) (int, error) {
	cpuCount := int(C.sysconf(C._SC_NPROCESSORS_ONLN))
	return cpuCount, nil
}

// GetHostMemoryBytes returns the total memory in bytes on the host
func GetHostMemoryBytes(pluginCtx context.Context) (uint64, error) {
	return uint64(C.sysconf(C._SC_PHYS_PAGES) * C.sysconf(C._SC_PAGE_SIZE)), nil
}

// GetHostDiskUsedBytes returns the used disk space in bytes
func GetHostDiskSizeBytes(pluginCtx context.Context) (uint64, error) {
	var stat syscall.Statfs_t
	err := syscall.Statfs("/", &stat)
	if err != nil {
		return 0, err
	}
	total := uint64(stat.Blocks) * uint64(stat.Bsize)
	return total, nil
}
