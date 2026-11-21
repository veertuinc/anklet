package anka

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/metrics"
)

// installDispatchFakeAnka installs a scriptable fake `anka` binary on PATH.
// `scriptBody` is bash code receiving "$@" (with -j/--debug possibly injected)
// and may use `$CALL_LOG` to record calls.
func installDispatchFakeAnka(t *testing.T, scriptBody string) string {
	t.Helper()
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	full := "#!/usr/bin/env bash\nset -e\nCALL_LOG=\"" + callLog + "\"\n" +
		"echo \"$*\" >> \"$CALL_LOG\"\n" +
		scriptBody + "\n"
	ankaPath := filepath.Join(dir, "anka")
	if err := os.WriteFile(ankaPath, []byte(full), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return callLog
}

func makeAnkaTestContexts(t *testing.T, cfg config.Config, pluginCfg config.Plugin, cli *Cli) (context.Context, context.Context, *config.Globals, *metrics.MetricsDataLock) {
	t.Helper()
	globals := &config.Globals{
		HostCPUCount:    8,
		HostMemoryBytes: 16 * 1024 * 1024 * 1024,
		TemplateTracker: config.NewTemplateTracker(),
		Plugins: map[string]map[string]*config.PluginGlobal{
			pluginCfg.Plugin: {pluginCfg.Name: {}},
		},
	}
	m := &metrics.MetricsDataLock{}
	_ = m.AddPlugin(metrics.Plugin{
		PluginBase: metrics.PluginBase{Name: pluginCfg.Name, PluginName: pluginCfg.Plugin, Status: "idle"},
	})
	base := testPluginCtxWithLogger(t, nil)
	workerCtx := context.WithValue(base, config.ContextKey("metrics"), m)
	workerCtx = context.WithValue(workerCtx, config.ContextKey("globals"), globals)

	pluginCtx := context.WithValue(base, config.ContextKey("config"), &cfg)
	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("plugin"), pluginCfg)
	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("globals"), globals)
	pluginCtx = context.WithValue(pluginCtx, config.ContextKey("ankacli"), cli)
	return workerCtx, pluginCtx, globals, m
}

func TestNewCLILicenseAndVersionValidation(t *testing.T) {
	ctx := testPluginCtxWithLogger(t, nil)

	t.Run("valid enterprise license", func(t *testing.T) {
		installDispatchFakeAnka(t, `
case "$*" in
  "") exit 0 ;;
  *version*) echo '{"status":"OK","code":0,"body":{"version":"3.9.0 (build 1)"},"message":""}' ;;
  *license*) echo '{"status":"OK","code":0,"body":{"status":"valid","license_type":"com.veertu.anka.enterprise","product":"Anka"},"message":""}' ;;
esac
`)
		cli, err := NewCLI(ctx)
		if err != nil {
			t.Fatalf("NewCLI() error = %v", err)
		}
		if cli.Version != "3.9.0 (build 1)" || cli.License.Status != "valid" {
			t.Fatalf("unexpected CLI: %+v", cli)
		}
	})

	t.Run("non-OK version status returns error", func(t *testing.T) {
		installDispatchFakeAnka(t, `
case "$*" in
  "") exit 0 ;;
  *version*) echo '{"status":"ERROR","code":0,"body":{},"message":"bad version"}' ;;
esac
`)
		if cli, err := NewCLI(ctx); err == nil || cli != nil {
			t.Fatalf("expected error for non-OK version status, got (%v, %v)", cli, err)
		}
	})

	t.Run("invalid license status rejected", func(t *testing.T) {
		installDispatchFakeAnka(t, `
case "$*" in
  "") exit 0 ;;
  *version*) echo '{"status":"OK","code":0,"body":{"version":"3.9.0"},"message":""}' ;;
  *license*) echo '{"status":"OK","code":0,"body":{"status":"expired","license_type":"com.veertu.anka.enterprise","product":"Anka"},"message":""}' ;;
esac
`)
		if _, err := NewCLI(ctx); err == nil || !strings.Contains(err.Error(), "license is invalid") {
			t.Fatalf("expected invalid license error, got %v", err)
		}
	})

	t.Run("unsupported anka.run license rejected", func(t *testing.T) {
		installDispatchFakeAnka(t, `
case "$*" in
  "") exit 0 ;;
  *version*) echo '{"status":"OK","code":0,"body":{"version":"3.9.0"},"message":""}' ;;
  *license*) echo '{"status":"OK","code":0,"body":{"status":"valid","license_type":"com.veertu.anka.run","product":"Anka"},"message":""}' ;;
esac
`)
		if _, err := NewCLI(ctx); err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Fatalf("expected unsupported license error, got %v", err)
		}
	})
}

func TestObtainAnkaVMHappyPathAndRollbackOnStartOrMountFailure(t *testing.T) {
	mountDir := t.TempDir()
	pluginCfg := config.Plugin{Name: "H1", Plugin: "github", HostToGuestFolderMounts: []string{mountDir + ":cache"}}
	cfg := config.Config{Plugins: []config.Plugin{pluginCfg}}

	t.Run("happy path clones starts mounts increments metrics and releases template InUse", func(t *testing.T) {
		callLog := installDispatchFakeAnka(t, `
echo '{"status":"OK","code":0,"body":{},"message":"ok"}'
`)
		cli := &Cli{Version: "3.9.0"}
		workerCtx, pluginCtx, globals, m := makeAnkaTestContexts(t, cfg, pluginCfg, cli)
		globals.TemplateTracker.RegisterDiscoveredTemplate("tmpl-1", "mac14", "v1", 100, time.Now())

		vm, err := cli.ObtainAnkaVM(workerCtx, pluginCtx, "tmpl-1")
		if err != nil || vm == nil || !strings.HasPrefix(vm.Name, "anklet-vm-") {
			t.Fatalf("ObtainAnkaVM() = (%v, %v)", vm, err)
		}
		if snap := m.Snapshot(); snap.TotalRunningVMs != 1 {
			t.Fatalf("TotalRunningVMs = %d, want 1", snap.TotalRunningVMs)
		}
		// Template InUse refcount must be released after clone completes.
		if lru := globals.TemplateTracker.GetLeastRecentlyUsedTemplates(); len(lru) != 1 {
			t.Fatalf("template remained locked InUse after ObtainAnkaVM")
		}
		calls, _ := os.ReadFile(callLog)
		if !strings.Contains(string(calls), "mount "+vm.Name) {
			t.Fatalf("expected anka mount call in log:\n%s", string(calls))
		}

		if err := cli.AnkaDelete(workerCtx, pluginCtx, vm.Name); err != nil {
			t.Fatalf("AnkaDelete() error = %v", err)
		}
		if snap := m.Snapshot(); snap.TotalRunningVMs != 0 {
			t.Fatalf("TotalRunningVMs after delete = %d, want 0", snap.TotalRunningVMs)
		}
	})

	t.Run("start failure deletes cloned VM and returns nil vm with zero running VMs", func(t *testing.T) {
		callLog := installDispatchFakeAnka(t, `
case "$*" in
  *" start "*) echo '{"status":"ERROR","code":1,"body":{},"message":"start failed"}'; exit 1 ;;
  *) echo '{"status":"OK","code":0,"body":{},"message":"ok"}' ;;
esac
`)
		cli := &Cli{Version: "3.9.0"}
		workerCtx, pluginCtx, _, m := makeAnkaTestContexts(t, cfg, pluginCfg, cli)

		vm, err := cli.ObtainAnkaVM(workerCtx, pluginCtx, "tmpl-1")
		if err == nil {
			t.Fatal("expected start failure error")
		}
		if vm != nil {
			t.Fatalf("expected nil vm after successful rollback delete, got %+v", vm)
		}
		if snap := m.Snapshot(); snap.TotalRunningVMs != 0 {
			t.Fatalf("TotalRunningVMs = %d, want 0", snap.TotalRunningVMs)
		}
		calls, _ := os.ReadFile(callLog)
		if !strings.Contains(string(calls), "delete --yes anklet-vm-") {
			t.Fatalf("expected rollback delete in call log:\n%s", string(calls))
		}
	})

	t.Run("mount failure deletes VM and does not increment TotalRunningVMs", func(t *testing.T) {
		callLog := installDispatchFakeAnka(t, `
case "$*" in
  *" mount "*) echo '{"status":"ERROR","code":1,"body":{},"message":"mount failed"}'; exit 1 ;;
  *) echo '{"status":"OK","code":0,"body":{},"message":"ok"}' ;;
esac
`)
		cli := &Cli{Version: "3.9.0"}
		workerCtx, pluginCtx, _, m := makeAnkaTestContexts(t, cfg, pluginCfg, cli)

		vm, err := cli.ObtainAnkaVM(workerCtx, pluginCtx, "tmpl-1")
		if err == nil || !strings.Contains(err.Error(), "host-to-guest folder mounts") {
			t.Fatalf("expected mount error, got %v", err)
		}
		if vm != nil {
			t.Fatalf("expected nil vm after mount rollback, got %+v", vm)
		}
		if snap := m.Snapshot(); snap.TotalRunningVMs != 0 {
			t.Fatalf("TotalRunningVMs = %d, want 0", snap.TotalRunningVMs)
		}
		calls, _ := os.ReadFile(callLog)
		if !strings.Contains(string(calls), "delete --yes anklet-vm-") {
			t.Fatalf("expected rollback delete in call log:\n%s", string(calls))
		}
	})
}

func TestHostHasVmCapacityAndResourceAdmission(t *testing.T) {
	pluginCfg := config.Plugin{Name: "H1", Plugin: "github"}
	cfg := config.Config{Plugins: []config.Plugin{pluginCfg}}

	t.Run("under Apple 2-VM SLA has capacity", func(t *testing.T) {
		installDispatchFakeAnka(t, `
echo '{"status":"OK","code":0,"body":[{"name":"vm-1"}],"message":""}'
`)
		cli := &Cli{}
		_, pluginCtx, _, _ := makeAnkaTestContexts(t, cfg, pluginCfg, cli)
		if !HostHasVmCapacity(pluginCtx) {
			t.Fatal("expected capacity available with 1 running VM")
		}
	})

	t.Run("at 2 running VMs rejects new VM", func(t *testing.T) {
		installDispatchFakeAnka(t, `
echo '{"status":"OK","code":0,"body":[{"name":"vm-1"},{"name":"vm-2"}],"message":""}'
`)
		cli := &Cli{}
		_, pluginCtx, _, _ := makeAnkaTestContexts(t, cfg, pluginCfg, cli)
		if HostHasVmCapacity(pluginCtx) {
			t.Fatal("expected no capacity with 2 running VMs")
		}
	})

	t.Run("VmHasEnoughHostResources and VmHasEnoughResources", func(t *testing.T) {
		installDispatchFakeAnka(t, `
case "$*" in
  *" list --running"*) echo '{"status":"OK","code":0,"body":[{"name":"running-1"}],"message":""}' ;;
  *" show running-1 tag"*) echo '{"status":"OK","code":0,"body":[{"tag":"v1"}],"message":""}' ;;
  *" show running-1"*) echo '{"status":"OK","code":0,"body":{"name":"running-1","cpu_cores":6,"ram_size":10737418240,"image_size":1000},"message":""}' ;;
esac
`)
		cli := &Cli{}
		_, pluginCtx, _, _ := makeAnkaTestContexts(t, cfg, pluginCfg, cli)

		// Host has 8 cores, 16 GiB RAM.
		if err := VmHasEnoughHostResources(pluginCtx, VM{CPUCount: 12, MEMBytes: 1024}); err == nil {
			t.Fatal("expected error when VM CPU exceeds host CPU")
		}
		if err := VmHasEnoughHostResources(pluginCtx, VM{CPUCount: 4, MEMBytes: 32 * 1024 * 1024 * 1024}); err == nil {
			t.Fatal("expected error when VM RAM exceeds host RAM")
		}
		// 6 cores used + 2 requested = 8 -> fits.
		if err := VmHasEnoughResources(pluginCtx, VM{CPUCount: 2, MEMBytes: 4 * 1024 * 1024 * 1024}); err != nil {
			t.Fatalf("expected 2 cores + 4 GiB to fit alongside 6 cores + 10 GiB: %v", err)
		}
		// 6 cores used + 4 requested = 10 > 8 -> fails.
		if err := VmHasEnoughResources(pluginCtx, VM{CPUCount: 4, MEMBytes: 2 * 1024 * 1024 * 1024}); err == nil {
			t.Fatal("expected error when combined CPU exceeds host cores")
		}
	})
}

func TestDiscoverAndEnsureVMTemplateExists(t *testing.T) {
	pluginCfg := config.Plugin{Name: "H1", Plugin: "github", TemplateDiskBuffer: 10}
	cfg := config.Config{Plugins: []config.Plugin{pluginCfg}}

	t.Run("DiscoverAndPopulateExistingTemplates populates tracker", func(t *testing.T) {
		installDispatchFakeAnka(t, `
case "$*" in
  *" list"*) echo '{"status":"OK","code":0,"body":[{"uuid":"u-1","name":"mac14","version":"v1"}],"message":""}' ;;
  *" show u-1 tag"*) echo '{"status":"OK","code":0,"body":[{"tag":"v1"}],"message":""}' ;;
  *" show u-1"*) echo '{"status":"OK","code":0,"body":{"name":"mac14","cpu_cores":4,"ram_size":8589934592,"image_size":5000000,"access_date":"2026-11-01T00:00:00Z"},"message":""}' ;;
esac
`)
		cli := &Cli{}
		_, pluginCtx, globals, _ := makeAnkaTestContexts(t, cfg, pluginCfg, cli)
		if err := cli.DiscoverAndPopulateExistingTemplates(pluginCtx, globals.TemplateTracker); err != nil {
			t.Fatalf("DiscoverAndPopulateExistingTemplates() error = %v", err)
		}
		if globals.TemplateTracker.Len() != 1 {
			t.Fatalf("tracker len = %d, want 1", globals.TemplateTracker.Len())
		}
	})

	t.Run("local matching tag skips pull and updates usage", func(t *testing.T) {
		callLog := installDispatchFakeAnka(t, `
case "$*" in
  *" list u-1"*) echo '{"status":"OK","code":0,"body":[{"uuid":"u-1","name":"mac14","version":"v1","status":"stopped"}],"message":""}' ;;
  *" show u-1 tag"*) echo '{"status":"OK","code":0,"body":[{"tag":"v1"}],"message":""}' ;;
  *" show u-1"*) echo '{"status":"OK","code":0,"body":{"name":"mac14","image_size":4096,"access_date":"2026-11-01T00:00:00Z"},"message":""}' ;;
esac
`)
		cli := &Cli{}
		workerCtx, pluginCtx, _, _ := makeAnkaTestContexts(t, cfg, pluginCfg, cli)
		if err := cli.EnsureVMTemplateExists(workerCtx, pluginCtx, "u-1", "v1"); err != nil {
			t.Fatalf("EnsureVMTemplateExists() error = %v", err)
		}
		calls, _ := os.ReadFile(callLog)
		if strings.Contains(string(calls), "registry") {
			t.Fatalf("unexpected registry pull when local tag already matches:\n%s", string(calls))
		}
	})

	t.Run("registry code 3 returns ErrTemplateNotFoundInRegistry", func(t *testing.T) {
		installDispatchFakeAnka(t, `
case "$*" in
  *" list missing-u"*) echo '{"status":"ERROR","code":1,"body":{},"message":"missing-u: not found"}' ;;
  *" pull missing-u --check-download-size"*) echo '{"status":"OK","code":0,"body":{},"message":"ok"}' ;;
  *" registry pull --shrink missing-u"*) echo '{"status":"ERROR","code":3,"body":{},"message":"not found in registry"}'; exit 1 ;;
  *) echo '{"status":"OK","code":0,"body":{},"message":"ok"}' ;;
esac
`)
		cli := &Cli{}
		workerCtx, pluginCtx, _, m := makeAnkaTestContexts(t, cfg, pluginCfg, cli)
		err := cli.EnsureVMTemplateExists(workerCtx, pluginCtx, "missing-u", "v1")
		if !errors.Is(err, ErrTemplateNotFoundInRegistry) {
			t.Fatalf("EnsureVMTemplateExists() error = %v, want ErrTemplateNotFoundInRegistry", err)
		}
		// Failed pull must not overwrite status with "running".
		if snap := m.Snapshot(); snap.Plugins[0].Status == "running" {
			t.Fatalf("failed pull must not set plugin status to running")
		}
	})

	t.Run("concurrent pull rejected when another template is pulling", func(t *testing.T) {
		installDispatchFakeAnka(t, `
echo '{"status":"ERROR","code":1,"body":{},"message":"u-2: not found"}'
`)
		cli := &Cli{}
		workerCtx, pluginCtx, globals, _ := makeAnkaTestContexts(t, cfg, pluginCfg, cli)
		globals.TemplateTracker.SetTemplatePulling("u-other", "other", "v1", true, time.Now())

		err := cli.EnsureVMTemplateExists(workerCtx, pluginCtx, "u-2", "v1")
		if err == nil || !strings.Contains(err.Error(), "a pull is already running on this host") {
			t.Fatalf("expected concurrent pull error, got %v", err)
		}
	})
}

func TestEnsureSpaceForTemplateLRUEviction(t *testing.T) {
	pluginCfg := config.Plugin{Name: "H1", Plugin: "github", TemplateDiskBuffer: 1}
	cfg := config.Config{Plugins: []config.Plugin{pluginCfg}}

	// Use a state file so the fake `anka` script increases available space after deleting old-1.
	stateDir := t.TempDir()
	deletedMarker := filepath.Join(stateDir, "deleted-old-1")

	callLog := installDispatchFakeAnka(t, `
MARKER="`+deletedMarker+`"
case "$*" in
  *"--check-download-size"*)
    if [ -f "$MARKER" ]; then
      echo '{"status":"OK","code":0,"body":{"size":100000000000,"cached":0,"available":200000000000},"message":""}'
    else
      echo '{"status":"OK","code":0,"body":{"size":100000000000,"cached":0,"available":80000000000},"message":""}'
    fi
    ;;
  *" delete --yes old-1"*)
    touch "$MARKER"
    echo '{"status":"OK","code":0,"body":{},"message":"deleted"}'
    ;;
  *)
    echo '{"status":"OK","code":0,"body":{},"message":"ok"}'
    ;;
esac
`)

	cli := &Cli{}
	workerCtx, pluginCtx, globals, _ := makeAnkaTestContexts(t, cfg, pluginCfg, cli)
	globals.TemplateTracker.RegisterDiscoveredTemplate("old-1", "mac-old", "v1", 120_000_000_000, time.Now().Add(-2*time.Hour))
	globals.TemplateTracker.RegisterDiscoveredTemplate("new-2", "mac-new", "v1", 120_000_000_000, time.Now())

	if err := cli.EnsureSpaceForTemplate(workerCtx, pluginCtx, "incoming-3", "v1"); err != nil {
		t.Fatalf("EnsureSpaceForTemplate() error = %v", err)
	}
	if globals.TemplateTracker.Contains("old-1") {
		t.Fatal("expected oldest template old-1 to be evicted from tracker")
	}
	if !globals.TemplateTracker.Contains("new-2") {
		t.Fatal("expected newer template new-2 to be kept once enough space was freed")
	}
	calls, _ := os.ReadFile(callLog)
	if strings.Contains(string(calls), "delete --yes new-2") {
		t.Fatalf("unexpected deletion of new-2 after space was already freed:\n%s", string(calls))
	}
}
