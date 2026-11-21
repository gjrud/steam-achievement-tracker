package main

import (
	"github.com/gjrud/steam-achievement-tracker/internal/repository"
	"github.com/gjrud/steam-achievement-tracker/internal/update"
	"github.com/gjrud/steam-achievement-tracker/internal/usererr"
)

// updateChangedEvent is the Wails event the update service emits on every snapshot change. Like sync:changed it has no
// payload: the frontend calls GetUpdateState when it arrives. main wires it; the frontend never subscribes to the
// framework's wails:updater:* events.
const updateChangedEvent = "update:changed"

// msgUpdateUnavailable is returned by every update binding except GetUpdateState when the updater is not running in
// this build (no valid version stamp, or the framework refused its configuration). It is the same sentence the update
// service gives for the same case, so the frontend sees one text whichever path refuses.
const msgUpdateUnavailable = "Updates are not available in this build."

var errUpdateUnavailable = usererr.New(msgUpdateUnavailable)

// setUpdater hands the update service to the App. main calls it once, after application.New and before Run.
func (a *App) setUpdater(svc *update.Service) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.updates = svc
}

// availableUpdates returns the update service when the updater runs in this build, and the fixed sentence otherwise.
// It takes a.mu only to copy the pointer.
func (a *App) availableUpdates() (*update.Service, error) {
	a.mu.Lock()
	svc := a.updates
	a.mu.Unlock()
	if svc == nil || svc.State().Unavailable {
		return nil, errUpdateUnavailable
	}
	return svc, nil
}

// logUpdateInitFailure logs a refused framework updater configuration, once. ServiceStartup calls it right after
// ensureInitialized, before the app's own error check, so the refusal is logged even when the app's init failed.
func (a *App) logUpdateInitFailure() {
	a.mu.Lock()
	svc := a.updates
	a.mu.Unlock()
	if svc != nil {
		svc.LogInitFailure()
	}
}

// startUpdateCheck starts the update service's startup check. ServiceStartup calls it only after a successful init.
// The service does nothing when the toggle is off or the updater is unavailable.
func (a *App) startUpdateCheck() {
	a.mu.Lock()
	svc := a.updates
	a.mu.Unlock()
	if svc != nil {
		svc.StartupCheck()
	}
}

// stopUpdates tells the update service that the app is closing. It never waits on a running update operation.
func (a *App) stopUpdates() {
	a.mu.Lock()
	svc := a.updates
	a.mu.Unlock()
	if svc != nil {
		svc.Shutdown()
	}
}

// currentRepo returns the open repository, or errNotReady when the database is not open. It takes a.mu only to copy
// the pointer, so callers never hold a.mu while they query.
func (a *App) currentRepo() (*repository.Repository, error) {
	a.mu.Lock()
	repo := a.repo
	a.mu.Unlock()
	if repo == nil {
		return nil, errNotReady
	}
	return repo, nil
}

// loadUpdateChecks tells the update service to load the checks toggle. ensureInitialized calls it once, when the
// database first opens, so it runs whether or not ServiceStartup did. It takes a.mu only to copy the pointer.
func (a *App) loadUpdateChecks() {
	a.mu.Lock()
	svc := a.updates
	a.mu.Unlock()
	if svc != nil {
		svc.LoadChecksEnabled()
	}
}

// updateChecksEnabled reads the update_checks_enabled setting for the update service. The service calls it once per
// load, from LoadChecksEnabled, which ensureInitialized triggers when the database opens. StartupCheck and
// GetUpdateState use the cached value and never the database. An error means unreadable, and the service treats the
// setting as off. It is never called while a.mu is held.
func (a *App) updateChecksEnabled() (bool, error) {
	repo, err := a.currentRepo()
	if err != nil {
		return false, err
	}
	return repo.UpdateChecksEnabled()
}

// saveUpdateChecksEnabled stores the toggle. It opens the database first, as the other settings methods do.
func (a *App) saveUpdateChecksEnabled(enabled bool) error {
	if err := a.ensureInitialized(); err != nil {
		return err
	}
	repo, err := a.currentRepo()
	if err != nil {
		return err
	}
	return repo.SetUpdateChecksEnabled(enabled)
}

// GetUpdateState is bound to the frontend. It never fails: without an updater it returns a snapshot with unavailable
// set, so the UI can hide the update controls.
func (a *App) GetUpdateState() (update.Snapshot, error) {
	return callBound("GetUpdateState", func() (update.Snapshot, error) {
		a.mu.Lock()
		svc := a.updates
		a.mu.Unlock()
		if svc == nil {
			return update.Snapshot{Unavailable: true}, nil
		}
		return svc.State(), nil
	})
}

// CheckForUpdates is the manual "Check now". It is refused while another update operation runs.
func (a *App) CheckForUpdates() error {
	return callBoundErr("CheckForUpdates", func() error {
		svc, err := a.availableUpdates()
		if err != nil {
			return err
		}
		return svc.CheckNow()
	})
}

// InstallUpdate downloads and stages the release the user saw (version). Nothing is downloaded before this call.
func (a *App) InstallUpdate(version string) error {
	return callBoundErr("InstallUpdate", func() error {
		svc, err := a.availableUpdates()
		if err != nil {
			return err
		}
		return svc.Install(version)
	})
}

// CancelUpdate stops a running download. A cancel that arrives too late has no effect; the phase tells the outcome.
func (a *App) CancelUpdate() error {
	return callBoundErr("CancelUpdate", func() error {
		svc, err := a.availableUpdates()
		if err != nil {
			return err
		}
		return svc.Cancel()
	})
}

// RestartToUpdate applies the staged update and relaunches the app. The app quits during the call; a failure returns
// an error and the app keeps running with the update staged.
func (a *App) RestartToUpdate() error {
	return callBoundErr("RestartToUpdate", func() error {
		svc, err := a.availableUpdates()
		if err != nil {
			return err
		}
		return svc.Restart()
	})
}

// SetUpdateChecksEnabled stores the startup check toggle and returns the updated snapshot. The setting is stored first;
// the service's cached toggle changes only after the store succeeds, so the snapshot and the stored value agree.
func (a *App) SetUpdateChecksEnabled(enabled bool) (update.Snapshot, error) {
	return callBound("SetUpdateChecksEnabled", func() (update.Snapshot, error) {
		svc, err := a.availableUpdates()
		if err != nil {
			return update.Snapshot{}, err
		}
		if err := a.saveUpdateChecksEnabled(enabled); err != nil {
			return update.Snapshot{}, err
		}
		if err := svc.SetChecksEnabled(enabled); err != nil {
			return update.Snapshot{}, err
		}
		return svc.State(), nil
	})
}
