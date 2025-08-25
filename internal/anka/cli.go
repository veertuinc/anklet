package anka

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
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
	CPU       int    `json:"cpu_cores"`
	MEMBytes  uint64 `json:"ram_size"`
	Tag       string `json:"tag"`
	ImageSize uint64 `json:"image_size"` // Template actual disk usage
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
	if body, ok := version.Body.(map[string]any); ok {
		cli.Version = body["version"].(string)
	}

	license, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "license", "show")
	if err != nil || license.Status != "OK" {
		return nil, err
	}

	if body, ok := license.Body.(map[string]any); ok {
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
	if args[2] != "list" { // hide spammy list command
		logging.Debug(pluginCtx, "executing", "command", strings.Join(args, " "))
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
			logging.Info(pluginCtx, fmt.Sprintf("execution of command %v is still in progress...", args))
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

func (cli *Cli) ExecuteAndParseJsonOnError(
	workerCtx context.Context,
	pluginCtx context.Context,
	args ...string,
) ([]byte, error) {
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
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
	logging.Debug(pluginCtx, "command executed successfully", "stdout", string(runOutput))
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

	output := &AnkaShowOutput{}
	// Get CPU information from anka show output
	if cpu, ok := ankaJson.Body.(map[string]any)["cpu_cores"]; ok {
		output.CPU = int(cpu.(float64))
	}
	// Get memory information from anka show output
	if mem, ok := ankaJson.Body.(map[string]any)["ram_size"]; ok {
		output.MEMBytes = uint64(mem.(float64))
	}
	// Get image size information from anka show output - image_size is actual usage, not logical disk_size
	if imageSize, ok := ankaJson.Body.(map[string]any)["image_size"].(float64); ok {
		output.ImageSize = uint64(imageSize)
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
	workerCtx context.Context,
	pluginCtx context.Context,
	template string,
	tag string,
) (*AnkaShowOutput, error) {
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
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

	output := &AnkaShowOutput{
		CPU:      int(showJson.Body.(map[string]any)["cpu_cores"].(float64)),
		MEMBytes: uint64(showJson.Body.(map[string]any)["ram_size"].(float64)),
	}

	// Try to get size information if available - prioritize image_size (actual usage)
	if imageSize, ok := showJson.Body.(map[string]any)["image_size"].(float64); ok {
		output.ImageSize = uint64(imageSize)
	}

	return output, nil
}

func (cli *Cli) AnkaRegistryPull(
	workerCtx context.Context,
	pluginCtx context.Context,
	template string,
	tag string,
) (*AnkaJson, error, error) { // pullJson, ensureSpaceError, genericError
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		return nil, nil, fmt.Errorf("context canceled before AnkaRegistryPull")
	}

	logging.Debug(pluginCtx, "pulling template to host")

	metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
	if err != nil {
		return nil, nil, err
	}

	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return nil, nil, err
	}

	// Get template size from registry before pulling
	templateSizeBytes, err := cli.getRegistryTemplateSize(pluginCtx, template, tag)
	if err != nil {
		logging.Warn(pluginCtx, "unable to get template size from registry, proceeding without space check", "error", err)
		templateSizeBytes = 0 // Continue without size check
	}

	// Ensure we have enough space for the template
	if templateSizeBytes > 0 {
		err = cli.EnsureSpaceForTemplateOnDarwin(workerCtx, pluginCtx, template, tag, templateSizeBytes)
		if err != nil {
			logging.Error(pluginCtx, "unable to ensure space for template", "error", err)
			return nil, err, nil // ensureSpaceError
		}
	}

	defer func() {
		// Clear pulling status
		workerGlobals.TemplateTracker.SetTemplatePulling(template, tag, false)
		err := metricsData.SetStatus(pluginCtx, "running")
		if err != nil {
			logging.Error(pluginCtx, "error setting metrics status", "error", err)
		}
	}()

	err = metricsData.SetStatus(pluginCtx, "pulling")
	if err != nil {
		logging.Error(pluginCtx, "error setting metrics status", "error", err)
		return nil, nil, err
	}

	// Mark template as being pulled
	workerGlobals.TemplateTracker.SetTemplatePulling(template, tag, true)

	var args []string
	if tag != "(using latest)" {
		args = append(args, "pull", "--shrink", template, "--tag", tag)
	} else {
		args = append(args, "pull", "--shrink", template)
	}
	pullJson, err := cli.AnkaExecuteRegistryCommand(pluginCtx, args...)
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		return nil, nil, fmt.Errorf("context canceled while pulling template")
	}
	if err != nil {
		return nil, nil, err
	}
	if pullJson.Status != "OK" {
		return nil, nil, fmt.Errorf("error pulling template from registry: %s", pullJson.Message)
	}

	// Get actual template size after pull if we didn't have it before
	if templateSizeBytes == 0 {
		templateSizeBytes, err = cli.AnkaGetTemplateSize(pluginCtx, template, tag)
		if err != nil {
			logging.Warn(pluginCtx, "unable to get template size after pull", "error", err)
			templateSizeBytes = 0 // Continue without size tracking
		}
	}

	// Update template usage tracking
	workerGlobals.TemplateTracker.UpdateTemplateUsage(template, tag, templateSizeBytes)

	logging.Info(pluginCtx, "successfully pulled template", "template", template, "tag", tag, "sizeBytes", templateSizeBytes)

	return pullJson, nil, nil
}

// AnkaGetTemplateSize gets the disk size of a template from the host
func (cli *Cli) AnkaGetTemplateSize(pluginCtx context.Context, template, tag string) (uint64, error) {
	// First try to get it from anka list which includes size information
	list, err := cli.AnkaList(pluginCtx, template)
	if err != nil {
		return 0, err
	}

	if list.Status == "OK" && list.Body != nil {
		if bodySlice, ok := list.Body.([]any); ok && len(bodySlice) > 0 {
			if body, ok := bodySlice[0].(map[string]any); ok {
				// Check if the tag matches what we're looking for
				if version, ok := body["version"].(string); ok {
					if tag == "(using latest)" || version == tag {
						// Try to get size from the body - prioritize image_size (actual usage)
						if imageSize, ok := body["image_size"].(float64); ok {
							return uint64(imageSize), nil
						}
					}
				}
			}
		}
	}

	// Fallback: use filesystem to get template size
	// This is less accurate but better than nothing
	return cli.getTemplateSizeFromFilesystem(pluginCtx, template, tag)
}

// getTemplateSizeFromFilesystem estimates template size using filesystem commands
func (cli *Cli) getTemplateSizeFromFilesystem(pluginCtx context.Context, template, tag string) (uint64, error) {
	// Use du command to get directory size of the template
	templatePath := fmt.Sprintf("~/.anka/vms/%s", template)
	if tag != "(using latest)" {
		templatePath = fmt.Sprintf("~/.anka/vms/%s/%s", template, tag)
	}

	out, _, err := cli.Execute(pluginCtx, "du", "-sb", templatePath)
	if err != nil {
		logging.Debug(pluginCtx, "failed to get template size from filesystem", "template", template, "tag", tag, "error", err)
		return 0, err
	}

	// Parse the output (format: "size_bytes	path")
	parts := strings.Fields(string(out))
	if len(parts) > 0 {
		if sizeBytes, err := strconv.ParseUint(parts[0], 10, 64); err == nil {
			return sizeBytes, nil
		}
	}

	return 0, fmt.Errorf("unable to determine template size")
}

// AnkaDeleteTemplate deletes a template from the host
func (cli *Cli) AnkaDeleteTemplate(pluginCtx context.Context, template, tag string) error {
	logging.Info(pluginCtx, "deleting template from host", "template", template, "tag", tag)
	var args []string
	if tag != "(using latest)" {
		args = []string{"anka", "-j", "delete", "--yes", template, "--tag", tag}
	} else {
		args = []string{"anka", "-j", "delete", "--yes", template}
	}

	deleteOutput, err := cli.ExecuteParseJson(pluginCtx, args...)
	if err != nil {
		logging.Error(pluginCtx, "error executing anka delete template", "err", err)
		return err
	}

	if deleteOutput.Status != "OK" {
		return fmt.Errorf("error deleting template: %s", deleteOutput.Message)
	}

	// Remove from template tracker
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		logging.Warn(pluginCtx, "unable to get worker globals to update template tracker", "error", err)
	} else {
		workerGlobals.TemplateTracker.RemoveTemplate(template, tag)
	}

	logging.Info(pluginCtx, "successfully deleted template", "template", template, "tag", tag)
	return nil
}

// getRegistryTemplateSize gets the size of a template from the registry
func (cli *Cli) getRegistryTemplateSize(pluginCtx context.Context, template, tag string) (uint64, error) {
	// Try to get size information from registry show command
	showOutput, err := cli.AnkaRegistryShowTemplate(context.Background(), pluginCtx, template, tag)
	if err != nil {
		return 0, err
	}

	// If the registry show command includes size information, use it
	if showOutput.ImageSize > 0 {
		return showOutput.ImageSize, nil
	}

	// Fallback: estimate based on typical template sizes
	// This is a rough estimate - actual sizes vary significantly
	return 10 * 1024 * 1024 * 1024, nil // 10GB default estimate
}

func (cli *Cli) AnkaDelete(workerCtx context.Context, pluginCtx context.Context, vmName string) error {
	deleteOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "delete", "--yes", vmName)
	if err != nil {
		logging.Error(pluginCtx, "error executing anka delete", "err", err)
		_, _, err = cli.Execute(pluginCtx, "anka", "delete", "--yes", vmName)
		if err != nil {
			logging.Error(pluginCtx, "error executing anka delete", "err", err)
		}
		return err
	}
	logging.Debug(pluginCtx, "successfully deleted vm", "std", deleteOutput.Message)
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
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		return nil, fmt.Errorf("context canceled before ObtainAnkaVMAndName")
	}
	vmID, err := uuid.NewRandom()
	if err != nil {
		logging.Error(pluginCtx, "error creating uuid for vm name", "err", err)
		return nil, err
	}
	vmName := fmt.Sprintf("anklet-vm-%s", vmID.String())
	vm := &VM{Name: vmName}
	err = cli.AnkaClone(workerCtx, pluginCtx, vmName, ankaTemplate)
	if err != nil {
		logging.Error(pluginCtx, "error executing anka clone", "err", err)
		return vm, err
	}
	// Start
	err = cli.AnkaStart(workerCtx, pluginCtx, vmName)
	if err != nil {
		logging.Debug(pluginCtx, "vm", "vm", vm)
		logging.Error(pluginCtx, "error executing anka start", "err", err)
		return vm, err
	}
	// increment total running VMs
	metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
	if err != nil {
		return vm, err
	}
	metricsData.IncrementTotalRunningVMs(workerCtx, pluginCtx)
	return vm, nil
}

func (cli *Cli) AnkaClone(
	workerCtx context.Context,
	pluginCtx context.Context,
	vmName string,
	template string,
) error {
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaClone")
	}

	cloneOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "clone", template, vmName)
	if err != nil {
		return err
	}
	if cloneOutput.Status != "OK" {
		return fmt.Errorf("error cloning template: %s", cloneOutput.Message)
	}
	logging.Info(pluginCtx, "successfully cloned template to new vm")
	return nil
}

func (cli *Cli) AnkaStart(
	workerCtx context.Context,
	pluginCtx context.Context,
	vmName string,
) error {
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaStart")
	}
	startOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "start", vmName)
	if err != nil {
		return err
	}
	if startOutput.Status != "OK" {
		return fmt.Errorf("error starting vm: %s", startOutput.Message)
	}
	logging.Info(pluginCtx, "successfully started vm")
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
	copyOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "cp", "-a", fmt.Sprintf("%s:%s", vmName, objectToCopyOut), hostLevelDestination)
	if err != nil {
		return err
	}
	if copyOutput.Status != "OK" {
		return fmt.Errorf("error copying out of vm: %s", copyOutput.Message)
	}
	logging.Debug(pluginCtx, "copy output", "std", copyOutput)
	logging.Info(pluginCtx, fmt.Sprintf("successfully copied %s out of vm to %s", objectToCopyOut, hostLevelDestination), "stdout", copyOutput.Message)

	return nil
}

func (cli *Cli) AnkaCopyIntoVM(
	workerCtx context.Context,
	pluginCtx context.Context,
	vmName string,
	filesToCopyIn ...string,
) error {
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaCopyIntoVM")
	}
	for _, hostLevelFile := range filesToCopyIn {
		// handle symlinks
		realPath, err := filepath.EvalSymlinks(hostLevelFile)
		if err != nil {
			return fmt.Errorf("error evaluating symlink for %s: %w", hostLevelFile, err)
		}
		hostLevelFile = realPath
		if workerCtx.Err() != nil || pluginCtx.Err() != nil {
			return fmt.Errorf("context canceled before AnkaCopyIntoVM executing anka cp")
		}
		copyOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "cp", "-a", hostLevelFile, fmt.Sprintf("%s:", vmName))
		if err != nil {
			return err
		}
		if copyOutput.Status != "OK" {
			return fmt.Errorf("error copying into vm: %s", copyOutput.Message)
		}
		logging.Debug(pluginCtx, "copy output", "std", copyOutput)
		logging.Info(pluginCtx, "successfully copied file into vm", "file", hostLevelFile, "stdout", copyOutput.Message)
	}

	return nil
}

func HostHasVmCapacity(pluginCtx context.Context) bool {
	ankaCLI, err := GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		return false
	}
	// check if there are already two VMS running or not
	runningVMsList, err := ankaCLI.ExecuteParseJson(pluginCtx, "anka", "-j", "list", "-r")
	if err != nil {
		logging.Error(pluginCtx, "error executing anka list -r", "err", err)
		return false
	}
	if runningVMsList.Status != "OK" {
		logging.Error(pluginCtx, "error listing running VMs", "status", runningVMsList.Status, "message", runningVMsList.Message)
		return false
	}
	runningVMsCount := 0
	if bodySlice, ok := runningVMsList.Body.([]any); ok {
		runningVMsCount = len(bodySlice)
	} else {
		logging.Error(pluginCtx, "unable to parse running VMs list body to []any")
		return false
	}
	if runningVMsCount >= 2 {
		logging.Warn(pluginCtx, "more than 2 VMs are running; unable to run more than 2 at a time due to Apple SLA")
		return false
	}
	return true
}

func (cli *Cli) EnsureVMTemplateExists(
	workerCtx context.Context,
	pluginCtx context.Context,
	targetTemplate string,
	targetTag string,
) (error, error, error) { // noTemplateTagExistsInRegistryError, ensureSpaceError, genericError
	ankaCLI, err := GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		return nil, nil, err
	}
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return nil, nil, err
	}
	pullTemplate := false
	list, err := ankaCLI.AnkaList(pluginCtx, targetTemplate)
	if err != nil {
		list, innerErr := ankaCLI.AnkaList(pluginCtx)
		if innerErr != nil {
			logging.Error(pluginCtx, "error executing anka list", "err", innerErr)
			return nil, nil, innerErr
		}
		logging.Debug(pluginCtx, "list", "stdout", list.Body)
	}
	logging.Info(pluginCtx, "ensuring vm template exists on host", "targetTemplate", targetTemplate, "targetTag", targetTag)
	logging.Debug(pluginCtx, "list output", "json", list)
	if list != nil {
		if list.Status == "ERROR" {
			if list.Message == fmt.Sprintf("%s: not found", targetTemplate) {
				pullTemplate = true
			} else {
				logging.Error(pluginCtx, "error executing anka list", "err", list.Message)
				return nil, nil, fmt.Errorf("error executing anka list: %s", list.Message)
			}
		}
		if list.Status == "OK" {
			// ensure tag is proper; skip if tag is hard coded and we already have it locally
			if bodySlice, ok := list.Body.([]any); ok {
				body, ok := bodySlice[0].(map[string]any)
				if !ok {
					logging.Info(pluginCtx, "list", "body", list.Body)
					logging.Error(pluginCtx, "unable to parse bodySlice[0] to map[string]any")
					return nil, nil, fmt.Errorf("unable to parse bodySlice[0] to map[string]any")
				}
				if status, ok := body["status"].(string); ok {
					if status == "failed" {
						return nil, nil, fmt.Errorf("vm template is not running and instead %s", status)
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
				return nil, nil, fmt.Errorf("unable to parse list.Body to []any")
			}
		}
	} else {
		pullTemplate = true
	}
	if pullTemplate {
		if !workerGlobals.PullLock.TryLock() {
			return nil, nil, fmt.Errorf("a pull is already running on this host")
		}
		defer workerGlobals.PullLock.Unlock()
		pullJson, ensureSpaceError, err := cli.AnkaRegistryPull(workerCtx, pluginCtx, targetTemplate, targetTag)
		if workerCtx.Err() != nil || pluginCtx.Err() != nil {
			logging.Error(pluginCtx, "context canceled while pulling template")
			return nil, nil, nil
		}
		if ensureSpaceError != nil {
			return nil, ensureSpaceError, nil
		}
		if pullJson == nil || pullJson.Code == 3 { // registry doesn't have template (or tag)
			return err, nil, nil // noTemplateTagExistsInRegistryError
		}
		if err != nil {
			logging.Error(pluginCtx, "error executing anka registry pull", "err", err)
			return nil, nil, err
		}
	} else {
		// Template already exists locally, update usage tracking
		templateSizeBytes, err := cli.AnkaGetTemplateSize(pluginCtx, targetTemplate, targetTag)
		if err != nil {
			logging.Warn(pluginCtx, "unable to get template size for existing template", "error", err)
			templateSizeBytes = 0
		}

		// Update template usage since we're using an existing template
		workerGlobals.TemplateTracker.UpdateTemplateUsage(targetTemplate, targetTag, templateSizeBytes)

		logging.Debug(pluginCtx, "using existing template", "template", targetTemplate, "tag", targetTag, "sizeBytes", templateSizeBytes)
	}
	return nil, nil, nil
}
