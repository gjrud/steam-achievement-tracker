// Package appdatatest isolates tests from the real user profile by pointing the XDG base directories at temp
// directories. It is for tests only.
package appdatatest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/adrg/xdg"
)

// Dirs are the XDG base directories a test has been isolated under.
type Dirs struct {
	Data  string // XDG_DATA_HOME
	Cache string // XDG_CACHE_HOME
	State string // XDG_STATE_HOME
}

// Isolate points XDG_DATA_HOME, XDG_CACHE_HOME and XDG_STATE_HOME at fresh subdirectories of a temp dir and reloads
// xdg. The variables are restored, and xdg reloaded, when the test ends.
func Isolate(t *testing.T) Dirs {
	t.Helper()
	return set(t, t.TempDir())
}

// Block points the XDG base directories under a regular file, so appdata.Ensure fails because a directory cannot be
// created below a file. It returns the blocking file's path.
func Block(t *testing.T) string {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	set(t, blocker)
	return blocker
}

func set(t *testing.T, base string) Dirs {
	t.Helper()
	// Cleanups run last-in first-out. Registering the reload before t.Setenv makes it run after the variables are
	// restored, so xdg ends up matching the real environment again.
	t.Cleanup(xdg.Reload)
	dirs := Dirs{
		Data:  filepath.Join(base, "data"),
		Cache: filepath.Join(base, "cache"),
		State: filepath.Join(base, "state"),
	}
	t.Setenv("XDG_DATA_HOME", dirs.Data)
	t.Setenv("XDG_CACHE_HOME", dirs.Cache)
	t.Setenv("XDG_STATE_HOME", dirs.State)
	xdg.Reload()
	return dirs
}
