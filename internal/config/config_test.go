package config

import (
	"testing"
)

func TestPlugin_GetQueueOwner(t *testing.T) {
	tests := []struct {
		name   string
		plugin Plugin
		want   string
	}{
		{
			name: "returns QueueName when set",
			plugin: Plugin{
				Owner:     "org1",
				QueueName: "shared_queue",
			},
			want: "shared_queue",
		},
		{
			name: "returns Owner when QueueName is empty",
			plugin: Plugin{
				Owner:     "org1",
				QueueName: "",
			},
			want: "org1",
		},
		{
			name: "returns Owner when QueueName is not set",
			plugin: Plugin{
				Owner: "veertuinc",
			},
			want: "veertuinc",
		},
		{
			name: "returns empty string when both are empty",
			plugin: Plugin{
				Owner:     "",
				QueueName: "",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.plugin.GetQueueOwner()
			if got != tt.want {
				t.Errorf("GetQueueOwner() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizePluginEnvPrefix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple uppercase",
			input: "GITHUB_HANDLER1",
			want:  "GITHUB_HANDLER1",
		},
		{
			name:  "lowercase to uppercase",
			input: "github_handler1",
			want:  "GITHUB_HANDLER1",
		},
		{
			name:  "mixed case with numbers",
			input: "GITHUB_HANDLER1_8_L_ARM_MACOS",
			want:  "GITHUB_HANDLER1_8_L_ARM_MACOS",
		},
		{
			name:  "hyphens become underscores",
			input: "github-handler-1",
			want:  "GITHUB_HANDLER_1",
		},
		{
			name:  "dots become underscores",
			input: "github.handler.1",
			want:  "GITHUB_HANDLER_1",
		},
		{
			name:  "spaces become underscores",
			input: "github handler 1",
			want:  "GITHUB_HANDLER_1",
		},
		{
			name:  "special chars become underscores",
			input: "handler@org#1",
			want:  "HANDLER_ORG_1",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizePluginEnvPrefix(tt.input)
			if got != tt.want {
				t.Errorf("normalizePluginEnvPrefix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeEnvKey(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple yaml key",
			input: "token",
			want:  "TOKEN",
		},
		{
			name:  "yaml key with underscore",
			input: "private_key",
			want:  "PRIVATE_KEY",
		},
		{
			name:  "yaml key with hyphen",
			input: "app-id",
			want:  "APP_ID",
		},
		{
			name:  "mixed case",
			input: "QueueName",
			want:  "QUEUENAME",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeEnvKey(tt.input)
			if got != tt.want {
				t.Errorf("normalizeEnvKey(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestApplyPluginEnvOverrides(t *testing.T) {
	tests := []struct {
		name         string
		plugin       Plugin
		envVars      map[string]string
		pluginPrefix string
		wantToken    string
		wantOwner    string
		wantAppID    int64
		wantDBURL    string
		wantDBPort   int
		wantErr      bool
	}{
		{
			name:         "token override",
			plugin:       Plugin{Name: "TEST_PLUGIN", Token: "original"},
			envVars:      map[string]string{"TEST_PLUGIN_TOKEN": "new_token"},
			pluginPrefix: "TEST_PLUGIN",
			wantToken:    "new_token",
		},
		{
			name:         "owner override",
			plugin:       Plugin{Name: "TEST_PLUGIN", Owner: "original_owner"},
			envVars:      map[string]string{"TEST_PLUGIN_OWNER": "new_owner"},
			pluginPrefix: "TEST_PLUGIN",
			wantOwner:    "new_owner",
		},
		{
			name:         "app_id override (int64)",
			plugin:       Plugin{Name: "TEST_PLUGIN", AppID: 100},
			envVars:      map[string]string{"TEST_PLUGIN_APP_ID": "200"},
			pluginPrefix: "TEST_PLUGIN",
			wantAppID:    200,
		},
		{
			name:         "database url override",
			plugin:       Plugin{Name: "TEST_PLUGIN", Database: Database{URL: "original"}},
			envVars:      map[string]string{"TEST_PLUGIN_DATABASE_URL": "new_url"},
			pluginPrefix: "TEST_PLUGIN",
			wantDBURL:    "new_url",
		},
		{
			name:         "database port override",
			plugin:       Plugin{Name: "TEST_PLUGIN", Database: Database{Port: 6379}},
			envVars:      map[string]string{"TEST_PLUGIN_DATABASE_PORT": "6380"},
			pluginPrefix: "TEST_PLUGIN",
			wantDBPort:   6380,
		},
		{
			name:         "multiple overrides",
			plugin:       Plugin{Name: "TEST_PLUGIN", Token: "old_token", Owner: "old_owner"},
			envVars:      map[string]string{"TEST_PLUGIN_TOKEN": "new_token", "TEST_PLUGIN_OWNER": "new_owner"},
			pluginPrefix: "TEST_PLUGIN",
			wantToken:    "new_token",
			wantOwner:    "new_owner",
		},
		{
			name:         "unrelated env vars ignored",
			plugin:       Plugin{Name: "TEST_PLUGIN", Token: "original"},
			envVars:      map[string]string{"OTHER_PLUGIN_TOKEN": "other_token"},
			pluginPrefix: "TEST_PLUGIN",
			wantToken:    "original",
		},
		{
			name:         "complex plugin name with special chars",
			plugin:       Plugin{Name: "GITHUB_HANDLER1_8_L_ARM_MACOS", Token: "old"},
			envVars:      map[string]string{"GITHUB_HANDLER1_8_L_ARM_MACOS_TOKEN": "new_token"},
			pluginPrefix: "GITHUB_HANDLER1_8_L_ARM_MACOS",
			wantToken:    "new_token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set test env vars using t.Setenv (automatically cleaned up after test)
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			plugin := tt.plugin
			err := applyPluginEnvOverrides(&plugin, tt.pluginPrefix)

			if (err != nil) != tt.wantErr {
				t.Errorf("applyPluginEnvOverrides() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantToken != "" && plugin.Token != tt.wantToken {
				t.Errorf("Token = %q, want %q", plugin.Token, tt.wantToken)
			}
			if tt.wantOwner != "" && plugin.Owner != tt.wantOwner {
				t.Errorf("Owner = %q, want %q", plugin.Owner, tt.wantOwner)
			}
			if tt.wantAppID != 0 && plugin.AppID != tt.wantAppID {
				t.Errorf("AppID = %d, want %d", plugin.AppID, tt.wantAppID)
			}
			if tt.wantDBURL != "" && plugin.Database.URL != tt.wantDBURL {
				t.Errorf("Database.URL = %q, want %q", plugin.Database.URL, tt.wantDBURL)
			}
			if tt.wantDBPort != 0 && plugin.Database.Port != tt.wantDBPort {
				t.Errorf("Database.Port = %d, want %d", plugin.Database.Port, tt.wantDBPort)
			}
		})
	}
}

func TestApplyPluginEnvOverrides_BoolFields(t *testing.T) {
	tests := []struct {
		name         string
		envVars      map[string]string
		pluginPrefix string
		wantSkipPull bool
	}{
		{
			name:         "skip_pull true",
			envVars:      map[string]string{"TEST_PLUGIN_SKIP_PULL": "true"},
			pluginPrefix: "TEST_PLUGIN",
			wantSkipPull: true,
		},
		{
			name:         "skip_pull false",
			envVars:      map[string]string{"TEST_PLUGIN_SKIP_PULL": "false"},
			pluginPrefix: "TEST_PLUGIN",
			wantSkipPull: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			plugin := Plugin{Name: "TEST_PLUGIN"}
			err := applyPluginEnvOverrides(&plugin, tt.pluginPrefix)
			if err != nil {
				t.Errorf("applyPluginEnvOverrides() error = %v", err)
				return
			}

			if plugin.SkipPull != tt.wantSkipPull {
				t.Errorf("SkipPull = %v, want %v", plugin.SkipPull, tt.wantSkipPull)
			}
		})
	}
}

func TestApplyPluginEnvOverrides_FloatFields(t *testing.T) {
	tests := []struct {
		name                   string
		envVars                map[string]string
		pluginPrefix           string
		wantTemplateDiskBuffer float64
	}{
		{
			name:                   "template_disk_buffer",
			envVars:                map[string]string{"TEST_PLUGIN_TEMPLATE_DISK_BUFFER": "15.5"},
			pluginPrefix:           "TEST_PLUGIN",
			wantTemplateDiskBuffer: 15.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			plugin := Plugin{Name: "TEST_PLUGIN"}
			err := applyPluginEnvOverrides(&plugin, tt.pluginPrefix)
			if err != nil {
				t.Errorf("applyPluginEnvOverrides() error = %v", err)
				return
			}

			if plugin.TemplateDiskBuffer != tt.wantTemplateDiskBuffer {
				t.Errorf("TemplateDiskBuffer = %f, want %f", plugin.TemplateDiskBuffer, tt.wantTemplateDiskBuffer)
			}
		})
	}
}

func TestApplyPluginEnvOverrides_InvalidValues(t *testing.T) {
	tests := []struct {
		name         string
		envVars      map[string]string
		pluginPrefix string
		wantErr      bool
	}{
		{
			name:         "invalid int value",
			envVars:      map[string]string{"TEST_PLUGIN_APP_ID": "not_a_number"},
			pluginPrefix: "TEST_PLUGIN",
			wantErr:      true,
		},
		{
			name:         "invalid float value",
			envVars:      map[string]string{"TEST_PLUGIN_TEMPLATE_DISK_BUFFER": "not_a_float"},
			pluginPrefix: "TEST_PLUGIN",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			plugin := Plugin{Name: "TEST_PLUGIN"}
			err := applyPluginEnvOverrides(&plugin, tt.pluginPrefix)

			if (err != nil) != tt.wantErr {
				t.Errorf("applyPluginEnvOverrides() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
