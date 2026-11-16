package appdata

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata/appdatatest"
	"github.com/gjrud/steam-achievement-tracker/internal/config"
)

func skipPermissionCheckOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
}

func TestResolveBuildsPathsUnderXDGDirs(t *testing.T) {
	dirs := appdatatest.Isolate(t)

	paths := Resolve()

	dataDir := filepath.Join(dirs.Data, config.AppDirName)
	cacheDir := filepath.Join(dirs.Cache, config.AppDirName)
	stateDir := filepath.Join(dirs.State, config.AppDirName)
	want := Paths{
		DataDir:    dataDir,
		CacheDir:   cacheDir,
		StateDir:   stateDir,
		DB:         filepath.Join(dataDir, config.DBFileName),
		Backups:    filepath.Join(dataDir, "backups"),
		GameImages: filepath.Join(cacheDir, "images", "games"),
		Logs:       filepath.Join(stateDir, "logs"),
		LogFile:    filepath.Join(stateDir, "logs", "app.log"),
	}
	if paths != want {
		t.Fatalf("Resolve() = %+v, want %+v", paths, want)
	}
}

func TestResolveDoesNotCreateDirectories(t *testing.T) {
	appdatatest.Isolate(t)

	paths := Resolve()

	if _, err := os.Stat(paths.DataDir); !os.IsNotExist(err) {
		t.Fatalf("Resolve() created %q (stat err = %v), want nothing on disk", paths.DataDir, err)
	}
}

func TestEnsureCreatesDirectoriesWith0700(t *testing.T) {
	skipPermissionCheckOnWindows(t)
	appdatatest.Isolate(t)
	paths := Resolve()

	if err := Ensure(paths); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	for name, dir := range map[string]string{
		"DataDir":    paths.DataDir,
		"CacheDir":   paths.CacheDir,
		"StateDir":   paths.StateDir,
		"Backups":    paths.Backups,
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
	skipPermissionCheckOnWindows(t)
	appdatatest.Isolate(t)
	paths := Resolve()

	if err := os.MkdirAll(paths.DataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.Chmod(paths.DataDir, 0o755); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if err := Ensure(paths); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}

	info, err := os.Stat(paths.DataDir)
	if err != nil {
		t.Fatalf("stat data dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("data dir mode = %o, want 700", perm)
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	appdatatest.Isolate(t)
	paths := Resolve()

	if err := Ensure(paths); err != nil {
		t.Fatalf("first Ensure() error = %v", err)
	}

	marker := filepath.Join(paths.CacheDir, "marker.txt")
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

func TestEnsureReturnsErrorWhenBaseDirIsAFile(t *testing.T) {
	// The XDG base directories sit under a regular file, so no app directory can be created below them.
	appdatatest.Block(t)
	paths := Resolve()

	if err := Ensure(paths); err == nil {
		t.Fatal("Ensure() error = nil, want error when a base directory is under a file")
	}
}

func TestEnsureReturnsErrorWhenDataDirIsAFile(t *testing.T) {
	dirs := appdatatest.Isolate(t)
	paths := Resolve()

	// A regular file where the data directory should be makes MkdirAll fail.
	if err := os.MkdirAll(dirs.Data, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(paths.DataDir, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := Ensure(paths); err == nil {
		t.Fatal("Ensure() error = nil, want error when data dir is a file")
	}
}
