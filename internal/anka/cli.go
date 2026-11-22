package anka

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

var (
	// ErrTemplateNotFoundInRegistry indicates the requested template or tag does not exist in the Anka registry.
	ErrTemplateNotFoundInRegistry = errors.New("no template tag exists in registry")
	// ErrInsufficientSpace indicates the host does not have enough disk space for the template even after LRU cleanup.
	ErrInsufficientSpace = errors.New("insufficient space on host")
	// ErrCommandTimeout indicates an Anka CLI command exceeded its configured timeout.
	ErrCommandTimeout = errors.New("anka command timed out")
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
	Version string
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
	if !ok || ankaCLI == nil {
		return nil, fmt.Errorf("GetAnkaCLIFromContext failed")
	}
	return ankaCLI, nil
}

func NewCLI(pluginCtx context.Context) (*Cli, error) {
	cli := &Cli{}

	cmd := exec.Command("anka")
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("anka command not found or not working properly: %w", err)
	}

	version, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "version")
	if err != nil {
		return nil, err
	}
	if version.Status != "OK" {
		return nil, fmt.Errorf("anka version returned status %q: %s", version.Status, version.Message)
	}
	if body, ok := version.Body.(map[string]any); ok {
		if v, ok := body["version"].(string); ok {
			cli.Version = v
		}
	}

	license, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "license", "show")
	if err != nil {
		return nil, err
	}
	if license.Status != "OK" {
		return nil, fmt.Errorf("anka license show returned status %q: %s", license.Status, license.Message)
	}

	if body, ok := license.Body.(map[string]any); ok {
		if status, ok := body["status"].(string); ok {
			cli.License.Status = status
		}
		if licenseType, ok := body["license_type"].(string); ok {
			cli.License.LicenseType = licenseType
		}
		if product, ok := body["product"].(string); ok {
			cli.License.Product = product
		}
	}

	if cli.License.Status != "valid" {
		return nil, fmt.Errorf("anka license is invalid %+v", license.Body)
	}

	if cli.License.LicenseType == "com.veertu.anka.run" || cli.License.LicenseType == "com.veertu.anka.develop" {
		return nil, fmt.Errorf("anka license type is not supported %+v", license.Body)
	}

	return cli, nil
}

// injectAnkaDebugFlag adds Anka --debug after -j/--machine-readable so JSON stays on stdout
// and Anka writes verbose output to stderr.
func injectAnkaDebugFlag(args []string) []string {
	if len(args) == 0 || args[0] != "anka" {
		return args
	}
	for _, arg := range args {
		if arg == "--debug" {
			return args
		}
	}
	out := make([]string, 0, len(args)+1)
	out = append(out, args[0])
	i := 1
	if i < len(args) && (args[i] == "-j" || args[i] == "--machine-readable") {
		out = append(out, args[i])
		i++
	}
	out = append(out, "--debug")
	out = append(out, args[i:]...)
	return out
}

func redactSensitiveArgs(args []string, secrets []string) []string {
	if len(secrets) == 0 {
		return args
	}
	out := make([]string, len(args))
	for i, arg := range args {
		redacted := arg
		for _, secret := range secrets {
			if secret != "" && strings.Contains(redacted, secret) {
				redacted = strings.ReplaceAll(redacted, secret, "[REDACTED]")
			}
		}
		out[i] = redacted
	}
	return out
}

func redactSensitiveString(input string, secrets []string) string {
	out := input
	for _, secret := range secrets {
		if secret != "" && strings.Contains(out, secret) {
			out = strings.ReplaceAll(out, secret, "[REDACTED]")
		}
	}
	return out
}

func shouldLogAnkaStderr(debugEnabled bool, failed bool, stderr []byte) bool {
	if len(bytes.TrimSpace(stderr)) == 0 {
		return false
	}
	return debugEnabled || failed
}

// lastAnkaProgressLine returns the latest non-empty stderr line. Anka --debug
// often rewrites progress with carriage returns on one physical line.
func lastAnkaProgressLine(stderr []byte) string {
	if len(stderr) == 0 {
		return ""
	}
	lines := bytes.Split(stderr, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if idx := bytes.LastIndexByte(line, '\r'); idx >= 0 {
			line = bytes.TrimSpace(line[idx+1:])
		}
		if len(line) > 0 {
			return string(line)
		}
	}
	return ""
}

// syncBuffer is a bytes.Buffer safe for concurrent Write (cmd) and Bytes (ticker).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Bytes() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]byte, s.buf.Len())
	copy(out, s.buf.Bytes())
	return out
}

func logAnkaCommandProgress(pluginCtx context.Context, safeArgs []string, stderr *syncBuffer, secrets []string) {
	progress := redactSensitiveString(lastAnkaProgressLine(stderr.Bytes()), secrets)
	if progress != "" {
		logging.Info(pluginCtx, "execution of command still in progress",
			"command", safeArgs,
			"progress", progress)
		return
	}
	logging.Info(pluginCtx, fmt.Sprintf("execution of command %v is still in progress...", safeArgs))
}

func logAnkaCommandStderr(pluginCtx context.Context, stderr []byte, failed bool, secrets []string) {
	if !shouldLogAnkaStderr(logging.IsDebugEnabled(), failed, stderr) {
		return
	}
	stderrText := redactSensitiveString(string(stderr), secrets)
	if failed {
		logging.Error(pluginCtx, "anka command stderr", "stderr", stderrText)
		return
	}
	logging.Debug(pluginCtx, "anka command stderr", "stderr", stderrText)
}

func finishAnkaExec(pluginCtx context.Context, stdout *bytes.Buffer, stderr *syncBuffer, err error, secrets []string) ([]byte, int, error) {
	exitCode := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	logAnkaCommandStderr(pluginCtx, stderr.Bytes(), err != nil, secrets)
	return stdout.Bytes(), exitCode, err
}

// executeCommand runs an Anka CLI command with optional timeout and log redaction.
// cmd.Start is called synchronously before waiting in a goroutine so cmd.Process is
// never accessed concurrently.
func (cli *Cli) executeCommand(
	pluginCtx context.Context,
	timeout time.Duration,
	secrets []string,
	args ...string,
) ([]byte, int, error, error) {
	if len(args) == 0 {
		return nil, 0, fmt.Errorf("empty command"), nil
	}
	isListCmd := len(args) > 2 && args[2] == "list"
	execArgs := injectAnkaDebugFlag(args)
	safeArgs := redactSensitiveArgs(execArgs, secrets)

	if !isListCmd {
		logging.Debug(pluginCtx, "executing", "command", strings.Join(safeArgs, " "))
	}

	var stdout bytes.Buffer
	var stderr syncBuffer

	cmd := exec.Command(execArgs[0], execArgs[1:]...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, 0, fmt.Errorf("starting command %v: %w", safeArgs, err), nil
	}

	var waitErr error
	done := make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	var timeoutCh <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		timeoutCh = timer.C
	}

	for {
		select {
		case <-pluginCtx.Done():
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-done
			logAnkaCommandStderr(pluginCtx, stderr.Bytes(), true, secrets)
			cancelErr := fmt.Errorf("context canceled during command execution: %w", pluginCtx.Err())
			if timeout > 0 {
				return nil, 0, nil, cancelErr
			}
			return nil, 0, cancelErr, nil
		case <-timeoutCh:
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-done
			logAnkaCommandStderr(pluginCtx, stderr.Bytes(), true, secrets)
			return nil, 0, nil, fmt.Errorf("%w: execution of command %v timed out", ErrCommandTimeout, safeArgs)
		case <-ticker.C:
			logAnkaCommandProgress(pluginCtx, safeArgs, &stderr, secrets)
		case <-done:
			out, exitCode, execErr := finishAnkaExec(pluginCtx, &stdout, &stderr, waitErr, secrets)
			return out, exitCode, execErr, nil
		}
	}
}

// Execute runs an Anka command until completion or pluginCtx cancellation.
// Critical cleanup operations should pass a non-canceled cleanup context.
func (cli *Cli) Execute(pluginCtx context.Context, args ...string) ([]byte, int, error) {
	out, exitCode, err, _ := cli.executeCommand(pluginCtx, 0, nil, args...)
	return out, exitCode, err
}

// ExecuteWithTimeout runs an Anka command with a timeout in seconds.
func (cli *Cli) ExecuteWithTimeout(
	pluginCtx context.Context,
	timeoutInSeconds int,
	args ...string,
) ([]byte, int, error, error) {
	return cli.executeCommand(pluginCtx, time.Duration(timeoutInSeconds)*time.Second, nil, args...)
}

func (cli *Cli) ParseAnkaJson(_ context.Context, jsonData []byte) (*AnkaJson, error) {
	ankaJson := &AnkaJson{}
	if err := json.Unmarshal(jsonData, &ankaJson); err != nil {
		return nil, err
	}
	return ankaJson, nil
}

func (cli *Cli) ExecuteParseJson(pluginCtx context.Context, args ...string) (*AnkaJson, error) {
	out, exitCode, execErr := cli.Execute(pluginCtx, args...)
	if pluginCtx.Err() != nil && execErr != nil {
		return nil, execErr
	}

	// registry pull can output multiple JSON objects per line; parse the last non-empty line.
	lines := bytes.Split(out, []byte("\n"))
	var lastLine []byte
	for i := len(lines) - 1; i >= 0; i-- {
		if len(bytes.TrimSpace(lines[i])) > 0 {
			lastLine = lines[i]
			break
		}
	}

	if len(lastLine) == 0 {
		if execErr != nil {
			return nil, execErr
		}
		return nil, fmt.Errorf("no valid JSON output found in command response")
	}

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
		if ankaJson.Message != "" {
			return ankaJson, fmt.Errorf("%s", ankaJson.Message)
		}
		if execErr != nil {
			return ankaJson, execErr
		}
		return ankaJson, fmt.Errorf("anka command failed with exit code %d", exitCode)
	}
	return ankaJson, nil
}

// AnkaRun executes a command inside the guest VM directly without bash -c shell evaluation.
func (cli *Cli) AnkaRun(pluginCtx context.Context, vmName string, args ...string) error {
	cmdArgs := make([]string, 0, 4+len(args))
	cmdArgs = append(cmdArgs, "anka", "-j", "run", vmName)
	cmdArgs = append(cmdArgs, args...)
	runOutput, exitCode, err := cli.Execute(pluginCtx, cmdArgs...)
	if exitCode != 0 || err != nil {
		return fmt.Errorf("command execution failed with code %d: %s %w", exitCode, string(runOutput), err)
	}
	logging.Debug(pluginCtx, "command executed successfully", "stdout", string(runOutput))
	return nil
}

// AnkaRunWithTimeout executes a command inside the guest VM directly without bash -c shell evaluation.
func (cli *Cli) AnkaRunWithTimeout(pluginCtx context.Context, timeoutInSeconds int, vmName string, args ...string) (error, error) {
	return cli.AnkaRunWithTimeoutRedacted(pluginCtx, timeoutInSeconds, vmName, nil, args...)
}

// AnkaRunWithTimeoutRedacted executes a command inside the guest VM directly without bash -c
// and redacts any matching secrets from debug logs, progress logs, stderr, and error messages.
func (cli *Cli) AnkaRunWithTimeoutRedacted(
	pluginCtx context.Context,
	timeoutInSeconds int,
	vmName string,
	secrets []string,
	args ...string,
) (error, error) {
	cmdArgs := make([]string, 0, 4+len(args))
	cmdArgs = append(cmdArgs, "anka", "-j", "run", vmName)
	cmdArgs = append(cmdArgs, args...)
	runOutput, exitCode, err, errTimeout := cli.executeCommand(
		pluginCtx,
		time.Duration(timeoutInSeconds)*time.Second,
		secrets,
		cmdArgs...,
	)
	safeOutput := redactSensitiveString(string(runOutput), secrets)
	if errTimeout != nil {
		return nil, fmt.Errorf("command execution timed out: %s %w", safeOutput, errTimeout)
	}
	if exitCode != 0 || err != nil {
		return fmt.Errorf("command execution failed with code %d: %s %w", exitCode, safeOutput, err), nil
	}
	logging.Debug(pluginCtx, "command executed successfully", "stdout", safeOutput)
	return nil, nil
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
	if bodyMap, ok := ankaJson.Body.(map[string]any); ok {
		if name, ok := bodyMap["name"].(string); ok {
			output.Name = name
		}
		if cpu, ok := bodyMap["cpu_cores"].(float64); ok {
			output.CPU = int(cpu)
		}
		if mem, ok := bodyMap["ram_size"].(float64); ok {
			output.MEMBytes = uint64(mem)
		}
		if imageSize, ok := bodyMap["image_size"].(float64); ok {
			output.ImageSize = uint64(imageSize)
		}
		if accessDate, ok := bodyMap["access_date"].(string); ok {
			if parsedDate, parseErr := time.Parse(time.RFC3339, accessDate); parseErr == nil {
				output.AccessDate = parsedDate
			}
		}
	}

	output.Tag = "(using latest)"
	if tagArr, isArray := ankaTagJson.Body.([]any); isArray && len(tagArr) > 0 {
		if tagMap, ok := tagArr[0].(map[string]any); ok && tagMap["tag"] != nil {
			output.Tag = fmt.Sprintf("%v", tagMap["tag"])
		}
	}
	return output, nil
}

func (cli *Cli) AnkaExecutePullCommand(pluginCtx context.Context, args ...string) (*AnkaJson, error) {
	ctxPlugin, err := config.GetPluginFromContext(pluginCtx)
	if err != nil {
		return nil, err
	}
	cmdArgs := append([]string{"anka", "-j", "pull"}, args...)
	if ctxPlugin.RegistryURL != "" {
		cmdArgs = append(cmdArgs, ctxPlugin.RegistryURL)
	}
	return cli.ExecuteParseJson(pluginCtx, cmdArgs...)
}

// AnkaGetTemplateSize gets the image size of a template from the host.
func (cli *Cli) AnkaGetTemplateSize(pluginCtx context.Context, template, _ string) (uint64, error) {
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
	cmdArgs := []string{"anka", "-j", "registry"}
	if ctxPlugin.RegistryURL != "" {
		cmdArgs = append(cmdArgs, "--remote", ctxPlugin.RegistryURL)
	}
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
	args := []string{"show", template}
	if tag != "(using latest)" {
		args = append(args, "--tag", tag)
	}
	showJson, err := cli.AnkaExecuteRegistryCommand(pluginCtx, args...)
	if err != nil {
		return nil, err
	}
	if showJson.Status != "OK" {
		return nil, fmt.Errorf("error showing template from registry: %s", showJson.Message)
	}

	output := &AnkaShowOutput{}
	if bodyMap, ok := showJson.Body.(map[string]any); ok {
		if name, ok := bodyMap["name"].(string); ok {
			output.Name = name
		}
		if cpu, ok := bodyMap["cpu_cores"].(float64); ok {
			output.CPU = int(cpu)
		}
		if mem, ok := bodyMap["ram_size"].(float64); ok {
			output.MEMBytes = uint64(mem)
		}
		if imageSize, ok := bodyMap["image_size"].(float64); ok {
			output.ImageSize = uint64(imageSize)
		}
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

	pullSucceeded := false
	defer func() {
		workerGlobals.TemplateTracker.SetTemplatePulling(templateUUID, templateName, templateTag, false, time.Now())
		if pullSucceeded && workerCtx.Err() == nil && pluginCtx.Err() == nil {
			if statusErr := metricsData.SetStatus(pluginCtx, "running"); statusErr != nil {
				logging.Error(pluginCtx, "error setting metrics status", "error", statusErr)
			}
		}
	}()

	if err = metricsData.SetStatus(pluginCtx, "pulling"); err != nil {
		logging.Error(pluginCtx, "error setting metrics status", "error", err)
		return nil, err
	}

	workerGlobals.TemplateTracker.SetTemplatePulling(templateUUID, templateName, templateTag, true, time.Now())

	args := []string{"pull", "--shrink", templateUUID}
	if templateTag != "(using latest)" {
		args = append(args, "--tag", templateTag)
	}
	pullJson, err := cli.AnkaExecuteRegistryCommand(pluginCtx, args...)
	if workerCtx.Err() != nil || pluginCtx.Err() != nil {
		return nil, fmt.Errorf("context canceled while pulling template")
	}
	if err != nil {
		return pullJson, err
	}
	if pullJson.Status != "OK" {
		return pullJson, fmt.Errorf("error pulling template from registry: %s", pullJson.Message)
	}

	templateSizeBytes, err := cli.AnkaGetTemplateSize(pluginCtx, templateUUID, templateTag)
	if err != nil {
		logging.Warn(pluginCtx, "unable to get template size after pull", "error", err)
		return nil, err
	}

	workerGlobals.TemplateTracker.UpdateTemplateUsage(templateUUID, templateName, templateTag, templateSizeBytes, time.Now())
	pullSucceeded = true

	logging.Debug(pluginCtx, "TemplateTracker state", "tracker", workerGlobals.TemplateTracker)
	logging.Info(pluginCtx, "successfully pulled template", "templateUUID", templateUUID, "templateName", templateName, "templateTag", templateTag, "sizeBytes", templateSizeBytes)

	return pullJson, nil
}

// AnkaDeleteTemplate deletes a template from the host.
func (cli *Cli) AnkaDeleteTemplate(pluginCtx context.Context, templateUUID string) error {
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		logging.Warn(pluginCtx, "unable to get worker globals to update template tracker", "error", err)
		return err
	}
	logging.Info(pluginCtx, "deleting template from host", "templateUUID", templateUUID)

	deleteOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "delete", "--yes", templateUUID)
	if err != nil {
		logging.Error(pluginCtx, "error executing anka delete template", "err", err)
		return err
	}
	if deleteOutput.Status != "OK" {
		return fmt.Errorf("error deleting template: %s", deleteOutput.Message)
	}

	workerGlobals.TemplateTracker.RemoveTemplate(templateUUID)
	logging.Info(pluginCtx, "successfully deleted template", "templateUUID", templateUUID)
	return nil
}

func (cli *Cli) AnkaDelete(workerCtx context.Context, pluginCtx context.Context, vmName string) error {
	if err := cli.AnkaDeleteVMOnly(pluginCtx, vmName); err != nil {
		return err
	}
	metricsData, err := metrics.GetMetricsDataFromContext(workerCtx)
	if err != nil {
		return err
	}
	metricsData.DecrementTotalRunningVMs()
	return nil
}

// AnkaDeleteVMOnly deletes a VM without adjusting running-VM metrics. Use when cleaning up a VM
// that was never counted (e.g. after start succeeded but host folder mounts failed).
func (cli *Cli) AnkaDeleteVMOnly(pluginCtx context.Context, vmName string) error {
	deleteOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "delete", "--yes", vmName)
	if err != nil {
		logging.Error(pluginCtx, "error executing anka delete", "err", err)
		_, _, fallbackErr := cli.Execute(pluginCtx, "anka", "delete", "--yes", vmName)
		if fallbackErr != nil {
			logging.Error(pluginCtx, "error executing anka delete", "err", fallbackErr)
		}
		return fallbackErr
	}
	if deleteOutput.Status != "OK" {
		return fmt.Errorf("error deleting vm: %s", deleteOutput.Message)
	}
	logging.Debug(pluginCtx, "successfully deleted vm", "std", deleteOutput.Message)
	return nil
}

func (cli *Cli) AnkaMount(pluginCtx context.Context, vmName string, mountSpec string) error {
	if pluginCtx.Err() != nil {
		return fmt.Errorf("context canceled before AnkaMount")
	}
	mountOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "mount", vmName, mountSpec)
	if err != nil {
		return err
	}
	if mountOutput.Status != "OK" {
		return fmt.Errorf("anka mount failed: %s", mountOutput.Message)
	}
	logging.Info(pluginCtx, "mounted host folder into guest vm", "vm", vmName, "mountSpec", mountSpec)
	return nil
}

// AnkaRegistryRunning pings the registry to see if it is reachable.
func (cli *Cli) AnkaRegistryRunning(pluginCtx context.Context) (bool, error) {
	listOutput, err := cli.AnkaExecuteRegistryCommand(pluginCtx, "list")
	if err != nil || listOutput == nil || listOutput.Status != "OK" {
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
		return nil, fmt.Errorf("context canceled before ObtainAnkaVM")
	}
	vmID, err := uuid.NewRandom()
	if err != nil {
		logging.Error(pluginCtx, "error creating uuid for vm name", "err", err)
		return nil, err
	}
	vmName := fmt.Sprintf("anklet-vm-%s", vmID.String())
	vm := &VM{Name: vmName}

	// Mark the source template in-use while cloning so concurrent LRU cleanup cannot evict it.
	if workerGlobals, gErr := config.GetWorkerGlobalsFromContext(pluginCtx); gErr == nil && workerGlobals.TemplateTracker != nil {
		workerGlobals.TemplateTracker.SetTemplateInUse(ankaTemplate, "", "", true)
		defer workerGlobals.TemplateTracker.SetTemplateInUse(ankaTemplate, "", "", false)
	}

	if err = cli.AnkaClone(workerCtx, pluginCtx, vmName, ankaTemplate); err != nil {
		logging.Error(pluginCtx, "error executing anka clone", "err", err)
		return nil, err
	}

	if err = cli.AnkaStart(workerCtx, pluginCtx, vmName); err != nil {
		logging.Debug(pluginCtx, "vm", "vm", vm)
		logging.Error(pluginCtx, "error executing anka start; deleting vm", "vm", vmName, "err", err)
		if delErr := cli.AnkaDeleteVMOnly(pluginCtx, vmName); delErr != nil {
			logging.Error(pluginCtx, "error deleting vm after start failure", "vm", vmName, "err", delErr)
			return vm, err
		}
		return nil, err
	}

	loadedConfig, cfgErr := config.GetLoadedConfigFromContext(pluginCtx)
	var globalHostToGuestFolderMounts []string
	if cfgErr == nil {
		globalHostToGuestFolderMounts = loadedConfig.GlobalHostToGuestFolderMounts
	}
	pluginInst, pluginErr := config.GetPluginFromContext(pluginCtx)
	var pluginHostToGuestFolderMounts []string
	if pluginErr == nil {
		pluginHostToGuestFolderMounts = pluginInst.HostToGuestFolderMounts
	}

	mergedMounts, mergeErr := MergeHostToGuestFolderMountLists(globalHostToGuestFolderMounts, pluginHostToGuestFolderMounts)
	if mergeErr != nil {
		logging.Error(pluginCtx, "host-to-guest folder mount configuration invalid; deleting vm", "vm", vmName, "err", mergeErr)
		if delErr := cli.AnkaDeleteVMOnly(pluginCtx, vmName); delErr != nil {
			logging.Error(pluginCtx, "error deleting vm after mount merge failure", "vm", vmName, "err", delErr)
			return vm, fmt.Errorf("host-to-guest folder mounts: %w", mergeErr)
		}
		return nil, fmt.Errorf("host-to-guest folder mounts: %w", mergeErr)
	}
	if len(mergedMounts) > 0 {
		logging.Info(pluginCtx, "applying host-to-guest folder mounts to vm", "vm", vmName, "mounts", mergedMounts)
		if err = cli.mountHostToGuestFolders(pluginCtx, vmName, mergedMounts); err != nil {
			logging.Error(pluginCtx, "host-to-guest folder mounts failed; deleting vm", "vm", vmName, "err", err)
			if delErr := cli.AnkaDeleteVMOnly(pluginCtx, vmName); delErr != nil {
				logging.Error(pluginCtx, "error deleting vm after mount failure", "vm", vmName, "err", delErr)
				return vm, fmt.Errorf("host-to-guest folder mounts: %w", err)
			}
			return nil, fmt.Errorf("host-to-guest folder mounts: %w", err)
		}
	}

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
	cmdArgs := append([]string{"anka", "-j", "list"}, args...)
	output, err := cli.ExecuteParseJson(pluginCtx, cmdArgs...)
	if err != nil {
		return nil, err
	}
	return output, nil
}

// DiscoverAndPopulateExistingTemplates discovers all existing templates on the system
// and populates the TemplateTracker with their information.
func (cli *Cli) DiscoverAndPopulateExistingTemplates(
	ctx context.Context,
	templateTracker *config.TemplateTracker,
) error {
	logging.Info(ctx, "discovering existing templates on system")

	listOutput, err := cli.AnkaList(ctx)
	if err != nil {
		logging.Warn(ctx, "unable to list existing templates", "error", err)
		return err
	}
	if listOutput.Status != "OK" {
		logging.Warn(ctx, "anka list returned error status", "status", listOutput.Status, "message", listOutput.Message)
		return fmt.Errorf("anka list failed: %s", listOutput.Message)
	}

	templateCount := 0
	if bodySlice, ok := listOutput.Body.([]any); ok {
		for _, templateData := range bodySlice {
			templateMap, ok := templateData.(map[string]any)
			if !ok {
				continue
			}
			templateUUID, _ := templateMap["uuid"].(string)
			templateName, _ := templateMap["name"].(string)
			templateTag, ok := templateMap["version"].(string)
			if !ok || templateTag == "" {
				templateTag = "latest"
			}
			if templateName == "" && templateUUID == "" {
				continue
			}

			accessDate := time.Now()
			var templateSize uint64
			ankaShowOutput, showErr := cli.AnkaShow(ctx, templateUUID)
			if showErr != nil {
				logging.Warn(ctx, "unable to inspect template during discovery", "templateUUID", templateUUID, "error", showErr)
			} else {
				if !ankaShowOutput.AccessDate.IsZero() {
					accessDate = ankaShowOutput.AccessDate
				}
				templateSize = ankaShowOutput.ImageSize
			}

			templateTracker.RegisterDiscoveredTemplate(templateUUID, templateName, templateTag, templateSize, accessDate)
			templateCount++
			logging.Debug(ctx, "discovered existing template",
				"templateUUID", templateUUID, "tag", templateTag, "size", templateSize)
		}
	}

	logging.Info(ctx, "template discovery complete", "templatesFound", templateCount, "templates", templateTracker)
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
		realPath, err := filepath.EvalSymlinks(hostLevelFile)
		if err != nil {
			return fmt.Errorf("error evaluating symlink for %s: %w", hostLevelFile, err)
		}
		if workerCtx.Err() != nil || pluginCtx.Err() != nil {
			return fmt.Errorf("context canceled before AnkaCopyIntoVM executing anka cp")
		}
		copyOutput, err := cli.ExecuteParseJson(pluginCtx, "anka", "-j", "cp", "-a", realPath, fmt.Sprintf("%s:", vmName))
		if err != nil {
			return err
		}
		if copyOutput.Status != "OK" {
			return fmt.Errorf("error copying into vm: %s", copyOutput.Message)
		}
		logging.Debug(pluginCtx, "copy output", "std", copyOutput)
		logging.Info(pluginCtx, "successfully copied file into vm", "file", realPath, "stdout", copyOutput.Message)
	}
	return nil
}

func HostHasVmCapacity(pluginCtx context.Context) bool {
	ankaCLI, err := GetAnkaCLIFromContext(pluginCtx)
	if err != nil {
		logging.Error(pluginCtx, "error getting anka cli", "err", err)
		return false
	}
	runningVMsList, err := ankaCLI.ExecuteParseJson(pluginCtx, "anka", "-j", "list", "-r")
	if err != nil {
		logging.Debug(pluginCtx, "unable to get running VMs list, assuming capacity available", "err", err)
		return true
	}
	if runningVMsList.Status != "OK" {
		logging.Error(pluginCtx, "error listing running VMs", "status", runningVMsList.Status, "message", runningVMsList.Message)
		return false
	}
	runningVMsCount := 0
	if bodySlice, ok := runningVMsList.Body.([]any); ok {
		runningVMsCount = len(bodySlice)
	} else {
		logging.Debug(pluginCtx, "running VMs list body is empty or not a slice, assuming 0 running VMs")
	}
	if runningVMsCount >= 2 {
		logging.Warn(pluginCtx, "more than 2 VMs are running; unable to run more than 2 at a time due to Apple SLA")
		return false
	}
	return true
}

// EnsureVMTemplateExists ensures the target template and tag exist locally, pulling from the registry if required.
// Sentinel errors returned: ErrTemplateNotFoundInRegistry, ErrInsufficientSpace.
func (cli *Cli) EnsureVMTemplateExists(
	workerCtx context.Context,
	pluginCtx context.Context,
	targetTemplateUUID string,
	targetTemplateTag string,
) error {
	workerGlobals, err := config.GetWorkerGlobalsFromContext(pluginCtx)
	if err != nil {
		return err
	}

	pullTemplate := false
	targetTemplateName := ""

	// Check locally first because Anklet supports non-registry setups.
	list, listErr := cli.AnkaList(pluginCtx, targetTemplateUUID)
	if listErr != nil {
		allList, innerErr := cli.AnkaList(pluginCtx)
		if innerErr != nil {
			logging.Error(pluginCtx, "error executing anka list", "err", innerErr)
			return innerErr
		}
		logging.Debug(pluginCtx, "list", "stdout", allList.Body)
	}
	logging.Info(pluginCtx, "ensuring vm template exists on host", "targetTemplateUUID", targetTemplateUUID, "targetTemplateName", targetTemplateName, "targetTemplateTag", targetTemplateTag)
	logging.Debug(pluginCtx, "list output", "json", list)

	if list != nil {
		if list.Status == "ERROR" {
			if list.Message == fmt.Sprintf("%s: not found", targetTemplateUUID) {
				pullTemplate = true
			} else {
				logging.Error(pluginCtx, "error executing anka list", "err", list.Message)
				return fmt.Errorf("error executing anka list: %s", list.Message)
			}
		}
		if list.Status == "OK" {
			bodySlice, ok := list.Body.([]any)
			if !ok {
				return fmt.Errorf("unable to parse list.Body to []any")
			}
			if len(bodySlice) == 0 {
				pullTemplate = true
			} else {
				body, ok := bodySlice[0].(map[string]any)
				if !ok {
					logging.Info(pluginCtx, "list", "body", list.Body)
					logging.Error(pluginCtx, "unable to parse bodySlice[0] to map[string]any")
					return fmt.Errorf("unable to parse bodySlice[0] to map[string]any")
				}
				if status, ok := body["status"].(string); ok && status == "failed" {
					return fmt.Errorf("vm template is not running and instead %s", status)
				}
				if version, ok := body["version"].(string); ok {
					if targetTemplateTag != "(using latest)" {
						if version != targetTemplateTag {
							pullTemplate = true
						}
					} else {
						pullTemplate = true
					}
				}
				if templateName, ok := body["name"].(string); ok {
					targetTemplateName = templateName
				}
			}
		}
	} else {
		pullTemplate = true
		ankaRegistryShowTemplate, showErr := cli.AnkaRegistryShowTemplate(workerCtx, pluginCtx, targetTemplateUUID, targetTemplateTag)
		if showErr != nil {
			logging.Error(pluginCtx, "error executing anka registry show template", "err", showErr)
			return showErr
		}
		if ankaRegistryShowTemplate != nil {
			targetTemplateName = ankaRegistryShowTemplate.Name
		}
	}

	if pullTemplate {
		if workerGlobals.TemplateTracker.HasPullingTemplates() {
			return fmt.Errorf("a pull is already running on this host")
		}

		if spaceErr := cli.EnsureSpaceForTemplate(workerCtx, pluginCtx, targetTemplateUUID, targetTemplateTag); spaceErr != nil {
			return spaceErr
		}

		pullJson, pullErr := cli.AnkaRegistryPull(workerCtx, pluginCtx, targetTemplateUUID, targetTemplateName, targetTemplateTag)
		if workerCtx.Err() != nil || pluginCtx.Err() != nil {
			logging.Error(pluginCtx, "context canceled while pulling template")
			return nil
		}
		if pullJson == nil || pullJson.Code == 3 {
			return ErrTemplateNotFoundInRegistry
		}
		if pullErr != nil {
			logging.Error(pluginCtx, "error executing anka registry pull", "err", pullErr)
			return pullErr
		}
		return nil
	}

	// Template already exists locally; inspect once for both size and access date.
	var templateSizeBytes uint64
	accessDate := time.Now()
	ankaShowOutput, showErr := cli.AnkaShow(pluginCtx, targetTemplateUUID)
	if showErr != nil {
		logging.Warn(pluginCtx, "unable to inspect existing template", "error", showErr)
	} else {
		templateSizeBytes = ankaShowOutput.ImageSize
		if !ankaShowOutput.AccessDate.IsZero() {
			accessDate = ankaShowOutput.AccessDate
		}
	}
	workerGlobals.TemplateTracker.UpdateTemplateUsage(
		targetTemplateUUID,
		targetTemplateName,
		targetTemplateTag,
		templateSizeBytes,
		accessDate,
	)
	logging.Debug(pluginCtx, "using existing template", "templateUUID", targetTemplateUUID, "tag", targetTemplateTag, "sizeBytes", templateSizeBytes, "accessDate", accessDate)
	return nil
}
