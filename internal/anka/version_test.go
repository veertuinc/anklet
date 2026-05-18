package anka

import "testing"

func TestNormalizeAnkaVersion(t *testing.T) {
	t.Parallel()
	if got := NormalizeAnkaVersion("  3.9.0 (build 123)  "); got != "3.9.0" {
		t.Errorf("NormalizeAnkaVersion = %q, want 3.9.0", got)
	}
	if got := NormalizeAnkaVersion("v4.0.1"); got != "4.0.1" {
		t.Errorf("NormalizeAnkaVersion = %q, want 4.0.1", got)
	}
}

func TestAnkaVersionAtLeast(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		v       string
		wantOK  bool
		wantErr bool
	}{
		{"below patch", "3.8.9", false, false},
		{"exact", "3.9.0", true, false},
		{"newer minor", "3.10.0", true, false},
		{"newer patch", "3.9.1", true, false},
		{"with suffix", "3.9.0 (build x)", true, false},
		{"empty", "", false, true},
		{"short", "3.9", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ok, err := AnkaVersionAtLeast(tt.v, 3, 9, 0)
			if (err != nil) != tt.wantErr {
				t.Fatalf("AnkaVersionAtLeast err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if ok != tt.wantOK {
				t.Errorf("AnkaVersionAtLeast(%q) = %v, want %v", tt.v, ok, tt.wantOK)
			}
		})
	}
}

func TestValidateAnkaVersionForHostToGuestFolderMounts(t *testing.T) {
	t.Parallel()
	if err := ValidateAnkaVersionForHostToGuestFolderMounts("3.9.0"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if err := ValidateAnkaVersionForHostToGuestFolderMounts("3.8.9"); err == nil {
		t.Error("expected error for 3.8.9")
	}
}
