package drain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDrainFilePath(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	got, err := DrainFilePath()
	if err != nil {
		t.Fatalf("DrainFilePath() returned error: %v", err)
	}
	want := filepath.Join(tmpHome, ".config", "anklet", ".drain")
	if got != want {
		t.Errorf("DrainFilePath() = %q, want %q", got, want)
	}
}

func TestIsDrainingFalseWhenAbsent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if IsDraining() {
		t.Errorf("IsDraining() = true with no drain file, want false")
	}
}

func TestIsDrainingTrueWhenPresent(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	drainPath := filepath.Join(tmpHome, ".config", "anklet", ".drain")
	if err := os.MkdirAll(filepath.Dir(drainPath), 0o755); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	if err := os.WriteFile(drainPath, []byte{}, 0o644); err != nil {
		t.Fatalf("failed to create drain file: %v", err)
	}

	if !IsDraining() {
		t.Errorf("IsDraining() = false with drain file present, want true")
	}
}
