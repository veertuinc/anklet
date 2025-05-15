package host

import (
	"context"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/veertuinc/anklet/internal/logging"
)

func GetHostCPUCount(pluginCtx context.Context) (int, error) {
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return 0, err
	}
	logger.DebugContext(pluginCtx, "getting host cpu count")
	cpuCount, err := cpu.Counts(true)
	if err != nil {
		return 0, err
	}
	return cpuCount, nil
}

func GetHostMemoryBytes(pluginCtx context.Context) (uint64, error) {
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return 0, err
	}
	logger.DebugContext(pluginCtx, "getting host memory bytes")
	memory, err := mem.VirtualMemory()
	if err != nil {
		return 0, err
	}
	memoryBytes := memory.Free
	return memoryBytes, nil
}
