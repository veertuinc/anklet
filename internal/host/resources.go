package host

// #include <unistd.h>
import "C"
import (
	"context"
)

func GetHostCPUCount(pluginCtx context.Context) (int, error) {
	cpuCount := int(C.sysconf(C._SC_NPROCESSORS_ONLN))
	return cpuCount, nil
}

func GetHostMemoryBytes(pluginCtx context.Context) (uint64, error) {
	return uint64(C.sysconf(C._SC_PHYS_PAGES) * C.sysconf(C._SC_PAGE_SIZE)), nil
}
