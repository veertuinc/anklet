package anka

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveHostToGuestFolderMountSpec_hostOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sub := filepath.Join(dir, "cache")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveHostToGuestFolderMountSpec(sub)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(sub)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveHostToGuestFolderMountSpec_symlink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveHostToGuestFolderMountSpec(link)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveHostToGuestFolderMountSpec_withGuest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	in := dir + ":myguest"
	got, err := ResolveHostToGuestFolderMountSpec(in)
	if err != nil {
		t.Fatal(err)
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := resolvedDir + ":myguest"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveHostToGuestFolderMountSpec_empty(t *testing.T) {
	t.Parallel()
	if _, err := ResolveHostToGuestFolderMountSpec(""); err == nil {
		t.Error("expected error")
	}
}

func TestMergeHostToGuestFolderMountLists_dedupeAndOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b, 0o755); err != nil {
		t.Fatal(err)
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatal(err)
	}
	if ra == rb {
		t.Fatal("paths should differ for this test")
	}

	global := []string{a, a}
	plugin := []string{a, b}
	out, err := MergeHostToGuestFolderMountLists(global, plugin)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("len got %d, want 2: %v", len(out), out)
	}
	if out[0] != a || out[1] != b {
		t.Errorf("order or dedupe wrong: %v", out)
	}
}

func TestMergeHostToGuestFolderMountLists_symlinkDedupe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	out, err := MergeHostToGuestFolderMountLists([]string{real}, []string{link})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 unique mount after symlink dedupe, got %v", out)
	}
}
