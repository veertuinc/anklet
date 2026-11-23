package run

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/plugin"
)

type stubRunner struct {
	role    plugin.Role
	runFunc func(workerCtx, pluginCtx context.Context, pluginCancel context.CancelFunc) (context.Context, error)
}

func (s *stubRunner) Role() plugin.Role { return s.role }
func (s *stubRunner) Run(workerCtx, pluginCtx context.Context, pluginCancel context.CancelFunc) (context.Context, error) {
	return s.runFunc(workerCtx, pluginCtx, pluginCancel)
}

func TestRegistryBuildAndRoles(t *testing.T) {
	t.Parallel()

	if !IsReceiverType("github_receiver") {
		t.Error("expected github_receiver to be a receiver")
	}
	if IsReceiverType("github") {
		t.Error("expected github to be a handler, not receiver")
	}
	if _, ok := LookupRole("does_not_exist"); ok {
		t.Error("expected LookupRole to return false for unknown plugin")
	}

	h, err := Build(plugin.Deps{PluginConfig: config.Plugin{Name: "H1", Plugin: "github"}})
	if err != nil || h.Role() != plugin.RoleHandler {
		t.Fatalf("Build(github) = (%v, %v), want RoleHandler", h, err)
	}

	r, err := Build(plugin.Deps{PluginConfig: config.Plugin{Name: "R1", Plugin: "github_receiver"}})
	if err != nil || r.Role() != plugin.RoleReceiver {
		t.Fatalf("Build(github_receiver) = (%v, %v), want RoleReceiver", r, err)
	}

	_, err = Build(plugin.Deps{PluginConfig: config.Plugin{Name: "Empty", Plugin: ""}})
	if err == nil || !strings.Contains(err.Error(), "plugin is not set") {
		t.Fatalf("Build(empty) error = %v", err)
	}

	_, err = Build(plugin.Deps{PluginConfig: config.Plugin{Name: "X1", Plugin: "unknown"}})
	if err == nil || !strings.Contains(err.Error(), "plugin not supported") {
		t.Fatalf("Build(unknown) error = %v, want 'plugin not supported'", err)
	}
}

func TestPluginExecutionLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("nil runner returns unsupported error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := Plugin(ctx, ctx, cancel, nil)
		if err == nil || !strings.Contains(err.Error(), "plugin not supported") {
			t.Fatalf("expected plugin not supported error, got %v", err)
		}
	})

	t.Run("canceled context skips runner execution", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		called := false
		runner := &stubRunner{
			role: plugin.RoleHandler,
			runFunc: func(_, pCtx context.Context, _ context.CancelFunc) (context.Context, error) {
				called = true
				return pCtx, nil
			},
		}
		_, err := Plugin(context.Background(), ctx, cancel, runner)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if called {
			t.Fatal("runner should not execute when pluginCtx is already canceled")
		}
	})

	t.Run("propagates updated context and wrapped error", func(t *testing.T) {
		type k struct{}
		wantErr := errors.New("boom")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		okRunner := &stubRunner{
			role: plugin.RoleHandler,
			runFunc: func(_, pCtx context.Context, _ context.CancelFunc) (context.Context, error) {
				return context.WithValue(pCtx, k{}, "updated"), nil
			},
		}
		gotCtx, err := Plugin(ctx, ctx, cancel, okRunner)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotCtx.Value(k{}) != "updated" {
			t.Fatalf("updated context not returned")
		}

		errRunner := &stubRunner{
			role: plugin.RoleHandler,
			runFunc: func(_, pCtx context.Context, _ context.CancelFunc) (context.Context, error) {
				return pCtx, wantErr
			},
		}
		_, err = Plugin(ctx, ctx, cancel, errRunner)
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want wrapped %v", err, wantErr)
		}
	})
}
