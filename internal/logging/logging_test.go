package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/veertuinc/anklet/internal/config"
)

func TestAppendCtxDoesNotMutateParentSlice(t *testing.T) {
	t.Parallel()

	var baseBuf bytes.Buffer
	logger := slog.New(&ContextHandler{Handler: slog.NewJSONHandler(&baseBuf, nil)})
	parent := context.WithValue(context.Background(), config.ContextKey("logger"), logger)
	parent = AppendCtx(parent, slog.String("shared", "parent"), slog.String("k", "orig"))

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			child := AppendCtx(parent, slog.Int("worker", idx), slog.String("k", "child"))
			var out bytes.Buffer
			localLogger := slog.New(&ContextHandler{Handler: slog.NewJSONHandler(&out, nil)})
			localLogger.InfoContext(child, "msg")
			var parsed map[string]any
			if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
				t.Errorf("unmarshal failed: %v", err)
				return
			}
			if parsed["shared"] != "parent" {
				t.Errorf("shared = %v, want parent", parsed["shared"])
			}
			if parsed["k"] != "child" {
				t.Errorf("k = %v, want child", parsed["k"])
			}
		}(i)
	}
	wg.Wait()

	// Parent context attributes must remain untouched.
	logger.InfoContext(parent, "parent check")
	var parsed map[string]any
	if err := json.Unmarshal(baseBuf.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal parent log failed: %v", err)
	}
	if parsed["k"] != "orig" {
		t.Fatalf("parent k was mutated to %v, want orig", parsed["k"])
	}
	if _, hasWorker := parsed["worker"]; hasWorker {
		t.Fatalf("parent context leaked child worker attribute: %v", parsed)
	}
}

func TestUpdateLoggerToFilePermissionsAndClose(t *testing.T) {
	t.Parallel()

	dir := t.TempDir() + string(filepath.Separator)
	baseLogger := New()
	fileLogger, path, fileHandle, err := UpdateLoggerToFile(baseLogger, dir, "-testplugin")
	if err != nil {
		t.Fatalf("UpdateLoggerToFile() error = %v", err)
	}
	defer func() { _ = fileHandle.Close() }()

	fileLogger.Info("hello")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat() error = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Errorf("log file permissions = %04o, want 0640", perm)
	}
}

func TestGetPluginAttributesMissingPluginDoesNotPanic(t *testing.T) {
	t.Parallel()

	globals := &config.Globals{
		Plugins: map[string]map[string]*config.PluginGlobal{},
	}
	ctx := context.WithValue(context.Background(), config.ContextKey("globals"), globals)
	ctx = context.WithValue(ctx, config.ContextKey("plugin"), config.Plugin{
		Name:   "UNREGISTERED",
		Plugin: "github",
		Owner:  "veertuinc",
	})

	attrs := GetPluginAttributes(ctx)
	if attrs["name"] != "UNREGISTERED" || attrs["owner"] != "veertuinc" {
		t.Fatalf("unexpected attributes: %v", attrs)
	}
}

func TestLoggingWrappersSourceAndPrettyHandler(t *testing.T) {
	t.Setenv("LOG_LEVEL", "DEV")
	if !IsDebugEnabled() {
		t.Fatal("expected IsDebugEnabled() = true for DEV")
	}

	var buf bytes.Buffer
	h := (&ContextHandler{Handler: slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})}).
		With(slog.String("service", "anklet"))
	logger := slog.New(h)
	ctx := context.WithValue(context.Background(), config.ContextKey("logger"), logger)

	Info(ctx, "info msg", "k", "v")
	Debug(ctx, "debug msg")
	Dev(ctx, "dev msg")
	Warn(ctx, "warn msg")
	Error(ctx, "error msg")

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	if len(lines) != 5 {
		t.Fatalf("got %d log lines, want 5: %s", len(lines), buf.String())
	}
	var first map[string]any
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatalf("unmarshal info line: %v", err)
	}
	if first["service"] != "anklet" || first["source"] == nil {
		t.Fatalf("expected service and source attrs in log: %v", first)
	}

	// Exercise prettylog handler methods across levels.
	pretty := NewPrettyHandler(&slog.HandlerOptions{Level: slog.LevelDebug})
	if !pretty.Enabled(ctx, slog.LevelDebug) {
		t.Fatal("expected pretty handler enabled at debug")
	}
	withAttrs := pretty.WithAttrs([]slog.Attr{slog.String("env", "test")})
	withGroup := withAttrs.WithGroup("grp")
	prettyLogger := slog.New(withGroup)
	prettyLogger.DebugContext(ctx, "pretty debug", "num", 1)
	prettyLogger.InfoContext(ctx, "pretty info")
	prettyLogger.WarnContext(ctx, "pretty warn")
	prettyLogger.ErrorContext(ctx, "pretty error")
}
