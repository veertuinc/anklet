// Package drain provides a host-wide "drain" signal: when the drain file
// exists, handler plugins stop picking up new jobs while continuing to run and
// finish in-progress work. Operators touch the file to drain a host and remove
// it to resume.
package drain

import (
	"os"
	"path/filepath"
)

// DrainFilePath returns the fixed, well-known drain file location:
// ~/.config/anklet/.drain
func DrainFilePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".config", "anklet", ".drain"), nil
}

// IsDraining reports whether the drain file exists. It fails open: any error
// resolving the path or stat-ing the file (other than the file existing) is
// treated as "not draining" so a transient error cannot silently halt all job
// processing on a host.
func IsDraining() bool {
	path, err := DrainFilePath()
	if err != nil {
		return false
	}
	if _, err := os.Stat(path); err != nil {
		return false
	}
	return true
}
