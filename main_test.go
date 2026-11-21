package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/veertuinc/anklet/internal/config"
)

func TestComputeSoloReceiver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		plugins []config.Plugin
		want    bool
	}{
		{name: "empty", plugins: nil, want: false},
		{name: "single receiver", plugins: []config.Plugin{{Plugin: "github_receiver"}}, want: true},
		{name: "handler then receiver", plugins: []config.Plugin{{Plugin: "github"}, {Plugin: "github_receiver"}}, want: false},
		{name: "receiver then handler", plugins: []config.Plugin{{Plugin: "github_receiver"}, {Plugin: "github"}}, want: false},
		{name: "only handlers", plugins: []config.Plugin{{Plugin: "github"}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeSoloReceiver(tt.plugins); got != tt.want {
				t.Errorf("computeSoloReceiver() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSteadyStatusRespectsDrainFileForHandlersOnly(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	handler := config.Plugin{Name: "H1", Plugin: "github"}
	receiver := config.Plugin{Name: "R1", Plugin: "github_receiver"}

	if got := steadyStatus(handler); got != "idle" {
		t.Fatalf("handler steadyStatus without drain = %q, want idle", got)
	}
	if got := steadyStatus(receiver); got != "idle" {
		t.Fatalf("receiver steadyStatus without drain = %q, want idle", got)
	}

	drainDir := filepath.Join(tmpHome, ".config", "anklet")
	if err := os.MkdirAll(drainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drainDir, ".drain"), []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := steadyStatus(handler); got != "draining" {
		t.Fatalf("handler steadyStatus with drain = %q, want draining", got)
	}
	if got := steadyStatus(receiver); got != "idle" {
		t.Fatalf("receiver steadyStatus with drain = %q, want idle", got)
	}
}

func TestApplyPluginDefaults(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		GlobalReceiverSecret:                 "global-secret",
		GlobalTemplateDiskBuffer:             18.5,
		GlobalSkipCPUAndMemoryResourceChecks: true,
		Plugins: []config.Plugin{
			{Name: "R1", Plugin: "github_receiver"},
			{Name: "H1", Plugin: "github"},
			{Name: "H2", Plugin: "github", TemplateDiskBuffer: 25.0},
		},
	}
	applyPluginDefaults(&cfg)

	if cfg.Plugins[0].RedeliverHours != 24 || cfg.Plugins[0].Secret != "global-secret" {
		t.Errorf("receiver defaults wrong: %+v", cfg.Plugins[0])
	}
	if cfg.Plugins[1].TemplateDiskBuffer != 18.5 || !cfg.Plugins[1].SkipCPUAndMemoryResourceChecks {
		t.Errorf("handler global defaults wrong: %+v", cfg.Plugins[1])
	}
	if cfg.Plugins[2].TemplateDiskBuffer != 25.0 {
		t.Errorf("explicit plugin TemplateDiskBuffer overwritten: %v", cfg.Plugins[2].TemplateDiskBuffer)
	}
}

func TestOpenMetricsListenerExplicitAndAutoProbe(t *testing.T) {
	t.Parallel()

	ln, port, err := openMetricsListener("127.0.0.1", "0")
	if err != nil {
		t.Fatalf("openMetricsListener(explicit 0) error = %v", err)
	}
	_ = ln.Close()
	if port != "0" {
		t.Errorf("port = %q, want 0", port)
	}

	// Occupy port 8080 if free, then verify auto-probe picks another free port cleanly.
	blocker, blockErr := net.Listen("tcp", "127.0.0.1:8080")
	if blockErr == nil {
		defer func() { _ = blocker.Close() }()
	}
	probedLn, probedPort, err := openMetricsListener("127.0.0.1", "")
	if err != nil {
		t.Fatalf("openMetricsListener(auto-probe) error = %v", err)
	}
	defer func() { _ = probedLn.Close() }()
	portNum, err := strconv.Atoi(probedPort)
	if err != nil || portNum < 8080 || portNum > 65535 {
		t.Fatalf("probedPort = %q, want valid port in 8080..65535", probedPort)
	}
	if blockErr == nil && portNum == 8080 {
		t.Fatalf("auto-probe returned occupied port 8080")
	}
}
