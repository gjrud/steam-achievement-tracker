package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/keyring"
)

// eventLog records every sync:changed emit with the SyncInProgress that GetAppState reports at that moment, as the
// frontend's refetch would see it.
type eventLog struct {
	mu         sync.Mutex
	names      []string
	inProgress []bool
}

// record is the emitter the tests install. It reads GetAppState before it takes eventLog.mu, so a nested emit cannot
// deadlock on it.
func (l *eventLog) record(app *App, name string) {
	state, err := app.GetAppState()
	inProgress := err == nil && state.SyncInProgress
	l.mu.Lock()
	defer l.mu.Unlock()
	l.names = append(l.names, name)
	l.inProgress = append(l.inProgress, inProgress)
}

// snapshot returns a copy of the recorded events.
func (l *eventLog) snapshot() ([]string, []bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	names := append([]string(nil), l.names...)
	inProgress := append([]bool(nil), l.inProgress...)
	return names, inProgress
}

// waitForFinalEvent polls until the last recorded event reports SyncInProgress=false. endSync emits the final event
// before it releases syncWG, so this normally returns at once; the poll bounds the wait and fails clearly after 5
// seconds if the final event is missing.
func (l *eventLog) waitForFinalEvent(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, inProgress := l.snapshot()
		if n := len(inProgress); n > 0 && !inProgress[n-1] {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no final sync:changed event with SyncInProgress=false; events in progress = %v", inProgress)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startFailingSync points the App at a Steam server that rejects every request, so a sync fails at its first call
// and finishes at once. It returns after startSync succeeds.
func startFailingSync(t *testing.T, app *App) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	app.mu.Lock()
	app.steamAPIKey = "fixture-steam-key"
	app.steamBaseURL = server.URL
	app.mu.Unlock()
	if err := app.startSync(true); err != nil {
		t.Fatalf("startSync: %v", err)
	}
}

// A sync emits sync:changed on start, on each state write, and on its end. The first event reports the sync as
// running and the last reports it over, which is the refetch the frontend relies on.
func TestSyncEmitsChangedEventsEndingAfterFlagClears(t *testing.T) {
	app := newTestApp(t)
	profile := saveTestProfile(t, app)
	log := &eventLog{}
	app.setEventEmitter(func(name string) { log.record(app, name) })

	startFailingSync(t, app)
	waitForSyncWG(t, app)
	log.waitForFinalEvent(t)

	names, inProgress := log.snapshot()
	if len(names) < 3 {
		t.Fatalf("sync:changed events = %v, want at least start, run write and end", names)
	}
	for _, name := range names {
		if name != syncChangedEvent {
			t.Fatalf("event name = %q, want %q", name, syncChangedEvent)
		}
	}
	if !inProgress[0] {
		t.Fatalf("first event SyncInProgress = false, want true while the sync starts")
	}
	if last := inProgress[len(inProgress)-1]; last {
		t.Fatal("last event SyncInProgress = true, want false once the flag has cleared")
	}
	assertIdleSync(t, app)
	waitForLatestSyncRun(t, app, profile.ID, "failed")
}

// blockRunFinish makes the final status write of every sync run fail, so the run stays 'running' after its sync ends.
func blockRunFinish(t *testing.T, app *App) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+app.paths.DB+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`create trigger block_run_finish before update of status on sync_runs begin select raise(abort, 'finish blocked'); end`); err != nil {
		t.Fatal(err)
	}
}

// With the final status write blocked, the run stays 'running', so GetAppState takes SyncInProgress from the flag
// alone. The only event that can show SyncInProgress=false is the one emitted after the flag clears, so the last
// event must report it.
func TestSyncChangedFinalEventReportsIdleWhenRunStaysRunning(t *testing.T) {
	app := newTestApp(t)
	profile := saveTestProfile(t, app)
	log := &eventLog{}
	app.setEventEmitter(func(name string) { log.record(app, name) })
	blockRunFinish(t, app)

	startFailingSync(t, app)
	waitForSyncWG(t, app)

	stored, err := app.repo.LatestSyncRun(profile.ID)
	if err != nil || stored == nil || stored.Status != "running" {
		t.Fatalf("stored run = %+v, %v; want the row left running", stored, err)
	}
	_, inProgress := log.snapshot()
	if len(inProgress) < 2 {
		t.Fatalf("events = %d, want at least start and end", len(inProgress))
	}
	if !inProgress[0] {
		t.Fatal("first event SyncInProgress = false, want true while the sync starts")
	}
	if last := inProgress[len(inProgress)-1]; last {
		t.Fatalf("last event SyncInProgress = true, want false; events = %v", inProgress)
	}
}

// With no API key, startSync returns before any Steam call. It still emits once at start and once at its end, so
// the emitted states are exactly [true, false].
func TestStartSyncEarlyExitEmitsStartAndEnd(t *testing.T) {
	app := newTestApp(t)
	log := &eventLog{}
	app.setEventEmitter(func(name string) { log.record(app, name) })

	err := app.startSync(true)
	if !errors.Is(err, keyring.ErrMissing) {
		t.Fatalf("startSync err = %v, want keyring.ErrMissing", err)
	}
	waitForSyncWG(t, app)
	names, inProgress := log.snapshot()
	if len(names) != 2 || !inProgress[0] || inProgress[1] {
		t.Fatalf("emitted SyncInProgress = %v (names %v), want exactly [true false]", inProgress, names)
	}
	assertIdleSync(t, app)
}

// With no emitter set (the default, and in tests), a sync runs to completion and the emit helper is a no-op.
func TestStartSyncWithoutEmitterWorks(t *testing.T) {
	app := newTestApp(t)
	profile := saveTestProfile(t, app)

	startFailingSync(t, app)
	waitForSyncWG(t, app)
	app.emitSyncChanged()
	assertIdleSync(t, app)
	waitForLatestSyncRun(t, app, profile.ID, "failed")
}
