package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/logging"
	"github.com/wailsapp/wails/v3/pkg/application"
	zk "github.com/zalando/go-keyring"
)

func TestServiceShutdownCancelsLifetimeContext(t *testing.T) {
	app := NewApp()
	if err := app.context().Err(); err != nil {
		t.Fatalf("lifetime context err before shutdown = %v, want nil", err)
	}
	if err := app.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	if app.context().Err() == nil {
		t.Fatal("lifetime context not cancelled by ServiceShutdown")
	}
	// A second shutdown must be harmless.
	if err := app.ServiceShutdown(); err != nil {
		t.Fatalf("second ServiceShutdown: %v", err)
	}
}

func TestAppLiteralContextIsBackground(t *testing.T) {
	app := &App{}
	ctx := app.context()
	if ctx == nil {
		t.Fatal("context() of &App{} returned nil")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("context() of &App{} err = %v, want nil", err)
	}
	if err := app.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown on &App{}: %v", err)
	}
	if err := app.context().Err(); err != nil {
		t.Fatalf("&App{} context cancelled by ServiceShutdown: %v", err)
	}
}

// TestServiceStartupInitFailureReturnsNilAndReportsInitError makes init fail with a regular file where the data
// directory's parent should be. ServiceStartup must return nil so the app still starts, and the failure must reach
// the UI through GetAppState().InitError.
func TestServiceStartupInitFailureReturnsNilAndReportsInitError(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", blocked)
	t.Cleanup(logging.Fallback)
	zk.MockInit()
	app := NewApp()
	t.Cleanup(func() { _ = app.ServiceShutdown() })

	if err := app.ServiceStartup(context.Background(), application.ServiceOptions{}); err != nil {
		t.Fatalf("ServiceStartup err = %v, want nil on init failure", err)
	}
	state, err := app.GetAppState()
	if err != nil {
		t.Fatalf("GetAppState err = %v, want nil with InitError set", err)
	}
	if state.InitError == "" {
		t.Fatal("GetAppState InitError empty after failed ServiceStartup, want the init failure")
	}
}

// TestStartSyncAfterShutdownReturnsErrShuttingDown checks that a sync requested after ServiceShutdown is refused
// before it touches sync state. The key is present and the app is initialized, so without the shutdown guard the
// call would reach the sync setup and fail with a different error.
func TestStartSyncAfterShutdownReturnsErrShuttingDown(t *testing.T) {
	app := newTestAppWithKey(t, "shutdown-test-key")
	if err := app.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}

	for _, force := range []bool{true, false} {
		err := app.startSync(force)
		if !errors.Is(err, errShuttingDown) {
			t.Fatalf("startSync(force=%v) err = %v, want errShuttingDown", force, err)
		}
		app.mu.Lock()
		inProgress, cancel := app.syncInProgress, app.syncCancel
		app.mu.Unlock()
		if inProgress || cancel != nil {
			t.Fatalf("startSync(force=%v) after shutdown set syncInProgress=%v syncCancel=%v, want neither", force, inProgress, cancel != nil)
		}
	}
	// The sync wait group must be idle: a goroutine started by the refused call would leave it counted.
	app.syncWG.Wait()
}
