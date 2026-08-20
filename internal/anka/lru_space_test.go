package anka

import "testing"

func TestTemplateDownloadSize(t *testing.T) {
	tests := []struct {
		name   string
		size   uint64
		cached uint64
		want   uint64
	}{
		{
			name:   "normal download needed",
			size:   100,
			cached: 40,
			want:   60,
		},
		{
			name:   "nothing cached",
			size:   100,
			cached: 0,
			want:   100,
		},
		{
			name:   "fully cached",
			size:   100,
			cached: 100,
			want:   0,
		},
		{
			name:   "cached greater than size does not underflow",
			size:   33455865856,
			cached: 34234957824,
			want:   0,
		},
		{
			name:   "zero size",
			size:   0,
			cached: 0,
			want:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := templateDownloadSize(tt.size, tt.cached)
			if got != tt.want {
				t.Errorf("templateDownloadSize(%d, %d) = %d, want %d", tt.size, tt.cached, got, tt.want)
			}
		})
	}
}
