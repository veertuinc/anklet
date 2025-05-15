package host

// #include <unistd.h>
import "C"
import (
	"context"

	"github.com/shirou/gopsutil/v4/cpu"
)

func GetHostCPUCount(pluginCtx context.Context) (int, error) {
	cpuCount, err := cpu.Counts(true)
	if err != nil {
		return 0, err
	}
	return cpuCount, nil
}

func GetHostMemoryBytes(pluginCtx context.Context) (uint64, error) {
	return uint64(C.sysconf(C._SC_PHYS_PAGES) * C.sysconf(C._SC_PAGE_SIZE)), nil
}
