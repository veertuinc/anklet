package anka

import (
	"context"
	"fmt"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
)

type VM struct {
	Name     string `json:"name"`
	CPUCount int    `json:"cpu_cores"`
	MEMBytes uint64 `json:"ram_size"`
}

func GetAnkaRegistryVmInfo(
	workerCtx context.Context,
	pluginCtx context.Context,
	template string,
	tag string,
) (*VM, error) {
	ankaCLI, err := GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	ankaShowOutput, err := ankaCLI.AnkaRegistryShowTemplate(workerCtx, pluginCtx, template, tag)
	if err != nil {
		logging.Warn(pluginCtx, "error getting anka show output", "err", err)
		return nil, fmt.Errorf("error getting anka show output: %w", err)
	}

	logging.Debug(pluginCtx, "anka show output", "output", ankaShowOutput)
	return &VM{
		CPUCount: ankaShowOutput.CPU,
		MEMBytes: ankaShowOutput.MEMBytes,
	}, nil
}

func GetAnkaVmInfo(pluginCtx context.Context, name string) (*VM, error) {
	ankaCLI, err := GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	ankaShowOutput, err := ankaCLI.AnkaShow(pluginCtx, name)
	if err != nil {
		logging.Error(pluginCtx, "error getting anka show output", "err", err)
		return nil, fmt.Errorf("error getting anka show output: %w", err)
	}

	logging.Debug(pluginCtx, "anka show output", "output", ankaShowOutput)
	return &VM{
		CPUCount: ankaShowOutput.CPU,
		MEMBytes: ankaShowOutput.MEMBytes,
	}, nil
}

func VmHasEnoughHostResources(pluginCtx context.Context, vm VM) error {
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return fmt.Errorf("error getting globals from context: %w", err)
	}
	if vm.CPUCount > workerGlobals.HostCPUCount {
		return fmt.Errorf("host does not have enough CPU cores yet to run VM")
	}
	if vm.MEMBytes > workerGlobals.HostMemoryBytes {
		return fmt.Errorf("host does not have enough memory yet to run VM")
	}
	return nil
}

// VmHasEnoughResources checks whether the host has enough unused CPU and RAM
// to run vm alongside currently running VMs.
func VmHasEnoughResources(pluginCtx context.Context, vm VM) error {
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return fmt.Errorf("error getting globals from context: %w", err)
	}
	ankaCLI, err := GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		return fmt.Errorf("error getting anka cli: %w", err)
	}
	ankaListOutput, err := ankaCLI.AnkaList(pluginCtx, "--running")
	if err != nil {
		return fmt.Errorf("error getting anka list output: %w", err)
	}

	totalVMCPUUsed := 0
	totalVMMEMBytesUsed := uint64(0)
	if bodySlice, ok := ankaListOutput.Body.([]any); ok {
		for _, vmFromList := range bodySlice {
			vmMap, ok := vmFromList.(map[string]any)
			if !ok {
				continue
			}
			vmName, ok := vmMap["name"].(string)
			if !ok || vmName == "" {
				continue
			}
			ankaShowOutput, showErr := ankaCLI.AnkaShow(pluginCtx, vmName)
			if showErr != nil {
				return fmt.Errorf("error getting anka show output: %w", showErr)
			}
			totalVMCPUUsed += ankaShowOutput.CPU
			totalVMMEMBytesUsed += ankaShowOutput.MEMBytes
		}
	}

	if (vm.CPUCount + totalVMCPUUsed) > workerGlobals.HostCPUCount {
		return fmt.Errorf("host does not have enough CPU cores to run VM")
	}
	if (vm.MEMBytes + totalVMMEMBytesUsed) > workerGlobals.HostMemoryBytes {
		return fmt.Errorf("host does not have enough memory to run VM")
	}
	return nil
}
