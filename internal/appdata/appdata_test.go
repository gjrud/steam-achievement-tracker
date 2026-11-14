package appdata

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/config"
)

func TestResolveBuildsPathsUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	paths, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	root := filepath.Join(home, config.DataDirName)
	want := Paths{
		Root:       root,
		DB:         filepath.Join(root, config.DBFileName),
		Backups:    filepath.Join(root, "backups"),
		Cache:      filepath.Join(root, "cache"),
		GameImages: filepath.Join(root, "cache", "images", "games"),
		Logs:       filepath.Join(root, "logs"),
		LogFile:    filepath.Join(root, "logs", "app.log"),
	}
	if paths != want {
		t.Fatalf("Resolve() = %+v, want %+v", paths, want)
	}
}

func TestResolveFailsWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")

	if _, err := Resolve(); err == nil {
		t.Fatal("Resolve() error = nil, want error when HOME is unset")
	}
}

func TestEnsureCreatesDirectoriesWith0700(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	paths, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if err := Ensure(paths); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	for name, dir := range map[string]string{
		"Root":       paths.Root,
		"Backups":    paths.Backups,
		"Cache":      paths.Cache,
		"GameImages": paths.GameImages,
		"Logs":       paths.Logs,
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("%s: stat %q: %v", name, dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s: %q is not a directory", name, dir)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("%s: mode = %o, want 700", name, perm)
		}
	}
}

func TestEnsureTightensExistingDirectoryMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	paths, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if err := os.MkdirAll(paths.Root, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(paths.Root, 0o755); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if err := Ensure(paths); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	info, err := os.Stat(paths.Root)
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("root mode = %o, want 700", perm)
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	paths, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if err := Ensure(paths); err != nil {
		t.Fatalf("first Ensure() error = %v", err)
	}

	marker := filepath.Join(paths.Cache, "marker.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := Ensure(paths); err != nil {
		t.Fatalf("second Ensure() error = %v", err)
	}

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("marker removed by second Ensure: %v", err)
	}
	if string(data) != "keep" {
		t.Fatalf("marker content = %q, want %q", data, "keep")
	}
}

func TestEnsureReturnsErrorWhenPathBlockedByFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// A regular file where the data root should be makes MkdirAll fail.
	blocker := filepath.Join(home, config.DataDirName)
	if err := os.WriteFile(blocker, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	paths, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if err := Ensure(paths); err == nil {
		t.Fatal("Ensure() error = nil, want error when root is a file")
	}
}
