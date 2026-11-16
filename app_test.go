package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/config"
	"github.com/gjrud/steam-achievement-tracker/internal/keyring"
	"github.com/gjrud/steam-achievement-tracker/internal/logging"
	"github.com/gjrud/steam-achievement-tracker/internal/repository"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
	zk "github.com/zalando/go-keyring"
)

const (
	testKeyringService = "steam-achievement-tracker"
	testKeyringUser    = "steam-web-api-key"
	testSteamID        = "76561198000000001"
)

// openTestApp points HOME at a temp dir and initializes the App there. The keyring must already be mocked.
func openTestApp(t *testing.T) *App {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(logging.Fallback)
	app := NewApp()
	if err := app.ensureInitialized(); err != nil {
		t.Fatalf("ensureInitialized: %v", err)
	}
	t.Cleanup(func() { _ = app.ServiceShutdown() })
	return app
}

// newTestApp opens an App with an empty mocked Secret Service.
func newTestApp(t *testing.T) *App {
	t.Helper()
	zk.MockInit()
	return openTestApp(t)
}

// newTestAppWithKey opens an App whose Secret Service already holds key.
func newTestAppWithKey(t *testing.T, key string) *App {
	t.Helper()
	zk.MockInit()
	if err := zk.Set(testKeyringService, testKeyringUser, key); err != nil {
		t.Fatal(err)
	}
	return openTestApp(t)
}

// saveTestProfile saves a profile with no API key in the keyring, so the post-save sync is a no-op.
// It fails fast if a key is present: SaveProfile would then start a real Steam sync.
func saveTestProfile(t *testing.T, app *App) repository.Profile {
	t.Helper()
	if key, err := zk.Get(testKeyringService, testKeyringUser); err == nil {
		t.Fatalf("keyring holds a key (%d bytes); saveTestProfile needs an empty keyring to avoid a real sync", len(key))
	}
	state, err := app.SaveProfile(ProfilePreview{SteamID64: testSteamID, DisplayName: "Tester"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if state.Profile == nil {
		t.Fatal("no active profile after SaveProfile")
	}
	return *state.Profile
}

// seedTwoSetGame saves app 10 with a base set (groupID 0) and a DLC set (groupID 7), both with one locked achievement.
func seedTwoSetGame(t *testing.T, app *App, profileID int64) {
	t.Helper()
	set := func(groupID int64, name string) repository.SetSyncUpdate {
		return repository.SetSyncUpdate{
			GroupID:           groupID,
			Name:              name,
			TotalAchievements: 1,
			Achievements:      []repository.AchievementRecord{{APIName: "ACH_ONE"}},
		}
	}
	if err := app.repo.SaveGameSync(repository.GameSyncUpdate{
		ProfileID:         profileID,
		AppID:             10,
		Name:              "Game Ten",
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets:              []repository.SetSyncUpdate{set(0, ""), set(7, "Bonus")},
		SyncedAt:          time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
}

func mustCard(t *testing.T, d *repository.Dashboard, appID, groupID int64) repository.GameCard {
	t.Helper()
	if d == nil {
		t.Fatal("dashboard is nil, want a profile dashboard")
	}
	for _, list := range [][]repository.GameCard{d.Suggestions, d.Completed, d.Warnings, d.Disabled} {
		for _, c := range list {
			if c.AppID == appID && c.GroupID == groupID {
				return c
			}
		}
	}
	t.Fatalf("card app %d group %d not on dashboard", appID, groupID)
	return repository.GameCard{}
}

func inList(cards []repository.GameCard, appID, groupID int64) bool {
	for _, c := range cards {
		if c.AppID == appID && c.GroupID == groupID {
			return true
		}
	}
	return false
}

// waitForSyncWG fails the test if the sync wait group does not drain, which would mean unbalanced Add/Done.
func waitForSyncWG(t *testing.T, app *App) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		app.syncWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("syncWG did not drain: sync bookkeeping is unbalanced")
	}
}

func assertIdleSync(t *testing.T, app *App) {
	t.Helper()
	app.mu.Lock()
	inProgress, cancel := app.syncInProgress, app.syncCancel
	app.mu.Unlock()
	if inProgress {
		t.Fatal("syncInProgress = true after failed start, want reset")
	}
	if cancel != nil {
		t.Fatal("syncCancel still set after failed start, want nil")
	}
	waitForSyncWG(t, app)
}

func TestStartSyncSingleFlightGuards(t *testing.T) {
	// syncInProgress is set by hand: a real running sync would need Steam. The start/finish/reset cycle
	// is covered by TestStartSyncLifecycleResetsAfterGuardFailure.
	app := &App{syncInProgress: true}

	if err := app.startSync(false); err != nil {
		t.Fatalf("non-forced startSync err = %v, want nil", err)
	}
	if !app.syncInProgress {
		t.Fatal("syncInProgress = false, want existing sync left running")
	}
	err := app.startSync(true)
	if err == nil || !strings.Contains(err.Error(), "sync already in progress") {
		t.Fatalf("forced startSync err = %v, want already-in-progress error", err)
	}
}

func TestStartSyncLifecycleResetsAfterGuardFailure(t *testing.T) {
	t.Run("no database", func(t *testing.T) {
		app := &App{}
		err := app.startSync(true)
		if err == nil || !strings.Contains(err.Error(), "database is not initialized") {
			t.Fatalf("startSync err = %v, want database-not-initialized", err)
		}
		assertIdleSync(t, app)
	})

	t.Run("no key", func(t *testing.T) {
		app := newTestApp(t)
		saveTestProfile(t, app)
		if err := app.startSync(true); !errors.Is(err, keyring.ErrMissing) {
			t.Fatalf("forced startSync err = %v, want ErrMissing", err)
		}
		assertIdleSync(t, app)
		// A leftover flag would make this non-forced call return nil silently; the reset makes it fail the same way.
		if err := app.startSync(false); !errors.Is(err, keyring.ErrMissing) {
			t.Fatalf("non-forced startSync after reset err = %v, want ErrMissing", err)
		}
	})

	t.Run("no active profile", func(t *testing.T) {
		app := newTestAppWithKey(t, "key-without-profile")
		err := app.startSync(true)
		if err == nil || !strings.Contains(err.Error(), "no active profile") {
			t.Fatalf("startSync err = %v, want no-active-profile", err)
		}
		assertIdleSync(t, app)
	})
}

func TestGetAppStateWithoutKeyOrProfile(t *testing.T) {
	app := newTestApp(t)
	state, err := app.GetAppState()
	if err != nil {
		t.Fatalf("GetAppState: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if state.APIKeyPresent || state.APIKeyError != "" {
		t.Fatalf("api key = present %v err %q, want missing without error", state.APIKeyPresent, state.APIKeyError)
	}
	if state.ProfileExists || state.Profile != nil || state.Dashboard != nil {
		t.Fatalf("profile state = exists %v profile %v dashboard %v, want none", state.ProfileExists, state.Profile, state.Dashboard)
	}
	if state.SyncInProgress || state.InitError != "" {
		t.Fatalf("SyncInProgress = %v InitError = %q, want idle and no init error", state.SyncInProgress, state.InitError)
	}
	dataDir := filepath.Join(home, config.DataDirName)
	if state.DataDir != dataDir || state.LogFile != filepath.Join(dataDir, "logs", "app.log") {
		t.Fatalf("DataDir = %q LogFile = %q, want paths under %q", state.DataDir, state.LogFile, dataDir)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secretSetupCommand") {
		t.Fatal("serialized AppState still carries the removed secretSetupCommand field")
	}
}

// Library sync settings are per profile. With no active profile, SetLibrarySettings fails and state shows defaults.
func TestSetLibrarySettingsRequiresActiveProfile(t *testing.T) {
	app := newTestApp(t)
	state, err := app.GetAppState()
	if err != nil {
		t.Fatalf("GetAppState: %v", err)
	}
	if state.Settings != repository.DefaultLibrarySettings() {
		t.Fatalf("Settings without profile = %+v, want defaults", state.Settings)
	}
	if _, err := app.SetLibrarySettings(false, false); err == nil || !strings.Contains(err.Error(), "no active profile") {
		t.Fatalf("SetLibrarySettings without profile err = %v, want no-active-profile", err)
	}
	state, err = app.GetAppState()
	if err != nil {
		t.Fatalf("GetAppState after failed save: %v", err)
	}
	if state.Settings != repository.DefaultLibrarySettings() {
		t.Fatalf("Settings after failed save = %+v, want defaults", state.Settings)
	}
}

// Settings save for the active profile without starting a sync. A new profile starts at defaults, and each
// profile keeps its own values when the active profile changes.
func TestSetLibrarySettingsIsPerProfile(t *testing.T) {
	app := newTestApp(t)
	profileA := saveTestProfile(t, app)
	defaults := repository.DefaultLibrarySettings()
	off := repository.LibrarySettings{IncludeUnvettedApps: false, IncludePlayedFreeGames: false}
	onB := repository.LibrarySettings{IncludeUnvettedApps: true, IncludePlayedFreeGames: false}

	state, err := app.SetLibrarySettings(false, false)
	if err != nil {
		t.Fatalf("SetLibrarySettings for profile A: %v", err)
	}
	if state.Settings != off {
		t.Fatalf("SetLibrarySettings returned Settings = %+v, want %+v", state.Settings, off)
	}
	if state.SyncInProgress {
		t.Fatal("SetLibrarySettings started a sync, want settings to apply on the next sync")
	}

	// SaveProfile makes the new profile active. With no key in the keyring its post-save sync does not run.
	state, err = app.SaveProfile(ProfilePreview{SteamID64: "76561198000000002", DisplayName: "Other"})
	if err != nil {
		t.Fatalf("SaveProfile profile B: %v", err)
	}
	if state.Profile == nil || state.Profile.ID == profileA.ID {
		t.Fatalf("active profile after SaveProfile = %+v, want a new profile", state.Profile)
	}
	if state.Settings != defaults {
		t.Fatalf("new profile Settings = %+v, want defaults %+v", state.Settings, defaults)
	}
	state, err = app.SetLibrarySettings(true, false)
	if err != nil {
		t.Fatalf("SetLibrarySettings for profile B: %v", err)
	}
	if state.Settings != onB {
		t.Fatalf("profile B SetLibrarySettings returned Settings = %+v, want %+v", state.Settings, onB)
	}

	// Switching back to profile A (SaveProfile of its Steam ID) shows A's own values.
	state, err = app.SaveProfile(ProfilePreview{SteamID64: testSteamID, DisplayName: "Tester"})
	if err != nil {
		t.Fatalf("SaveProfile back to profile A: %v", err)
	}
	if state.Profile == nil || state.Profile.ID != profileA.ID {
		t.Fatalf("active profile = %+v, want profile A %d", state.Profile, profileA.ID)
	}
	if state.Settings != off {
		t.Fatalf("profile A Settings after switching back = %+v, want %+v", state.Settings, off)
	}
	state, err = app.GetAppState()
	if err != nil {
		t.Fatalf("GetAppState: %v", err)
	}
	if state.Settings != off {
		t.Fatalf("GetAppState Settings for profile A = %+v, want %+v", state.Settings, off)
	}
}

// Each sync maps the settings of the profile it syncs: the Steam client flags come from that profile only.
func TestSteamClientForProfileUsesOwnSettings(t *testing.T) {
	app := newTestApp(t)
	profileA := saveTestProfile(t, app)
	if _, err := app.SetLibrarySettings(false, false); err != nil {
		t.Fatalf("SetLibrarySettings for profile A: %v", err)
	}
	profileB, err := app.repo.SaveProfile("76561198000000002", "Other", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.repo.SaveLibrarySettings(profileB.ID, repository.LibrarySettings{IncludeUnvettedApps: true, IncludePlayedFreeGames: false}); err != nil {
		t.Fatal(err)
	}

	clientA, err := steamClientForProfile(app.repo, "fixture-steam-key", profileA.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !clientA.SkipUnvettedApps || !clientA.ExcludePlayedFreeGames {
		t.Fatalf("profile A client SkipUnvettedApps=%v ExcludePlayedFreeGames=%v, want both true", clientA.SkipUnvettedApps, clientA.ExcludePlayedFreeGames)
	}
	clientB, err := steamClientForProfile(app.repo, "fixture-steam-key", profileB.ID)
	if err != nil {
		t.Fatal(err)
	}
	if clientB.SkipUnvettedApps || !clientB.ExcludePlayedFreeGames {
		t.Fatalf("profile B client SkipUnvettedApps=%v ExcludePlayedFreeGames=%v, want false and true", clientB.SkipUnvettedApps, clientB.ExcludePlayedFreeGames)
	}
}

// newSteamClient maps the inverted Steam flags: SkipUnvettedApps = !IncludeUnvettedApps and
// ExcludePlayedFreeGames = !IncludePlayedFreeGames, for every combination of the two settings.
func TestNewSteamClientMapsLibrarySettings(t *testing.T) {
	tests := []struct {
		name string
		s    repository.LibrarySettings
	}{
		{name: "both included", s: repository.LibrarySettings{IncludeUnvettedApps: true, IncludePlayedFreeGames: true}},
		{name: "unvetted excluded", s: repository.LibrarySettings{IncludeUnvettedApps: false, IncludePlayedFreeGames: true}},
		{name: "played free excluded", s: repository.LibrarySettings{IncludeUnvettedApps: true, IncludePlayedFreeGames: false}},
		{name: "both excluded", s: repository.LibrarySettings{IncludeUnvettedApps: false, IncludePlayedFreeGames: false}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := newSteamClient("fixture-steam-key", tc.s)
			if client.SkipUnvettedApps != !tc.s.IncludeUnvettedApps {
				t.Fatalf("SkipUnvettedApps = %v, want %v", client.SkipUnvettedApps, !tc.s.IncludeUnvettedApps)
			}
			if client.ExcludePlayedFreeGames != !tc.s.IncludePlayedFreeGames {
				t.Fatalf("ExcludePlayedFreeGames = %v, want %v", client.ExcludePlayedFreeGames, !tc.s.IncludePlayedFreeGames)
			}
		})
	}
}

func TestGetAppStateReportsKeyPresenceWithoutLeakingKey(t *testing.T) {
	const secret = "fixture-steam-key-value"
	app := newTestAppWithKey(t, secret)
	state, err := app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.APIKeyPresent || state.APIKeyError != "" {
		t.Fatalf("api key = present %v err %q, want present without error", state.APIKeyPresent, state.APIKeyError)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("serialized AppState contains the API key")
	}
}

func TestGetAppStateReportsKeyringReadErrorAtInit(t *testing.T) {
	zk.MockInitWithError(errors.New("secret service unavailable"))
	app := openTestApp(t)
	state, err := app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	if state.APIKeyPresent || state.APIKeyError != "secret service unavailable" || state.InitError != "" {
		t.Fatalf("state = present %v apiKeyError %q initError %q, want key error reported and no init error",
			state.APIKeyPresent, state.APIKeyError, state.InitError)
	}
}

// TestGetAppStateNormalizesStaleRunningRun checks the read-side fix for a run left 'running' when its final write
// failed. With no live sync, GetAppState reports the run as failed and the row stays 'running' until the next
// startup. A live sync keeps its run as running, and a finished run is returned unchanged.
func TestGetAppStateNormalizesStaleRunningRun(t *testing.T) {
	tests := []struct {
		name           string
		liveSync       bool
		runStatus      string
		wantStatus     string
		wantErr        bool
		wantInProgress bool
	}{
		{name: "stale running run, no live sync", runStatus: "running", wantStatus: "failed", wantErr: true},
		{name: "running run with live sync", liveSync: true, runStatus: "running", wantStatus: "running", wantInProgress: true},
		{name: "finished run, no live sync", runStatus: "success", wantStatus: "success"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(t)
			profile := saveTestProfile(t, app)
			recordSyncRun(t, app, profile.ID, tt.runStatus)
			if tt.liveSync {
				setLiveSync(app, profile.ID, true, 0)
			}

			state, err := app.GetAppState()
			if err != nil {
				t.Fatal(err)
			}
			if state.SyncInProgress != tt.wantInProgress {
				t.Fatalf("state.SyncInProgress = %v, want %v", state.SyncInProgress, tt.wantInProgress)
			}
			run := state.Dashboard.LatestSyncRun
			if run == nil || run.Status != tt.wantStatus {
				t.Fatalf("dashboard latest run = %+v, want status %q", run, tt.wantStatus)
			}
			if tt.wantErr {
				if run.Error == nil || !strings.Contains(*run.Error, "final status could not be saved") {
					t.Fatalf("dashboard latest run error = %v, want the unsaved-final-status error", run.Error)
				}
				if run.FinishedAt != nil {
					t.Fatalf("dashboard latest run finishedAt = %q, want unset", *run.FinishedAt)
				}
			} else if run.Error != nil {
				t.Fatalf("dashboard latest run error = %q, want none", *run.Error)
			}
			stored, err := app.repo.LatestSyncRun(profile.ID)
			if err != nil || stored == nil {
				t.Fatalf("LatestSyncRun = %+v, %v", stored, err)
			}
			if stored.Status != tt.runStatus {
				t.Fatalf("stored status = %q, want %q: the read side must not write the row", stored.Status, tt.runStatus)
			}
		})
	}
}

// TestGetAppStateRunStuckAfterFailedFinish runs a real sync whose final status write is blocked by a trigger. The
// row stays 'running' after the sync ends; GetAppState must report it as failed and no sync in progress.
func TestGetAppStateRunStuckAfterFailedFinish(t *testing.T) {
	app := newTestApp(t)
	profile := saveTestProfile(t, app)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	app.mu.Lock()
	app.steamAPIKey = "fixture-steam-key"
	app.steamBaseURL = server.URL
	app.mu.Unlock()

	db, err := sql.Open("sqlite3", "file:"+app.paths.DB+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`create trigger block_run_finish before update of status on sync_runs begin select raise(abort, 'finish blocked'); end`); err != nil {
		t.Fatal(err)
	}

	if err := app.startSync(true); err != nil {
		t.Fatalf("startSync: %v", err)
	}
	waitForSyncWG(t, app)

	stored, err := app.repo.LatestSyncRun(profile.ID)
	if err != nil || stored == nil || stored.Status != "running" {
		t.Fatalf("stored run = %+v, %v; want the row left running", stored, err)
	}
	state, err := app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	if state.SyncInProgress {
		t.Fatal("SyncInProgress = true after the sync ended, want false")
	}
	if run := state.Dashboard.LatestSyncRun; run == nil || run.Status != "failed" || run.Error == nil {
		t.Fatalf("dashboard latest run = %+v, want failed with error", run)
	}
}

// setLiveSync stands in for startSync's bookkeeping for a sync that is still running: flag, cancel func, profile and
// baseline are set together, as startSync sets them, but no sync goroutine exists and syncWG is left alone.
func setLiveSync(app *App, profileID int64, baselineKnown bool, baselineRunID int64) {
	_, cancel := context.WithCancel(context.Background())
	app.mu.Lock()
	app.syncInProgress = true
	app.syncCancel = cancel
	app.syncProfileID = profileID
	app.syncBaselineKnown = baselineKnown
	app.syncBaselineRunID = baselineRunID
	app.mu.Unlock()
}

// waitForLatestSyncRun polls until the profile's latest sync run has status and returns it. It fails after 5 seconds.
func waitForLatestSyncRun(t *testing.T, app *App, profileID int64, status string) repository.SyncRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := app.repo.LatestSyncRun(profileID)
		if err != nil {
			t.Fatal(err)
		}
		if run != nil && run.Status == status {
			return *run
		}
		if time.Now().After(deadline) {
			t.Fatalf("latest sync run = %+v, want status %q", run, status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// recordSyncRun inserts a sync run for profileID. A status other than "running" is written as the run's final status.
func recordSyncRun(t *testing.T, app *App, profileID int64, status string) int64 {
	t.Helper()
	id, err := app.repo.StartSyncRun(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if status != "running" {
		if err := app.repo.FinishSyncRun(id, status, 1, 1, 0, nil); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// TestGetAppStateSyncInProgressFollowsOwnRun checks that a live sync is reported over as soon as its own run (an id
// above the baseline, of the same profile) has a final status. The flag and cancel func stay set for the sync
// goroutine to clear.
func TestGetAppStateSyncInProgressFollowsOwnRun(t *testing.T) {
	tests := []struct {
		name           string
		priorRun       bool   // a finished run exists before the sync starts
		baselineKnown  bool   // the baseline was recorded when the sync started
		ownRun         string // status of the sync's own run; "" when its run row was never inserted
		otherProfile   bool   // the sync belongs to a profile other than the active one
		wantInProgress bool
	}{
		{name: "own run succeeded", priorRun: true, baselineKnown: true, ownRun: "success"},
		{name: "own run partial", priorRun: true, baselineKnown: true, ownRun: "partial"},
		{name: "own run failed", priorRun: true, baselineKnown: true, ownRun: "failed"},
		{name: "own run canceled", priorRun: true, baselineKnown: true, ownRun: "canceled"},
		{name: "own run succeeded with no earlier run", baselineKnown: true, ownRun: "success"},
		{name: "own run still running", priorRun: true, baselineKnown: true, ownRun: "running", wantInProgress: true},
		{name: "own run not inserted yet", priorRun: true, baselineKnown: true, wantInProgress: true},
		{name: "first run not inserted yet", baselineKnown: true, wantInProgress: true},
		{name: "baseline not recorded yet", priorRun: true, ownRun: "success", wantInProgress: true},
		{name: "sync profile differs from active profile", priorRun: true, baselineKnown: true, ownRun: "success", otherProfile: true, wantInProgress: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := newTestApp(t)
			profile := saveTestProfile(t, app)
			var baselineRunID int64
			if tt.priorRun {
				baselineRunID = recordSyncRun(t, app, profile.ID, "success")
			}
			syncProfileID := profile.ID
			if tt.otherProfile {
				syncProfileID = profile.ID + 1 // no such profile: the active profile's runs must not match this sync
			}
			setLiveSync(app, syncProfileID, tt.baselineKnown, baselineRunID)
			if tt.ownRun != "" {
				recordSyncRun(t, app, profile.ID, tt.ownRun)
			}

			state, err := app.GetAppState()
			if err != nil {
				t.Fatal(err)
			}
			if state.SyncInProgress != tt.wantInProgress {
				t.Fatalf("state.SyncInProgress = %v, want %v", state.SyncInProgress, tt.wantInProgress)
			}
			app.mu.Lock()
			flag, cancel := app.syncInProgress, app.syncCancel
			app.mu.Unlock()
			if !flag || cancel == nil {
				t.Fatal("GetAppState cleared the live sync's flag or cancel func, want the sync goroutine to own them")
			}
		})
	}
}

// TestSyncStillRunningDecision table-tests the locked re-check behind GetAppState. snapshot is the flag as GetAppState
// read it before the database reads; the sync state is set up (or left cleared) before the call, as if a sync
// goroutine had changed it meanwhile.
func TestSyncStillRunningDecision(t *testing.T) {
	finished := &repository.SyncRun{ID: 5, Status: "success"}
	tests := []struct {
		name          string
		snapshot      bool
		liveSync      bool // the sync's flag and cancel func are set now
		syncProfileID int64
		baselineKnown bool
		baselineRunID int64
		latest        *repository.SyncRun
		want          bool
	}{
		{name: "flag cleared since the snapshot", snapshot: true, latest: finished, want: false},
		{name: "snapshot already clear", snapshot: false, latest: finished, want: false},
		{name: "own run finished", snapshot: true, liveSync: true, syncProfileID: 1, baselineKnown: true, baselineRunID: 4, latest: finished, want: false},
		{name: "new sync started meanwhile, baseline at its run", snapshot: true, liveSync: true, syncProfileID: 1, baselineKnown: true, baselineRunID: 5, latest: finished, want: true},
		{name: "other profile's sync", snapshot: true, liveSync: true, syncProfileID: 2, baselineKnown: true, baselineRunID: 4, latest: finished, want: true},
		{name: "own run still running", snapshot: true, liveSync: true, syncProfileID: 1, baselineKnown: true, baselineRunID: 4, latest: &repository.SyncRun{ID: 5, Status: "running"}, want: true},
		{name: "baseline unknown", snapshot: true, liveSync: true, syncProfileID: 1, baselineKnown: false, baselineRunID: 0, latest: finished, want: true},
		{name: "no latest run", snapshot: true, liveSync: true, syncProfileID: 1, baselineKnown: true, baselineRunID: 4, latest: nil, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &App{}
			if tt.liveSync {
				setLiveSync(app, tt.syncProfileID, tt.baselineKnown, tt.baselineRunID)
			}
			if got := app.syncStillRunning(tt.snapshot, 1, tt.latest); got != tt.want {
				t.Fatalf("syncStillRunning = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestLogStartSyncErrorFilter checks which startSync errors reach the log when a caller discards them. A missing key
// and an already-running sync are expected and stay out of the log.
func TestLogStartSyncErrorFilter(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	tests := []struct {
		name    string
		err     error
		wantLog bool
	}{
		{name: "nil", err: nil, wantLog: false},
		{name: "missing key", err: keyring.ErrMissing, wantLog: false},
		{name: "wrapped missing key", err: fmt.Errorf("start: %w", keyring.ErrMissing), wantLog: false},
		{name: "already in progress", err: errSyncAlreadyInProgress, wantLog: false},
		{name: "unreadable baseline", err: errors.New("no such table: sync_runs"), wantLog: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()
			logStartSyncError(tt.err)
			if !tt.wantLog {
				if buf.Len() != 0 {
					t.Fatalf("logged %q, want nothing", buf.String())
				}
				return
			}
			if want := "start sync: " + tt.err.Error(); !strings.Contains(buf.String(), want) {
				t.Fatalf("log = %q, want it to contain %q", buf.String(), want)
			}
		})
	}
}

// A finished run of another profile can have an id above this sync's baseline, because run ids are shared by all
// profiles. While that profile is active the sync must still read as in progress.
func TestGetAppStateIgnoresFinishedRunOfOtherActiveProfile(t *testing.T) {
	app := newTestApp(t)
	profileA := saveTestProfile(t, app)
	baseline := recordSyncRun(t, app, profileA.ID, "success")
	setLiveSync(app, profileA.ID, true, baseline)

	// SaveProfile makes profile B active. Its own post-save sync is refused because a sync is already live.
	state, err := app.SaveProfile(ProfilePreview{SteamID64: "76561198000000002", DisplayName: "Other"})
	if err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	if state.Profile == nil || state.Profile.ID == profileA.ID {
		t.Fatalf("active profile = %+v, want profile B", state.Profile)
	}
	recordSyncRun(t, app, state.Profile.ID, "success")

	state, err = app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.SyncInProgress {
		t.Fatal("SyncInProgress = false while profile A's sync is live and profile B's run finished, want true")
	}
}

// TestStartSyncRecordsBaselineAndFollowsItsOwnRun runs the real startSync against a Steam stub. The stub holds every
// request until the test releases it, then answers with a 403, which is not retried, so the sync run fails.
func TestStartSyncRecordsBaselineAndFollowsItsOwnRun(t *testing.T) {
	app := newTestApp(t)
	profile := saveTestProfile(t, app)
	prior := recordSyncRun(t, app, profile.ID, "success")

	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(unblock) // runs before server.Close, so a failed test cannot hang on the held request
	app.mu.Lock()
	app.steamAPIKey = "fixture-steam-key"
	app.steamBaseURL = server.URL
	app.mu.Unlock()

	if err := app.startSync(true); err != nil {
		t.Fatalf("startSync: %v", err)
	}
	app.mu.Lock()
	known, runID, profileID := app.syncBaselineKnown, app.syncBaselineRunID, app.syncProfileID
	app.mu.Unlock()
	if !known || runID != prior || profileID != profile.ID {
		t.Fatalf("baseline after startSync = known %v run %d profile %d, want known, run %d, profile %d",
			known, runID, profileID, prior, profile.ID)
	}

	waitForLatestSyncRun(t, app, profile.ID, "running")
	state, err := app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.SyncInProgress {
		t.Fatal("SyncInProgress = false while the sync's run is running, want true")
	}

	unblock()
	waitForSyncWG(t, app)
	assertIdleSync(t, app)
	app.mu.Lock()
	known, runID, profileID = app.syncBaselineKnown, app.syncBaselineRunID, app.syncProfileID
	app.mu.Unlock()
	if known || runID != 0 || profileID != 0 {
		t.Fatalf("baseline after sync end = known %v run %d profile %d, want cleared", known, runID, profileID)
	}
	state, err = app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	if state.SyncInProgress {
		t.Fatal("SyncInProgress = true after the sync ended, want false")
	}
	final, err := app.repo.LatestSyncRun(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final == nil || final.Status != "failed" || final.ID <= prior {
		t.Fatalf("latest sync run = %+v, want a failed run newer than %d", final, prior)
	}
}

// TestStartSyncFailsWhenBaselineCannotBeRead checks that a sync does not start when the profile's latest run cannot
// be read. The sync_runs table is renamed away through a second connection. Only the baseline read touches that
// table before a goroutine starts, so only that read fails.
func TestStartSyncFailsWhenBaselineCannotBeRead(t *testing.T) {
	app := newTestApp(t)
	saveTestProfile(t, app)
	app.mu.Lock()
	app.steamAPIKey = "fixture-steam-key"
	// Safety net: an unreachable address, so a regression that starts the sync cannot reach the real Steam API.
	app.steamBaseURL = "http://127.0.0.1:1"
	app.mu.Unlock()

	db, err := sql.Open("sqlite3", "file:"+app.paths.DB+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`ALTER TABLE sync_runs RENAME TO sync_runs_unreadable`); err != nil {
		t.Fatal(err)
	}

	err = app.startSync(true)
	if err == nil || !strings.Contains(err.Error(), "sync_runs") {
		t.Fatalf("startSync err = %v, want the unreadable sync_runs error", err)
	}
	assertIdleSync(t, app)
	app.mu.Lock()
	known, runID, profileID := app.syncBaselineKnown, app.syncBaselineRunID, app.syncProfileID
	app.mu.Unlock()
	if known || runID != 0 || profileID != 0 {
		t.Fatalf("baseline after failed start = known %v run %d profile %d, want cleared", known, runID, profileID)
	}
}

// TestStartSyncFailureResetsSyncBaseline checks that the done path of a failed start leaves no baseline behind.
func TestStartSyncFailureResetsSyncBaseline(t *testing.T) {
	app := &App{}
	app.mu.Lock()
	app.syncProfileID = 7
	app.syncBaselineRunID = 7
	app.syncBaselineKnown = true
	app.mu.Unlock()

	// No database, so startSync fails through done().
	if err := app.startSync(true); err == nil || !strings.Contains(err.Error(), "database is not initialized") {
		t.Fatalf("startSync err = %v, want database-not-initialized", err)
	}
	assertIdleSync(t, app)
	app.mu.Lock()
	known, runID, profileID := app.syncBaselineKnown, app.syncBaselineRunID, app.syncProfileID
	app.mu.Unlock()
	if known || runID != 0 || profileID != 0 {
		t.Fatalf("sync baseline = known %v run %d profile %d after failed start, want cleared", known, runID, profileID)
	}
}

func TestRetryAPIKeyPicksUpKeyAddedAfterStartup(t *testing.T) {
	app := newTestApp(t)
	if err := zk.Set(testKeyringService, testKeyringUser, "late-key"); err != nil {
		t.Fatal(err)
	}
	state, err := app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	if state.APIKeyPresent {
		t.Fatal("GetAppState refreshed the key, want cached state until RetryAPIKey")
	}
	state, err = app.RetryAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !state.APIKeyPresent || state.APIKeyError != "" {
		t.Fatalf("after RetryAPIKey present = %v err %q, want present without error", state.APIKeyPresent, state.APIKeyError)
	}
}

func TestRequireAPIKeyTracksKeyringState(t *testing.T) {
	app := newTestAppWithKey(t, "first-key")

	zk.MockInit() // key removed from the Secret Service
	if key, err := app.requireAPIKey(); key != "" || !errors.Is(err, keyring.ErrMissing) {
		t.Fatalf("requireAPIKey with key removed = %q, %v; want empty and ErrMissing", key, err)
	}
	if state, _ := app.GetAppState(); state.APIKeyPresent || state.APIKeyError != "" {
		t.Fatalf("state after key removed = present %v err %q, want missing without error", state.APIKeyPresent, state.APIKeyError)
	}

	zk.MockInitWithError(errors.New("secret service locked"))
	if _, err := app.requireAPIKey(); err == nil || err.Error() != "secret service locked" {
		t.Fatalf("requireAPIKey with keyring error = %v, want the keyring error", err)
	}
	if state, _ := app.GetAppState(); state.APIKeyPresent || state.APIKeyError != "secret service locked" {
		t.Fatalf("state after keyring error = present %v err %q, want error reported", state.APIKeyPresent, state.APIKeyError)
	}

	zk.MockInit()
	if err := zk.Set(testKeyringService, testKeyringUser, "second-key"); err != nil {
		t.Fatal(err)
	}
	key, err := app.requireAPIKey()
	if err != nil || key != "second-key" {
		t.Fatalf("requireAPIKey after key restored = %q, %v; want second-key", key, err)
	}
	if state, _ := app.GetAppState(); !state.APIKeyPresent || state.APIKeyError != "" {
		t.Fatalf("state after key restored = present %v err %q, want present", state.APIKeyPresent, state.APIKeyError)
	}
}

func TestSaveProfileWithoutKeyStoresProfileAndReportsMissingKey(t *testing.T) {
	app := newTestApp(t)
	state, err := app.SaveProfile(ProfilePreview{SteamID64: testSteamID, DisplayName: "Tester", AvatarURL: "https://avatars.example/a.jpg"})
	if err != nil {
		t.Fatalf("SaveProfile without key err = %v, want nil (startSync error is ignored)", err)
	}
	if !state.ProfileExists || state.Profile == nil {
		t.Fatal("profile not saved")
	}
	if state.Profile.SteamID64 != testSteamID || state.Profile.DisplayName != "Tester" {
		t.Fatalf("profile = %+v, want saved steam id and display name", *state.Profile)
	}
	if state.Profile.AvatarURL == nil || *state.Profile.AvatarURL != "https://avatars.example/a.jpg" {
		t.Fatalf("avatar = %v, want saved avatar", state.Profile.AvatarURL)
	}
	if state.APIKeyPresent || state.SyncInProgress {
		t.Fatalf("APIKeyPresent = %v SyncInProgress = %v, want missing key and no sync", state.APIKeyPresent, state.SyncInProgress)
	}
}

func TestSaveProfileDefaultsBlankDisplayNameToSteamID(t *testing.T) {
	app := newTestApp(t)
	state, err := app.SaveProfile(ProfilePreview{SteamID64: testSteamID})
	if err != nil {
		t.Fatal(err)
	}
	if state.Profile == nil || state.Profile.DisplayName != testSteamID {
		t.Fatalf("display name = %v, want steam id fallback", state.Profile)
	}
}

func TestSaveProfileRequiresSteamID(t *testing.T) {
	app := newTestApp(t)
	_, err := app.SaveProfile(ProfilePreview{DisplayName: "No Id"})
	if err == nil || !strings.Contains(err.Error(), "SteamID64 is required") {
		t.Fatalf("SaveProfile err = %v, want SteamID64 required", err)
	}
	state, err := app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	if state.ProfileExists {
		t.Fatal("profile saved despite validation error")
	}
}

func TestSaveProfileUpdatesExistingAndSwitchesActive(t *testing.T) {
	app := newTestApp(t)
	first, err := app.SaveProfile(ProfilePreview{SteamID64: testSteamID, DisplayName: "Tester", AvatarURL: "https://avatars.example/a.jpg"})
	if err != nil {
		t.Fatal(err)
	}

	// Same SteamID: the row is updated in place, so the id is kept.
	updated, err := app.SaveProfile(ProfilePreview{SteamID64: testSteamID, DisplayName: "Renamed"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Profile.ID != first.Profile.ID || updated.Profile.DisplayName != "Renamed" {
		t.Fatalf("updated profile = id %d name %q, want id %d name Renamed", updated.Profile.ID, updated.Profile.DisplayName, first.Profile.ID)
	}

	// A different SteamID is inserted and becomes the active profile.
	other, err := app.SaveProfile(ProfilePreview{SteamID64: "76561198000000002", DisplayName: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	if other.Profile.ID == first.Profile.ID || other.Profile.SteamID64 != "76561198000000002" {
		t.Fatalf("second profile = id %d steam %q, want a new active profile", other.Profile.ID, other.Profile.SteamID64)
	}
}

func TestInitFailureIsReportedAndBlocksActions(t *testing.T) {
	// A regular file where the data directory's parent should be makes appdata.Ensure fail.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", blocked)
	t.Cleanup(logging.Fallback)
	zk.MockInit()
	app := &App{}

	state, err := app.GetAppState()
	if err != nil {
		t.Fatalf("GetAppState err = %v, want nil with InitError set", err)
	}
	if state.InitError == "" || state.ProfileExists || state.DataDir != "" {
		t.Fatalf("state = initError %q profile %v dataDir %q, want init error and no paths", state.InitError, state.ProfileExists, state.DataDir)
	}
	if _, err := app.SaveProfile(ProfilePreview{SteamID64: testSteamID}); err == nil {
		t.Fatal("SaveProfile err = nil after failed init, want error")
	}
	if _, err := app.DisableGame(10, 0); err == nil {
		t.Fatal("DisableGame err = nil after failed init, want error")
	}
}

func TestClearActiveUserDataCancelsSyncAndRemovesCache(t *testing.T) {
	app := newTestApp(t)
	profile := saveTestProfile(t, app)
	seedTwoSetGame(t, app, profile.ID)
	if err := app.repo.SaveCover(repository.CoverUpdate{
		ImageAppID: 10,
		Kind:       "library",
		FileName:   "library_600x900.jpg",
		Path:       "/cache/images/games/10/library_600x900.jpg",
		SourceURL:  "https://example.test/library.jpg",
		Downloaded: true,
	}); err != nil {
		t.Fatal(err)
	}
	coverDir := filepath.Join(app.paths.GameImages, "10")
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coverDir, "library_600x900.jpg"), []byte("cover"), 0o644); err != nil {
		t.Fatal(err)
	}

	canceled := 0
	app.syncCancel = func() { canceled++ }
	state, err := app.ClearActiveUserData()
	if err != nil {
		t.Fatalf("ClearActiveUserData: %v", err)
	}
	if canceled != 1 {
		t.Fatalf("sync cancel called %d times, want 1", canceled)
	}
	if state.ProfileExists || state.Profile != nil {
		t.Fatal("profile still active after clear")
	}
	if _, err := os.Stat(coverDir); !os.IsNotExist(err) {
		t.Fatalf("orphaned cover dir still present, err=%v", err)
	}
}

func TestCancelSyncAndWaitWaitsForSyncToFinish(t *testing.T) {
	// No database or keyring is needed: cancelSyncAndWait only touches syncCancel and syncWG.
	app := &App{}
	ctx, cancel := context.WithCancel(context.Background())
	app.syncCancel = cancel
	app.syncWG.Add(1)
	var finished atomic.Bool
	go func() {
		<-ctx.Done()
		finished.Store(true)
		app.syncWG.Done()
	}()

	app.cancelSyncAndWait()
	if !finished.Load() {
		t.Fatal("cancelSyncAndWait returned before the sync goroutine finished")
	}
}

func TestGameActionsRequireActiveProfile(t *testing.T) {
	app := newTestApp(t)
	actions := map[string]func() (AppState, error){
		"DisableGame":                        func() (AppState, error) { return app.DisableGame(10, 0) },
		"EnableGame":                         func() (AppState, error) { return app.EnableGame(10, 0) },
		"MarkGamePreviouslyCompleted":        func() (AppState, error) { return app.MarkGamePreviouslyCompleted(10, 0) },
		"ToggleGameMissingAchievementsInDLC": func() (AppState, error) { return app.ToggleGameMissingAchievementsInDLC(10, 0) },
	}
	for name, action := range actions {
		t.Run(name, func(t *testing.T) {
			if _, err := action(); err == nil || !strings.Contains(err.Error(), "no active profile") {
				t.Fatalf("%s err = %v, want no-active-profile", name, err)
			}
		})
	}
}

func TestDisableAndEnableGameAffectOnlyOneSet(t *testing.T) {
	app := newTestApp(t)
	profile := saveTestProfile(t, app)
	seedTwoSetGame(t, app, profile.ID)

	state, err := app.DisableGame(10, 7)
	if err != nil {
		t.Fatal(err)
	}
	disabled := mustCard(t, state.Dashboard, 10, 7)
	if !disabled.Disabled || !disabled.ManualDisabled || disabled.DisabledReason == nil || *disabled.DisabledReason != "manual" {
		t.Fatalf("disabled set = %+v, want manually disabled", disabled)
	}
	if !inList(state.Dashboard.Disabled, 10, 7) || inList(state.Dashboard.Suggestions, 10, 7) {
		t.Fatal("disabled set not listed under Disabled only")
	}
	sibling := mustCard(t, state.Dashboard, 10, 0)
	if sibling.Disabled || !inList(state.Dashboard.Suggestions, 10, 0) {
		t.Fatalf("sibling set = %+v, want still enabled", sibling)
	}

	state, err = app.EnableGame(10, 7)
	if err != nil {
		t.Fatal(err)
	}
	enabled := mustCard(t, state.Dashboard, 10, 7)
	if enabled.Disabled || enabled.ManualDisabled || enabled.DisabledReason != nil {
		t.Fatalf("re-enabled set = %+v, want enabled with no reason", enabled)
	}
	if len(state.Dashboard.Disabled) != 0 {
		t.Fatalf("Disabled list = %d cards after enable, want 0", len(state.Dashboard.Disabled))
	}
}

func TestMarkGamePreviouslyCompletedTogglesOneSet(t *testing.T) {
	app := newTestApp(t)
	profile := saveTestProfile(t, app)
	seedTwoSetGame(t, app, profile.ID)

	state, err := app.MarkGamePreviouslyCompleted(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	marked := mustCard(t, state.Dashboard, 10, 0)
	if !marked.WasCompleted || !marked.ManualWasCompleted {
		t.Fatalf("marked set = %+v, want was-completed via manual flag", marked)
	}
	if other := mustCard(t, state.Dashboard, 10, 7); other.WasCompleted || other.ManualWasCompleted {
		t.Fatalf("sibling set = %+v, want unchanged", other)
	}

	// The action toggles the manual flag: a second call clears it again.
	state, err = app.MarkGamePreviouslyCompleted(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if again := mustCard(t, state.Dashboard, 10, 0); again.WasCompleted || again.ManualWasCompleted {
		t.Fatalf("set after second call = %+v, want flag cleared", again)
	}
}

func TestMarkGamePreviouslyCompletedUnknownSetLeavesNoFlag(t *testing.T) {
	app := newTestApp(t)
	profile := saveTestProfile(t, app)
	seedTwoSetGame(t, app, profile.ID)

	if _, err := app.MarkGamePreviouslyCompleted(10, 99); err == nil {
		t.Fatal("MarkGamePreviouslyCompleted unknown set err = nil, want error")
	}
	// Add set 99 to the game. The dashboard joins profile_game_flags on (profile, appid, groupid), so a flag
	// leaked by the failed mark would show on this card.
	set := func(groupID int64, name string) repository.SetSyncUpdate {
		return repository.SetSyncUpdate{
			GroupID:           groupID,
			Name:              name,
			TotalAchievements: 1,
			Achievements:      []repository.AchievementRecord{{APIName: "ACH_ONE"}},
		}
	}
	if err := app.repo.SaveGameSync(repository.GameSyncUpdate{
		ProfileID:         profile.ID,
		AppID:             10,
		Name:              "Game Ten",
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets:              []repository.SetSyncUpdate{set(0, ""), set(7, "Bonus"), set(99, "Unknown")},
		SyncedAt:          time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	state, err := app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	if card := mustCard(t, state.Dashboard, 10, 99); card.ManualWasCompleted {
		t.Fatal("failed mark left a manual flag behind on set 99")
	}
}

func TestRemoveCachedGameImagesOnlyRequestedDirs(t *testing.T) {
	root := t.TempDir()
	for _, appID := range []string{"10", "20", "30"} {
		dir := filepath.Join(root, appID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "library_600x900.jpg"), []byte("image"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := removeCachedGameImages(root, []int64{10, 0, -1, 20}); err != nil {
		t.Fatal(err)
	}

	for _, appID := range []string{"10", "20"} {
		if _, err := os.Stat(filepath.Join(root, appID)); !os.IsNotExist(err) {
			t.Fatalf("cache dir %s exists after removal, err=%v", appID, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "30", "library_600x900.jpg")); err != nil {
		t.Fatalf("unrequested cache dir removed: %v", err)
	}
}

// steamStubRequest is one request received by a steamStub.
type steamStubRequest struct {
	path  string
	query url.Values
}

// newSteamStub starts a fake Steam API that answers each request with respond and records the requests it gets. The
// server is closed at test end. Tests point app.steamBaseURL at it, so no real network is touched. The returned func
// reports the requests received so far.
func newSteamStub(t *testing.T, respond func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, func() []steamStubRequest) {
	t.Helper()
	var mu sync.Mutex
	var reqs []steamStubRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, steamStubRequest{path: r.URL.Path, query: r.URL.Query()})
		mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(server.Close)
	received := func() []steamStubRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]steamStubRequest(nil), reqs...)
	}
	return server, received
}

// forbidSteam rejects a request with a non-retryable 403, so the call fails at once.
func forbidSteam(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "forbidden", http.StatusForbidden)
}

func setSteamBaseURL(app *App, baseURL string) {
	app.mu.Lock()
	app.steamBaseURL = baseURL
	app.mu.Unlock()
}

// The Steam base URL override reaches ValidateProfile: a 17-digit SteamID64 needs only the player summary request.
func TestValidateProfileWithSteamIDMakesOnlySummaryRequest(t *testing.T) {
	app := newTestAppWithKey(t, "fixture-steam-key")
	stub, received := newSteamStub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"response":{"players":[{"steamid":%q,"personaname":"Tester","avatarfull":"https://avatars.example/full.jpg","avatarmedium":"https://avatars.example/medium.jpg"}]}}`, testSteamID)
	})
	setSteamBaseURL(app, stub.URL)

	preview, err := app.ValidateProfile(testSteamID)
	if err != nil {
		t.Fatalf("ValidateProfile: %v", err)
	}
	want := ProfilePreview{SteamID64: testSteamID, DisplayName: "Tester", AvatarURL: "https://avatars.example/full.jpg"}
	if preview != want {
		t.Fatalf("preview = %+v, want %+v", preview, want)
	}
	reqs := received()
	if len(reqs) != 1 || reqs[0].path != "/ISteamUser/GetPlayerSummaries/v2/" {
		t.Fatalf("Steam requests = %+v, want one GetPlayerSummaries request", reqs)
	}
	if reqs[0].query.Get("steamids") != testSteamID || reqs[0].query.Get("key") != "fixture-steam-key" {
		t.Fatalf("summary query = %v, want steamids %s and the API key", reqs[0].query, testSteamID)
	}
}

// A vanity name is resolved to a SteamID64 first, then the summary of that ID is fetched. Without a full avatar the
// medium avatar is used.
func TestValidateProfileVanityNameResolvesThenFetchesSummary(t *testing.T) {
	const resolved = "76561198000000009"
	app := newTestAppWithKey(t, "fixture-steam-key")
	stub, received := newSteamStub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ISteamUser/ResolveVanityURL/v1/":
			fmt.Fprintf(w, `{"response":{"success":1,"steamid":%q}}`, resolved)
		case "/ISteamUser/GetPlayerSummaries/v2/":
			fmt.Fprintf(w, `{"response":{"players":[{"steamid":%q,"personaname":"Vanity Tester","avatarmedium":"https://avatars.example/medium.jpg"}]}}`, resolved)
		default:
			http.NotFound(w, r)
		}
	})
	setSteamBaseURL(app, stub.URL)

	preview, err := app.ValidateProfile("vanity-tester")
	if err != nil {
		t.Fatalf("ValidateProfile: %v", err)
	}
	want := ProfilePreview{SteamID64: resolved, DisplayName: "Vanity Tester", AvatarURL: "https://avatars.example/medium.jpg"}
	if preview != want {
		t.Fatalf("preview = %+v, want %+v", preview, want)
	}
	reqs := received()
	if len(reqs) != 2 || reqs[0].path != "/ISteamUser/ResolveVanityURL/v1/" || reqs[1].path != "/ISteamUser/GetPlayerSummaries/v2/" {
		t.Fatalf("Steam requests = %+v, want ResolveVanityURL then GetPlayerSummaries", reqs)
	}
	if reqs[0].query.Get("vanityurl") != "vanity-tester" {
		t.Fatalf("resolve query = %v, want vanityurl vanity-tester", reqs[0].query)
	}
	if reqs[1].query.Get("steamids") != resolved {
		t.Fatalf("summary query = %v, want steamids %s", reqs[1].query, resolved)
	}
}

func TestValidateProfileWithoutKeyReturnsErrMissing(t *testing.T) {
	app := newTestApp(t)
	stub, received := newSteamStub(t, forbidSteam)
	setSteamBaseURL(app, stub.URL)

	if _, err := app.ValidateProfile(testSteamID); !errors.Is(err, keyring.ErrMissing) {
		t.Fatalf("ValidateProfile err = %v, want ErrMissing", err)
	}
	if reqs := received(); len(reqs) != 0 {
		t.Fatalf("Steam requests = %+v without a key, want none", reqs)
	}
}

// A Steam HTTP error is returned to the caller as the steam package's HTTPStatusError.
func TestValidateProfileSteamHTTPErrorPropagates(t *testing.T) {
	app := newTestAppWithKey(t, "fixture-steam-key")
	stub, _ := newSteamStub(t, forbidSteam)
	setSteamBaseURL(app, stub.URL)

	_, err := app.ValidateProfile(testSteamID)
	var statusErr *steam.HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusForbidden {
		t.Fatalf("ValidateProfile err = %v, want HTTPStatusError with code 403", err)
	}
}

// Blank input fails before any Steam request is made.
func TestValidateProfileEmptyInputErrors(t *testing.T) {
	app := newTestAppWithKey(t, "fixture-steam-key")
	stub, received := newSteamStub(t, forbidSteam)
	setSteamBaseURL(app, stub.URL)

	for _, input := range []string{"", "   "} {
		if _, err := app.ValidateProfile(input); err == nil || !strings.Contains(err.Error(), "profile input is required") {
			t.Fatalf("ValidateProfile(%q) err = %v, want profile-input-required", input, err)
		}
	}
	if reqs := received(); len(reqs) != 0 {
		t.Fatalf("Steam requests = %+v for blank input, want none", reqs)
	}
}

func TestValidateProfileReportsPlayerNotFound(t *testing.T) {
	app := newTestAppWithKey(t, "fixture-steam-key")
	stub, _ := newSteamStub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"response":{"players":[]}}`)
	})
	setSteamBaseURL(app, stub.URL)

	if _, err := app.ValidateProfile(testSteamID); err == nil || !strings.Contains(err.Error(), "Steam profile not found") {
		t.Fatalf("ValidateProfile err = %v, want Steam profile not found", err)
	}
}

func TestSyncNowWithoutKeyReturnsErrMissing(t *testing.T) {
	app := newTestApp(t)
	saveTestProfile(t, app)

	if _, err := app.SyncNow(); !errors.Is(err, keyring.ErrMissing) {
		t.Fatalf("SyncNow err = %v, want ErrMissing", err)
	}
	assertIdleSync(t, app)
}

// With a key but no profile saved, SyncNow passes the key check and startSync fails on the missing profile.
func TestSyncNowWithoutActiveProfileErrors(t *testing.T) {
	app := newTestAppWithKey(t, "fixture-steam-key")

	if _, err := app.SyncNow(); err == nil || !strings.Contains(err.Error(), "no active profile") {
		t.Fatalf("SyncNow err = %v, want no-active-profile", err)
	}
	assertIdleSync(t, app)
}

// TestSyncNowStartsSyncAndRejectsSecondStart runs SyncNow against a Steam stub that holds its first request. The first
// call starts the sync and returns state with it in progress; a second call while it runs gets
// errSyncAlreadyInProgress. Releasing the stub fails the sync at its first call, as in
// TestStartSyncRecordsBaselineAndFollowsItsOwnRun.
func TestSyncNowStartsSyncAndRejectsSecondStart(t *testing.T) {
	app := newTestApp(t)
	profile := saveTestProfile(t, app)
	if err := zk.Set(testKeyringService, testKeyringUser, "fixture-steam-key"); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	stub, received := newSteamStub(t, func(w http.ResponseWriter, r *http.Request) {
		<-release
		forbidSteam(w, r)
	})
	// Cleanups run in reverse order, so this unblock runs before the stub's server.Close and a failed test cannot hang.
	t.Cleanup(unblock)
	setSteamBaseURL(app, stub.URL)

	state, err := app.SyncNow()
	if err != nil {
		t.Fatalf("SyncNow: %v", err)
	}
	if !state.SyncInProgress || state.Profile == nil || state.Profile.ID != profile.ID {
		t.Fatalf("SyncNow state = in progress %v profile %+v, want the active profile syncing", state.SyncInProgress, state.Profile)
	}
	if _, err := app.SyncNow(); !errors.Is(err, errSyncAlreadyInProgress) {
		t.Fatalf("second SyncNow err = %v, want errSyncAlreadyInProgress", err)
	}

	unblock()
	waitForSyncWG(t, app)
	assertIdleSync(t, app)
	reqs := received()
	if len(reqs) == 0 || reqs[0].query.Get("key") != "fixture-steam-key" {
		t.Fatalf("Steam requests = %+v, want the sync's request carrying the API key", reqs)
	}
	final, err := app.repo.LatestSyncRun(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final == nil || final.Status != "failed" {
		t.Fatalf("latest sync run = %+v, want failed", final)
	}
}

// countSteamKeyReads wraps readSteamAPIKey so that reads are counted. The real read path still runs.
func countSteamKeyReads(t *testing.T) *atomic.Int32 {
	t.Helper()
	var reads atomic.Int32
	prev := readSteamAPIKey
	readSteamAPIKey = func() (string, error) {
		reads.Add(1)
		return prev()
	}
	t.Cleanup(func() { readSteamAPIKey = prev })
	return &reads
}

// cachedAPIKeyState returns the cached key fields under the lock.
func cachedAPIKeyState(app *App) (key string, present bool, keyErr string) {
	app.mu.Lock()
	defer app.mu.Unlock()
	return app.steamAPIKey, app.apiKeyPresent, app.apiKeyError
}

func TestSaveAPIKeyStoresKeyAndUpdatesCacheWithoutExtraRead(t *testing.T) {
	app := newTestApp(t)
	reads := countSteamKeyReads(t)
	before := reads.Load()

	if err := app.SaveAPIKey("  first-key \t"); err != nil {
		t.Fatalf("SaveAPIKey: %v", err)
	}
	if n := reads.Load() - before; n != 0 {
		t.Fatalf("SaveAPIKey read the keyring %d times, want 0: the cache is set from the store", n)
	}
	key, present, keyErr := cachedAPIKeyState(app)
	if key != "first-key" || !present || keyErr != "" {
		t.Fatalf("cache = key %q present %v err %q, want trimmed key present without error", key, present, keyErr)
	}
	state, err := app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.APIKeyPresent || state.APIKeyError != "" {
		t.Fatalf("GetAppState api key = present %v err %q, want present without error", state.APIKeyPresent, state.APIKeyError)
	}
	stored, err := zk.Get(testKeyringService, testKeyringUser)
	if err != nil || stored != "first-key" {
		t.Fatalf("keyring = %q, %v; want first-key", stored, err)
	}
}

func TestSaveAPIKeyReplacesExistingKey(t *testing.T) {
	app := newTestAppWithKey(t, "old-key")

	if err := app.SaveAPIKey("new-key"); err != nil {
		t.Fatalf("SaveAPIKey: %v", err)
	}
	key, present, keyErr := cachedAPIKeyState(app)
	if key != "new-key" || !present || keyErr != "" {
		t.Fatalf("cache = key %q present %v err %q, want new-key present without error", key, present, keyErr)
	}
	stored, err := zk.Get(testKeyringService, testKeyringUser)
	if err != nil || stored != "new-key" {
		t.Fatalf("keyring = %q, %v; want new-key", stored, err)
	}
}

// SaveAPIKey must not start a sync. A profile is active and the key is set, so a started sync would reach the Steam
// stub; the stub must receive nothing and the sync flags must stay idle.
func TestSaveAPIKeyDoesNotStartSync(t *testing.T) {
	app := newTestApp(t)
	saveTestProfile(t, app)
	stub, received := newSteamStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	})
	setSteamBaseURL(app, stub.URL)

	if err := app.SaveAPIKey("fixture-steam-key"); err != nil {
		t.Fatalf("SaveAPIKey: %v", err)
	}
	app.mu.Lock()
	inProgress, cancel := app.syncInProgress, app.syncCancel
	app.mu.Unlock()
	if inProgress || cancel != nil {
		t.Fatalf("SaveAPIKey started a sync: syncInProgress %v syncCancel set %v", inProgress, cancel != nil)
	}
	if reqs := received(); len(reqs) != 0 {
		t.Fatalf("SaveAPIKey sent %d Steam requests, want none", len(reqs))
	}
}

// A failed store leaves the cached key state as it was: a working old key stays present, and an earlier key error
// stays reported.
func TestSaveAPIKeyKeyringErrorLeavesCacheUnchanged(t *testing.T) {
	t.Run("working old key stays present", func(t *testing.T) {
		app := newTestAppWithKey(t, "old-key")
		zk.MockInitWithError(errors.New("secret service locked"))

		err := app.SaveAPIKey("new-key")
		if err == nil || !strings.Contains(err.Error(), "secret service locked") {
			t.Fatalf("SaveAPIKey err = %v, want the keyring error", err)
		}
		key, present, keyErr := cachedAPIKeyState(app)
		if key != "old-key" || !present || keyErr != "" {
			t.Fatalf("cache = key %q present %v err %q, want old-key present without error", key, present, keyErr)
		}
	})

	t.Run("earlier key error stays reported", func(t *testing.T) {
		zk.MockInitWithError(errors.New("secret service unavailable"))
		app := openTestApp(t)
		zk.MockInitWithError(errors.New("secret service locked"))

		if err := app.SaveAPIKey("new-key"); err == nil {
			t.Fatal("SaveAPIKey err = nil, want keyring error")
		}
		key, present, keyErr := cachedAPIKeyState(app)
		if key != "" || present || keyErr != "secret service unavailable" {
			t.Fatalf("cache = key %q present %v err %q, want no key and the earlier error", key, present, keyErr)
		}
	})
}

// Profile-switch flow: with no active profile the saved key is what the next key-using call reads. ValidateProfile
// sends it to Steam.
func TestSaveAPIKeyWithoutActiveProfileIsUsedByNextRequests(t *testing.T) {
	app := newTestApp(t)
	if err := app.SaveAPIKey("profile-b-key"); err != nil {
		t.Fatalf("SaveAPIKey: %v", err)
	}
	key, err := app.requireAPIKey()
	if err != nil || key != "profile-b-key" {
		t.Fatalf("requireAPIKey = %q, %v; want profile-b-key", key, err)
	}

	stub, received := newSteamStub(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"response":{"players":[{"steamid":%q,"personaname":"Tester"}]}}`, testSteamID)
	})
	setSteamBaseURL(app, stub.URL)
	if _, err := app.ValidateProfile(testSteamID); err != nil {
		t.Fatalf("ValidateProfile: %v", err)
	}
	reqs := received()
	if len(reqs) != 1 || reqs[0].query.Get("key") != "profile-b-key" {
		t.Fatalf("Steam requests = %+v, want one request with the saved key", reqs)
	}
}

// Init failure: SaveAPIKey returns the init error, stores nothing, and the error does not contain the input.
func TestSaveAPIKeyInitFailureStoresNothing(t *testing.T) {
	// A regular file where the data directory's parent should be makes appdata.Ensure fail.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", blocked)
	t.Cleanup(logging.Fallback)
	zk.MockInit()

	var stores atomic.Int32
	prevStore := storeSteamAPIKey
	storeSteamAPIKey = func(key string) (string, error) {
		stores.Add(1)
		return prevStore(key)
	}
	t.Cleanup(func() { storeSteamAPIKey = prevStore })

	app := &App{}
	err := app.SaveAPIKey("  SENTINELkey  ")
	if err == nil {
		t.Fatal("SaveAPIKey err = nil after failed init, want error")
	}
	if strings.Contains(err.Error(), "SENTINEL") {
		t.Fatalf("error text %q contains the key", err.Error())
	}
	if n := stores.Load(); n != 0 {
		t.Fatalf("store called %d times after failed init, want 0", n)
	}
}

// SaveAPIKey error paths that the App reaches keep the key out of the error text. Each path gets its own input so
// that it is reached; padding is trimmed before the store, so the assertion uses the bare sentinel. The keyring
// package tests cover the invalid-character, read-back, and mismatch paths in depth.
func TestSaveAPIKeyErrorsNeverContainTheKey(t *testing.T) {
	const sentinel = "SENTINEL"
	backendErr := errors.New("boom")
	tests := []struct {
		name  string
		input string
		setup func()
	}{
		{"invalid input passes through", "SENTINEL\u200bkey", zk.MockInit},
		{"store error", "  SENTINELkey  ", func() { zk.MockInitWithError(backendErr) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			app := openTestApp(t)
			err := app.SaveAPIKey(tt.input)
			if err == nil {
				t.Fatal("SaveAPIKey err = nil, want an error")
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Fatalf("error text %q contains the key", err.Error())
			}
		})
	}
}

// Ordering: a refresh that read the old key before a save must not overwrite the saved key in the cache. The fake
// read blocks until released, so the refresh holds keyMu while the save waits behind it. Fakes are installed after
// openTestApp, because ensureInitialized calls the read fake. The fake maps are not used, so there is no race.
func TestSaveAPIKeyWaitsForInFlightRefresh(t *testing.T) {
	app := newTestApp(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	storeCalled := make(chan struct{})
	var releaseOnce, enteredOnce, storeOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	defer closeRelease()

	var reads, stores atomic.Int32
	prevRead, prevStore := readSteamAPIKey, storeSteamAPIKey
	t.Cleanup(func() {
		readSteamAPIKey = prevRead
		storeSteamAPIKey = prevStore
	})
	readSteamAPIKey = func() (string, error) {
		reads.Add(1)
		enteredOnce.Do(func() { close(entered) })
		<-release
		return "old", nil
	}
	storeSteamAPIKey = func(key string) (string, error) {
		stores.Add(1)
		storeOnce.Do(func() { close(storeCalled) })
		return key, nil
	}

	refreshDone := make(chan error, 1)
	go func() { refreshDone <- app.refreshAPIKey() }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refreshAPIKey did not read the keyring")
	}

	saveDone := make(chan error, 1)
	go func() { saveDone <- app.SaveAPIKey("new") }()
	select {
	case <-storeCalled:
		t.Fatal("SaveAPIKey stored while refreshAPIKey held the key lock")
	case <-time.After(50 * time.Millisecond):
	}

	closeRelease()
	if err := <-refreshDone; err != nil {
		t.Fatalf("refreshAPIKey: %v", err)
	}
	if err := <-saveDone; err != nil {
		t.Fatalf("SaveAPIKey: %v", err)
	}
	key, _, _ := cachedAPIKeyState(app)
	if key != "new" {
		t.Fatalf("cached key = %q, want new: the refresh overwrote the save", key)
	}
	if reads.Load() != 1 || stores.Load() != 1 {
		t.Fatalf("reads = %d stores = %d, want 1 and 1", reads.Load(), stores.Load())
	}
}
