package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
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
	return strings.ToUpper(os.Getenv("LOG_LEVEL")) == "DEBUG" || strings.ToUpper(os.Getenv("LOG_LEVEL")) == "DEV"
}

// getCallerSource returns the source information for the actual caller,
// skipping the logging wrapper functions
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
	logLevel := os.Getenv("LOG_LEVEL")
	var options *slog.HandlerOptions
	if strings.ToUpper(logLevel) == "DEBUG" {
		// DEBUG uses pretty handler for development readability
		handler := &ContextHandler{Handler: NewPrettyHandler(&slog.HandlerOptions{
			Level:       slog.LevelDebug,
			AddSource:   false, // We handle source manually
			ReplaceAttr: nil,
		})}
		return slog.New(handler)
	} else if strings.ToUpper(logLevel) == "DEV" {
		// DEV uses JSON handler for pure JSON output
		options = &slog.HandlerOptions{
			Level:     slog.LevelDebug,
			AddSource: false, // We handle source manually
		}
	} else if strings.ToUpper(logLevel) == "ERROR" {
		options = &slog.HandlerOptions{Level: slog.LevelError}
	} else {
		options = &slog.HandlerOptions{Level: slog.LevelInfo}
	}
	handler := &ContextHandler{Handler: slog.NewJSONHandler(os.Stdout, options)}
	return slog.New(handler)
}

func UpdateLoggerToFile(logger *slog.Logger, filePath string, suffix string) (*slog.Logger, string, error) {
	fileLocation := filePath + "anklet" + suffix + ".log"
	file, err := os.OpenFile(fileLocation, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, "", err
	}

	options := &slog.HandlerOptions{Level: slog.LevelDebug}
	handler := &ContextHandler{Handler: slog.NewJSONHandler(file, options)}

	// Copy existing logger attributes to the new logger
	if contextHandler, ok := logger.Handler().(*ContextHandler); ok {
		handler.attrs = append(handler.attrs, contextHandler.attrs...)
	}

	newLogger := slog.New(handler)
	return newLogger, fileLocation, nil
}

// Handle adds contextual attributes to the Record before calling the underlying
// handler
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

// With adds attributes to the handler
func (h *ContextHandler) With(attrs ...slog.Attr) *ContextHandler {
	h.attrs = append(h.attrs, attrs...)
	return h
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
	var attributes map[string]any
	if pluginGlobal, exists := workerGlobals.Plugins[pluginConfig.Plugin][pluginConfig.Name]; exists {
		attributes = map[string]any{
			"runs":               pluginGlobal.PluginRunCount.Load(),
			"paused":             pluginGlobal.Paused.Load(),
			"finishedInitialRun": pluginGlobal.FinishedInitialRun.Load(),
			"preparing":          pluginGlobal.Preparing.Load(),
		}
	}
	if pluginConfig.Name != "" {
		attributes["repo"] = pluginConfig.Repo
		attributes["owner"] = pluginConfig.Owner
		attributes["plugin"] = pluginConfig.Plugin
		attributes["name"] = pluginConfig.Name
	}
	return attributes
}

// AppendCtx adds an slog attribute to the provided context so that it will be
// included in any Record created with such context
func AppendCtx(ctx context.Context, attr slog.Attr) context.Context {
	if ctx == nil {
		panic("parent context required")
	}

	if v, ok := ctx.Value(slogFields).([]slog.Attr); ok {
		v = append(v, attr)
		return context.WithValue(ctx, slogFields, v)
	}

	v := []slog.Attr{}
	v = append(v, attr)
	return context.WithValue(ctx, slogFields, v)
}

func Panic(workerCtx context.Context, pluginCtx context.Context, errorMessage string) {
	logger, err := GetLoggerFromContext(pluginCtx)
	if err != nil {
		panic(err)
	}
	logger.ErrorContext(pluginCtx, errorMessage)
	panic(errorMessage)
}

func GetLoggerFromContext(ctx context.Context) (*slog.Logger, error) {
	logger, ok := ctx.Value(config.ContextKey("logger")).(*slog.Logger)
	if !ok {
		return nil, fmt.Errorf("GetLoggerFromContext failed")
	}
	return logger, nil
}

func Info(ctx context.Context, message string, args ...any) {
	logger, err := GetLoggerFromContext(ctx)
	if err != nil {
		panic(err)
	}
	pluginCtx := AppendCurrentPluginAttributes(ctx)

	// Add source information for the actual caller (skip 2: runtime.Caller, this function)
	if source := getCallerSource(2); source != nil {
		args = append(args, slog.Any("source", source))
	}

	logger.InfoContext(pluginCtx, message, args...)
}

func Dev(ctx context.Context, message string, args ...any) {
	if strings.ToUpper(os.Getenv("LOG_LEVEL")) == "DEV" {
		logger, err := GetLoggerFromContext(ctx)
		if err != nil {
			panic(err)
		}
		pluginCtx := AppendCurrentPluginAttributes(ctx)

		// Add source information for the actual caller (skip 2: runtime.Caller, this function)
		if source := getCallerSource(2); source != nil {
			args = append(args, slog.Any("source", source))
		}

		logger.DebugContext(pluginCtx, message, args...)
	}
}

func Debug(ctx context.Context, message string, args ...any) {
	logger, err := GetLoggerFromContext(ctx)
	if err != nil {
		panic(err)
	}
	pluginCtx := AppendCurrentPluginAttributes(ctx)

	// Add source information for the actual caller (skip 2: runtime.Caller, this function)
	if source := getCallerSource(2); source != nil {
		args = append(args, slog.Any("source", source))
	}

	logger.DebugContext(pluginCtx, message, args...)
}

func Warn(ctx context.Context, message string, args ...any) {
	logger, err := GetLoggerFromContext(ctx)
	if err != nil {
		panic(err)
	}
	pluginCtx := AppendCurrentPluginAttributes(ctx)

	// Add source information for the actual caller (skip 2: runtime.Caller, this function)
	if source := getCallerSource(2); source != nil {
		args = append(args, slog.Any("source", source))
	}

	logger.WarnContext(pluginCtx, message, args...)
}

func Error(ctx context.Context, message string, args ...any) {
	logger, err := GetLoggerFromContext(ctx)
	if err != nil {
		panic(err)
	}
	pluginCtx := AppendCurrentPluginAttributes(ctx)

	// Add source information for the actual caller (skip 2: runtime.Caller, this function)
	if source := getCallerSource(2); source != nil {
		args = append(args, slog.Any("source", source))
	}

	logger.ErrorContext(pluginCtx, message, args...)
}
