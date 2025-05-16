package anka

import (
	"context"
	"fmt"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/host"
	"github.com/veertuinc/anklet/internal/logging"
)

type VM struct {
	Name     string `json:"name"`
	CPU      int    `json:"cpu_cores"`
	MEMBytes uint64 `json:"ram_size"`
}

func GetAnkaVmFromContext(ctx context.Context) (*VM, error) {
	ankaVm, ok := ctx.Value(config.ContextKey("ankavm")).(*VM)
	if !ok {
		return nil, fmt.Errorf("GetAnkaVmFromContext failed")
	}
	return ankaVm, nil
}

func VmHasEnoughResources(pluginCtx context.Context, templateName string) (bool, VM, error) {
	var vm VM
	ankaCLI, err := GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		return false, vm, err
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return false, vm, err
	}
	ankaShowOutput, err := ankaCLI.AnkaShow(pluginCtx, templateName)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting anka show output", "err", err)
		return false, vm, fmt.Errorf("error getting anka show output: %s", err.Error())
	}

	logger.DebugContext(pluginCtx, "anka show output", "output", ankaShowOutput)

	vm.CPU = ankaShowOutput.CPU
	vm.MEMBytes = ankaShowOutput.MEMBytes

	// Determine host level resources
	hostCPUCount, err := host.GetHostCPUCount(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting host cpu count", "err", err)
		return false, vm, fmt.Errorf("error getting host cpu count: %s", err.Error())
	}
	logger.DebugContext(pluginCtx, "hostCPUCount", "hostCPUCount", hostCPUCount)

	hostMemoryBytes, err := host.GetHostMemoryBytes(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting host memory bytes", "err", err)
		return false, vm, fmt.Errorf("error getting host memory bytes: %s", err.Error())
	}
	logger.DebugContext(pluginCtx, "hostMemoryBytes", "hostMemoryBytes", hostMemoryBytes)

	// Get resource usage of other active VMs on the host
	ankaListOutput, err := ankaCLI.AnkaList(pluginCtx, "--running")
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting anka list output", "err", err)
		return false, vm, fmt.Errorf("error getting anka list output: %s", err.Error())
	}
	logger.DebugContext(pluginCtx, "ankaListOutput", "ankaListOutput", ankaListOutput)
	totalVMCPUUsed := 0
	totalVMMEMBytesUsed := uint64(0)
	for _, vmFromList := range ankaListOutput.Body.([]any) {
		vmMap := vmFromList.(map[string]any)
		vmName := vmMap["name"].(string)
		ankaShowOutput, err := ankaCLI.AnkaShow(pluginCtx, vmName)
		if err != nil {
			logger.ErrorContext(pluginCtx, "error getting anka show output", "err", err)
			return false, vm, fmt.Errorf("error getting anka show output: %s", err.Error())
		}
		totalVMCPUUsed += ankaShowOutput.CPU
		totalVMMEMBytesUsed += ankaShowOutput.MEMBytes
	}
	// See if host has enough resources to run VM
	logger.DebugContext(pluginCtx, "totalVMCPUUsed", "totalVMCPUUsed", totalVMCPUUsed)
	logger.DebugContext(pluginCtx, "totalVMMEMBytesUsed", "totalVMMEMBytesUsed", totalVMMEMBytesUsed)
	// be sure to check if the template is just needing more than the host and can never run
	if vm.CPU > hostCPUCount {
		logger.WarnContext(pluginCtx, "host does not have enough CPU cores to run VM", "vm.CPU", vm.CPU, "hostCPUCount", hostCPUCount)
		return false, vm, fmt.Errorf("host does not have enough CPU cores to run VM")
	}
	if vm.MEMBytes > hostMemoryBytes {
		logger.WarnContext(pluginCtx, "host does not have enough memory to run VM", "vm.MEMBytes", vm.MEMBytes, "hostMemoryBytes", hostMemoryBytes)
		return false, vm, fmt.Errorf("host does not have enough memory to run VM")
	}
	// check if the host has enough resources to run the VM given other VMs already running
	if (vm.CPU + totalVMCPUUsed) > hostCPUCount {
		logger.WarnContext(pluginCtx, "host does not have enough CPU cores to run VM", "vm.CPU", vm.CPU, "totalVMCPUUsed", totalVMCPUUsed, "hostCPUCount", hostCPUCount)
		return false, vm, nil
	}
	if (vm.MEMBytes + totalVMMEMBytesUsed) > hostMemoryBytes {
		logger.WarnContext(pluginCtx, "host does not have enough memory to run VM", "vm.MEMBytes", vm.MEMBytes, "totalVMMEMBytesUsed", totalVMMEMBytesUsed, "hostMemoryBytes", hostMemoryBytes)
		return false, vm, nil
	}
	return true, vm, nil
}
