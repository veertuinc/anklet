package anka

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/veertuinc/anklet/internal/config"
)

func TestAnkaRunPassesDirectArgsWithoutBashCAndRedactsSecrets(t *testing.T) {
	t.Setenv("LOG_LEVEL", "DEBUG")
	argsFile := installFakeAnka(t, fakeAnkaJSON, "stderr containing SECRET_TOKEN_123\n", 0)
	var logs bytes.Buffer
	ctx := testPluginCtxWithLogger(t, &logs)
	cli := &Cli{}

	maliciousLabel := "$(touch /tmp/pwn);id"
	err, timeoutErr := cli.AnkaRunWithTimeoutRedacted(
		ctx,
		10,
		"anklet-vm-1",
		[]string{"SECRET_TOKEN_123"},
		"./register-runner.bash",
		"anklet-vm-1",
		"SECRET_TOKEN_123",
		"https://github.com/org",
		maliciousLabel,
	)
	if err != nil || timeoutErr != nil {
		t.Fatalf("unexpected errors: err=%v timeoutErr=%v", err, timeoutErr)
	}

	gotArgs := readFakeAnkaArgs(t, argsFile)
	wantArgs := []string{
		"-j",
		"--debug",
		"run",
		"anklet-vm-1",
		"./register-runner.bash",
		"anklet-vm-1",
		"SECRET_TOKEN_123",
		"https://github.com/org",
		maliciousLabel,
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("anka args = %v, want %v (must not use bash -c)", gotArgs, wantArgs)
	}
	if strings.Contains(logs.String(), "SECRET_TOKEN_123") {
		t.Fatalf("secret token leaked into logs: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "[REDACTED]") {
		t.Fatalf("expected [REDACTED] placeholder in debug logs: %s", logs.String())
	}
}

func TestExecuteShortArgsDoesNotPanic(t *testing.T) {
	t.Setenv("LOG_LEVEL", "INFO")
	installFakeAnka(t, fakeAnkaJSON, "", 0)
	ctx := testPluginCtxWithLogger(t, nil)
	cli := &Cli{}

	_, exitCode, err := cli.Execute(ctx, "anka", "version")
	if err != nil || exitCode != 0 {
		t.Fatalf("Execute short args failed: exitCode=%d err=%v", exitCode, err)
	}
}

func TestExecuteParseJsonPropagatesContextCanceled(t *testing.T) {
	t.Setenv("LOG_LEVEL", "INFO")
	installFakeAnka(t, "", "", 0)
	ctx, cancel := context.WithCancel(testPluginCtxWithLogger(t, nil))
	cancel()
	cli := &Cli{}

	_, err := cli.ExecuteParseJson(ctx, "anka", "-j", "version")
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got %v", err)
	}
}

func TestResolveHostToGuestFolderMountSpecRejectsUnsafeGuestNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	invalidGuests := []string{"", ".", "..", "../escape", "a/b", `a\b`, "a:b"}
	for _, guest := range invalidGuests {
		if _, err := ResolveHostToGuestFolderMountSpec(dir + ":" + guest); err == nil {
			t.Errorf("expected error for unsafe guest name %q", guest)
		}
	}
}

func TestFilterEvictableTemplatesExcludesTargetWithoutOffByOne(t *testing.T) {
	t.Parallel()

	// Target template is NOT in candidates (e.g., already InUse or Pulling);
	// other template must still remain evictable.
	candidates := []*config.TemplateUsage{
		{UUID: "other-tmpl", ImageSize: 100},
	}
	evictable := filterEvictableTemplates(candidates, "target-tmpl")
	if len(evictable) != 1 || evictable[0].UUID != "other-tmpl" {
		t.Fatalf("evictable = %+v, want [other-tmpl]", evictable)
	}

	// Target template IS in candidates; only other template remains.
	candidatesWithTarget := []*config.TemplateUsage{
		{UUID: "target-tmpl", ImageSize: 200},
		{UUID: "other-tmpl", ImageSize: 100},
	}
	evictable = filterEvictableTemplates(candidatesWithTarget, "target-tmpl")
	if len(evictable) != 1 || evictable[0].UUID != "other-tmpl" {
		t.Fatalf("evictable = %+v, want [other-tmpl]", evictable)
	}
}
