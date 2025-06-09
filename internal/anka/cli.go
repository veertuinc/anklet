package anka

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
)

type AnkaJson struct {
	Status  string
	Code    int
	Body    any
	Message string
}

type Cli struct {
	License struct {
		Product     string
		LicenseType string
		Status      string
	}
	Version           string
	RegistryPullMutex sync.Mutex
}

type AnkaShowOutput struct {
	CPU      int    `json:"cpu_cores"`
	MEMBytes uint64 `json:"ram_size"`
	Tag      string `json:"tag"`
}

func GetAnkaCLIFromContext(pluginCtx context.Context) (*Cli, error) {
	ankaCLI, ok := pluginCtx.Value(config.ContextKey("ankacli")).(*Cli)
	if !ok {
		return nil, fmt.Errorf("GetAnkaCLIFromContext failed")
	}
	return ankaCLI, nil
}

func NewCLI(pluginCtx context.Context) (*Cli, error) {
	cli := &Cli{}

	cmd := exec.Command("anka")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("anka command not found or not working properly: %v", err)
	}

	version, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "version")
	if err != nil || version.Status != "OK" {
		return nil, err
	}
	if body, ok := version.Body.(map[string]interface{}); ok {
		cli.Version = body["version"].(string)
	}

	license, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "license", "show")
	if err != nil || license.Status != "OK" {
		return nil, err
	}

	if body, ok := license.Body.(map[string]interface{}); ok {
		cli.License.Status = body["status"].(string)
		cli.License.LicenseType = body["license_type"].(string)
		cli.License.Product = body["product"].(string)
	}

	if cli.License.Status != "valid" {
		return nil, fmt.Errorf("anka license is invalid %+v", license.Body)
	}

	if cli.License.LicenseType == "com.veertu.anka.run" || cli.License.LicenseType == "com.veertu.anka.develop" {
		return nil, fmt.Errorf("anka license type is not supported %+v", license.Body)
	}

	return cli, nil
}

// DO NOT RUN exec.Command with context or else the cancellation will interrupt things like VM deletion, which we don't want!
func (cli *Cli) Execute(pluginCtx context.Context, args ...string) ([]byte, int, error) {
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return nil, 0, err
	}
	if args[2] != "list" { // hide spammy list command
		logger.DebugContext(pluginCtx, "executing", "command", strings.Join(args, " "))
	}
	done := make(chan error, 1)
	var cmd *exec.Cmd
	var combinedOutput bytes.Buffer

	go func() {
		cmd = exec.Command(args[0], args[1:]...)
		cmd.Stdout = &combinedOutput
		cmd.Stderr = &combinedOutput
		err := cmd.Run()
		done <- err
	}()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			logger.InfoContext(pluginCtx, fmt.Sprintf("execution of command %v is still in progress...", args))
		case err := <-done:
			exitCode := 0
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
			return combinedOutput.Bytes(), exitCode, err
		}
	}
}

func (cli *Cli) ParseAnkaJson(pluginCtx context.Context, jsonData []byte) (*AnkaJson, error) {
	ankaJson := &AnkaJson{}
	err := json.Unmarshal(jsonData, &ankaJson)
	if err != nil {
		return nil, err
	}
	return ankaJson, nil
}

func (cli *Cli) ExecuteParseJson(pluginCtx context.Context, args ...string) (*AnkaJson, error) {
	out, exitCode, _ := cli.Execute(pluginCtx, args...)
	// registry pull can output muliple json objects, per line, so we need to only get the last line
	lines := bytes.Split(out, []byte("\n"))
	lastLine := lines[len(lines)-1]
	ankaJson, err := cli.ParseAnkaJson(pluginCtx, lastLine)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return ankaJson, fmt.Errorf("%s", ankaJson.Message)
	}
	return ankaJson, nil
}

func (cli *Cli) ExecuteAndParseJsonOnError(pluginCtx context.Context, args ...string) ([]byte, error) {
	if pluginCtx.Err() != nil {
		return nil, fmt.Errorf("context canceled before ExecuteAndParseJsonOnError")
	}
	ankaJson := &AnkaJson{}
	out, exitCode, _ := cli.Execute(pluginCtx, args...)
	if exitCode != 0 {
		return out, fmt.Errorf("%s", string(out))
	}
	err := json.Unmarshal(out, &ankaJson)
	if err != nil {
		return out, fmt.Errorf("command execution failed: %v", err)
	}
	return out, nil
}

func (cli *Cli) AnkaRun(pluginCtx context.Context, vmName string, args ...string) error {
	runOutput, exitCode, err := cli.Execute(pluginCtx, "anka", "-j", "run", vmName, "bash", "-c", strings.Join(args, " "))
	if exitCode != 0 || err != nil {
		return fmt.Errorf("command execution failed with code %d: %s %s", exitCode, string(runOutput), err)
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return err
	}
	logger.DebugContext(pluginCtx, "command executed successfully", "stdout", string(runOutput))
	return nil
}

func (cli *Cli) AnkaShow(pluginCtx context.Context, vmName string) (*AnkaShowOutput, error) {
	ankaJson, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "show", vmName)
	if err != nil {
		return nil, err
	}
	ankaTagJson, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "show", vmName, "tag")
	if err != nil {
		return nil, err
	}
	output := &AnkaShowOutput{
		CPU:      int(ankaJson.Body.(map[string]any)["cpu_cores"].(float64)),
		MEMBytes: uint64(ankaJson.Body.(map[string]any)["ram_size"].(float64)),
	}
	tagBody := ankaTagJson.Body
	if tagArr, isArray := tagBody.([]any); isArray {
		// Handle case where body is an array
		if len(tagArr) > 0 {
			if tagMap, ok := tagArr[0].(map[string]any); ok && tagMap["tag"] != nil {
				output.Tag = fmt.Sprintf("%v", tagMap["tag"])
			} else {
				output.Tag = "(using latest)"
			}
		} else {
			output.Tag = "(using latest)"
		}
	} else {
		// Default fallback
		output.Tag = "(using latest)"
	}
	return output, nil
}

func (cli *Cli) AnkaExecuteRegistryCommand(pluginCtx context.Context, args ...string) (*AnkaJson, error) {
	ctxPlugin, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	var registryExtra []string
	if ctxPlugin.RegistryURL != "" {
		registryExtra = []string{"--remote", ctxPlugin.RegistryURL}
	}
	cmdArgs := append([]string{"anka", "-j", "registry"}, registryExtra...)
	cmdArgs = append(cmdArgs, args...)
	return cli.ExecuteParseJson(pluginCtx, cmdArgs...)
}

func (cli *Cli) AnkaRegistryShowTemplate(
	pluginCtx context.Context,
	template string,
	tag string,
) (*AnkaShowOutput, error) {
	if pluginCtx.Err() != nil {
		return nil, fmt.Errorf("context canceled before AnkaRegistryShowTemplate")
	}
	var args []string
	if tag != "(using latest)" {
		args = append(args, "show", template, "--tag", tag)
	} else {
		args = append(args, "show", template)
	}
	showJson, err := cli.AnkaExecuteRegistryCommand(pluginCtx, args...)
	if err != nil {
		return nil, err
	}
	if showJson.Status != "OK" {
		return nil, fmt.Errorf("error showing template from registry: %s", showJson.Message)
	}
	return &AnkaShowOutput{
		CPU:      int(showJson.Body.(map[string]any)["cpu_cores"].(float64)),
		MEMBytes: uint64(showJson.Body.(map[string]any)["ram_size"].(float64)),
	}, nil
}

func (cli *Cli) AnkaRegistryPull(
	workerCtx context.Context,
	pluginCtx context.Context,
	template string,
	tag string,
) (*AnkaJson, error) {
	if pluginCtx.Err() != nil {
		return nil, fmt.Errorf("context canceled before AnkaRegistryPull")
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}

	logger.DebugContext(pluginCtx, "pulling template to host")

	metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
	if err != nil {
		return nil, err
	}

	defer metricsData.SetStatus(pluginCtx, logger, "running")

	metricsData.SetStatus(pluginCtx, logger, "pulling")

	var args []string
	if tag != "(using latest)" {
		args = append(args, "pull", "--shrink", template, "--tag", tag)
	} else {
		args = append(args, "pull", "--shrink", template)
	}
	pullJson, err := cli.AnkaExecuteRegistryCommand(pluginCtx, args...)
	if err != nil {
		return nil, err
	}
	if pullJson.Status != "OK" {
		return nil, fmt.Errorf("error pulling template from registry: %s", pullJson.Message)
	}

	return pullJson, nil
}

func (cli *Cli) AnkaDelete(workerCtx context.Context, pluginCtx context.Context, vmName string) error {
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return err
	}
	deleteOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "delete", "--yes", vmName)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error executing anka delete", "err", err)
		cli.Execute(pluginCtx, "anka", "delete", "--yes", vmName)
		return err
	}
	logger.DebugContext(pluginCtx, "successfully deleted vm", "std", deleteOutput.Message)
	// decrement total running VMs
	metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
	if err != nil {
		return err
	}
	metricsData.DecrementTotalRunningVMs()
	return nil
}

// ping the registry to see if it's up
func (cli *Cli) AnkaRegistryRunning(pluginCtx context.Context) (bool, error) {
	listOutput, err := cli.AnkaExecuteRegistryCommand(pluginCtx, "list")
	if err != nil {
		return false, nil
	}
	if listOutput.Status != "OK" {
		return false, nil
	}
	return true, nil
}

func (cli *Cli) ObtainAnkaVM(
	workerCtx context.Context,
	pluginCtx context.Context,
	ankaTemplate string,
) (*VM, error) {
	if pluginCtx.Err() != nil {
		return nil, fmt.Errorf("context canceled before ObtainAnkaVMAndName")
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	vmID, err := uuid.NewRandom()
	if err != nil {
		logger.ErrorContext(pluginCtx, "error creating uuid for vm name", "err", err)
		return nil, err
	}
	vmName := fmt.Sprintf("anklet-vm-%s", vmID.String())
	vm := &VM{Name: vmName}
	err = cli.AnkaClone(pluginCtx, vmName, ankaTemplate)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error executing anka clone", "err", err)
		return vm, err
	}
	// Start
	err = cli.AnkaStart(pluginCtx, vmName)
	if err != nil {
		logger.DebugContext(pluginCtx, "vm", "vm", vm)
		logger.ErrorContext(pluginCtx, "error executing anka start", "err", err)
		return vm, err
	}
	// increment total running VMs
	metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
	if err != nil {
		return vm, err
	}
	metricsData.IncrementTotalRunningVMs(workerCtx, pluginCtx, logger)
	return vm, nil
}

func (cli *Cli) AnkaClone(pluginCtx context.Context, vmName string, template string) error {
	if pluginCtx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaClone")
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return err
	}
	cloneOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "clone", template, vmName)
	if err != nil {
		return err
	}
	if cloneOutput.Status != "OK" {
		return fmt.Errorf("error cloning template: %s", cloneOutput.Message)
	}
	logger.InfoContext(pluginCtx, "successfully cloned template to new vm")
	return nil
}

func (cli *Cli) AnkaStart(pluginCtx context.Context, vmName string) error {
	if pluginCtx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaStart")
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return err
	}
	startOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "start", vmName)
	if err != nil {
		return err
	}
	if startOutput.Status != "OK" {
		return fmt.Errorf("error starting vm: %s", startOutput.Message)
	}
	logger.InfoContext(pluginCtx, "successfully started vm")
	return nil
}

func (cli *Cli) AnkaList(pluginCtx context.Context, args ...string) (*AnkaJson, error) {
	args = append([]string{"anka", "-j", "list"}, args...)
	output, err := cli.ExecuteParseJson(pluginCtx, args...)
	if err != nil {
		return nil, err
	}
	return output, nil
}

func (cli *Cli) AnkaCopyOutOfVM(pluginCtx context.Context, vmName string, objectToCopyOut string, hostLevelDestination string) error {
	// if pluginCtx.Err() != nil {
	// 	return fmt.Errorf("context canceled before AnkaCopyOutOfVM")
	// }
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return err
	}
	copyOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "cp", "-a", fmt.Sprintf("%s:%s", vmName, objectToCopyOut), hostLevelDestination)
	if err != nil {
		return err
	}
	if copyOutput.Status != "OK" {
		return fmt.Errorf("error copying out of vm: %s", copyOutput.Message)
	}
	logger.DebugContext(pluginCtx, "copy output", "std", copyOutput)
	logger.InfoContext(pluginCtx, fmt.Sprintf("successfully copied %s out of vm to %s", objectToCopyOut, hostLevelDestination), "stdout", copyOutput.Message)

	return nil
}

func (cli *Cli) AnkaCopyIntoVM(pluginCtx context.Context, vmName string, filesToCopyIn ...string) error {
	if pluginCtx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaCopyIntoVM")
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return err
	}
	for _, hostLevelFile := range filesToCopyIn {
		// handle symlinks
		realPath, err := filepath.EvalSymlinks(hostLevelFile)
		if err != nil {
			return fmt.Errorf("error evaluating symlink for %s: %w", hostLevelFile, err)
		}
		hostLevelFile = realPath
		if pluginCtx.Err() != nil {
			return fmt.Errorf("context canceled before AnkaCopyIntoVM executing anka cp")
		}
		copyOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "cp", "-a", hostLevelFile, fmt.Sprintf("%s:", vmName))
		if err != nil {
			return err
		}
		if copyOutput.Status != "OK" {
			return fmt.Errorf("error copying into vm: %s", copyOutput.Message)
		}
		logger.DebugContext(pluginCtx, "copy output", "std", copyOutput)
		logger.InfoContext(pluginCtx, "successfully copied file into vm", "file", hostLevelFile, "stdout", copyOutput.Message)
	}

	return nil
}

func HostHasVmCapacity(pluginCtx context.Context) bool {
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return false
	}
	ankaCLI, err := GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		return false
	}
	// check if there are already two VMS running or not
	runningVMsList, err := ankaCLI.ExecuteParseJson(pluginCtx, "anka", "-j", "list", "-r")
	if err != nil {
		logger.ErrorContext(pluginCtx, "error executing anka list -r", "err", err)
		return false
	}
	if runningVMsList.Status != "OK" {
		logger.ErrorContext(pluginCtx, "error listing running VMs", "status", runningVMsList.Status, "message", runningVMsList.Message)
		return false
	}
	runningVMsCount := 0
	if bodySlice, ok := runningVMsList.Body.([]any); ok {
		runningVMsCount = len(bodySlice)
	} else {
		logger.ErrorContext(pluginCtx, "unable to parse running VMs list body to []any")
		return false
	}
	if runningVMsCount >= 2 {
		logger.WarnContext(pluginCtx, "more than 2 VMs are running; unable to run more than 2 at a time due to Apple SLA")
		return false
	}
	return true
}

func (cli *Cli) EnsureVMTemplateExists(workerCtx context.Context, pluginCtx context.Context, targetTemplate string, targetTag string) (error, error) {
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	ankaCLI, err := GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	pullTemplate := false
	list, err := ankaCLI.AnkaList(pluginCtx, targetTemplate)
	if err != nil {
		list, innerErr := ankaCLI.AnkaList(pluginCtx)
		if innerErr != nil {
			logger.ErrorContext(pluginCtx, "error executing anka list", "err", innerErr)
			return nil, innerErr
		}
		logger.DebugContext(pluginCtx, "list", "stdout", list.Body)
	}
	logger.InfoContext(pluginCtx, "ensuring vm template exists on host", "targetTemplate", targetTemplate, "targetTag", targetTag)
	logger.DebugContext(pluginCtx, "list output", "json", list)
	if list != nil {
		if list.Status == "ERROR" {
			if list.Message == fmt.Sprintf("%s: not found", targetTemplate) {
				pullTemplate = true
			} else {
				logger.ErrorContext(pluginCtx, "error executing anka list", "err", list.Message)
				return nil, fmt.Errorf("error executing anka list: %s", list.Message)
			}
		}
		if list.Status == "OK" {
			// ensure tag is proper; skip if tag is hard coded and we already have it locally
			if bodySlice, ok := list.Body.([]any); ok {
				body, ok := bodySlice[0].(map[string]any)
				if !ok {
					logger.InfoContext(pluginCtx, "list", "body", list.Body)
					logger.ErrorContext(pluginCtx, "unable to parse bodySlice[0] to map[string]any")
					return nil, fmt.Errorf("unable to parse bodySlice[0] to map[string]any")
				}
				if status, ok := body["status"].(string); ok {
					if status == "failed" {
						return nil, fmt.Errorf("vm template is not running and instead %s", status)
					}
				}
				if version, ok := body["version"].(string); ok {
					if targetTag != "(using latest)" {
						if version != targetTag {
							pullTemplate = true
						}
					} else {
						// always pull to ensure latest, if (using latest)
						pullTemplate = true
					}
				}
			} else {
				return nil, fmt.Errorf("unable to parse list.Body to []interface{}")
			}
		}
	} else {
		pullTemplate = true
	}
	if pullTemplate {
		if !workerGlobals.PullLock.TryLock() {
			return nil, fmt.Errorf("a pull is already running on this host")
		}
		defer workerGlobals.PullLock.Unlock()
		pullJson, err := cli.AnkaRegistryPull(workerCtx, pluginCtx, targetTemplate, targetTag)
		if pullJson == nil || pullJson.Code == 3 { // registry doesn't have template (or tag)
			return err, nil
		}
		if err != nil {
			logger.ErrorContext(pluginCtx, "error executing anka registry pull", "err", err)
			return nil, err
		}
	}
	return nil, nil
}
