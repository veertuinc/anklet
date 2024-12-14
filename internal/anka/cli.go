package anka

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	Body    interface{}
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
			// logger.InfoContext(pluginCtx, fmt.Sprintf("execution of command %v completed", args))
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

func (cli *Cli) AnkaRun(pluginCtx context.Context, args ...string) error {
	vm, err := GetAnkaVmFromContext(pluginCtx)
	if err != nil {
		return err
	}
	runOutput, exitCode, err := cli.Execute(pluginCtx, "anka", "-j", "run", vm.Name, "bash", "-c", strings.Join(args, " "))
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

func (cli *Cli) AnkaRegistryPull(workerCtx context.Context, pluginCtx context.Context, template string, tag string) (*AnkaJson, error) {
	if pluginCtx.Err() != nil {
		return nil, fmt.Errorf("context canceled before AnkaRegistryPull")
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	ctxPlugin, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	var registryExtra []string
	if ctxPlugin.RegistryURL != "" {
		registryExtra = []string{"--remote", ctxPlugin.RegistryURL}
	}
	var args []string
	if tag != "(using latest)" {
		args = append([]string{"anka", "-j", "registry"}, registryExtra...)
		args = append(args, "pull", "--shrink", template, "--tag", tag)
	} else {
		args = append([]string{"anka", "-j", "registry"}, registryExtra...)
		args = append(args, "pull", "--shrink", template)
	}
	logger.DebugContext(pluginCtx, "pulling template to host")

	metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
	if err != nil {
		return nil, err
	}

	defer metricsData.SetStatus(pluginCtx, logger, "running")

	metricsData.SetStatus(pluginCtx, logger, "pulling")

	pullJson, err := cli.ExecuteParseJson(pluginCtx, args...)
	if err != nil {
		return pullJson, err
	}
	if pullJson.Status != "OK" {
		return pullJson, fmt.Errorf("error pulling template from registry: %s", pullJson.Message)
	}
	logger.DebugContext(pluginCtx, "successfully pulled template from registry")
	return pullJson, nil
}

func (cli *Cli) AnkaDelete(workerCtx context.Context, pluginCtx context.Context, vm *VM) error {
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return err
	}
	deleteOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "delete", "--yes", vm.Name)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error executing anka delete", "err", err)
		cli.Execute(pluginCtx, "anka", "delete", "--yes", vm.Name)
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

func (cli *Cli) ObtainAnkaVM(workerCtx context.Context, pluginCtx context.Context, ankaTemplate string) (context.Context, *VM, error) {
	if pluginCtx.Err() != nil {
		return pluginCtx, nil, fmt.Errorf("context canceled before ObtainAnkaVMAndName")
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return pluginCtx, nil, err
	}
	vmID, err := uuid.NewRandom()
	if err != nil {
		logger.ErrorContext(pluginCtx, "error creating uuid for vm name", "err", err)
		return pluginCtx, nil, err
	}
	vmName := fmt.Sprintf("anklet-vm-%s", vmID.String())
	pluginCtx = logging.AppendCtx(pluginCtx, slog.String("vmName", vmName))
	vm := &VM{Name: vmName}
	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("ankavm"), vm)
	err = cli.AnkaClone(pluginCtx, ankaTemplate)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error executing anka clone", "err", err)
		return pluginCtx, vm, err
	}
	// Start
	err = cli.AnkaStart(pluginCtx)
	if err != nil {
		logger.ErrorContext(pluginCtx, "error executing anka start", "err", err)
		return pluginCtx, vm, err
	}
	// increment total running VMs
	metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
	if err != nil {
		return pluginCtx, vm, err
	}
	metricsData.IncrementTotalRunningVMs(workerCtx, pluginCtx, logger)
	return pluginCtx, vm, nil
}

func (cli *Cli) AnkaClone(pluginCtx context.Context, template string) error {
	if pluginCtx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaClone")
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return err
	}
	vm, err := GetAnkaVmFromContext(pluginCtx)
	if err != nil {
		return err
	}
	cloneOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "clone", template, vm.Name)
	if err != nil {
		return err
	}
	if cloneOutput.Status != "OK" {
		return fmt.Errorf("error cloning template: %s", cloneOutput.Message)
	}
	logger.InfoContext(pluginCtx, "successfully cloned template to new vm")
	return nil
}

func (cli *Cli) AnkaStart(pluginCtx context.Context) error {
	if pluginCtx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaStart")
	}
	logger, err := logging.GetLoggerFromContext(pluginCtx)
	if err != nil {
		return err
	}
	vm, err := GetAnkaVmFromContext(pluginCtx)
	if err != nil {
		return err
	}
	startOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "start", vm.Name)
	if err != nil {
		return err
	}
	if startOutput.Status != "OK" {
		return fmt.Errorf("error starting vm: %s", startOutput.Message)
	}
	logger.InfoContext(pluginCtx, "successfully started vm")
	return nil
}

func (cli *Cli) AnkaCopyOutOfVM(ctx context.Context, objectToCopyOut string, hostLevelDestination string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaCopyOutOfVM")
	}
	logger, err := logging.GetLoggerFromContext(ctx)
	if err != nil {
		return err
	}
	vm, err := GetAnkaVmFromContext(ctx)
	if err != nil {
		return err
	}
	copyOutput, err := cli.ExecuteParseJson(ctx, "anka", "-j", "cp", "-a", fmt.Sprintf("%s:%s", vm.Name, objectToCopyOut), hostLevelDestination)
	if err != nil {
		return err
	}
	if copyOutput.Status != "OK" {
		return fmt.Errorf("error copying out of vm: %s", copyOutput.Message)
	}
	logger.DebugContext(ctx, "copy output", "std", copyOutput)
	logger.InfoContext(ctx, fmt.Sprintf("successfully copied %s out of vm to %s", objectToCopyOut, hostLevelDestination), "stdout", copyOutput.Message)

	return nil
}

func (cli *Cli) AnkaCopyIntoVM(ctx context.Context, filesToCopyIn ...string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaCopyIntoVM")
	}
	logger, err := logging.GetLoggerFromContext(ctx)
	if err != nil {
		return err
	}
	vm, err := GetAnkaVmFromContext(ctx)
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
		copyOutput, err := cli.ExecuteParseJson(ctx, "anka", "-j", "cp", "-a", hostLevelFile, fmt.Sprintf("%s:", vm.Name))
		if err != nil {
			return err
		}
		if copyOutput.Status != "OK" {
			return fmt.Errorf("error copying into vm: %s", copyOutput.Message)
		}
		logger.DebugContext(ctx, "copy output", "std", copyOutput)
		logger.InfoContext(ctx, "successfully copied file into vm", "file", hostLevelFile, "stdout", copyOutput.Message)
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
	if bodySlice, ok := runningVMsList.Body.([]interface{}); ok {
		runningVMsCount = len(bodySlice)
	} else {
		logger.ErrorContext(pluginCtx, "unable to parse running VMs list body to []interface{}")
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
	globals, err := config.GetGlobalsFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	pullTemplate := false
	list, err := ankaCLI.ExecuteParseJson(pluginCtx, "anka", "-j", "list", targetTemplate)
	if err != nil {
		list, innerErr := ankaCLI.ExecuteParseJson(pluginCtx, "anka", "-j", "list")
		if innerErr != nil {
			logger.ErrorContext(pluginCtx, "error executing anka list", "err", innerErr)
			return nil, innerErr
		}
		logger.DebugContext(pluginCtx, "list", "stdout", list.Body)
	}
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
			if bodySlice, ok := list.Body.([]interface{}); ok {
				body, ok := bodySlice[0].(map[string]interface{})
				if !ok {
					logger.InfoContext(pluginCtx, "list", "body", list.Body)
					logger.ErrorContext(pluginCtx, "unable to parse bodySlice[0] to map[string]interface{}")
					return nil, fmt.Errorf("unable to parse bodySlice[0] to map[string]interface{}")
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
		if !globals.PullLock.TryLock() {
			return nil, fmt.Errorf("a pull is already running on this host")
		}
		defer globals.PullLock.Unlock()
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
