package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	"github.com/gjrud/steam-achievement-tracker/internal/config"
	"github.com/gjrud/steam-achievement-tracker/internal/keyring"
	"github.com/gjrud/steam-achievement-tracker/internal/logging"
	"github.com/gjrud/steam-achievement-tracker/internal/repository"
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
	app := &App{ctx: context.Background()}
	if err := app.ensureInitialized(); err != nil {
		t.Fatalf("ensureInitialized: %v", err)
	}
	t.Cleanup(func() { app.shutdown(context.Background()) })
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
	app := &App{ctx: context.Background(), syncInProgress: true}

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
		app := &App{ctx: context.Background()}
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
	if state.SecretSetupCommand != config.SecretSetupCommand {
		t.Fatalf("SecretSetupCommand = %q, want config value", state.SecretSetupCommand)
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

// TestGetAppStateStaleSyncFlag guards defensive code in GetAppState: it clears syncInProgress when no sync
// goroutine is live (syncCancel == nil) and the latest run is no longer "running". Production writes
// syncInProgress and syncCancel together under mu, so this state is only reachable by setting it by hand here.
func TestGetAppStateStaleSyncFlag(t *testing.T) {
	app := newTestApp(t)
	profile := saveTestProfile(t, app)
	id, err := app.repo.StartSyncRun(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.repo.FinishSyncRun(id, "completed", 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	app.mu.Lock()
	app.syncInProgress = true
	app.mu.Unlock()

	state, err := app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	if state.SyncInProgress {
		t.Fatal("state.SyncInProgress = true after a finished run, want stale flag cleared")
	}
	app.mu.Lock()
	got := app.syncInProgress
	app.mu.Unlock()
	if got {
		t.Fatal("app.syncInProgress = true after a finished run, want stale flag cleared")
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
	app := &App{ctx: context.Background()}

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

func TestAssetHandlerRejectsNonPositiveAppIDEvenWhenFileExists(t *testing.T) {
	root := t.TempDir()
	// Files exist for every id, so a 404 can only come from the appid check and not from a missing file.
	for _, id := range []string{"0", "-5", "1"} {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "library_600x900.jpg"), []byte("cover-"+id), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	app := &App{paths: appdata.Paths{GameImages: root}}
	handler := app.assetHandler()

	tests := []struct {
		id     string
		status int
	}{
		{"0", http.StatusNotFound},
		{"-5", http.StatusNotFound},
		{"1", http.StatusOK},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/game-covers/"+tt.id+"/library_600x900.jpg", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tt.status {
			t.Fatalf("appid %s status = %d, want %d", tt.id, rec.Code, tt.status)
		}
	}
}

// TestAssetHandlerReadsPathsSafelyWhileInitializing races ensureInitialized against assetHandler requests.
// It only detects a regression (unlocked read of a.paths) under `go test -race`; without the race detector
// the unsynchronized read usually passes silently.
func TestAssetHandlerReadsPathsSafelyWhileInitializing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(logging.Fallback)
	zk.MockInit()
	app := &App{ctx: context.Background()}
	handler := app.assetHandler()

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		if err := app.ensureInitialized(); err != nil {
			t.Errorf("ensureInitialized: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 200; i++ {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/game-covers/10/library_600x900.jpg", nil))
		}
	}()
	close(start)
	wg.Wait()
	t.Cleanup(func() { app.shutdown(context.Background()) })

	coverDir := filepath.Join(app.paths.GameImages, "10")
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coverDir, "library_600x900.jpg"), []byte("after-init"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/game-covers/10/library_600x900.jpg", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "after-init" {
		t.Fatalf("after init status = %d body = %q, want 200 with cover bytes", rec.Code, rec.Body.String())
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

// TestAssetHandlerServesCoverKinds covers every happy-path cover route: a cached
// per-game cover plus the hero, main and header fallbacks for DLC image apps.
// Each case writes one file under <GameImages>/<appID>/<file> and asserts the
// handler returns 200 with exactly those bytes.
func TestAssetHandlerServesCoverKinds(t *testing.T) {
	tests := []struct {
		kind  string
		appID string
		file  string
		body  string
	}{
		{kind: "cached", appID: "123", file: "library_600x900.jpg", body: "cover-bytes"},
		{kind: "hero", appID: "4173830", file: "hero_capsule.jpg", body: "hero-bytes"},
		{kind: "main", appID: "4173830", file: "main_capsule.jpg", body: "main-bytes"},
		{kind: "header", appID: "4173830", file: "header.jpg", body: "header-bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			imagesDir := filepath.Join(t.TempDir(), "cache", "images", "games")
			coverPath := filepath.Join(imagesDir, tt.appID, tt.file)
			if err := os.MkdirAll(filepath.Dir(coverPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(coverPath, []byte(tt.body), 0o644); err != nil {
				t.Fatal(err)
			}
			app := &App{paths: appdata.Paths{GameImages: imagesDir}}

			req := httptest.NewRequest(http.MethodGet, "/game-covers/"+tt.appID+"/"+tt.file, nil)
			rec := httptest.NewRecorder()
			app.assetHandler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Body.String(); got != tt.body {
				t.Fatalf("body = %q, want %q", got, tt.body)
			}
		})
	}
}

func TestAssetHandlerRejectsInvalidCoverRoutes(t *testing.T) {
	app := &App{paths: appdata.Paths{GameImages: t.TempDir()}}
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/game-covers/123/library_600x900.jpg"},
		{http.MethodGet, "/game-covers/not-an-id/library_600x900.jpg"},
		{http.MethodGet, "/game-covers/0/library_600x900.jpg"},
		{http.MethodGet, "/game-covers/-5/library_600x900.jpg"},
		{http.MethodGet, "/game-covers/123/other.jpg"},
		{http.MethodGet, "/game-covers/123/header.png"},
		{http.MethodGet, "/game-covers/123/../library_600x900.jpg"},
		{http.MethodGet, "/game-covers/123/nested/library_600x900.jpg"},
		{http.MethodGet, "/game-covers/123/hero_capsule.png"},
		{http.MethodGet, "/game-covers/123/main_capsule.png"},
		{http.MethodGet, "/game-covers/123/main_capsule.jpg.tmp"},
		{http.MethodGet, "/other/123/library_600x900.jpg"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		rec := httptest.NewRecorder()
		app.assetHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want %d", tt.method, tt.path, rec.Code, http.StatusNotFound)
		}
	}
}
