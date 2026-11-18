# App service (root package)

This document describes the Go root package `main`: `main.go`, `app.go`, `covers.go`, `wailslog.go`, and the frontend binding contract built on them. For the surrounding layers, see [architecture.md](architecture.md).

Files and Go identifiers cited here are in the repository, except names from the Wails v3.1.0 module (for example `AssetFileServerFS`, `WebviewUserDataPath` and `ServiceOptions`). Line numbers are omitted on purpose.

## Files

| File | Contents |
| --- | --- |
| `main.go` | Embeds `frontend/dist` and the app icon, builds the Wails application, registers the two services, creates the window, runs the app. |
| `app.go` | `App` type, `AppState` and `ProfilePreview` types, lifecycle hooks, all bound methods, init, sync orchestration, state assembly, API key cache. |
| `covers.go` | `CoverService`, the `/game-covers` HTTP handler. |
| `wailslog.go` | `logWriter` and `newWailsLogger`, which send Wails's own logging to `app.log`. |
| `*_test.go` | `app_test.go`, `app_events_test.go`, `service_lifecycle_test.go`, `covers_test.go`, `wailslog_test.go`. |

## main.go

- `//go:embed all:frontend/dist` provides the frontend. `//go:embed build/appicon.png` provides the icon.
- `NewApp()` creates the backend. Its exported methods are bound through `application.NewService(backend)`.
- On Windows, `<cache>/webview` is created with mode `0700` before the application is built, and its path is passed as `WebviewUserDataPath`. If it cannot be created, the path is left empty. Linux does nothing here.
- `application.Options` sets `Name` (`steam-achievement-tracker`), `Logger` (`newWailsLogger(logWriter{})`), the icon, the GTK application id `io.github.gjrud.steam-achievement-tracker` on Linux, and the asset handler (`AssetFileServerFS`).
- The services are:
  - `application.NewService(backend)`: the `App`, no route.
  - `application.NewServiceWithOptions(NewCoverService(backend), ServiceOptions{Name: "covers", Route: "/game-covers"})`.
- `backend.setEventEmitter(func(name string) { app.Event.Emit(name) })` connects the sync event to the event bus.
- The window is `Steam Achievement Tracker`, 1280x980, minimum 900x650, background `RGB(27, 38, 54)`.
- `app.Run()` blocks. A `Run` error is fatal (`log.Fatal`).

## App type

`App` is a Wails v3 service, created by `NewApp()`, which sets its lifetime context and cancel func.

| Field | Purpose |
| --- | --- |
| `lifeCtx`, `lifeCancel` | Lifetime context. Parent of sync and Steam request contexts. Cancelled by `ServiceShutdown`. |
| `keyMu` | Serializes key-state changes (`SaveAPIKey`, `refreshAPIKey`, `clearSteamAPIKey`). |
| `mu` | Guards all other fields. |
| `initMu` | Serializes the init sequence. |
| `emit` | Set by `setEventEmitter` in `main` after `application.New`. In tests it is nil unless the test sets one; events are dropped while it is nil. |
| `initialized`, `initError` | Init state. `initError` holds the last init failure text. |
| `paths` | `appdata.Paths` for this run. |
| `repo` | `*repository.Repository`. Nil before init and after shutdown. |
| `steamAPIKey`, `apiKeyPresent`, `apiKeyError` | Cached key state. The key itself is never exposed. |
| `syncInProgress`, `syncCancel` | Current sync flag and its cancel func. |
| `syncProfileID`, `syncBaselineRunID`, `syncBaselineKnown` | Identify the run started by the current sync. A run of the same profile with a higher id is this sync's own run. |
| `syncWG` | Tracks sync goroutines for shutdown and for `cancelSyncAndWait`. |
| `steamBaseURL` | Overrides the Steam API base URL. Set only by tests. |

## Public types

`AppState` is the result of `GetAppState` and every method marked `state` in the bound methods table. `SaveAPIKey` returns only `error`, and `ValidateProfile` returns `ProfilePreview`. JSON field names are camelCase.

| Field (JSON) | Go type | Meaning |
| --- | --- | --- |
| `appName` | `string` | `config.AppName` (`Steam Achievement Tracker`). |
| `dataDir`, `cacheDir`, `logDir`, `logFile` | `string` | Resolved directories and log file, shown in Settings. Empty before init. |
| `apiKeyPresent` | `bool` | A key is cached. |
| `apiKeyError` | `string` | Last keyring read error other than missing. |
| `profileExists` | `bool` | An active profile exists. |
| `profile` | `*repository.Profile` | Active profile, or null. |
| `dashboard` | `*repository.Dashboard` | Dashboard data for the active profile, or null. |
| `settings` | `repository.LibrarySettings` | `includeUnvettedApps`, `includePlayedFreeGames` for the active profile. Defaults to both `true`. |
| `syncInProgress` | `bool` | The flag as read at the start of `GetAppState`, unless `syncStillRunning` corrects it. The re-check runs only when the snapshot is `true`, there is an active profile, and its latest run exists and is not `running`. It then returns `false` if the flag has been cleared or the latest run is the sync's own (same profile, id above the baseline). In every other case (latest run nil or `running`, or no active profile) the snapshot is returned unchanged. A row that reads `running` with no live sync gives `false`, and `normalizeStaleRun` shows it as `failed`. |
| `initError` | `string` | Init failure text with full paths, copied into `AppState.InitError` and shown as is by the UI. Empty when init succeeded. |

`ProfilePreview` is the result of `ValidateProfile` and the input of `SaveProfile`: `steamId64`, `displayName`, `avatarUrl`.

## Lifecycle hooks

These hooks are not bound. The binding generator excludes `ServiceStartup` and `ServiceShutdown` (see [architecture.md](architecture.md#bound-methods-and-bindings)).

### ServiceStartup(ctx, options) error

- Calls `ensureInitialized`. On failure it logs `startup init: ...` and returns `nil`. The app starts with `AppState.InitError` set.
- On success, reads the state. If `apiKeyPresent` and `profileExists` are both true, it calls `startSync(false)`. A sync that is already running is not restarted.
- The Wails context argument is ignored. The `App` uses its own lifetime context.
- Runs inside `app.Run`, before the window opens.

### ServiceShutdown() error

1. Cancels `lifeCtx`.
2. Reads `syncCancel` under `mu` and cancels it.
3. Waits on `syncWG`.
4. Sets `repo` to nil under `mu` and closes the database. Errors from `Close` are ignored.

Calling it twice is safe. A nil `lifeCancel` (an `App` built as a literal in tests) is skipped.

## Initialization: ensureInitialized

`ensureInitialized` runs at the start of every bound method except `RetryAPIKey`. `RetryAPIKey` re-reads the keyring (`refreshAPIKey` does not call `ensureInitialized`), then reaches that check only through `GetAppState`. If init succeeds, its key read replaces the cached result of any pre-init key read. `ensureInitialized` holds `initMu` throughout the sequence. It returns if `initialized` is already true; otherwise it runs these steps:

1. `appdata.Resolve()`, then `appdata.Ensure(paths)`. On failure: `logging.Fallback()`, record `create app directories: ...` as the init error, return it.
2. `logging.Init(paths.LogFile)`.
3. `dbinit.OpenAndMigrate(paths)`. On failure: record the init error, return it.
4. `repository.New(db)`, then `CancelRunningSyncRuns("app restarted before sync completed")`. Failure is logged only.
5. `readSteamAPIKey()`. `keyring.ErrMissing` is normal. Any other error is logged and stored in `apiKeyError`.
6. Under `mu`: `initialized = true`, `paths`, `repo`, the key cache, `apiKeyError`, and `initError = ""`.

Errors that `ensureInitialized` returns go through `redact.Paths`, so bound methods return base names only; `AppState.InitError` keeps the full text. A failed init leaves `initialized` false, so the next call tries again. Each retry re-runs `logging.Init` and `OpenAndMigrate`.

## Bound methods

The table lists every bound method: exported methods on `*App`, excluding lifecycle hooks. Each appears in the generated bindings and frontend mock. "state" means the method returns `AppState`, assembled by `GetAppState`.

| Method | Parameters | Returns | Behavior | Errors and side effects |
| --- | --- | --- | --- | --- |
| `GetAppState` | none | `AppState`, `error` | Read-only snapshot. Runs `ensureInitialized`. Reads the active profile, its library settings and its dashboard. Corrects a stale running run (see below). | If init fails and there is no repo, returns the base state with `initError` and no error. DB read errors are returned. No database writes. |
| `SetLibrarySettings` | `includeUnvettedApps bool`, `includePlayedFreeGames bool` | state, `error` | Saves the active profile's library settings in `profile_library_settings`, then returns state. Does not start a sync. | `no active profile`, `database is not initialized`, DB errors. |
| `RetryAPIKey` | none | state, `error` | Re-reads the keyring into the cache. A read error is recorded in `apiKeyError` and is not returned. Then returns state. | Only `GetAppState` errors are returned. |
| `SaveAPIKey` | `key string` | `error` | Stores the key through `keyring.SetSteamAPIKey` (trim, validate, write, read back). On success, updates the key cache. Returns no state and does not start a sync. | Init error. `keyring.ErrInvalidKey`, `keyring.ErrNotStored`, or a wrapped backend error. The cache changes only after the store succeeds. |
| `ValidateProfile` | `input string` | `ProfilePreview`, `error` | Requires a key (re-reads the keyring). Resolves `input` to a SteamID64 and fetches the player summary. No database writes. Uses the Steam Web API. | Missing key (`steam web api key missing from system keyring`), `profile input is required`, the URL-shape error, Steam's own messages, `Steam profile not found`, `no Steam profile matches that custom name`, and redacted transport errors. |
| `SaveProfile` | `preview ProfilePreview` | state, `error` | Requires `steamId64`. Uses it as the display name when `displayName` is empty. Upserts the profile by SteamID64, makes it active, starts a forced sync (`startSync(true)`), and returns state. | `SteamID64 is required`, `database is not initialized`, DB errors. Sync start errors are logged, except a missing key, an already running sync, or shutdown, which are silent. |
| `SyncNow` | none | state, `error` | Requires a key. Starts a forced sync and returns state. | Missing key, `sync already in progress`, `no active profile`, `database is not initialized`, `shutting down`. |
| `ClearActiveUserData` | `deleteProfileData bool`, `deleteAPIKey bool` | state, `error` | Cancels and waits for any running sync. Requires an active profile. Then, in order: optionally deletes the key, then either deactivates the profile or deletes its data and cached images. Returns state. | See the Remove profile section. |
| `MarkGamePreviouslyCompleted` | `appID int64`, `groupID int64` | state, `error` | Toggles `manual_was_completed` on one achievement set of the active profile. Updates the completion history fields and recomputes the set's tags. | `no active profile`. `sql.ErrNoRows` (unwrapped) if the set has no `profile_games` row. |
| `ToggleGameMissingAchievementsInDLC` | `appID int64`, `groupID int64` | state, `error` | Toggles `missing_achievements_in_dlc` on one set and recomputes its tags. | `no active profile`, DB errors. No check that the set exists. |
| `DisableGame` | `appID int64`, `groupID int64` | state, `error` | Sets `disabled = 1` with reason `manual` on one set and deletes its tags. Sibling sets are not changed. | `no active profile`, DB errors. No check that the set exists. |
| `EnableGame` | `appID int64`, `groupID int64` | state, `error` | Clears `disabled` and its reason on one set, then recomputes its tags. | `no active profile`, DB errors. No check that the set exists. |

Every method except `GetAppState` and `RetryAPIKey` can return the init error from `ensureInitialized`. Those two return the base state with `initError` set and no error. The `groupID` selects one achievement set of a game. Group `0` is the base set. The frontend passes the set's group id on every game action.

### GetAppState details

1. Runs `ensureInitialized`. If that fails and there is no repo, returns `baseState(nil, nil)`.
2. Reads the flags under `mu`: repo, key state, sync flag, init error.
3. If a repo and an active profile exist: reads `LibrarySettings` and `Dashboard`, then calls `normalizeStaleRun`, and then `syncStillRunning` to decide the final `syncInProgress` value.
4. `baseState` fills the paths, init error, key state and sync flag. The extra fields are then set.

`normalizeStaleRun` handles a latest run that reads `running` with no live sync. The sync goroutine tries to save the final status before it ends, so a run still `running` means that write failed. The stored row is not changed. The method re-reads the run under `mu`. If the run is no longer `running`, that row is used. Otherwise a copy with status `failed`, error `sync ended but its final status could not be saved`, and no finish time is shown.

`syncStillRunning` reports the sync as over when the flag was cleared since the snapshot, or when the latest run of the active profile is above the baseline and has a final status. Under `mu` it re-reads `syncInProgress`, `syncProfileID`, `syncBaselineKnown` and `syncBaselineRunID`. It does not re-read the run from the database: `latest` is the run from the dashboard read in `GetAppState`.

## Profile validation and vanity resolution

`ValidateProfile` gets the key from `requireAPIKey`, which first re-reads the keyring (`refreshAPIKey`), builds a `steam.Client` with it, applies `steamBaseURL` if set, and calls `ResolveProfileInput`. That function in `internal/steam` accepts:

| Input | Result |
| --- | --- |
| 17-digit SteamID64 (`^\d{17}$`) | Used as is. |
| URL with a `profiles/<steamid64>` segment | The SteamID64 from the path. |
| URL with an `id/<vanity>` segment | `ResolveVanityURL` on the vanity part. |
| Any other URL with a host and no matching segment | Error: `Steam profile URL must contain /profiles/{steamid64} or /id/{vanity}`. |
| Anything else (no host) | Sent to `ResolveVanityURL` as a vanity name. |

Input is trimmed first. An empty input is an error. `GetPlayerSummary` then fetches the display name and avatar (`avatarfull`, falling back to `avatarmedium`). No profile row is written by validation.

The code treats a URL without a scheme, such as `steamcommunity.com/id/name`, as a vanity name, so the whole string goes to the vanity lookup and fails. The user guide asks for URLs with a scheme (`https://`). Any URL with a host is parsed (an error if no `/profiles/` or `/id/` segment matches), and anything else goes to the vanity lookup.

`SaveProfile` takes the preview as given. It checks only that `steamId64` is not empty. It does not check the format and does not call Steam. The frontend passes back the object returned by `ValidateProfile`.

## Sync orchestration

`startSync(force bool) error` is the sole entry point for starting a sync. It:

1. Creates a child context of the lifetime context and refuses with `errShuttingDown` if the lifetime context is done.
2. Under `mu`: returns quietly when a sync runs and `force` is false, returns `errSyncAlreadyInProgress` when it runs and `force` is true, otherwise sets `syncInProgress`, the cancel func, and `syncWG`.
3. Emits `sync:changed`.
4. Validates the state. Each early failure calls `endSync` (so the flag is cleared and the event is emitted): no repo, no key (`keyring.ErrMissing`), a failed active-profile read, no active profile, client build failure (a failed library settings read), or failure to read the latest run.
5. Records the profile's latest run id as the baseline.
6. Starts a goroutine: `syncer.Service{Repo, Steam, Images: images.New(paths), OnChange: emitSyncChanged}.FullSync`. Its error is logged (`full sync failed`). The goroutine always calls `endSync`.

`endSync` cancels the context, clears `syncInProgress`, the cancel func, and the baseline under `mu`, emits the final event, and calls `syncWG.Done`. Emission happens after the flag is cleared, so the frontend refetch reads the new state.

`logStartSyncError` logs a `startSync` error for callers that do not return it. A missing key, an already-running sync, and shutdown are expected and are not logged.

The sync client uses the profile's library settings: `SkipUnvettedApps = !includeUnvettedApps`, `ExcludePlayedFreeGames = !includePlayedFreeGames`.

## Library settings

`SetLibrarySettings` saves the settings for the active profile. Each profile has its own row. A profile without a row gets `DefaultLibrarySettings()`, which has both options `true`. The settings apply on the next sync. Changing them does not start one.

## Remove the active profile

`ClearActiveUserData(deleteProfileData, deleteAPIKey)` runs in this order:

1. `ensureInitialized`.
2. `cancelSyncAndWait`: cancels a running sync and waits for it.
3. `activeRepoAndProfile`. Fails with `no active profile` when there is none. This happens before any key deletion, so the key cannot be deleted while no profile is active.
4. If `deleteAPIKey`: `clearSteamAPIKey`. It takes `keyMu`, deletes the keyring item (`keyring.DeleteSteamAPIKey`), and then resets the key cache. If the delete fails, nothing else changes.
5. If not `deleteProfileData`: `repo.DeactivateProfile()`. The profile row and its data stay. Reconnecting the same SteamID64 restores it.
6. Otherwise: `repo.ClearProfileData(profile.ID)` returns the image app ids that no remaining profile shows. Then `removeCachedGameImages` removes each `<GameImages>/<appid>` directory (`fsretry.RemoveAll`). Non-positive ids are skipped. A removal error is returned with `redact.Paths` applied, so the cache root does not reach the UI.
7. Returns `GetAppState`.

Failures after step 4 leave the key deleted. Failures in image removal after the database commit leave some cached files behind. Log files are not removed by this action (see README "Log files").

## API key save and retry

- `SaveAPIKey` is the only way to store a key. It does not check whether a profile exists and does not sync. The cache changes after the store is verified.
- `RetryAPIKey` re-reads the keyring. It is used when the user stored the key outside the app, and the missing-key screen's "Retry key lookup" button calls it (`retryKey` in `App.svelte`). It reaches `ensureInitialized` only through `GetAppState`, so it also retries a failed init.
- The startup-error screen (`SetupView` mode `startup-error`) has no retry control, and the `onRetry` prop it receives is not rendered. A failed init is retried by the next bound call, such as the refetch on mount or after a `sync:changed` event.
- `refreshAPIKey` is the internal re-read. It takes `keyMu`, reads the keyring, then updates the cache under `mu`. A missing key clears the cache and clears `apiKeyError`. Any other error sets `apiKeyError` to the error text.
- `requireAPIKey` calls `refreshAPIKey`, then returns the cached key, or `keyring.ErrMissing` when it is empty. `ValidateProfile` and `SyncNow` use it, so both always check the keyring first.
- `startSync` uses the cached key without a re-read.

Settings cannot change the key. A key can be replaced from the setup screen (`SaveAPIKey`). The user guide describes the screens.

## Flag and disable actions

The flag and disable methods all work on one `(appID, groupID)` set. Their effects on the stored data:

| Method | Table and column | Effect |
| --- | --- | --- |
| `MarkGamePreviouslyCompleted` | `profile_game_flags.manual_was_completed` (toggle) | Then updates `profile_games.was_completed` and `newly_incomplete`, and recomputes tags. |
| `ToggleGameMissingAchievementsInDLC` | `profile_game_flags.missing_achievements_in_dlc` (toggle) | Recomputes tags. |
| `DisableGame` | `profile_game_flags.disabled = 1`, reason `manual` | Deletes the set's tags. Sync skips a game only when every one of its sets is disabled (`DisabledProfileGameIDs`). |
| `EnableGame` | `profile_game_flags.disabled = 0`, reason cleared | Recomputes tags. |

The disable path is separate from the automatic `not_owned` disable. `DisableMissingOwnedGames` (called during sync) disables games the profile no longer owns, with reason `not_owned`, and clears that reason when the game is owned again. A manual disable is kept across that sweep.

## Cover service (covers.go)

`CoverService` is a service with one method, `ServeHTTP`. It is not bound to the frontend. The binding generator excludes `ServeHTTP`.

- Mounted in `main.go` as service `covers` with route `/game-covers`. Wails strips the prefix, and the handler sees `/<appid>/<file>`.
- Only `GET` is served. Any other method gets `404`.
- The path must begin with `/` and have exactly two segments. A request such as `/game-covers123/...` arrives without a leading `/` and gets `404`.
- The second segment must be one of the four names the image cache writes: `library_600x900.jpg`, `hero_capsule.jpg`, `header.jpg`, `main_capsule.jpg`. The check uses the constants in `internal/images`.
- The first segment must parse as a positive `int64`.
- The base directory is `App.paths.GameImages`, read under `mu`. If it is empty (before init), `appdata.Resolve()` is used instead.
- The file is served with `http.ServeFile`. Because both segments are validated, the path cannot contain a separator or `..`, and the handler does no database lookup.

The cover content and the source order are described in [covers.md](covers.md).

## Wails logger (wailslog.go)

- `logWriter` implements `io.Writer`. Each `Write` calls `logging.Writer()`, so the destination is looked up at write time. Before `logging.Init`, and after `logging.Fallback`, writes go to stderr.
- `newWailsLogger` returns a `slog` logger with a text handler (`time=... level=... msg=...`) and level `Warn`.
- Warnings and errors from Wails reach `app.log`. Bound-method errors are logged at Error.
- Rules for this file: never set the level to Debug. Wails logs the arguments of every bound call at Debug, and one of them is the Steam Web API key passed to `SaveAPIKey`. Bound methods must not return text that contains the key.

## Frontend binding contract

The frontend relies on generated bindings and a hand-written mock.

- Generated file: `frontend/bindings/github.com/gjrud/steam-achievement-tracker/app.js`, with `models.js` next to it and the repository models under `internal/repository/models.js`. The directory is git-ignored. Do not edit it. Regenerate with `wails3 generate bindings` from the repository root. `wails3 dev` and `wails3 build` do this too.
- `App.svelte` imports these twelve functions from the generated file:

  | Function | Go result | JavaScript result |
  | --- | --- | --- |
  | `GetAppState` | `AppState, error` | `AppState` |
  | `SetLibrarySettings` | `AppState, error` | `AppState` |
  | `RetryAPIKey` | `AppState, error` | `AppState` |
  | `SaveAPIKey` | `error` | `void` |
  | `ValidateProfile` | `ProfilePreview, error` | `ProfilePreview` |
  | `SaveProfile` | `AppState, error` | `AppState` |
  | `SyncNow` | `AppState, error` | `AppState` |
  | `ClearActiveUserData` | `AppState, error` | `AppState` |
  | `MarkGamePreviouslyCompleted` | `AppState, error` | `AppState` |
  | `ToggleGameMissingAchievementsInDLC` | `AppState, error` | `AppState` |
  | `DisableGame` | `AppState, error` | `AppState` |
  | `EnableGame` | `AppState, error` | `AppState` |

  These are the twelve bound methods. The generated file has exactly these twelve functions. Lifecycle hooks and the unexported `setEventEmitter` are not in it.

- `frontend/src/test/wails-app-mock.js` is used in place of the generated file by the test alias. It exports one function per binding, with explicit parameters, and a `defaultHandlers` object that holds one handler per binding. `__setWailsMocks` and `__resetWailsMocks` swap handlers per test.
- `frontend/src/test/wails-bindings.test.js` checks the mock against the generated file and against the frontend and Go sources:
  - Every mock export exists in the generated file with the same parameter count.
  - No mock export uses rest parameters.
  - Every generated binding has a mock export.
  - Every name `App.svelte` imports from the bindings exists in the generated file.
  - `syncChangedEvent` in `app.go` and `App.svelte` is the same string.

  The comparison checks are skipped when the generated file is absent. A separate test, "are present when CI is set", fails when the file is absent and the `CI` environment variable is `1` or `true` (case-insensitive). The event-name check reads only the sources and always runs.
- The drift test does not check `defaultHandlers`. When a method is added, update the export, its parameters, and the `defaultHandlers` entry by hand.

Steps for a change to an exported method:

1. Change the method in `app.go`, including its doc comment, which the generator copies into `app.js`.
2. Run `wails3 generate bindings`.
3. Update `frontend/src/test/wails-app-mock.js`: the export (explicit parameters) and the `defaultHandlers` entry.
4. Update `App.svelte` if the frontend calls the method.
5. Run `cd frontend && npm test`.

## Behavior notes

These are behaviors that the code shows and that the user guide does not describe in detail:

- A failed init is retried on every call: `ensureInitialized` returns the redacted error and leaves `initialized` false, so the next call runs the init steps again.
- `SaveProfile` does not cancel a running sync. If one is running, the new profile's sync is not started (the error is dropped), and the running sync keeps going for the old profile. The frontend only shows the setup screen when no profile is active, so this needs a second profile to be saved while a sync runs.
- `ClearActiveUserData` needs an active profile, so the API key cannot be deleted while no profile is active. The key can still be replaced there with `SaveAPIKey`.
- `MarkGamePreviouslyCompleted` returns `sql.ErrNoRows` unwrapped when the set has no row. `DisableGame`, `EnableGame`, and `ToggleGameMissingAchievementsInDLC` do not check that the set exists.
- The keyring is read inside `initMu` during init. `ServiceStartup` runs init inside `app.Run`, before the first window opens, so a slow or blocked Secret Service prompt delays the window itself, and every bound call that needs init waits until the read returns. `CoverService` does not take `initMu`; before init it falls back to `appdata.Resolve()`.
- `SaveProfile` does not check the SteamID64 format or call Steam. A malformed id reaches the sync and fails there.
- An empty but valid `GetOwnedGames` answer (explicit `game_count` of 0) makes every profile game `not_owned`. That is by design in `internal/steam`, but it means one empty response disables the whole library until the next non-empty sync.
- The cover handler serves any cache file of the four names that exists on disk. It does not check the `cover_images` table.

## Tests

- `app_test.go`: bound orchestration with the `go-keyring` mock backend, and key state.
- `app_events_test.go`: `sync:changed` emission.
- `service_lifecycle_test.go`: `ServiceStartup` and `ServiceShutdown`, lifetime context, init failure, shutdown refusal.
- `covers_test.go`: `/game-covers` handler.
- `wailslog_test.go`: the Wails logger bridge.

`ValidateProfile` and `SyncNow` are tested offline by setting `steamBaseURL` to an `httptest` server. See [testing.md](testing.md).
