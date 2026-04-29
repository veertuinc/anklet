package jobqueue

import "testing"

func TestStringifyJSONPath(t *testing.T) {
	root := map[string]any{
		"workflow_job": map[string]any{
			"id":     float64(123456789),
			"name":   "build",
			"active": true,
		},
		"missing": nil,
	}
	tests := []struct {
		name string
		path []string
		want string
	}{
		{"top-level string", []string{"workflow_job", "name"}, "build"},
		{"top-level number formats as int", []string{"workflow_job", "id"}, "123456789"},
		{"top-level bool", []string{"workflow_job", "active"}, "true"},
		{"missing leaf", []string{"workflow_job", "nope"}, ""},
		{"nil mid-path", []string{"missing", "anything"}, ""},
		{"non-map mid-path", []string{"workflow_job", "name", "x"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stringifyJSONPath(root, tt.path)
			if got != tt.want {
				t.Errorf("stringifyJSONPath(%v) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
