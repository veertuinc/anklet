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
)

type AnkaJson struct {
	Status  string
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

func GetAnkaCLIFromContext(ctx context.Context) *Cli {
	ankaCLI, ok := ctx.Value(config.ContextKey("ankacli")).(*Cli)
	if !ok {
		panic("function GetAnkaCLIFromContext failed")
	}
	return ankaCLI
}

func NewCLI(ctx context.Context) (*Cli, error) {
	cli := &Cli{}

	cmd := exec.Command("anka")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("anka command not found or not working properly: %v", err)
	}

	version, err := cli.ExecuteParseJson(ctx, "anka", "-j", "version")
	if err != nil || version.Status != "OK" {
		return nil, err
	}
	if body, ok := version.Body.(map[string]interface{}); ok {
		cli.Version = body["version"].(string)
	}

	license, err := cli.ExecuteParseJson(ctx, "anka", "-j", "license", "show")
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

func (cli *Cli) Execute(ctx context.Context, args ...string) ([]byte, int, error) {
	logger := logging.GetLoggerFromContext(ctx)
	if args[2] != "list" { // hide spammy list command
		logger.DebugContext(ctx, "executing", "command", strings.Join(args, " "))
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
			logger.InfoContext(ctx, fmt.Sprintf("execution of command %v is still in progress...", args))
		case err := <-done:
			exitCode := 0
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
			return combinedOutput.Bytes(), exitCode, err
		}
	}
}

func (cli *Cli) ParseAnkaJson(ctx context.Context, jsonData []byte) (*AnkaJson, error) {
	ankaJson := &AnkaJson{}
	err := json.Unmarshal(jsonData, &ankaJson)
	if err != nil {
		return nil, err
	}
	return ankaJson, nil
}

func (cli *Cli) ExecuteParseJson(ctx context.Context, args ...string) (*AnkaJson, error) {
	out, exitCode, _ := cli.Execute(ctx, args...)
	if exitCode != 0 {
		return nil, fmt.Errorf("command execution failed with code %d: %s", exitCode, string(out))
	}
	// registry pull can output muliple json objects, per line, so we need to only get the last line
	lines := bytes.Split(out, []byte("\n"))
	lastLine := lines[len(lines)-1]
	ankaJson, err := cli.ParseAnkaJson(ctx, lastLine)
	if err != nil {
		return nil, err
	}
	return ankaJson, nil
}

func (cli *Cli) ExecuteAndParseJsonOnError(ctx context.Context, args ...string) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, fmt.Errorf("context canceled before ExecuteAndParseJsonOnError")
	}
	ankaJson := &AnkaJson{}
	out, exitCode, _ := cli.Execute(ctx, args...)
	if exitCode != 0 {
		return out, fmt.Errorf("command execution failed with code %d: %s", exitCode, string(out))
	}
	err := json.Unmarshal(out, &ankaJson)
	if err != nil {
		return out, fmt.Errorf("command execution failed: %v", err)
	}
	return out, nil
}

func (cli *Cli) AnkaRun(ctx context.Context, args ...string) error {
	vm := GetAnkaVmFromContext(ctx)
	runOutput, exitCode, err := cli.Execute(ctx, "anka", "-j", "run", vm.Name, "bash", "-c", strings.Join(args, " "))
	if exitCode != 0 || err != nil {
		return fmt.Errorf("command execution failed with code %d: %s %s", exitCode, string(runOutput), err)
	}
	logger := logging.GetLoggerFromContext(ctx)
	logger.DebugContext(ctx, "command executed successfully", "stdout", string(runOutput))
	return nil
}

func (cli *Cli) AnkaRegistryPull(ctx context.Context, template string, tag string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaRegistryPull")
	}
	logger := logging.GetLoggerFromContext(ctx)
	service := config.GetServiceFromContext(ctx)
	var registryExtra []string
	if service.RegistryURL != "" {
		registryExtra = []string{"--remote", service.RegistryURL}
	}
	var args []string
	if tag != "(using latest)" {
		args = append([]string{"anka", "-j", "registry"}, registryExtra...)
		args = append(args, "pull", "--shrink", template, "--tag", tag)
	} else {
		args = append([]string{"anka", "-j", "registry"}, registryExtra...)
		args = append(args, "pull", "--shrink", template)
	}
	logger.DebugContext(ctx, "pulling template to host")
	pulledTemplate, err := cli.ExecuteParseJson(ctx, args...)
	if err != nil {
		return err
	}
	if pulledTemplate.Status != "OK" {
		return fmt.Errorf("error pulling template from registry: %s", pulledTemplate.Message)
	}
	logger.DebugContext(ctx, "successfully pulled template from registry")
	return nil
}

func (cli *Cli) AnkaDelete(ctx context.Context, vm *VM) error {
	logger := logging.GetLoggerFromContext(ctx)
	deleteOutput, err := cli.ExecuteParseJson(ctx, "anka", "-j", "delete", "--yes", vm.Name)
	if err != nil {
		logger.ErrorContext(ctx, "error executing anka delete", "err", err)
		return err
	}
	logger.DebugContext(ctx, "successfully deleted vm", "std", deleteOutput.Message)
	return nil
}

func (cli *Cli) ObtainAnkaVM(ctx context.Context, ankaTemplate string) (context.Context, *VM, error) {
	if ctx.Err() != nil {
		return ctx, nil, fmt.Errorf("context canceled before ObtainAnkaVMAndName")
	}
	logger := logging.GetLoggerFromContext(ctx)
	vmID, err := uuid.NewRandom()
	if err != nil {
		logger.ErrorContext(ctx, "error creating uuid for vm name", "err", err)
		return ctx, nil, err
	}
	vmName := fmt.Sprintf("anklet-vm-%s", vmID.String())
	ctx = logging.AppendCtx(ctx, slog.String("vmName", vmName))
	vm := &VM{Name: vmName}
	ctx = context.WithValue(ctx, config.ContextKey("ankavm"), vm)
	err = cli.AnkaClone(ctx, ankaTemplate)
	if err != nil {
		logger.ErrorContext(ctx, "error executing anka clone", "err", err)
		return ctx, vm, err
	}
	// Start
	err = cli.AnkaStart(ctx)
	if err != nil {
		logger.ErrorContext(ctx, "error executing anka start", "err", err)
		return ctx, vm, err
	}
	return ctx, vm, nil
}

func (cli *Cli) AnkaClone(ctx context.Context, template string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaClone")
	}
	logger := logging.GetLoggerFromContext(ctx)
	vm := GetAnkaVmFromContext(ctx)
	cloneOutput, err := cli.ExecuteParseJson(ctx, "anka", "-j", "clone", template, vm.Name)
	if err != nil {
		return err
	}
	if cloneOutput.Status != "OK" {
		return fmt.Errorf("error cloning template: %s", cloneOutput.Message)
	}
	logger.InfoContext(ctx, "successfully cloned template to new vm")
	return nil
}

func (cli *Cli) AnkaStart(ctx context.Context) error {
	if ctx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaStart")
	}
	logger := logging.GetLoggerFromContext(ctx)
	vm := GetAnkaVmFromContext(ctx)
	startOutput, err := cli.ExecuteParseJson(ctx, "anka", "-j", "start", vm.Name)
	if err != nil {
		return err
	}
	if startOutput.Status != "OK" {
		return fmt.Errorf("error starting vm: %s", startOutput.Message)
	}
	logger.InfoContext(ctx, "successfully started vm")
	return nil
}

func (cli *Cli) AnkaCopy(ctx context.Context, filesToCopyIn ...string) error {
	if ctx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaCopy")
	}
	logger := logging.GetLoggerFromContext(ctx)
	vm := GetAnkaVmFromContext(ctx)
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
		logger.DebugContext(ctx, "successfully copied file into vm", "file", hostLevelFile)
	}

	return nil
}

func HostHasVmCapacity(ctx context.Context) bool {
	logger := logging.GetLoggerFromContext(ctx)
	ankaCLI := GetAnkaCLIFromContext(ctx)
	// check if there are already two VMS running or not
	runningVMsList, err := ankaCLI.ExecuteParseJson(ctx, "anka", "-j", "list", "-r")
	if err != nil {
		logger.ErrorContext(ctx, "error executing anka list -r", "err", err)
		return false
	}
	if runningVMsList.Status != "OK" {
		logger.ErrorContext(ctx, "error listing running VMs", "status", runningVMsList.Status, "message", runningVMsList.Message)
		return false
	}
	runningVMsCount := 0
	if bodySlice, ok := runningVMsList.Body.([]interface{}); ok {
		runningVMsCount = len(bodySlice)
	} else {
		logger.ErrorContext(ctx, "unable to parse running VMs list body to []interface{}")
		return false
	}
	if runningVMsCount >= 2 {
		logger.WarnContext(ctx, "more than 2 VMs are running; unable to run more than 2 at a time due to Apple SLA")
		return false
	}
	return true
}

func (cli *Cli) EnsureVMTemplateExists(ctx context.Context, targetTemplate string, targetTag string) error {
	logger := logging.GetLoggerFromContext(ctx)
	ankaCLI := GetAnkaCLIFromContext(ctx)
	globals := config.GetGlobalsFromContext(ctx)
	pullTemplate := false
	list, err := ankaCLI.ExecuteParseJson(ctx, "anka", "-j", "list", targetTemplate)
	if err != nil {
		list, innerErr := ankaCLI.ExecuteParseJson(ctx, "anka", "-j", "list")
		if innerErr != nil {
			logger.ErrorContext(ctx, "error executing anka list", "err", innerErr)
			return innerErr
		}
		logger.DebugContext(ctx, "list", "stdout", list.Body)
	}
	logger.DebugContext(ctx, "list output", "json", list)
	if list != nil {
		if list.Status == "ERROR" {
			if list.Message == fmt.Sprintf("%s: not found", targetTemplate) {
				pullTemplate = true
			} else {
				logger.ErrorContext(ctx, "error executing anka list", "err", list.Message)
				return err
			}
		}
		if list.Status == "OK" {
			// ensure tag is proper; skip if tag is hard coded and we already have it locally
			if bodySlice, ok := list.Body.([]interface{}); ok {
				body, ok := bodySlice[0].(map[string]interface{})
				if !ok {
					logger.InfoContext(ctx, "list", "body", list.Body)
					logger.ErrorContext(ctx, "unable to parse bodySlice[0] to map[string]interface{}")
					return fmt.Errorf("unable to parse bodySlice[0] to map[string]interface{}")
				}
				if status, ok := body["status"].(string); ok {
					if status == "failed" {
						return fmt.Errorf("vm template is not running and instead %s", status)
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
				return fmt.Errorf("unable to parse list.Body to []interface{}")
			}
		}
	} else {
		pullTemplate = true
	}
	if pullTemplate {
		if !globals.PullLock.TryLock() {
			return fmt.Errorf("a pull is already running on this host")
		}
		defer globals.PullLock.Unlock()
		err := cli.AnkaRegistryPull(ctx, targetTemplate, targetTag)
		if err != nil {
			logger.ErrorContext(ctx, "error executing anka registry pull", "err", err)
			return err
		}
	}
	return nil
}
