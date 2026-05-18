package anka

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	minHostToGuestFolderMountMajor = 3
	minHostToGuestFolderMountMinor = 9
	minHostToGuestFolderMountPatch = 0
)

// NormalizeAnkaVersion trims the CLI version string to the leading semantic portion (e.g. "3.9.0 (build …)" → "3.9.0").
func NormalizeAnkaVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return v
	}
	if i := strings.IndexByte(v, ' '); i >= 0 {
		v = v[:i]
	}
	v = strings.TrimPrefix(v, "v")
	return v
}

// ValidateAnkaVersionForHostToGuestFolderMounts returns an error if the Anka CLI is older than 3.9.0
// (required for mounting host folders into the guest VM).
func ValidateAnkaVersionForHostToGuestFolderMounts(ankaVersion string) error {
	ok, err := AnkaVersionAtLeast(ankaVersion, minHostToGuestFolderMountMajor, minHostToGuestFolderMountMinor, minHostToGuestFolderMountPatch)
	if err != nil {
		return fmt.Errorf("anka version for host-to-guest folder mounts: %w", err)
	}
	if !ok {
		return fmt.Errorf("mounting host folders into the guest VM requires Anka >= 3.9.0, got %q", NormalizeAnkaVersion(ankaVersion))
	}
	return nil
}

// AnkaVersionAtLeast reports whether v compares >= major.minor.patch (numeric segments only).
func AnkaVersionAtLeast(v string, major, minor, patch int) (bool, error) {
	have, err := parseAnkaVersionTriple(NormalizeAnkaVersion(v))
	if err != nil {
		return false, err
	}
	want := [3]int{major, minor, patch}
	for i := 0; i < 3; i++ {
		if have[i] > want[i] {
			return true, nil
		}
		if have[i] < want[i] {
			return false, nil
		}
	}
	return true, nil
}

func parseAnkaVersionTriple(v string) ([3]int, error) {
	var z [3]int
	if v == "" {
		return z, fmt.Errorf("empty anka version")
	}
	parts := strings.Split(v, ".")
	if len(parts) < 3 {
		return z, fmt.Errorf("anka version %q has fewer than 3 components", v)
	}
	for i := 0; i < 3; i++ {
		seg := parts[i]
		seg = stripNumericPrefix(seg)
		n, err := strconv.Atoi(seg)
		if err != nil {
			return z, fmt.Errorf("anka version segment %q: %w", parts[i], err)
		}
		z[i] = n
	}
	return z, nil
}

func stripNumericPrefix(s string) string {
	for i, r := range s {
		if r < '0' || r > '9' {
			return s[:i]
		}
	}
	return s
}
