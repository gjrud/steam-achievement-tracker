package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	"github.com/gjrud/steam-achievement-tracker/internal/config"
	dbinit "github.com/gjrud/steam-achievement-tracker/internal/db"
	"github.com/gjrud/steam-achievement-tracker/internal/images"
	"github.com/gjrud/steam-achievement-tracker/internal/keyring"
	"github.com/gjrud/steam-achievement-tracker/internal/logging"
	"github.com/gjrud/steam-achievement-tracker/internal/repository"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
	"github.com/gjrud/steam-achievement-tracker/internal/syncer"
)

type App struct {
	ctx    context.Context
	mu     sync.Mutex
	initMu sync.Mutex

	initialized    bool
	initError      string
	paths          appdata.Paths
	repo           *repository.Repository
	steamAPIKey    string
	apiKeyPresent  bool
	apiKeyError    string
	syncInProgress bool
	syncCancel     context.CancelFunc
	syncWG         sync.WaitGroup
}

type AppState struct {
	AppName            string                `json:"appName"`
	DataDir            string                `json:"dataDir"`
	LogFile            string                `json:"logFile"`
	APIKeyPresent      bool                  `json:"apiKeyPresent"`
	APIKeyError        string                `json:"apiKeyError"`
	SecretSetupCommand string                `json:"secretSetupCommand"`
	ProfileExists      bool                  `json:"profileExists"`
	Profile            *repository.Profile   `json:"profile"`
	Dashboard          *repository.Dashboard `json:"dashboard"`
	SyncInProgress     bool                  `json:"syncInProgress"`
	InitError          string                `json:"initError"`
}

type ProfilePreview struct {
	SteamID64   string `json:"steamId64"`
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrl"`
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if err := a.ensureInitialized(); err != nil {
		log.Printf("startup init: %v", err)
		return
	}
	state, err := a.GetAppState()
	if err == nil && state.APIKeyPresent && state.ProfileExists {
		a.startSync(false)
	}
}

func (a *App) shutdown(ctx context.Context) {
	a.mu.Lock()
	cancel := a.syncCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	a.syncWG.Wait()
	a.mu.Lock()
	repo := a.repo
	a.repo = nil
	a.mu.Unlock()
	if repo != nil {
		_ = repo.Close()
	}
}

func (a *App) GetAppState() (AppState, error) {
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
	activeSync := a.syncCancel != nil
	initError := a.initError
	a.mu.Unlock()

	var profile *repository.Profile
	var dashboard *repository.Dashboard
	if repo != nil {
		p, err := repo.ActiveProfile()
		if err != nil {
			return AppState{}, err
		}
		profile = p
		if p != nil {
			d, err := repo.Dashboard(p.ID)
			if err != nil {
				return AppState{}, err
			}
			dashboard = &d
			if d.LatestSyncRun != nil && d.LatestSyncRun.Status != "running" && syncInProgress && !activeSync {
				a.mu.Lock()
				a.syncInProgress = false
				syncInProgress = false
				a.mu.Unlock()
			}
		}
	}
	state := a.baseState(profile, dashboard)
	state.APIKeyPresent = apiKeyPresent
	state.APIKeyError = apiKeyError
	state.SyncInProgress = syncInProgress
	state.InitError = initError
	return state, nil
}

func (a *App) RetryAPIKey() (AppState, error) {
	_ = a.refreshAPIKey()
	return a.GetAppState()
}

func (a *App) ValidateProfile(input string) (ProfilePreview, error) {
	if err := a.ensureInitialized(); err != nil {
		return ProfilePreview{}, err
	}
	key, err := a.requireAPIKey()
	if err != nil {
		return ProfilePreview{}, err
	}
	client := steam.New(key)
	steamID64, err := client.ResolveProfileInput(a.context(), input)
	if err != nil {
		return ProfilePreview{}, err
	}
	summary, err := client.GetPlayerSummary(a.context(), steamID64)
	if err != nil {
		return ProfilePreview{}, err
	}
	return ProfilePreview{SteamID64: summary.SteamID64, DisplayName: summary.DisplayName, AvatarURL: summary.AvatarURL}, nil
}

func (a *App) SaveProfile(preview ProfilePreview) (AppState, error) {
	if err := a.ensureInitialized(); err != nil {
		return AppState{}, err
	}
	if preview.SteamID64 == "" {
		return AppState{}, errors.New("SteamID64 is required")
	}
	if preview.DisplayName == "" {
		preview.DisplayName = preview.SteamID64
	}
	a.mu.Lock()
	repo := a.repo
	a.mu.Unlock()
	if repo == nil {
		return AppState{}, errors.New("database is not initialized")
	}
	if _, err := repo.SaveProfile(preview.SteamID64, preview.DisplayName, preview.AvatarURL); err != nil {
		return AppState{}, err
	}
	a.startSync(true)
	return a.GetAppState()
}

func (a *App) SyncNow() (AppState, error) {
	if err := a.ensureInitialized(); err != nil {
		return AppState{}, err
	}
	if _, err := a.requireAPIKey(); err != nil {
		return AppState{}, err
	}
	if err := a.startSync(true); err != nil {
		return AppState{}, err
	}
	return a.GetAppState()
}

func (a *App) ClearActiveUserData() (AppState, error) {
	if err := a.ensureInitialized(); err != nil {
		return AppState{}, err
	}
	a.cancelSyncAndWait()
	repo, profile, err := a.activeRepoAndProfile()
	if err != nil {
		return AppState{}, err
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
	return a.GetAppState()
}

func (a *App) MarkGamePreviouslyCompleted(appID int64) (AppState, error) {
	repo, profile, err := a.activeRepoAndProfile()
	if err != nil {
		return AppState{}, err
	}
	if err := repo.MarkGamePreviouslyCompleted(profile.ID, appID); err != nil {
		return AppState{}, err
	}
	return a.GetAppState()
}

func (a *App) ToggleGameMissingAchievementsInDLC(appID int64) (AppState, error) {
	repo, profile, err := a.activeRepoAndProfile()
	if err != nil {
		return AppState{}, err
	}
	if err := repo.ToggleMissingAchievementsInDLC(profile.ID, appID); err != nil {
		return AppState{}, err
	}
	return a.GetAppState()
}

func (a *App) DisableGame(appID int64) (AppState, error) {
	repo, profile, err := a.activeRepoAndProfile()
	if err != nil {
		return AppState{}, err
	}
	if err := repo.DisableProfileGame(profile.ID, appID); err != nil {
		return AppState{}, err
	}
	return a.GetAppState()
}

func (a *App) EnableGame(appID int64) (AppState, error) {
	repo, profile, err := a.activeRepoAndProfile()
	if err != nil {
		return AppState{}, err
	}
	if err := repo.EnableProfileGame(profile.ID, appID); err != nil {
		return AppState{}, err
	}
	return a.GetAppState()
}

func (a *App) ensureInitialized() error {
	a.initMu.Lock()
	defer a.initMu.Unlock()

	a.mu.Lock()
	if a.initialized {
		defer a.mu.Unlock()
		if a.initError != "" {
			return errors.New(a.initError)
		}
		return nil
	}
	a.mu.Unlock()

	paths, err := appdata.Resolve()
	if err == nil {
		err = appdata.Ensure(paths)
	}
	if err != nil {
		logging.Fallback()
		a.setInitError(err)
		return err
	}
	logging.Init(paths.LogFile)
	db, err := dbinit.OpenAndMigrate(paths)
	if err != nil {
		a.setInitError(err)
		return err
	}
	repo := repository.New(db)
	if err := repo.CancelRunningSyncRuns("app restarted before sync completed"); err != nil {
		log.Printf("cancel stale sync runs: %v", err)
	}
	key, keyErr := keyring.GetSteamAPIKey()
	if keyErr != nil && !errors.Is(keyErr, keyring.ErrMissing) {
		log.Printf("keyring lookup: %v", keyErr)
	}
	a.mu.Lock()
	a.initialized = true
	a.paths = paths
	a.repo = repo
	a.steamAPIKey = key
	a.apiKeyPresent = key != ""
	if keyErr != nil && !errors.Is(keyErr, keyring.ErrMissing) {
		a.apiKeyError = keyErr.Error()
	} else {
		a.apiKeyError = ""
	}
	a.initError = ""
	a.mu.Unlock()
	return nil
}

func (a *App) activeRepoAndProfile() (*repository.Repository, *repository.Profile, error) {
	if err := a.ensureInitialized(); err != nil {
		return nil, nil, err
	}
	a.mu.Lock()
	repo := a.repo
	a.mu.Unlock()
	if repo == nil {
		return nil, nil, errors.New("database is not initialized")
	}
	profile, err := repo.ActiveProfile()
	if err != nil {
		return nil, nil, err
	}
	if profile == nil {
		return nil, nil, errors.New("no active profile")
	}
	return repo, profile, nil
}

func (a *App) refreshAPIKey() error {
	key, err := keyring.GetSteamAPIKey()
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.steamAPIKey = ""
		a.apiKeyPresent = false
		if errors.Is(err, keyring.ErrMissing) {
			a.apiKeyError = ""
		} else {
			a.apiKeyError = err.Error()
		}
		if errors.Is(err, keyring.ErrMissing) {
			return err
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
	if a.syncInProgress && !force {
		a.mu.Unlock()
		cancel()
		return nil
	}
	if a.syncInProgress {
		a.mu.Unlock()
		cancel()
		return errors.New("sync already in progress")
	}
	repo := a.repo
	key := a.steamAPIKey
	paths := a.paths
	a.syncInProgress = true
	a.syncCancel = cancel
	a.syncWG.Add(1)
	a.mu.Unlock()
	done := func() {
		cancel()
		a.mu.Lock()
		a.syncCancel = nil
		a.syncInProgress = false
		a.mu.Unlock()
		a.syncWG.Done()
	}
	if repo == nil {
		done()
		return errors.New("database is not initialized")
	}
	if key == "" {
		done()
		return keyring.ErrMissing
	}
	profile, err := repo.ActiveProfile()
	if err != nil {
		done()
		return err
	}
	if profile == nil {
		done()
		return errors.New("no active profile")
	}
	go func(p repository.Profile) {
		defer done()
		service := syncer.Service{Repo: repo, Steam: steam.New(key), Images: images.New(paths)}
		if _, err := service.FullSync(syncCtx, p); err != nil {
			log.Printf("full sync failed: %v", err)
		}
	}(*profile)
	return nil
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

func (a *App) setInitError(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.initError = err.Error()
	}
}

func (a *App) baseState(profile *repository.Profile, dashboard *repository.Dashboard) AppState {
	a.mu.Lock()
	paths := a.paths
	initError := a.initError
	apiKeyPresent := a.apiKeyPresent
	apiKeyError := a.apiKeyError
	syncInProgress := a.syncInProgress
	a.mu.Unlock()
	return AppState{
		AppName: config.AppName, DataDir: paths.Root, LogFile: paths.LogFile, APIKeyPresent: apiKeyPresent, APIKeyError: apiKeyError,
		SecretSetupCommand: config.SecretSetupCommand, ProfileExists: profile != nil, Profile: profile,
		Dashboard: dashboard, SyncInProgress: syncInProgress, InitError: initError,
	}
}

func (a *App) context() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

func removeCachedGameImages(root string, appIDs []int64) error {
	for _, appID := range appIDs {
		if appID <= 0 {
			continue
		}
		path := filepath.Join(root, strconv.FormatInt(appID, 10))
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove cached images for appid %d: %w", appID, err)
		}
	}
	return nil
}

func (a *App) assetHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/game-covers/") {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/game-covers/"), "/")
		if len(parts) != 2 || parts[1] != "library_600x900.jpg" {
			http.NotFound(w, r)
			return
		}
		appid, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || appid <= 0 {
			http.NotFound(w, r)
			return
		}
		paths := a.paths
		if paths.GameImages == "" {
			resolved, err := appdata.Resolve()
			if err != nil {
				http.NotFound(w, r)
				return
			}
			paths = resolved
		}
		path := filepath.Join(paths.GameImages, parts[0], "library_600x900.jpg")
		http.ServeFile(w, r, path)
	})
}
