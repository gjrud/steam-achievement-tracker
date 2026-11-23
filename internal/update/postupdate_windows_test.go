//go:build windows

package update

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// The registry test uses a throwaway HKCU key that it creates and deletes. It does not touch the installer's entry.
// Process-global state is not involved, so it does not call t.Parallel.
func TestSyncDisplayVersion(t *testing.T) {
	keyPath := fmt.Sprintf(`Software\sat-test-%d`, os.Getpid())
	k, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE|registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = k.Close()
		_ = registry.DeleteKey(registry.CURRENT_USER, keyPath)
	})

	exe := `C:\Users\test\AppData\Local\Programs\Steam Achievement Tracker\steam-achievement-tracker.exe`
	if err := k.SetStringValue("DisplayIcon", exe); err != nil {
		t.Fatal(err)
	}
	if err := k.SetStringValue("DisplayVersion", "1.0.0"); err != nil {
		t.Fatal(err)
	}

	readVersion := func() string {
		t.Helper()
		v, _, err := k.GetStringValue("DisplayVersion")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	t.Run("installed copy is updated once", func(t *testing.T) {
		written, err := syncDisplayVersion(keyPath, exe, "1.2.3-rc.1")
		if err != nil || !written {
			t.Fatalf("first sync: written %v, err %v; want true, nil", written, err)
		}
		if got := readVersion(); got != "1.2.3" {
			t.Fatalf("DisplayVersion = %q, want 1.2.3", got)
		}
		written, err = syncDisplayVersion(keyPath, exe, "1.2.3")
		if err != nil || written {
			t.Fatalf("second sync: written %v, err %v; want false, nil", written, err)
		}
	})

	t.Run("exe path compares case-insensitively", func(t *testing.T) {
		if err := k.SetStringValue("DisplayVersion", "1.0.0"); err != nil {
			t.Fatal(err)
		}
		written, err := syncDisplayVersion(keyPath, strings.ToUpper(exe), "1.2.3")
		if err != nil || !written {
			t.Fatalf("sync with upper-case exe: written %v, err %v; want true, nil", written, err)
		}
	})

	t.Run("another install's entry is left alone", func(t *testing.T) {
		if err := k.SetStringValue("DisplayVersion", "1.0.0"); err != nil {
			t.Fatal(err)
		}
		written, err := syncDisplayVersion(keyPath, `C:\Other\steam-achievement-tracker.exe`, "1.2.3")
		if err != nil || written {
			t.Fatalf("sync for another exe: written %v, err %v; want false, nil", written, err)
		}
		if got := readVersion(); got != "1.0.0" {
			t.Fatalf("DisplayVersion changed to %q for another exe", got)
		}
	})

	t.Run("missing key is not an install", func(t *testing.T) {
		missing := keyPath + "-missing"
		written, err := syncDisplayVersion(missing, exe, "1.2.3")
		if err != nil || written {
			t.Fatalf("missing key: written %v, err %v; want false, nil", written, err)
		}
		if _, err := registry.OpenKey(registry.CURRENT_USER, missing, registry.QUERY_VALUE); !errors.Is(err, registry.ErrNotExist) {
			t.Fatalf("sync created or changed a missing key: %v", err)
		}
	})
}
