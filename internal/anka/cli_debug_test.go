package anka

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/veertuinc/anklet/internal/config"
)

const fakeAnkaJSON = `{"status":"OK","code":0,"body":{"ok":true},"message":""}` + "\n"
const fakeAnkaFailJSON = `{"status":"ERROR","code":1,"body":{},"message":"boom"}` + "\n"
const fakeAnkaStderr = "anka debug output\n"

func testPluginCtxWithLogger(t *testing.T, w io.Writer) context.Context {
	t.Helper()
	if w == nil {
		w = io.Discard
	}
	logger := slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return context.WithValue(context.Background(), config.ContextKey("logger"), logger)
}

func installFakeAnka(t *testing.T, stdout string, stderr string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	if err := os.WriteFile(filepath.Join(dir, "stdout"), []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stderr"), []byte(stderr), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANKA_FAKE_ARGS_FILE", argsFile)
	script := fmt.Sprintf(`#!/bin/sh
DIR=$(dirname "$0")
{
	for a in "$@"; do
		printf '%%s\n' "$a"
	done
} > "${ANKA_FAKE_ARGS_FILE:?}"
cat "$DIR/stdout"
cat "$DIR/stderr" >&2
exit %d
`, exitCode)
	if err := os.WriteFile(filepath.Join(dir, "anka"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

func readFakeAnkaArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestInjectAnkaDebugFlag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "json version",
			in:   []string{"anka", "-j", "version"},
			want: []string{"anka", "-j", "--debug", "version"},
		},
		{
			name: "machine-readable long flag",
			in:   []string{"anka", "--machine-readable", "list"},
			want: []string{"anka", "--machine-readable", "--debug", "list"},
		},
		{
			name: "no json flag",
			in:   []string{"anka", "delete", "--yes", "vm"},
			want: []string{"anka", "--debug", "delete", "--yes", "vm"},
		},
		{
			name: "already has debug",
			in:   []string{"anka", "-j", "--debug", "version"},
			want: []string{"anka", "-j", "--debug", "version"},
		},
		{
			name: "not anka",
			in:   []string{"echo", "hello"},
			want: []string{"echo", "hello"},
		},
		{
			name: "empty",
			in:   nil,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := injectAnkaDebugFlag(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("injectAnkaDebugFlag(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestInjectAnkaDebugFlagDoesNotMutateInput(t *testing.T) {
	t.Parallel()
	in := []string{"anka", "-j", "version"}
	_ = injectAnkaDebugFlag(in)
	want := []string{"anka", "-j", "version"}
	if !reflect.DeepEqual(in, want) {
		t.Errorf("input mutated: %v, want %v", in, want)
	}
}

func TestShouldLogAnkaStderr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		debugEnabled bool
		failed       bool
		stderr       []byte
		want         bool
	}{
		{name: "success info with stderr", debugEnabled: false, failed: false, stderr: []byte("debug line\n"), want: false},
		{name: "success debug", debugEnabled: true, failed: false, stderr: []byte("debug line\n"), want: true},
		{name: "failure info", debugEnabled: false, failed: true, stderr: []byte("debug line\n"), want: true},
		{name: "failure debug", debugEnabled: true, failed: true, stderr: []byte("debug line\n"), want: true},
		{name: "failure empty stderr", debugEnabled: false, failed: true, stderr: []byte("  \n"), want: false},
		{name: "debug empty stderr", debugEnabled: true, failed: false, stderr: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shouldLogAnkaStderr(tt.debugEnabled, tt.failed, tt.stderr)
			if got != tt.want {
				t.Errorf("shouldLogAnkaStderr(%v, %v, %q) = %v, want %v",
					tt.debugEnabled, tt.failed, tt.stderr, got, tt.want)
			}
		})
	}
}

func TestExecutePassesDebugAndKeepsJSONOnStdout(t *testing.T) {
	t.Setenv("LOG_LEVEL", "INFO")
	argsFile := installFakeAnka(t, fakeAnkaJSON, fakeAnkaStderr, 0)
	ctx := testPluginCtxWithLogger(t, nil)
	cli := &Cli{}
	out, exitCode, err := cli.Execute(ctx, "anka", "-j", "version")
	if err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if string(out) != fakeAnkaJSON {
		t.Fatalf("stdout = %q, want %q", out, fakeAnkaJSON)
	}
	if strings.Contains(string(out), "anka debug output") {
		t.Fatal("stderr was mixed into stdout")
	}
	gotArgs := readFakeAnkaArgs(t, argsFile)
	wantArgs := []string{"-j", "--debug", "version"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("anka args = %v, want %v", gotArgs, wantArgs)
	}
}

func TestExecuteParseJsonIgnoresStderrDebug(t *testing.T) {
	t.Setenv("LOG_LEVEL", "INFO")
	stdout := `{"status":"OK","code":0,"message":"progress"}` + "\n" + fakeAnkaJSON
	installFakeAnka(t, stdout, fakeAnkaStderr+fakeAnkaStderr, 0)
	ctx := testPluginCtxWithLogger(t, nil)
	cli := &Cli{}
	ankaJSON, err := cli.ExecuteParseJson(ctx, "anka", "-j", "registry", "pull", "tmpl")
	if err != nil {
		t.Fatalf("ExecuteParseJson err = %v", err)
	}
	if ankaJSON.Status != "OK" {
		t.Fatalf("status = %q, want OK", ankaJSON.Status)
	}
}

func TestExecuteDoesNotLogStderrOnSuccessAtInfo(t *testing.T) {
	t.Setenv("LOG_LEVEL", "INFO")
	installFakeAnka(t, fakeAnkaJSON, fakeAnkaStderr, 0)
	var logs bytes.Buffer
	ctx := testPluginCtxWithLogger(t, &logs)
	cli := &Cli{}
	if _, _, err := cli.Execute(ctx, "anka", "-j", "version"); err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if strings.Contains(logs.String(), "anka debug output") {
		t.Fatalf("logged stderr at INFO on success: %s", logs.String())
	}
	if strings.Contains(logs.String(), "anka command stderr") {
		t.Fatalf("logged anka command stderr at INFO on success: %s", logs.String())
	}
}

func TestExecuteLogsStderrOnFailureAtInfo(t *testing.T) {
	t.Setenv("LOG_LEVEL", "INFO")
	installFakeAnka(t, fakeAnkaFailJSON, fakeAnkaStderr, 1)
	var logs bytes.Buffer
	ctx := testPluginCtxWithLogger(t, &logs)
	cli := &Cli{}
	_, exitCode, err := cli.Execute(ctx, "anka", "-j", "version")
	if err == nil {
		t.Fatal("expected Execute error")
	}
	if exitCode != 1 {
		t.Fatalf("exitCode = %d, want 1", exitCode)
	}
	if !strings.Contains(logs.String(), "anka command stderr") {
		t.Fatalf("missing anka command stderr log: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "anka debug output") {
		t.Fatalf("missing stderr body in logs: %s", logs.String())
	}
}

func TestExecuteLogsStderrOnSuccessAtDebug(t *testing.T) {
	t.Setenv("LOG_LEVEL", "DEBUG")
	installFakeAnka(t, fakeAnkaJSON, fakeAnkaStderr, 0)
	var logs bytes.Buffer
	ctx := testPluginCtxWithLogger(t, &logs)
	cli := &Cli{}
	if _, _, err := cli.Execute(ctx, "anka", "-j", "version"); err != nil {
		t.Fatalf("Execute err = %v", err)
	}
	if !strings.Contains(logs.String(), "anka command stderr") {
		t.Fatalf("missing anka command stderr log: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "anka debug output") {
		t.Fatalf("missing stderr body in logs: %s", logs.String())
	}
}

func TestExecuteWithTimeoutPassesDebugAndKeepsJSONOnStdout(t *testing.T) {
	t.Setenv("LOG_LEVEL", "INFO")
	argsFile := installFakeAnka(t, fakeAnkaJSON, fakeAnkaStderr, 0)
	ctx := testPluginCtxWithLogger(t, nil)
	cli := &Cli{}
	out, exitCode, err, timeoutErr := cli.ExecuteWithTimeout(ctx, 5, "anka", "-j", "version")
	if timeoutErr != nil {
		t.Fatalf("timeout err = %v", timeoutErr)
	}
	if err != nil {
		t.Fatalf("ExecuteWithTimeout err = %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if string(out) != fakeAnkaJSON {
		t.Fatalf("stdout = %q, want %q", out, fakeAnkaJSON)
	}
	gotArgs := readFakeAnkaArgs(t, argsFile)
	wantArgs := []string{"-j", "--debug", "version"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("anka args = %v, want %v", gotArgs, wantArgs)
	}
}
