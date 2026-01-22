package config

import (
	"testing"
)

func TestPlugin_GetQueueOwner(t *testing.T) {
	tests := []struct {
		name      string
		plugin    Plugin
		want      string
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
