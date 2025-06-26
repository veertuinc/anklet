package logging

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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

func New() *slog.Logger {
	logLevel := os.Getenv("LOG_LEVEL")
	var options *slog.HandlerOptions
	if IsDebugEnabled() {
		handler := &ContextHandler{Handler: NewPrettyHandler(&slog.HandlerOptions{
			Level:       slog.LevelDebug,
			AddSource:   true,
			ReplaceAttr: nil,
		})}
		return slog.New(handler)
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

func DevContext(ctx context.Context, message string) {
	if strings.ToUpper(os.Getenv("LOG_LEVEL")) == "DEV" {
		logger, err := GetLoggerFromContext(ctx)
		if err != nil {
			panic(err)
		}
		logger.DebugContext(ctx, message)
	}
}

func GetLoggerFromContext(ctx context.Context) (*slog.Logger, error) {
	logger, ok := ctx.Value(config.ContextKey("logger")).(*slog.Logger)
	if !ok {
		return nil, fmt.Errorf("GetLoggerFromContext failed")
	}
	return logger, nil
}
