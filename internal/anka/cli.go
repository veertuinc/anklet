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
	Name       string    `json:"name"`
	CPU        int       `json:"cpu_cores"`
	MEMBytes   uint64    `json:"ram_size"`
	Tag        string    `json:"tag"`
	ImageSize  uint64    `json:"image_size"`  // Template actual disk usage
	AccessDate time.Time `json:"access_date"` // Last access date from anka show
}

type AnkaPullCheckOutput struct {
	Size      uint64 `json:"size"`
	Cached    uint64 `json:"cached"`
	Available uint64 `json:"available"`
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

	// Find the last non-empty line to avoid parsing empty lines as JSON
	var lastLine []byte
	for i := len(lines) - 1; i >= 0; i-- {
		if len(bytes.TrimSpace(lines[i])) > 0 {
			lastLine = lines[i]
			break
		}
	}

	// If no non-empty line found, return an error
	if len(lastLine) == 0 {
		return nil, fmt.Errorf("no valid JSON output found in command response")
	}

	// Debug logging to help troubleshoot JSON parsing issues
	logging.Debug(pluginCtx, "parsing JSON from command output",
		"lastLine", string(lastLine),
		"allOutput", string(out))

	ankaJson, err := cli.ParseAnkaJson(pluginCtx, lastLine)
	if err != nil {
		logging.Error(pluginCtx, "failed to parse JSON",
			"error", err,
			"lastLine", string(lastLine),
			"allLines", len(lines))
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
	// Get access date information from anka show output
	if accessDate, ok := ankaJson.Body.(map[string]any)["access_date"].(string); ok {
		if parsedDate, err := time.Parse(time.RFC3339, accessDate); err == nil {
			output.AccessDate = parsedDate
		}
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

func (cli *Cli) AnkaExecutePullCommand(pluginCtx context.Context, args ...string) (*AnkaJson, error) {
	ctxPlugin, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	var registryExtra []string
	if ctxPlugin.RegistryURL != "" {
		registryExtra = []string{ctxPlugin.RegistryURL}
	}
	cmdArgs := append([]string{"anka", "-j", "pull"}, args...)
	cmdArgs = append(cmdArgs, registryExtra...)
	return cli.ExecuteParseJson(pluginCtx, cmdArgs...)
}

// AnkaGetTemplateSize gets the image size of a template from the host
func (cli *Cli) AnkaGetTemplateSize(pluginCtx context.Context, template, tag string) (uint64, error) {
	// First try to get it from anka list which includes size information
	show, err := cli.AnkaShow(pluginCtx, template)
	if err != nil {
		return 0, err
	}

	if show.ImageSize > 0 {
		return show.ImageSize, nil
	}
	return 0, fmt.Errorf("unable to determine template size")
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

	output := &AnkaShowOutput{}

	if name, ok := showJson.Body.(map[string]any)["name"].(string); ok {
		output.Name = name
	}

	if cpu, ok := showJson.Body.(map[string]any)["cpu_cores"].(float64); ok {
		output.CPU = int(cpu)
	}
	if mem, ok := showJson.Body.(map[string]any)["ram_size"].(float64); ok {
		output.MEMBytes = uint64(mem)
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
	templateUUID string,
	templateName string,
	templateTag string,
) (*AnkaJson, error) {
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		return nil, fmt.Errorf("context canceled before AnkaRegistryPull")
	}

	logging.Debug(pluginCtx, "pulling template to host")

	metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
	if err != nil {
		return nil, err
	}

	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}

	defer func() {
		// Clear pulling status
		workerGlobals.TemplateTracker.SetTemplatePulling(templateUUID, templateName, templateTag, false, time.Now())
		err := metricsData.SetStatus(pluginCtx, "running")
		if err != nil {
			logging.Error(pluginCtx, "error setting metrics status", "error", err)
		}
	}()

	err = metricsData.SetStatus(pluginCtx, "pulling")
	if err != nil {
		logging.Error(pluginCtx, "error setting metrics status", "error", err)
		return nil, err
	}

	// Mark template as being pulled
	workerGlobals.TemplateTracker.SetTemplatePulling(templateUUID, templateName, templateTag, true, time.Now())

	var args []string
	if templateTag != "(using latest)" {
		args = append(args, "pull", "--shrink", templateUUID, "--tag", templateTag)
	} else {
		args = append(args, "pull", "--shrink", templateUUID)
	}
	pullJson, err := cli.AnkaExecuteRegistryCommand(pluginCtx, args...)
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		return nil, fmt.Errorf("context canceled while pulling template")
	}
	if err != nil {
		return nil, err
	}
	if pullJson.Status != "OK" {
		return nil, fmt.Errorf("error pulling template from registry: %s", pullJson.Message)
	}

	templateSizeBytes, err := cli.AnkaGetTemplateSize(pluginCtx, templateUUID, templateTag)
	if err != nil {
		logging.Warn(pluginCtx, "unable to get template size after pull", "error", err)
		return nil, err
	}

	// Update template usage tracking - use current time since we just pulled it
	workerGlobals.TemplateTracker.UpdateTemplateUsage(templateUUID, templateName, templateTag, templateSizeBytes, time.Now())

	logging.Debug(pluginCtx, "TemplateTracker state", "tracker", workerGlobals.TemplateTracker)

	logging.Info(pluginCtx, "successfully pulled template", "templateUUID", templateUUID, "templateName", templateName, "templateTag", templateTag, "sizeBytes", templateSizeBytes)

	return pullJson, nil
}

// AnkaDeleteTemplate deletes a template from the host
func (cli *Cli) AnkaDeleteTemplate(pluginCtx context.Context, templateUUID string) error {
	// we don't use tag here because the deletion of a tag keeps the template still around
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		logging.Warn(pluginCtx, "unable to get worker globals to update template tracker", "error", err)
		return err
	}
	logging.Info(pluginCtx, "deleting template from host", "templateUUID", templateUUID)
	args := []string{"anka", "-j", "delete", "--yes", templateUUID}

	deleteOutput, err := cli.ExecuteParseJson(pluginCtx, args...)
	if err != nil {
		logging.Error(pluginCtx, "error executing anka delete template", "err", err)
		return err
	}

	if deleteOutput.Status != "OK" {
		return fmt.Errorf("error deleting template: %s", deleteOutput.Message)
	}

	// Remove from template tracker
	workerGlobals.TemplateTracker.RemoveTemplate(templateUUID)

	logging.Info(pluginCtx, "successfully deleted template", "templateUUID", templateUUID)
	return nil
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

// DiscoverAndPopulateExistingTemplates discovers all existing templates on the system
// and populates the TemplateTracker with their information
func (cli *Cli) DiscoverAndPopulateExistingTemplates(
	ctx context.Context,
	templateTracker *config.TemplateTracker,
) error {
	logging.Info(ctx, "discovering existing templates on system")

	// Get list of all templates
	listOutput, err := cli.AnkaList(ctx)
	if err != nil {
		logging.Warn(ctx, "unable to list existing templates", "error", err)
		return err
	}

	if listOutput.Status != "OK" {
		logging.Warn(ctx, "anka list returned error status", "status", listOutput.Status, "message", listOutput.Message)
		return fmt.Errorf("anka list failed: %s", listOutput.Message)
	}

	// Parse the template list
	templateCount := 0
	if bodySlice, ok := listOutput.Body.([]any); ok {
		for _, templateData := range bodySlice {
			if templateMap, ok := templateData.(map[string]any); ok {
				templateUUID := ""
				if uuid, ok := templateMap["uuid"].(string); ok {
					templateUUID = uuid
				}

				templateName := ""
				if name, ok := templateMap["name"].(string); ok {
					templateName = name
				}

				templateTag := ""
				if version, ok := templateMap["version"].(string); ok {
					templateTag = version
				} else {
					templateTag = "latest" // Default tag if none specified
				}

				var accessDate time.Time
				ankaShowOutput, err := cli.AnkaShow(ctx, templateUUID)
				if err != nil {
					logging.Warn(ctx, "unable to get template access date during discovery", "error", err)
					accessDate = time.Now() // fallback to current time
				} else {
					accessDate = ankaShowOutput.AccessDate
				}

				if templateName != "" || templateUUID != "" {
					// Get template size
					templateSize, err := cli.AnkaGetTemplateSize(ctx, templateUUID, templateTag)
					if err != nil {
						logging.Warn(ctx, "unable to get template size during discovery",
							"templateUUID", templateUUID, "tag", templateTag, "error", err)
						templateSize = 0 // Continue with 0 size rather than failing
					}

					// Add to template tracker with initial usage data
					templateTracker.Mutex.Lock()
					key := templateTracker.GetTemplateKey(templateUUID)
					templateTracker.Templates[key] = &config.TemplateUsage{
						UUID:         templateUUID,
						Name:         templateName,
						Tag:          templateTag,
						ImageSize:    templateSize,
						LastAccessed: accessDate, // Mark as recently discovered
						UsageCount:   0,          // No usage yet, just discovered
						InUse:        false,
						Pulling:      false,
					}
					templateTracker.Mutex.Unlock()

					templateCount++
					logging.Debug(ctx, "discovered existing template",
						"templateUUID", templateUUID, "tag", templateTag, "size", templateSize)
				}
			}
		}
	}

	logging.Info(ctx, "template discovery complete", "templatesFound", templateCount, "templates", templateTracker.Templates)
	return nil
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
	targetTemplateUUID string,
	targetTemplateTag string,
) (error, error, error) { // noTemplateTagExistsInRegistryError, ensureSpaceError, genericError
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return nil, nil, err
	}

	pullTemplate := false
	targetTemplateName := ""

	// we need to do Anka List here because Anklet is allowed to work with non-Registry setups
	list, err := cli.AnkaList(pluginCtx, targetTemplateUUID)
	if err != nil {
		list, innerErr := cli.AnkaList(pluginCtx)
		if innerErr != nil {
			logging.Error(pluginCtx, "error executing anka list", "err", innerErr)
			return nil, nil, innerErr
		}
		logging.Debug(pluginCtx, "list", "stdout", list.Body)
	}
	logging.Info(pluginCtx, "ensuring vm template exists on host", "targetTemplateUUID", targetTemplateUUID, "targetTemplateName", targetTemplateName, "targetTemplateTag", targetTemplateTag)
	logging.Debug(pluginCtx, "list output", "json", list)
	if list != nil {
		if list.Status == "ERROR" {
			if list.Message == fmt.Sprintf("%s: not found", targetTemplateUUID) {
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
					if targetTemplateTag != "(using latest)" {
						if version != targetTemplateTag {
							pullTemplate = true
						}
					} else {
						// always pull to ensure latest, if (using latest)
						pullTemplate = true
					}
				}
				if templateName, ok := body["name"].(string); ok {
					targetTemplateName = templateName
				}
			} else {
				return nil, nil, fmt.Errorf("unable to parse list.Body to []any")
			}
		}
	} else {
		pullTemplate = true
		ankaRegistryShowTemplate, err := cli.AnkaRegistryShowTemplate(workerCtx, pluginCtx, targetTemplateUUID, targetTemplateTag)
		if err != nil {
			logging.Error(pluginCtx, "error executing anka registry show template", "err", err)
			return nil, nil, err
		}
		if ankaRegistryShowTemplate != nil {
			targetTemplateName = ankaRegistryShowTemplate.Name
		}
	}
	if pullTemplate {

		// Check if any templates are currently being pulled
		if workerGlobals.TemplateTracker.HasPullingTemplates() {
			return nil, nil, fmt.Errorf("a pull is already running on this host")
		}

		// ensure space for template
		ensureSpaceError, genericError := cli.EnsureSpaceForTemplate(workerCtx, pluginCtx, targetTemplateUUID, targetTemplateTag)
		if ensureSpaceError != nil {
			return nil, ensureSpaceError, nil
		}
		if genericError != nil {
			return nil, nil, genericError
		}

		pullJson, err := cli.AnkaRegistryPull(workerCtx, pluginCtx, targetTemplateUUID, targetTemplateName, targetTemplateTag)
		if workerCtx.Err() != nil || pluginCtx.Err() != nil {
			logging.Error(pluginCtx, "context canceled while pulling template")
			return nil, nil, nil
		}
		if pullJson == nil || pullJson.Code == 3 { // registry doesn't have template (or tag)
			return fmt.Errorf("no template tag exists in registry"), nil, nil
		}
		if err != nil {
			logging.Error(pluginCtx, "error executing anka registry pull", "err", err)
			return nil, nil, err
		}
	} else {
		// Template already exists locally, update usage tracking
		templateSizeBytes, err := cli.AnkaGetTemplateSize(pluginCtx, targetTemplateUUID, targetTemplateTag)
		if err != nil {
			logging.Warn(pluginCtx, "unable to get template size for existing template", "error", err)
			templateSizeBytes = 0
		}
		// Get the actual access date from anka show
		ankaShowOutput, err := cli.AnkaShow(pluginCtx, targetTemplateUUID)
		var accessDate time.Time
		if err != nil {
			logging.Warn(pluginCtx, "unable to get access date for existing template", "error", err)
			accessDate = time.Now() // fallback to current time
		} else {
			accessDate = ankaShowOutput.AccessDate
		}
		// Update template usage since we're using an existing template
		workerGlobals.TemplateTracker.UpdateTemplateUsage(
			targetTemplateUUID,
			targetTemplateName,
			targetTemplateTag,
			templateSizeBytes,
			accessDate,
		)
		logging.Debug(pluginCtx, "using existing template", "templateUUID", targetTemplateUUID, "tag", targetTemplateTag, "sizeBytes", templateSizeBytes, "accessDate", accessDate)
	}
	return nil, nil, nil
}
