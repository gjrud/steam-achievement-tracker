package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	"github.com/gjrud/steam-achievement-tracker/internal/config"
	dbinit "github.com/gjrud/steam-achievement-tracker/internal/db"
	"github.com/gjrud/steam-achievement-tracker/internal/fsretry"
	"github.com/gjrud/steam-achievement-tracker/internal/images"
	"github.com/gjrud/steam-achievement-tracker/internal/keyring"
	"github.com/gjrud/steam-achievement-tracker/internal/logging"
	"github.com/gjrud/steam-achievement-tracker/internal/redact"
	"github.com/gjrud/steam-achievement-tracker/internal/repository"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
	"github.com/gjrud/steam-achievement-tracker/internal/syncer"
	"github.com/gjrud/steam-achievement-tracker/internal/update"
	"github.com/gjrud/steam-achievement-tracker/internal/usererr"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// syncChangedEvent is the Wails event emitted whenever sync state changes. It carries no payload: the frontend
// refetches GetAppState when it arrives.
const syncChangedEvent = "sync:changed"

// readSteamAPIKey, storeSteamAPIKey and deleteSteamAPIKey are the keyring calls the App makes. Tests swap them to count
// reads and to simulate store or delete failures or ordering; production uses the keyring package. Tests restore them
// with t.Cleanup.
var (
	readSteamAPIKey   = keyring.GetSteamAPIKey
	storeSteamAPIKey  = keyring.SetSteamAPIKey
	deleteSteamAPIKey = keyring.DeleteSteamAPIKey
)

type App struct {
	// lifeCtx is the service lifetime context, created by NewApp and cancelled by ServiceShutdown. Sync and Steam
	// request contexts derive from it through context(). lifeCancel is its cancel func.
	lifeCtx    context.Context
	lifeCancel context.CancelFunc
	// keyMu serializes the keyring operations that update the cached key state: SaveAPIKey and refreshAPIKey. It is
	// held over the keyring call and the cache write, so a read that started before a save cannot overwrite the
	// saved key. Lock order: keyMu before mu. Never take keyMu while holding mu.
	keyMu  sync.Mutex
	mu     sync.Mutex
	initMu sync.Mutex
	// emit delivers named Wails events to the frontend. Set once by main through setEventEmitter; nil in tests.
	// It is read under mu and called only after mu is released.
	emit func(name string)

	initialized bool
	// initError is the user-facing init failure sentence shown in AppState (msgInitDataFolder or msgInitDatabase).
	// The raw error is logged by the caller, with paths reduced to base names. It is set by setInitError on a failed
	// init and cleared on success.
	initError string
	// paths is set as soon as they are resolved, so a failed init still names the data folder. logFile is set once
	// logging is up; it stays empty when the data folder cannot be created.
	paths          appdata.Paths
	logFile        string
	repo           *repository.Repository
	steamAPIKey    string
	apiKeyPresent  bool
	apiKeyError    string
	syncInProgress bool
	syncCancel     context.CancelFunc
	// syncProfileID, syncBaselineRunID and syncBaselineKnown describe the current sync. syncBaselineRunID is the
	// profile's latest sync run id when the sync started, valid only while syncBaselineKnown is set. A later run of
	// the same profile with a higher id is the current sync's own run.
	syncProfileID     int64
	syncBaselineRunID int64
	syncBaselineKnown bool
	syncWG            sync.WaitGroup
	// updates is the in-app updater, set once by main through setUpdater; nil in tests that do not set one. It is read
	// under mu and used only after mu is released.
	updates *update.Service
	// steamBaseURL overrides the Steam API base URL of sync clients when non-empty. Tests only; production leaves it empty.
	steamBaseURL string
}

type AppState struct {
	AppName        string                     `json:"appName"`
	DataDir        string                     `json:"dataDir"`
	CacheDir       string                     `json:"cacheDir"`
	LogDir         string                     `json:"logDir"`
	LogFile        string                     `json:"logFile"`
	APIKeyPresent  bool                       `json:"apiKeyPresent"`
	APIKeyError    string                     `json:"apiKeyError"`
	ProfileExists  bool                       `json:"profileExists"`
	Profile        *repository.Profile        `json:"profile"`
	Dashboard      *repository.Dashboard      `json:"dashboard"`
	Settings       repository.LibrarySettings `json:"settings"`
	SyncInProgress bool                       `json:"syncInProgress"`
	InitError      string                     `json:"initError"`
}

type ProfilePreview struct {
	SteamID64   string `json:"steamId64"`
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrl"`
}

// NewApp returns the backend service with its lifetime context set. Wails v3 services are plain structs: there is no
// runtime context to receive, so the lifetime context is owned here and cancelled by ServiceShutdown.
func NewApp() *App {
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	return &App{lifeCtx: lifeCtx, lifeCancel: lifeCancel}
}

// ServiceStartup is the Wails v3 service startup hook. Wails calls it before the first window opens. Its name is
// reserved by the binding engine, so the frontend never sees it as a bound method. Init failures are not returned:
// they surface to the UI through AppState.InitError, so the app still starts.
func (a *App) ServiceStartup(_ context.Context, _ application.ServiceOptions) error {
	err := a.ensureInitialized()
	// The update service's Init failure is logged here, whether or not the app's own init succeeded.
	a.logUpdateInitFailure()
	// Post-update cleanup runs before the init-error return, so a database failure does not skip it.
	a.startUpdateHousekeeping()
	if err != nil {
		// The raw init error is logged here, once at startup. The UI gets only the sentence in InitError. The update
		// check is not started either: the toggle was never loaded, so StartupCheck would skip anyway.
		log.Printf("startup init: %s", logText(err))
		return nil
	}
	// The startup check relies on ServiceStartup being the first caller of ensureInitialized: that call loads the update
	// toggle when the database opens, so the toggle is cached before StartupCheck reads it. Do not add machinery for
	// other call orders.
	a.startUpdateCheck()
	state, err := a.appState()
	if err != nil {
		log.Printf("startup state: %v", redact.Paths(err))
		return nil
	}
	if state.APIKeyPresent && state.ProfileExists {
		logStartSyncError(a.startSync(false))
	}
	return nil
}

// ServiceShutdown is the Wails v3 service shutdown hook. Wails calls it during application shutdown; windows may
// still be open, so calls from the frontend can still arrive. Its name is reserved by the binding engine, so the
// frontend never sees it as a bound method. It cancels the lifetime context, stops the update service and any running
// sync, waits a bounded time for the sync, and closes the database.
//
// Restart in the update service runs this inside the framework's quit, and the helper gives the old process 30 s to
// exit. The wait is bounded so a long sync cannot make the helper relaunch the old version.
func (a *App) ServiceShutdown() error {
	if a.lifeCancel != nil {
		a.lifeCancel()
	}
	a.stopUpdates()
	a.mu.Lock()
	cancel := a.syncCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.waitForSyncs()
	a.mu.Lock()
	repo := a.repo
	a.repo = nil
	a.mu.Unlock()
	if repo != nil {
		_ = repo.Close()
	}
	return nil
}

// syncShutdownWait bounds ServiceShutdown's wait for running syncs. It is comfortably under the 30 s the update helper
// gives the old process to exit. A variable so tests can shorten it.
var syncShutdownWait = 10 * time.Second

// waitForSyncs waits for running syncs to finish, for at most syncShutdownWait. After the bound it logs a warning
// without paths and returns; the caller then closes the database. The waiting goroutine ends when the sync does.
func (a *App) waitForSyncs() {
	done := make(chan struct{})
	go func() {
		a.syncWG.Wait()
		close(done)
	}()
	timer := time.NewTimer(syncShutdownWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		log.Printf("warning: shutdown: sync still running after %s; closing the database", syncShutdownWait)
	}
}

// GetAppState is bound to the frontend. Like every bound method that returns an error, it is a one-line call to
// callBound (app_errors.go), which keeps the error user-facing and turns a panic into a generic sentence.
func (a *App) GetAppState() (AppState, error) {
	return callBound("GetAppState", func() (AppState, error) { return a.appState() })
}

func (a *App) appState() (AppState, error) {
	if err := a.ensureInitialized(); err != nil {
		a.mu.Lock()
		repo := a.repo
		a.mu.Unlock()
		if repo == nil {
			return a.baseState(nil, nil), nil
		}
	}
	a.mu.Lock()
	repo := a.repo
	apiKeyPresent := a.apiKeyPresent
	apiKeyError := a.apiKeyError
	syncInProgress := a.syncInProgress
	initError := a.initError
	a.mu.Unlock()

	var profile *repository.Profile
	var dashboard *repository.Dashboard
	settings := repository.DefaultLibrarySettings()
	if repo != nil {
		p, err := repo.ActiveProfile()
		if err != nil {
			return AppState{}, err
		}
		profile = p
		if p != nil {
			s, err := repo.LibrarySettings(p.ID)
			if err != nil {
				return AppState{}, err
			}
			settings = s
			d, err := repo.Dashboard(p.ID)
			if err != nil {
				return AppState{}, err
			}
			dashboard = &d
			a.normalizeStaleRun(repo, p.ID, dashboard)
			// A live sync writes its run's final status before its goroutine clears the flag, so the run can already
			// be final here. The sync state is re-read under the lock by syncStillRunning, after the reads above.
			syncInProgress = a.syncStillRunning(syncInProgress, p.ID, d.LatestSyncRun)
		}
	}
	state := a.baseState(profile, dashboard)
	state.APIKeyPresent = apiKeyPresent
	state.APIKeyError = apiKeyError
	state.SyncInProgress = syncInProgress
	state.InitError = initError
	state.Settings = settings
	return state, nil
}

// SetLibrarySettings saves the library sync options of the active profile. They apply on the next sync of that
// profile; this does not start one.
func (a *App) SetLibrarySettings(includeUnvettedApps, includePlayedFreeGames bool) (AppState, error) {
	return callBound("SetLibrarySettings", func() (AppState, error) { return a.setLibrarySettings(includeUnvettedApps, includePlayedFreeGames) })
}

func (a *App) setLibrarySettings(includeUnvettedApps, includePlayedFreeGames bool) (AppState, error) {
	repo, profile, err := a.activeRepoAndProfile()
	if err != nil {
		return AppState{}, err
	}
	if err := repo.SaveLibrarySettings(profile.ID, repository.LibrarySettings{IncludeUnvettedApps: includeUnvettedApps, IncludePlayedFreeGames: includePlayedFreeGames}); err != nil {
		return AppState{}, err
	}
	return a.appState()
}

func (a *App) RetryAPIKey() (AppState, error) {
	return callBound("RetryAPIKey", func() (AppState, error) { return a.retryAPIKey() })
}

func (a *App) retryAPIKey() (AppState, error) {
	// A failed lookup is reported in AppState.APIKeyError, so it is not returned here. Log it, unless it is a
	// missing key, which is a normal state.
	if err := a.refreshAPIKey(); err != nil && !errors.Is(err, keyring.ErrMissing) {
		log.Printf("RetryAPIKey: %v", redact.Paths(err))
	}
	return a.appState()
}

// SaveAPIKey stores the Steam Web API key in the system keyring, replacing any key already stored, and updates the
// cached key state. It does not start a sync. It returns only an error: the key is not returned, and no error text
// contains it, because Wails logs the error of a bound method. The cache changes only after the store is verified,
// so a failed save leaves the previous key state as it was. Accepted edge: if the store succeeds but the read-back
// errors, the keyring may hold the new key while the cache keeps the old one until the next refresh (requireAPIKey
// or RetryAPIKey re-reads). Do not "fix" this by re-reading inside SaveAPIKey.
func (a *App) SaveAPIKey(key string) error {
	return callBoundErr("SaveAPIKey", func() error { return a.saveAPIKey(key) })
}

func (a *App) saveAPIKey(key string) error {
	if err := a.ensureInitialized(); err != nil {
		return err
	}
	a.keyMu.Lock()
	defer a.keyMu.Unlock()
	stored, err := storeSteamAPIKey(key)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steamAPIKey = stored
	a.apiKeyPresent = true
	a.apiKeyError = ""
	return nil
}

func (a *App) ValidateProfile(input string) (ProfilePreview, error) {
	return callBound("ValidateProfile", func() (ProfilePreview, error) { return a.validateProfile(input) })
}

func (a *App) validateProfile(input string) (ProfilePreview, error) {
	if err := a.ensureInitialized(); err != nil {
		return ProfilePreview{}, err
	}
	key, err := a.requireAPIKey()
	if err != nil {
		return ProfilePreview{}, err
	}
	a.mu.Lock()
	baseURL := a.steamBaseURL
	a.mu.Unlock()
	client := steam.New(key)
	applySteamBaseURL(client, baseURL)
	// The step names are kept in the raw error, so the log shows which lookup failed.
	steamID64, err := client.ResolveProfileInput(a.context(), input)
	if err != nil {
		return ProfilePreview{}, fmt.Errorf("resolve profile: %w", err)
	}
	summary, err := client.GetPlayerSummary(a.context(), steamID64)
	if err != nil {
		return ProfilePreview{}, fmt.Errorf("profile summary: %w", err)
	}
	return ProfilePreview{SteamID64: summary.SteamID64, DisplayName: summary.DisplayName, AvatarURL: summary.AvatarURL}, nil
}

func (a *App) SaveProfile(preview ProfilePreview) (AppState, error) {
	return callBound("SaveProfile", func() (AppState, error) { return a.saveProfile(preview) })
}

func (a *App) saveProfile(preview ProfilePreview) (AppState, error) {
	if err := a.ensureInitialized(); err != nil {
		return AppState{}, err
	}
	if preview.SteamID64 == "" {
		return AppState{}, errSteamIDMissing
	}
	if preview.DisplayName == "" {
		preview.DisplayName = preview.SteamID64
	}
	a.mu.Lock()
	repo := a.repo
	a.mu.Unlock()
	if repo == nil {
		return AppState{}, errNotReady
	}
	if _, err := repo.SaveProfile(preview.SteamID64, preview.DisplayName, preview.AvatarURL); err != nil {
		return AppState{}, err
	}
	logStartSyncError(a.startSync(true))
	return a.appState()
}

func (a *App) SyncNow() (AppState, error) {
	return callBound("SyncNow", func() (AppState, error) { return a.syncNow() })
}

func (a *App) syncNow() (AppState, error) {
	if err := a.ensureInitialized(); err != nil {
		return AppState{}, err
	}
	if _, err := a.requireAPIKey(); err != nil {
		return AppState{}, err
	}
	if err := a.startSync(true); err != nil {
		return AppState{}, err
	}
	return a.appState()
}

// ClearActiveUserData removes the active profile from the app. The two flags are independent:
//
//   - deleteProfileData=false: the profile is deactivated (DeactivateProfile). Its games, achievements, sync history,
//     settings and cached images are kept, and reconnecting the same SteamID64 shows them immediately; SaveProfile
//     still starts its usual sync.
//   - deleteProfileData=true: the profile and its data are deleted (ClearProfileData), and cached images that no
//     remaining profile shows are removed from disk.
//   - deleteAPIKey=true: the Steam Web API key is deleted from the system keyring and the cached key state is reset,
//     so the app shows the missing-key screen. Without it the key is kept.
//
// The key is deleted first, before any profile change. A key deletion error is returned with nothing changed: no
// data touched and the key cache unchanged. If a later step fails after the key was deleted, the key stays deleted
// and the app shows the missing-key screen, from which the key can be entered again. ClearProfileData commits its
// transaction before cached images are removed, so a data failure before that commit leaves the profile and its data
// intact; a failure removing images after the commit leaves the data deleted, and some cached images may remain on
// disk. Lock order: keyMu, then mu.
func (a *App) ClearActiveUserData(deleteProfileData, deleteAPIKey bool) (AppState, error) {
	return callBound("ClearActiveUserData", func() (AppState, error) { return a.clearActiveUserData(deleteProfileData, deleteAPIKey) })
}

func (a *App) clearActiveUserData(deleteProfileData, deleteAPIKey bool) (AppState, error) {
	if err := a.ensureInitialized(); err != nil {
		return AppState{}, err
	}
	a.cancelSyncAndWait()
	repo, profile, err := a.activeRepoAndProfile()
	if err != nil {
		return AppState{}, err
	}
	if deleteAPIKey {
		if err := a.clearSteamAPIKey(); err != nil {
			return AppState{}, err
		}
	}
	if !deleteProfileData {
		if err := repo.DeactivateProfile(); err != nil {
			return AppState{}, err
		}
		return a.appState()
	}
	a.mu.Lock()
	paths := a.paths
	a.mu.Unlock()
	appIDs, err := repo.ClearProfileData(profile.ID)
	if err != nil {
		return AppState{}, err
	}
	if err := removeCachedGameImages(paths.GameImages, appIDs); err != nil {
		return AppState{}, err
	}
	return a.appState()
}

// clearSteamAPIKey deletes the key from the system keyring and resets the cached key state. The cache changes only
// after the keyring delete succeeds, so a failed delete leaves the key state as it was.
func (a *App) clearSteamAPIKey() error {
	a.keyMu.Lock()
	defer a.keyMu.Unlock()
	if err := deleteSteamAPIKey(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steamAPIKey = ""
	a.apiKeyPresent = false
	a.apiKeyError = ""
	return nil
}

// MarkGamePreviouslyCompleted toggles the manual previously-completed flag on one achievement set (appID, groupID) of the active profile.
func (a *App) MarkGamePreviouslyCompleted(appID, groupID int64) (AppState, error) {
	return callBound("MarkGamePreviouslyCompleted", func() (AppState, error) { return a.markGamePreviouslyCompleted(appID, groupID) })
}

func (a *App) markGamePreviouslyCompleted(appID, groupID int64) (AppState, error) {
	repo, profile, err := a.activeRepoAndProfile()
	if err != nil {
		return AppState{}, err
	}
	if err := repo.MarkGamePreviouslyCompleted(profile.ID, appID, groupID); err != nil {
		return AppState{}, err
	}
	return a.appState()
}

// ToggleGameMissingAchievementsInDLC toggles the missing-DLC flag on one achievement set.
func (a *App) ToggleGameMissingAchievementsInDLC(appID, groupID int64) (AppState, error) {
	return callBound("ToggleGameMissingAchievementsInDLC", func() (AppState, error) { return a.toggleGameMissingAchievementsInDLC(appID, groupID) })
}

func (a *App) toggleGameMissingAchievementsInDLC(appID, groupID int64) (AppState, error) {
	repo, profile, err := a.activeRepoAndProfile()
	if err != nil {
		return AppState{}, err
	}
	if err := repo.ToggleMissingAchievementsInDLC(profile.ID, appID, groupID); err != nil {
		return AppState{}, err
	}
	return a.appState()
}

// DisableGame disables one achievement set (appID, groupID); sibling sets are unaffected.
func (a *App) DisableGame(appID, groupID int64) (AppState, error) {
	return callBound("DisableGame", func() (AppState, error) { return a.disableGame(appID, groupID) })
}

func (a *App) disableGame(appID, groupID int64) (AppState, error) {
	repo, profile, err := a.activeRepoAndProfile()
	if err != nil {
		return AppState{}, err
	}
	if err := repo.DisableProfileGame(profile.ID, appID, groupID); err != nil {
		return AppState{}, err
	}
	return a.appState()
}

// EnableGame re-enables one achievement set (appID, groupID).
func (a *App) EnableGame(appID, groupID int64) (AppState, error) {
	return callBound("EnableGame", func() (AppState, error) { return a.enableGame(appID, groupID) })
}

func (a *App) enableGame(appID, groupID int64) (AppState, error) {
	repo, profile, err := a.activeRepoAndProfile()
	if err != nil {
		return AppState{}, err
	}
	if err := repo.EnableProfileGame(profile.ID, appID, groupID); err != nil {
		return AppState{}, err
	}
	return a.appState()
}

// ensureInitialized opens the data folder, the log and the database once. When that first succeeds it loads the update
// checks toggle, so the update service caches it before anything reads the snapshot. The load runs after initMu is
// released: it reads the database and may notify.
func (a *App) ensureInitialized() error {
	first, err := a.initializeOnce()
	if first {
		a.loadUpdateChecks()
	}
	return err
}

// initializeOnce does the work of ensureInitialized under initMu. It reports whether this call is the one that
// succeeded in opening the app; every later call returns false.
func (a *App) initializeOnce() (bool, error) {
	a.initMu.Lock()
	defer a.initMu.Unlock()

	a.mu.Lock()
	if a.initialized {
		a.mu.Unlock()
		return false, nil
	}
	a.mu.Unlock()

	paths := appdata.Resolve()
	a.mu.Lock()
	a.paths = paths
	a.mu.Unlock()
	if err := appdata.Ensure(paths); err != nil {
		logging.Fallback()
		return false, a.setInitError(msgInitDataFolder, fmt.Errorf("create app directories: %w", err))
	}
	logging.Init(paths.LogFile)
	a.mu.Lock()
	a.logFile = paths.LogFile
	a.mu.Unlock()
	db, err := dbinit.OpenAndMigrate(paths)
	if err != nil {
		return false, a.setInitError(msgInitDatabase, err)
	}
	repo := repository.New(db)
	if err := repo.CancelRunningSyncRuns("app restarted before sync completed"); err != nil {
		log.Printf("cancel stale sync runs: %v", redact.Paths(err))
	}
	key, keyErr := readSteamAPIKey()
	if keyErr != nil && !errors.Is(keyErr, keyring.ErrMissing) {
		log.Printf("keyring lookup: %v", redact.Paths(keyErr))
	}
	a.mu.Lock()
	a.initialized = true
	a.paths = paths
	a.repo = repo
	a.steamAPIKey = key
	a.apiKeyPresent = key != ""
	if keyErr != nil && !errors.Is(keyErr, keyring.ErrMissing) {
		a.apiKeyError = msgKeyring
	} else {
		a.apiKeyError = ""
	}
	a.initError = ""
	a.mu.Unlock()
	return true, nil
}

func (a *App) activeRepoAndProfile() (*repository.Repository, *repository.Profile, error) {
	if err := a.ensureInitialized(); err != nil {
		return nil, nil, err
	}
	a.mu.Lock()
	repo := a.repo
	a.mu.Unlock()
	if repo == nil {
		return nil, nil, errNotReady
	}
	profile, err := repo.ActiveProfile()
	if err != nil {
		return nil, nil, err
	}
	if profile == nil {
		return nil, nil, errNoProfile
	}
	return repo, profile, nil
}

func (a *App) refreshAPIKey() error {
	a.keyMu.Lock()
	defer a.keyMu.Unlock()
	key, err := readSteamAPIKey()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.steamAPIKey = ""
		a.apiKeyPresent = false
		if errors.Is(err, keyring.ErrMissing) {
			a.apiKeyError = ""
		} else {
			a.apiKeyError = msgKeyring
		}
		return err
	}
	a.steamAPIKey = key
	a.apiKeyPresent = true
	a.apiKeyError = ""
	return nil
}

func (a *App) requireAPIKey() (string, error) {
	if err := a.refreshAPIKey(); err != nil {
		return "", err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.steamAPIKey == "" {
		return "", keyring.ErrMissing
	}
	return a.steamAPIKey, nil
}

func (a *App) startSync(force bool) error {
	syncCtx, cancel := context.WithCancel(a.context())
	a.mu.Lock()
	// ServiceShutdown cancels the lifetime context before it reads syncCancel under a.mu, so a start that passes this
	// check registers its cancel func and WaitGroup entry before shutdown looks at them.
	if a.context().Err() != nil {
		a.mu.Unlock()
		cancel()
		return errShuttingDown
	}
	if a.syncInProgress && !force {
		a.mu.Unlock()
		cancel()
		return nil
	}
	if a.syncInProgress {
		a.mu.Unlock()
		cancel()
		return errSyncAlreadyInProgress
	}
	repo := a.repo
	key := a.steamAPIKey
	paths := a.paths
	baseURL := a.steamBaseURL
	a.syncInProgress = true
	a.syncCancel = cancel
	a.syncWG.Add(1)
	a.mu.Unlock()
	// done ends the sync on every path, including the early exits below. endSync emits the final event. Until the
	// goroutine takes ownership of done (started), the deferred guard calls it on any exit, a panic included, so the
	// flag is never left set and syncWG is always released. The guard is set up before the emit below, so a panic in
	// the emitter also ends the sync. done runs exactly once: the guard is the only caller before started, and the
	// goroutine's defer is the only caller after.
	done := func() { a.endSync(cancel) }
	started := false
	defer func() {
		if !started {
			done()
		}
	}()
	a.emitSyncChanged()
	if repo == nil {
		return errNotReady
	}
	if key == "" {
		return keyring.ErrMissing
	}
	profile, err := repo.ActiveProfile()
	if err != nil {
		return err
	}
	if profile == nil {
		return errNoProfile
	}
	client, err := steamClientForProfile(repo, key, profile.ID)
	if err != nil {
		return err
	}
	applySteamBaseURL(client, baseURL)
	// The baseline is read before the goroutine starts, so no run of this sync exists yet: it is the run that was
	// latest at start. If it cannot be read, the sync does not start and the error is returned. StartSyncRun would
	// very likely fail too, and a sync started without a baseline could not tell its own run from an older one.
	latest, err := repo.LatestSyncRun(profile.ID)
	if err != nil {
		return err
	}
	var baselineRunID int64
	if latest != nil {
		baselineRunID = latest.ID
	}
	a.mu.Lock()
	a.syncProfileID = profile.ID
	a.syncBaselineRunID = baselineRunID
	a.syncBaselineKnown = true
	a.mu.Unlock()
	started = true
	go func(p repository.Profile) {
		defer done()
		service := syncer.Service{Repo: repo, Steam: client, Images: images.New(paths), OnChange: a.emitSyncChanged}
		if _, err := service.FullSync(syncCtx, p); err != nil {
			log.Printf("full sync failed: %s", logText(err))
		}
	}(*profile)
	return nil
}

// endSync ends the current sync: it cancels the sync context, clears the flag, cancel func and baseline, emits the
// final sync:changed event, and releases syncWG. startSync's done and the sync goroutine both end here. The event
// comes after the flag clear, so the frontend's refetch reads syncInProgress=false. It is emitted before syncWG is
// released, so ServiceShutdown's wait covers it. The emit runs outside a.mu.
func (a *App) endSync(cancel context.CancelFunc) {
	// Deferred so the release runs even if the emit panics. The state is cleared before the emit, so a panic there
	// cannot leave the flag set either.
	defer a.syncWG.Done()
	cancel()
	a.mu.Lock()
	a.syncCancel = nil
	a.syncInProgress = false
	a.syncProfileID = 0
	a.syncBaselineRunID = 0
	a.syncBaselineKnown = false
	a.mu.Unlock()
	a.emitSyncChanged()
}

// syncStillRunning decides whether GetAppState reports a sync as running. snapshot is the flag as read before the
// database reads, profileID the active profile and latest its latest sync run. The sync is over when the flag has
// been cleared since the snapshot, or when its own run (a run of its profile above the baseline) has a final status.
// The flag and cancel func are left for the sync goroutine to clear. Only the flag is re-read, under the lock. The
// baseline is only set while the flag and cancel func are set (startSync sets it after them, endSync clears all three
// together), so it needs no separate liveness check.
func (a *App) syncStillRunning(snapshot bool, profileID int64, latest *repository.SyncRun) bool {
	if !snapshot || latest == nil || latest.Status == "running" {
		return snapshot
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ended := !a.syncInProgress
	own := a.syncBaselineKnown && a.syncProfileID == profileID && latest.ID > a.syncBaselineRunID
	if ended || own {
		return false
	}
	return true
}

// staleRunErrorText is the error shown for a run whose sync ended but whose final status could not be saved.
const staleRunErrorText = "sync ended but its final status could not be saved"

// normalizeStaleRun corrects the latest run of profileID in d when it reads 'running' but no sync is live. The sync
// goroutine tries to save the run's final status before it ends, so a run still 'running' here means that write
// failed. The row is not changed. d.LatestSyncRun takes the fresh row when it is no longer running; otherwise it takes
// a failed copy with staleRunErrorText and no finish time. The fresh read runs under a.mu, so no sync can start in
// between. The repository never takes a.mu, so holding it here cannot deadlock.
func (a *App) normalizeStaleRun(repo *repository.Repository, profileID int64, d *repository.Dashboard) {
	if d.LatestSyncRun == nil || d.LatestSyncRun.Status != "running" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.syncCancel != nil {
		return
	}
	fresh, err := repo.LatestSyncRun(profileID)
	if err != nil {
		log.Printf("read latest sync run: %v", redact.Paths(err))
		return
	}
	if fresh == nil || fresh.Status != "running" {
		d.LatestSyncRun = fresh
		return
	}
	failed := *fresh
	failed.Status = "failed"
	msg := staleRunErrorText
	failed.Error = &msg
	d.LatestSyncRun = &failed
}

// logStartSyncError logs a startSync error for a caller that does not return it. A missing API key, a sync that is
// already in progress, and a start during shutdown are expected outcomes and are not logged (see shouldLog).
func logStartSyncError(err error) {
	if err == nil || !shouldLog(err) {
		return
	}
	log.Printf("start sync: %s", logText(err))
}

func (a *App) cancelSyncAndWait() {
	a.mu.Lock()
	cancel := a.syncCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
		a.syncWG.Wait()
	}
}

// setEventEmitter sets the function that delivers Wails events. main calls it once, after application.New. It is
// unexported: exported App methods are bound to the frontend. Without an emitter, events are dropped.
func (a *App) setEventEmitter(emit func(name string)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.emit = emit
}

// emitSyncChanged tells the frontend that sync state changed. It is nil-safe and never holds a.mu while it emits.
func (a *App) emitSyncChanged() {
	a.mu.Lock()
	emit := a.emit
	a.mu.Unlock()
	if emit != nil {
		emit(syncChangedEvent)
	}
}

// setInitError records the user-facing init failure msg in AppState and returns it as an error. The cause is kept
// for the log: the caller logs logText of the returned error, and the frontend gets msg only.
func (a *App) setInitError(msg string, cause error) error {
	a.mu.Lock()
	a.initError = msg
	a.mu.Unlock()
	return usererr.Wrap(msg, redact.Paths(cause))
}

func (a *App) baseState(profile *repository.Profile, dashboard *repository.Dashboard) AppState {
	a.mu.Lock()
	paths := a.paths
	logFile := a.logFile
	initError := a.initError
	apiKeyPresent := a.apiKeyPresent
	apiKeyError := a.apiKeyError
	syncInProgress := a.syncInProgress
	a.mu.Unlock()
	return AppState{
		AppName: config.AppName, DataDir: paths.DataDir, CacheDir: paths.CacheDir, LogDir: paths.Logs, LogFile: logFile,
		APIKeyPresent: apiKeyPresent, APIKeyError: apiKeyError,
		ProfileExists: profile != nil, Profile: profile,
		Dashboard: dashboard, Settings: repository.DefaultLibrarySettings(), SyncInProgress: syncInProgress, InitError: initError,
	}
}

// steamClientForProfile builds the Steam client for a sync of profileID, using that profile's library settings.
func steamClientForProfile(repo *repository.Repository, key string, profileID int64) (*steam.Client, error) {
	s, err := repo.LibrarySettings(profileID)
	if err != nil {
		return nil, err
	}
	return newSteamClient(key, s), nil
}

// newSteamClient builds the Steam client for a sync. Library settings map to the client's inverted flags.
func newSteamClient(key string, s repository.LibrarySettings) *steam.Client {
	client := steam.New(key)
	client.SkipUnvettedApps = !s.IncludeUnvettedApps
	client.ExcludePlayedFreeGames = !s.IncludePlayedFreeGames
	return client
}

// applySteamBaseURL points client at baseURL when it is non-empty. Sync and profile validation both call it with
// App.steamBaseURL, which only tests set; production leaves it empty and the client keeps its default base URL.
func applySteamBaseURL(client *steam.Client, baseURL string) {
	if baseURL != "" {
		client.BaseURL = baseURL
	}
}

// context returns the service lifetime context, the parent of sync and Steam request contexts. It is nil-safe: an App
// built as a literal (tests) has no lifetime context and gets context.Background().
func (a *App) context() context.Context {
	if a.lifeCtx != nil {
		return a.lifeCtx
	}
	return context.Background()
}

// removeCachedGameImages deletes the image directories of appIDs. Callers pass only orphaned image app ids
// (see repository.ClearProfileData), so covers still used by other profiles are kept.
func removeCachedGameImages(root string, appIDs []int64) error {
	for _, appID := range appIDs {
		if appID <= 0 {
			continue
		}
		path := filepath.Join(root, strconv.FormatInt(appID, 10))
		if err := fsretry.RemoveAll(path); err != nil {
			return fmt.Errorf("remove cached images for appid %d: %w", appID, redact.Paths(err))
		}
	}
	return nil
}
