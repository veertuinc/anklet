package metrics

import "testing"

func TestRedisMetricsKey(t *testing.T) {
	tests := []struct {
		name  string
		owner string
		pluginName string
		want  string
	}{
		{
			name:       "owner and name",
			owner:      "veertuinc",
			pluginName: "GITHUB_RECEIVER",
			want:       "anklet/metrics/veertuinc/GITHUB_RECEIVER",
		},
		{
			name:       "empty owner omits segment",
			owner:      "",
			pluginName: "GITHUB_RECEIVER",
			want:       "anklet/metrics/GITHUB_RECEIVER",
		},
		{
			name:       "enterprise slug as owner",
			owner:      "veertu-inc",
			pluginName: "GITHUB_RECEIVER",
			want:       "anklet/metrics/veertu-inc/GITHUB_RECEIVER",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redisMetricsKey(tt.owner, tt.pluginName)
			if got != tt.want {
				t.Errorf("redisMetricsKey(%q, %q) = %q, want %q", tt.owner, tt.pluginName, got, tt.want)
			}
		})
	}
}
