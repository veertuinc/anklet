package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/veertuinc/anklet/internal/config"
)

type ctxKey string

const (
	slogFields ctxKey = "slog_fields"
)

type ContextHandler struct {
	slog.Handler
	attrs []slog.Attr
}

func IsDebugEnabled() bool {
	logLevel := strings.ToUpper(os.Getenv("LOG_LEVEL"))
	return logLevel == "DEBUG" || logLevel == "DEBUG-PRETTY" || logLevel == "DEV"
}

// getCallerSource returns the source information for the actual caller,
// skipping the logging wrapper functions.
func getCallerSource(skip int) *slog.Source {
	pc, file, line, ok := runtime.Caller(skip)
	if !ok {
		return nil
	}

	fn := runtime.FuncForPC(pc)
	var name string
	if fn != nil {
		name = fn.Name()
	}

	return &slog.Source{
		Function: name,
		File:     file,
		Line:     line,
	}
}

func New() *slog.Logger {
	logLevel := strings.ToUpper(os.Getenv("LOG_LEVEL"))
	var options *slog.HandlerOptions
	switch logLevel {
	case "DEBUG-PRETTY", "DEV":
		handler := &ContextHandler{Handler: NewPrettyHandler(&slog.HandlerOptions{
			Level:       slog.LevelDebug,
			AddSource:   false,
			ReplaceAttr: nil,
		})}
		return slog.New(handler)
	case "DEBUG":
		options = &slog.HandlerOptions{
			Level:     slog.LevelDebug,
			AddSource: false,
		}
	case "ERROR":
		options = &slog.HandlerOptions{Level: slog.LevelError}
	default:
		options = &slog.HandlerOptions{Level: slog.LevelInfo}
	}
	handler := &ContextHandler{Handler: slog.NewJSONHandler(os.Stdout, options)}
	return slog.New(handler)
}

// UpdateLoggerToFile opens a plugin log file with restricted permissions (0640)
// and returns the new logger, its file path, and the open file handle so the caller
// can close it during shutdown.
func UpdateLoggerToFile(logger *slog.Logger, filePath string, suffix string) (*slog.Logger, string, *os.File, error) {
	fileLocation := filePath + "anklet" + suffix + ".log"
	file, err := os.OpenFile(fileLocation, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, "", nil, err
	}

	options := &slog.HandlerOptions{Level: slog.LevelDebug}
	handler := &ContextHandler{Handler: slog.NewJSONHandler(file, options)}

	if contextHandler, ok := logger.Handler().(*ContextHandler); ok {
		handler.attrs = slices.Clone(contextHandler.attrs)
	}

	return slog.New(handler), fileLocation, file, nil
}

// Handle adds contextual attributes to the Record before calling the underlying handler.
func (h *ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctxAttrs, ok := ctx.Value(slogFields).([]slog.Attr); ok {
		for _, v := range ctxAttrs {
			r.AddAttrs(v)
		}
	}
	for _, v := range h.attrs {
		r.AddAttrs(v)
	}
	return h.Handler.Handle(ctx, r)
}

// With returns a derived handler containing the additional attributes.
func (h *ContextHandler) With(attrs ...slog.Attr) *ContextHandler {
	cloned := slices.Clone(h.attrs)
	cloned = append(cloned, attrs...)
	return &ContextHandler{
		Handler: h.Handler,
		attrs:   cloned,
	}
}

func AppendCurrentPluginAttributes(pluginCtx context.Context) context.Context {
	attributes := GetPluginAttributes(pluginCtx)
	return AppendCtx(pluginCtx, slog.Any("attributes", attributes))
}

func GetPluginAttributes(ctx context.Context) map[string]any {
	workerGlobals, err := config.GetWorkerGlobalsFromContext(ctx)
	if err != nil {
		return map[string]any{}
	}
	pluginConfig, _ := config.GetPluginFromContext(ctx)
	attributes := map[string]any{}
	if pluginGroup, ok := workerGlobals.Plugins[pluginConfig.Plugin]; ok {
		if pluginGlobal, exists := pluginGroup[pluginConfig.Name]; exists {
			attributes["runs"] = pluginGlobal.PluginRunCount.Load()
			attributes["paused"] = pluginGlobal.Paused.Load()
			attributes["finishedInitialRun"] = pluginGlobal.FinishedInitialRun.Load()
			attributes["preparing"] = pluginGlobal.Preparing.Load()
		}
	}
	if pluginConfig.Name != "" {
		attributes["repo"] = pluginConfig.Repo
		attributes["owner"] = pluginConfig.Owner
		attributes["enterprise"] = pluginConfig.Enterprise
		attributes["plugin"] = pluginConfig.Plugin
		attributes["name"] = pluginConfig.Name
	}
	return attributes
}

// AppendCtx returns a derived context with the provided slog attributes without
// mutating any parent context's attribute slice.
func AppendCtx(ctx context.Context, attrs ...slog.Attr) context.Context {
	if ctx == nil {
		panic("parent context required")
	}
	if len(attrs) == 0 {
		return ctx
	}

	var next []slog.Attr
	if existing, ok := ctx.Value(slogFields).([]slog.Attr); ok {
		next = make([]slog.Attr, len(existing), len(existing)+len(attrs))
		copy(next, existing)
	} else {
		next = make([]slog.Attr, 0, len(attrs))
	}

	for _, attr := range attrs {
		idx := slices.IndexFunc(next, func(existingAttr slog.Attr) bool {
			return existingAttr.Key == attr.Key
		})
		if idx >= 0 {
			next[idx] = attr
		} else {
			next = append(next, attr)
		}
	}
	return context.WithValue(ctx, slogFields, next)
}

func GetLoggerFromContext(ctx context.Context) (*slog.Logger, error) {
	logger, ok := ctx.Value(config.ContextKey("logger")).(*slog.Logger)
	if !ok || logger == nil {
		return nil, fmt.Errorf("GetLoggerFromContext failed")
	}
	return logger, nil
}

func logWithCaller(ctx context.Context, level slog.Level, message string, args ...any) {
	logger, err := GetLoggerFromContext(ctx)
	if err != nil {
		panic(err)
	}
	pluginCtx := AppendCurrentPluginAttributes(ctx)

	// skip 3: runtime.Caller, getCallerSource, logWithCaller, public wrapper
	if source := getCallerSource(3); source != nil {
		args = append(args, slog.Any("source", source))
	}

	logger.Log(pluginCtx, level, message, args...)
}

func Info(ctx context.Context, message string, args ...any) {
	logWithCaller(ctx, slog.LevelInfo, message, args...)
}

func Dev(ctx context.Context, message string, args ...any) {
	logLevel := strings.ToUpper(os.Getenv("LOG_LEVEL"))
	if logLevel == "DEV" || logLevel == "DEBUG-PRETTY" {
		logWithCaller(ctx, slog.LevelDebug, message, args...)
	}
}

func Debug(ctx context.Context, message string, args ...any) {
	logWithCaller(ctx, slog.LevelDebug, message, args...)
}

func Warn(ctx context.Context, message string, args ...any) {
	logWithCaller(ctx, slog.LevelWarn, message, args...)
}

func Error(ctx context.Context, message string, args ...any) {
	logWithCaller(ctx, slog.LevelError, message, args...)
}
