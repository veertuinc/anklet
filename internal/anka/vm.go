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

// func GetAnkaVmFromContext(pluginCtx context.Context) (*VM, error) {
// 	ankaVm, ok := pluginCtx.Value(config.ContextKey("ankavm")).(*VM)
// 	if !ok {
// 		return nil, fmt.Errorf("GetAnkaVmFromContext failed")
// 	}
// 	return ankaVm, nil
// }

func GetAnkaRegistryVmInfo(
	pluginCtx context.Context,
	template string,
	tag string,
) (*VM, error) {
	var vm VM
	ankaCLI, err := GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	ankaShowOutput, err := ankaCLI.AnkaRegistryShowTemplate(pluginCtx, template, tag)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting anka show output", "err", err)
		return nil, fmt.Errorf("error getting anka show output: %s", err.Error())
	}

	logger.DebugContext(pluginCtx, "anka show output", "output", ankaShowOutput)

	//vm.Name = name this would end up the template name, which we don't want
	vm.CPUCount = ankaShowOutput.CPU
	vm.MEMBytes = ankaShowOutput.MEMBytes
	return &vm, nil
}

func GetAnkaVmInfo(pluginCtx context.Context, name string) (*VM, error) {
	var vm VM
	ankaCLI, err := GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	ankaShowOutput, err := ankaCLI.AnkaShow(pluginCtx, name)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error getting anka show output", "err", err)
		return nil, fmt.Errorf("error getting anka show output: %s", err.Error())
	}

	logger.DebugContext(pluginCtx, "anka show output", "output", ankaShowOutput)

	//vm.Name = name this would end up the template name, which we don't want
	vm.CPUCount = ankaShowOutput.CPU
	vm.MEMBytes = ankaShowOutput.MEMBytes
	return &vm, nil
}

func VmHasEnoughHostResources(pluginCtx context.Context, vm VM) error {
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return fmt.Errorf("error getting globals from context: %s", err.Error())
	}
	if vm.CPUCount > workerGlobals.HostCPUCount {
		return fmt.Errorf("host does not have enough CPU cores to run VM")
	}
	if vm.MEMBytes > workerGlobals.HostMemoryBytes {
		return fmt.Errorf("host does not have enough memory to run VM")
	}
	return nil
}

// compared to other VMs potentially running, do we have enough resources to run this VM?
func VmHasEnoughResources(pluginCtx context.Context, vm VM) error {
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return fmt.Errorf("error getting globals from context: %s", err.Error())
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return fmt.Errorf("error getting logger: %s", err.Error())
	}
	ankaCLI, err := GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		return fmt.Errorf("error getting anka cli: %s", err.Error())
	}
	// Get resource usage of other active VMs on the host
	ankaListOutput, err := ankaCLI.AnkaList(pluginCtx, "--running")
	if err != nil {
		return fmt.Errorf("error getting anka list output: %s", err.Error())
	}
	// logger.DebugContext(pluginCtx, "ankaListOutput", "ankaListOutput", ankaListOutput)
	totalVMCPUUsed := 0
	totalVMMEMBytesUsed := uint64(0)
	for _, vmFromList := range ankaListOutput.Body.([]any) {
		vmMap := vmFromList.(map[string]any)
		vmName := vmMap["name"].(string)
		ankaShowOutput, err := ankaCLI.AnkaShow(pluginCtx, vmName)
		if err != nil {
			return fmt.Errorf("error getting anka show output: %s", err.Error())
		}
		totalVMCPUUsed += ankaShowOutput.CPU
		totalVMMEMBytesUsed += ankaShowOutput.MEMBytes
	}
	// See if host has enough resources to run VM
	// logger.DebugContext(pluginCtx, "totalVMCPUUsed", "totalVMCPUUsed", totalVMCPUUsed)
	// logger.DebugContext(pluginCtx, "totalVMMEMBytesUsed", "totalVMMEMBytesUsed", totalVMMEMBytesUsed)
	// check if the host has enough resources to run the VM given other VMs already running
	if (vm.CPUCount + totalVMCPUUsed) > workerGlobals.HostCPUCount {
		logger.WarnContext(pluginCtx, "host does not have enough CPU cores to run VM", "vm.CPUCount", vm.CPUCount, "totalVMCPUUsed", totalVMCPUUsed, "hostCPUCount", workerGlobals.HostCPUCount)
		return fmt.Errorf("host does not have enough CPU cores to run VM")
	}
	if (vm.MEMBytes + totalVMMEMBytesUsed) > workerGlobals.HostMemoryBytes {
		logger.WarnContext(pluginCtx, "host does not have enough memory to run VM", "vm.MEMBytes", vm.MEMBytes, "totalVMMEMBytesUsed", totalVMMEMBytesUsed, "hostMemoryBytes", workerGlobals.HostMemoryBytes)
		return fmt.Errorf("host does not have enough memory to run VM")
	}
	return nil
}
