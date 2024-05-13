package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/metrics"
)

func New() *slog.Logger {
	logLevel := os.Getenv("LOG_LEVEL")
	var options *slog.HandlerOptions
	if logLevel == "dev" {
		handler := &ContextHandler{Handler: NewPrettyHandler(&slog.HandlerOptions{
			Level:       slog.LevelDebug,
			AddSource:   true,
			ReplaceAttr: nil,
		})}
		return slog.New(handler)
	} else if strings.ToUpper(logLevel) == "DEBUG" {
		options = &slog.HandlerOptions{Level: slog.LevelDebug}
	} else if strings.ToUpper(logLevel) == "ERROR" {
		options = &slog.HandlerOptions{Level: slog.LevelError}
	} else {
		options = &slog.HandlerOptions{Level: slog.LevelInfo}
	}
	handler := &ContextHandler{Handler: slog.NewJSONHandler(os.Stdout, options)}
	return slog.New(handler)
}

type ctxKey string

const (
	slogFields ctxKey = "slog_fields"
)

type ContextHandler struct {
	slog.Handler
}

// Handle adds contextual attributes to the Record before calling the underlying
// handler
func (h ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if attrs, ok := ctx.Value(slogFields).([]slog.Attr); ok {
		for _, v := range attrs {
			r.AddAttrs(v)
		}
	}

	return h.Handler.Handle(ctx, r)
}

// AppendCtx adds an slog attribute to the provided context so that it will be
// included in any Record created with such context
func AppendCtx(parent context.Context, attr slog.Attr) context.Context {
	if parent == nil {
		panic("parent context required")
	}

	if v, ok := parent.Value(slogFields).([]slog.Attr); ok {
		v = append(v, attr)
		return context.WithValue(parent, slogFields, v)
	}

	v := []slog.Attr{}
	v = append(v, attr)
	return context.WithValue(parent, slogFields, v)
}

func Panic(workerCtx context.Context, serviceCtx context.Context, errorMessage string) {
	logger := GetLoggerFromContext(serviceCtx)
	logger.ErrorContext(serviceCtx, errorMessage)
	metrics.UpdateService(workerCtx, serviceCtx, logger, metrics.Service{
		Status:        "failed",
		LastFailedRun: time.Now(),
	})
	panic(errorMessage)
}

func GetLoggerFromContext(ctx context.Context) *slog.Logger {
	logger, ok := ctx.Value(config.ContextKey("logger")).(*slog.Logger)
	if !ok {
		panic("GetLoggerFromContext failed")
	}
	return logger
}
