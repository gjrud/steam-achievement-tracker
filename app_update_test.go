package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/update"
)

// testUpdateContext is the service lifetime for a test: cancelled at cleanup, which also ends any startup check the
// test scheduled.
func testUpdateContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// unavailableUpdates returns an App whose update service is unavailable. Tests have no version stamp, so Init leaves
// the service unavailable, as in a dev build, and makes no request.
func unavailableUpdates(t *testing.T) *App {
	t.Helper()
	app := NewApp()
	svc := update.New(update.Deps{Context: testUpdateContext(t)})
	svc.Init()
	app.setUpdater(svc)
	return app
}

// An App without an update service (nil) and one with an unavailable service give the same fixed sentence from every
// binding except GetUpdateState, and GetUpdateState reports unavailable without failing.
func TestUpdateBindingsWithoutUpdaterGiveFixedSentence(t *testing.T) {
	cases := map[string]*App{
		"nil service":         NewApp(),
		"unavailable service": unavailableUpdates(t),
	}
	for name, app := range cases {
		t.Run(name, func(t *testing.T) {
			snap, err := app.GetUpdateState()
			if err != nil {
				t.Fatalf("GetUpdateState err = %v, want nil", err)
			}
			if !snap.Unavailable {
				t.Fatalf("GetUpdateState Unavailable = false, want true")
			}
			if snap.ChecksEnabled {
				t.Fatalf("GetUpdateState ChecksEnabled = true with no update service, want false")
			}
			calls := map[string]error{
				"CheckForUpdates": app.CheckForUpdates(),
				"InstallUpdate":   app.InstallUpdate("9.9.9"),
				"CancelUpdate":    app.CancelUpdate(),
				"RestartToUpdate": app.RestartToUpdate(),
			}
			_, toggleErr := app.SetUpdateChecksEnabled(true)
			calls["SetUpdateChecksEnabled"] = toggleErr
			for op, err := range calls {
				if err == nil || err.Error() != msgUpdateUnavailable {
					t.Errorf("%s err = %v, want %q", op, err, msgUpdateUnavailable)
				}
			}
		})
	}
}

// The App's fixed sentence and the update service's own sentence for the same case must match, so the frontend sees one
// text whichever layer refuses. The service gives its sentence for an unavailable CheckNow.
func TestFixedSentenceMatchesUpdateService(t *testing.T) {
	svc := update.New(update.Deps{Context: testUpdateContext(t)})
	svc.Init()
	err := svc.CheckNow()
	if err == nil || err.Error() != msgUpdateUnavailable {
		t.Fatalf("service CheckNow on unavailable updater = %v, want %q", err, msgUpdateUnavailable)
	}
	if err := svc.SetChecksEnabled(true); err == nil || err.Error() != msgUpdateUnavailable {
		t.Fatalf("service SetChecksEnabled on unavailable updater = %v, want %q", err, msgUpdateUnavailable)
	}
	if err := svc.Cancel(); err == nil || err.Error() != msgUpdateUnavailable {
		t.Fatalf("service Cancel on unavailable updater = %v, want %q", err, msgUpdateUnavailable)
	}
}

// The toggle is stored in the settings table. The service caches it: LoadChecksEnabled reads it once and
// SetUpdateChecksEnabled stores it before updating the cache, so GetUpdateState and the stored value agree. A fresh
// database has it on. The service is not initialized, so it is not unavailable. The test loads the toggle and never
// calls StartupCheck, so no startup check is scheduled.
func TestSetUpdateChecksEnabledPersists(t *testing.T) {
	app := newTestApp(t)
	svc := update.New(update.Deps{Context: testUpdateContext(t), ChecksEnabled: app.updateChecksEnabled})
	app.setUpdater(svc)
	svc.LoadChecksEnabled()

	snap, err := app.GetUpdateState()
	if err != nil {
		t.Fatalf("GetUpdateState: %v", err)
	}
	if !snap.ChecksEnabled {
		t.Fatal("fresh database: ChecksEnabled = false, want the default on")
	}

	for _, enabled := range []bool{false, true, false} {
		snap, err := app.SetUpdateChecksEnabled(enabled)
		if err != nil {
			t.Fatalf("SetUpdateChecksEnabled(%v): %v", enabled, err)
		}
		if snap.ChecksEnabled != enabled {
			t.Fatalf("SetUpdateChecksEnabled(%v) returned ChecksEnabled=%v", enabled, snap.ChecksEnabled)
		}
		stored, err := app.repo.UpdateChecksEnabled()
		if err != nil {
			t.Fatalf("read stored setting: %v", err)
		}
		if stored != enabled {
			t.Fatalf("stored setting = %v, want %v", stored, enabled)
		}
		again, err := app.GetUpdateState()
		if err != nil {
			t.Fatalf("GetUpdateState: %v", err)
		}
		if again.ChecksEnabled != enabled {
			t.Fatalf("GetUpdateState ChecksEnabled = %v after set to %v", again.ChecksEnabled, enabled)
		}
	}
}

// Storing the toggle on an unavailable updater is refused before anything is written: the stored value stays as it was.
func TestSetUpdateChecksEnabledUnavailableDoesNotPersist(t *testing.T) {
	app := newTestApp(t)
	svc := update.New(update.Deps{Context: testUpdateContext(t), ChecksEnabled: app.updateChecksEnabled})
	svc.Init()
	app.setUpdater(svc)

	_, err := app.SetUpdateChecksEnabled(false)
	if err == nil || err.Error() != msgUpdateUnavailable {
		t.Fatalf("SetUpdateChecksEnabled err = %v, want %q", err, msgUpdateUnavailable)
	}
	stored, err := app.repo.UpdateChecksEnabled()
	if err != nil || !stored {
		t.Fatalf("stored setting = %v, %v; want the default on, nil", stored, err)
	}
}

// GetUpdateState reads the cached toggle, never the database: the setting is read once, by LoadChecksEnabled. The
// test loads and never calls StartupCheck, so no startup check is scheduled.
func TestGetUpdateStateDoesNotReadSettings(t *testing.T) {
	app := newTestApp(t)
	reads := 0
	svc := update.New(update.Deps{Context: testUpdateContext(t), ChecksEnabled: func() (bool, error) {
		reads++
		return app.updateChecksEnabled()
	}})
	app.setUpdater(svc)
	svc.LoadChecksEnabled()
	if reads != 1 {
		t.Fatalf("LoadChecksEnabled read the setting %d times, want 1", reads)
	}
	for i := 0; i < 3; i++ {
		snap, err := app.GetUpdateState()
		if err != nil || !snap.ChecksEnabled {
			t.Fatalf("GetUpdateState = %+v, %v; want checks on", snap, err)
		}
	}
	if reads != 1 {
		t.Fatalf("GetUpdateState read the setting: %d reads, want 1", reads)
	}
}

// The toggle is loaded when the database first opens, not only by ServiceStartup. The stored value is on and the service
// is set before the first open, as in main. An unloaded toggle reads as off, so on here can only come from the load,
// with no StartupCheck call.
func TestToggleLoadedWhenDatabaseOpens(t *testing.T) {
	first := newTestApp(t)
	if err := first.saveUpdateChecksEnabled(true); err != nil {
		t.Fatalf("saveUpdateChecksEnabled(true): %v", err)
	}
	if err := first.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}

	app := NewApp()
	svc := update.New(update.Deps{Context: testUpdateContext(t), ChecksEnabled: app.updateChecksEnabled})
	app.setUpdater(svc)
	if err := app.ensureInitialized(); err != nil {
		t.Fatalf("ensureInitialized: %v", err)
	}
	t.Cleanup(func() { _ = app.ServiceShutdown() })

	snap, err := app.GetUpdateState()
	if err != nil || !snap.ChecksEnabled {
		t.Fatalf("GetUpdateState after first open = %+v, %v; want checks on from the stored value", snap, err)
	}
}

// ServiceShutdown does not wait past syncShutdownWait for a sync that never ends. It logs a warning without paths and
// still closes the database.
func TestServiceShutdownBoundsSyncWait(t *testing.T) {
	app := newTestApp(t)
	prev := syncShutdownWait
	syncShutdownWait = 20 * time.Millisecond
	t.Cleanup(func() { syncShutdownWait = prev })

	// A sync that never ends: its WaitGroup entry is released only by this cleanup, after the shutdown under test.
	app.syncWG.Add(1)
	t.Cleanup(app.syncWG.Done)
	logs := captureLog(t)

	start := time.Now()
	if err := app.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("ServiceShutdown took %s with a stuck sync, want the bound to end the wait", elapsed)
	}
	if !strings.Contains(logs.String(), "warning: shutdown: sync still running") {
		t.Fatalf("log = %q, want the shutdown warning", logs.String())
	}
	if strings.Contains(logs.String(), app.paths.DataDir) && app.paths.DataDir != "" {
		t.Fatalf("log = %q carries a path", logs.String())
	}
	app.mu.Lock()
	repo := app.repo
	app.mu.Unlock()
	if repo != nil {
		t.Fatal("database still open after bounded shutdown")
	}
}

// A sync that ends inside the bound leaves no warning.
func TestServiceShutdownQuietWhenSyncEnds(t *testing.T) {
	app := newTestApp(t)
	app.syncWG.Add(1)
	app.syncWG.Done()
	logs := captureLog(t)
	if err := app.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	if strings.Contains(logs.String(), "warning: shutdown") {
		t.Fatalf("log = %q, want no shutdown warning when no sync is running", logs.String())
	}
}
