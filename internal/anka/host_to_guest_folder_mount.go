package anka

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// ResolveHostToGuestFolderMountSpec resolves symlinks in the host path portion and returns a string
// suitable for `anka -j mount <vm> <spec>`, mapping a host folder into the guest. Optional guest name
// uses a single first colon: host_path[:guest_folder_name].
func ResolveHostToGuestFolderMountSpec(mountSpec string) (string, error) {
	mountSpec = strings.TrimSpace(mountSpec)
	if mountSpec == "" {
		return "", fmt.Errorf("empty host-to-guest folder mount entry")
	}
	hostPart, guestPart, hasGuest := strings.Cut(mountSpec, ":")
	hostPart = strings.TrimSpace(hostPart)
	if hostPart == "" {
		return "", fmt.Errorf("invalid host-to-guest folder mount %q: empty host path", mountSpec)
	}
	realPath, err := filepath.EvalSymlinks(hostPart)
	if err != nil {
		return "", fmt.Errorf("eval symlinks for host-to-guest folder mount %q: %w", hostPart, err)
	}
	if !hasGuest {
		return realPath, nil
	}
	guestPart = strings.TrimSpace(guestPart)
	if guestPart == "" {
		return "", fmt.Errorf("invalid host-to-guest folder mount %q: empty guest_folder_name", mountSpec)
	}
	return realPath + ":" + guestPart, nil
}

// MergeHostToGuestFolderMountLists combines global and plugin-specific entries in that order,
// dropping duplicates that resolve to the same path (via ResolveHostToGuestFolderMountSpec).
func MergeHostToGuestFolderMountLists(globalEntries, pluginEntries []string) ([]string, error) {
	seen := make(map[string]struct{})
	var out []string
	for _, list := range [][]string{globalEntries, pluginEntries} {
		for _, raw := range list {
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				continue
			}
			resolved, err := ResolveHostToGuestFolderMountSpec(trimmed)
			if err != nil {
				return nil, err
			}
			if _, dup := seen[resolved]; dup {
				continue
			}
			seen[resolved] = struct{}{}
			out = append(out, trimmed)
		}
	}
	return out, nil
}

func (cli *Cli) mountHostToGuestFolders(pluginCtx context.Context, vmName string, mounts []string) error {
	for _, raw := range mounts {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		resolved, err := ResolveHostToGuestFolderMountSpec(trimmed)
		if err != nil {
			return err
		}
		err = cli.AnkaMount(pluginCtx, vmName, resolved)
		if err != nil {
			return err
		}
	}
	return nil
}
