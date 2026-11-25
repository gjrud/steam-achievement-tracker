# Platform packages

Small, cross-cutting Go packages used by the backend. They do not depend on app logic. Other docs describe packages built on top of them: [architecture.md](architecture.md), [app-service.md](app-service.md), [database.md](database.md), [covers.md](covers.md).

| Package | Purpose |
| --- | --- |
| `internal/config` | Shared identity constants (app name, service id, directory and file names). |
| `internal/appdata` | Resolves and creates the per-OS data, cache and state directories. |
| `internal/appdata/appdatatest` | Test-only helper that points the XDG base directories at temp dirs. |
| `internal/keyring` | Stores, reads and deletes the Steam Web API key in the OS keyring. |
| `internal/logging` | Process-wide `log` output to a rotating file, with a stderr fallback. |
| `internal/fsretry` | `Rename`, `Remove`, `RemoveAll` that retry transient Windows file-lock errors. |
| `internal/redact` | `Paths`: reduces file paths in `fs.PathError` and `os.LinkError` to base names before an error reaches a log or the UI. |

## internal/config

Contains constants only; it has no behavior or tests.

| Name | Value | Used for |
| --- | --- | --- |
| `AppName` | `Steam Achievement Tracker` | Display name (`AppState.AppName`, built by `App.baseState`). |
| `AppID` | `steam-achievement-tracker` | Keyring service name. |
| `AppDirName` | `steam-achievement-tracker` | Subdirectory under each XDG base directory. |
| `DBFileName` | `steam-achievement-tracker.db` | SQLite file name inside the data directory. |
| `KeyringUser` | `steam-web-api-key` | Keyring user (account) name for the API key. |

Note: `config.AppID` is not the reverse-DNS application id. The Wayland app id and desktop file name use `io.github.gjrud.steam-achievement-tracker` (`applicationID` in `main.go`, also the single-instance `UniqueID`; `APP_ID` in `build/linux/Taskfile.yml`). Do not mix them: the keyring service and directory names must stay `steam-achievement-tracker`.

## internal/appdata

### Exported API

| Symbol | Kind | Description |
| --- | --- | --- |
| `Paths` | struct | `DataDir`, `CacheDir`, `StateDir`, `DB`, `Backups`, `GameImages`, `Logs`, `LogFile`. JSON tags are camelCase. |
| `Resolve()` | func | Computes all paths from the current `xdg` base directories. Touches no file system. |
| `Ensure(Paths)` | func | Creates `DataDir`, `Backups`, `CacheDir`, `GameImages`, `StateDir`, `Logs` with `MkdirAll(0o700)` and `Chmod(0o700)`. |

### Path layout

`xdg` (`github.com/adrg/xdg`) provides the base directories. `config.AppDirName` is appended to each.

| Path | Linux | Windows |
| --- | --- | --- |
| `DataDir` (DB, backups) | `$XDG_DATA_HOME` or `~/.local/share`/`steam-achievement-tracker` | `%LocalAppData%\steam-achievement-tracker` |
| `Backups` | `DataDir/backups` | `DataDir\backups` |
| `CacheDir` | `$XDG_CACHE_HOME` or `~/.cache`/`steam-achievement-tracker` | `%LocalAppData%\cache\steam-achievement-tracker` |
| `GameImages` | `CacheDir/images/games` | `CacheDir\images\games` |
| `StateDir` | `$XDG_STATE_HOME` or `~/.local/state`/`steam-achievement-tracker` | `%LocalAppData%\steam-achievement-tracker` |
| `Logs` / `LogFile` | `StateDir/logs`, `StateDir/logs/app.log` | `StateDir\logs`, `StateDir\logs\app.log` |
| `DB` | `DataDir/steam-achievement-tracker.db` | `DataDir\steam-achievement-tracker.db` |

`XDG_DATA_HOME`, `XDG_CACHE_HOME` and `XDG_STATE_HOME` override the base directories on both OSes (the `xdg` library reads them on Windows too).

The WebView2 profile lives under `CacheDir/webview`; `main.go` creates it on Windows only.

### Behavior

- `Resolve` is pure: it reads the `xdg` variables as they were at the last `xdg` load. Call `xdg.Reload` after changing them. Tests reload through `appdatatest`.
- `Ensure` is idempotent. Re-running it tightens existing directories to `0700`.
- `Ensure` fails when a path component is a regular file (`MkdirAll` error).
- Production call order in `App.ensureInitialized` (`app.go`): `appdata.Resolve()` -> `appdata.Ensure(paths)` -> `logging.Init(paths.LogFile)` -> database open. If `Ensure` fails, `logging.Fallback()` runs and the init error is shown on the startup-error screen.

### appdatatest (tests only)

| Symbol | Description |
| --- | --- |
| `Dirs` | `Data`, `Cache`, `State` absolute paths of a test's XDG directories. |
| `Isolate(t)` | Sets `XDG_DATA_HOME`, `XDG_CACHE_HOME`, `XDG_STATE_HOME` to subdirectories of `t.TempDir()`, reloads `xdg`, and reloads again on cleanup. |
| `Block(t)` | Points the XDG base directories under a regular file, so `Ensure` fails. Returns the blocker path. |

Gotchas:

- Do not isolate with `HOME`. On Windows `appdata.Resolve` does not use `HOME`.
- Cleanup order matters: `set` registers `xdg.Reload` before `t.Setenv`, so it runs after the variables are restored (Go cleanups run last-in first-out).
- Tests that use `Isolate` or `Block` must not call `t.Parallel`, because they change process environment variables.

## Files on disk

The table lists every file the app writes and the code that creates it. `<data>`, `<cache>` and `<state>` are the three roots in the path layout above.

| Path | Created by | Purpose |
| --- | --- | --- |
| `<data>/` | `appdata.Ensure` at startup | Data root, mode `0700`. |
| `<data>/steam-achievement-tracker.db` | DB open and migration (`db.OpenAndMigrate`) | SQLite database: profiles, games, achievements, flags, tags, snapshots and sync history. See [database.md](database.md). |
| `<data>/steam-achievement-tracker.db-wal`, `...-shm` | SQLite WAL mode | Write-ahead log and shared memory, present while the database is open. |
| `<data>/backups/` | `appdata.Ensure` at startup (also the migration backup) | Backup folder, mode `0700`. |
| `<data>/backups/steam-achievement-tracker-<unix-ns>.db` | Migration, before a schema change to a database that already has tables | Point-in-time copy of the database. Not created for a new database. See [database.md](database.md#backup-before-migration). |
| `<cache>/` | `appdata.Ensure` at startup | Cache root, mode `0700`. |
| `<cache>/images/games/` | `appdata.Ensure`, cover writes | Cover cache root. |
| `<cache>/images/games/<appid>/library_600x900.jpg`, `hero_capsule.jpg`, `header.jpg`, `main_capsule.jpg` | Cover refresh (`images.Cache`) | Cached Steam images for one app, or for the DLC app id of a DLC set. See [covers.md](covers.md). |
| `<cache>/webview/` | `main.go`, at every startup (Windows only) | Browser profile (cache and state) of the app window. Created before the window opens; kept on uninstall. |
| `<state>/logs/` | `appdata.Ensure` at startup, and `logging.Init` | Log folder, mode `0700`. |
| `<state>/logs/app.log` | `logging.Init` | Current log: app messages plus Wails warnings and errors. |
| `<state>/logs/app-*.log` | Log rotation (lumberjack) | Rotated logs. Each up to 5 MB, with up to 5 backups kept. |

The Steam Web API key is not a file. It is one keyring item outside the app folders: Secret Service with `service=steam-achievement-tracker`, `username=steam-web-api-key` on Linux, and a Credential Manager generic credential named `steam-achievement-tracker:steam-web-api-key` on Windows. `keyring.SetSteamAPIKey` writes it when the user saves the key, and only `ClearActiveUserData(_, true)` deletes it (see [app-service.md](app-service.md)).

Deleting the cache folder is safe; see [covers.md](covers.md#cache-layout).

### Updater leftovers

The in-app updater writes outside the three app roots: `<target>.bak`, `<exe>.old.<n>` (Windows) and `wails-update-*` next to the exe or AppImage, `wails-update-<pid>.log` in the OS temp dir, and on Windows the HKCU Apps & Features `DisplayVersion`. Lifetimes and cleanup: [updater.md](updater.md#staging-and-leftover-files), [After an update](updater.md#after-an-update).

The NSIS uninstaller (`build/windows/nsis/project.nsi`) deletes `$INSTDIR\<exe>.old.*` and `$INSTDIR\<exe>.bak`. It then handles each `$INSTDIR\wails-update-*` folder: it deletes only that folder's `.artifact` and `*-windows-amd64-portable.exe` files and removes the folder with a non-recursive `RMDir`. A leftover staging folder is therefore removed, and so is the install folder once it is empty. Anything else inside a `wails-update-*` folder keeps that folder, and the install folder then stays behind. The uninstaller never uses `RMDir /r`: an unrelated file in `$INSTDIR` also keeps the install folder.

## internal/keyring

Stores the Steam Web API key exclusively in the OS keyring via `github.com/zalando/go-keyring`: Secret Service on Linux, Windows Credential Manager on Windows (go-keyring also supports macOS, which this project does not target). No environment-variable or plaintext fallback exists.

### Exported API

| Symbol | Kind | Description |
| --- | --- | --- |
| `ErrMissing` | error | No key stored (keyring item absent or empty). A `usererr` sentence, shown as is. |
| `ErrInvalidKey` | error | Key is empty after trimming, or contains whitespace, control or `Cf` (format) characters. Constant message; never includes the input. A `usererr` sentence, shown as is. |
| `ErrNotStored` | error | Read-back after write did not return the same key, or was absent. Usually a dismissed unlock or create prompt (Secret Service on Linux), or a duplicate item from another provider. |
| `BackendError` | type | A failure of the keyring backend (locked, unavailable, denied) from Get, Set or Delete. `Error` and `Unwrap` pass the backend text through. The App logs it and shows `msgKeyring`. |
| `GetSteamAPIKey() (string, error)` | func | Reads the key. Maps `go-keyring` `ErrNotFound` and empty values to `ErrMissing`. Other errors are returned as `*BackendError`. |
| `SetSteamAPIKey(key) (string, error)` | func | Trims the key, validates it, stores it, reads it back, returns the trimmed key. A store or read-back backend failure is a `*BackendError` (`store api key: ...`, `read back api key: ...`). A read-back that is absent or different is `ErrNotStored`. |
| `DeleteSteamAPIKey() error` | func | Removes the item. An absent item is success (idempotent). Other errors are a `*BackendError` wrapping `delete api key: ...`. |

Storage identity:

- Service: `config.AppID` (`steam-achievement-tracker`).
- User: `config.KeyringUser` (`steam-web-api-key`).
- On Windows, go-keyring builds the generic credential target `service + ":" + user`, that is `steam-achievement-tracker:steam-web-api-key` (verified in go-keyring v0.2.8 source).

### Behavior

- Read path (`GetSteamAPIKey`) is called at startup and by actions that need the key (`app.go`). `ErrMissing` leads to the missing-key screen. Another read error is logged as `keyring lookup: ...` (paths reduced by `redact.Paths`). `apiKeyError` is set to the fixed `msgKeyring` sentence (`app_errors.go`), which is shown on that same screen (`SetupView.svelte`).
- Write path (`SetSteamAPIKey`) returns the trimmed key. Callers never log it.
- Error text never contains the key. Tests check this for set and delete (`TestSetSteamAPIKeyErrorsNeverContainTheKey`, `TestDeleteSteamAPIKeyErrorNeverContainsKey`).

### Test seams and mock backend

- Package variables `zkSet`, `zkGet`, `zkDelete` wrap the go-keyring calls so tests can simulate a silent non-store (read-back mismatch) or a failing backend.
- `GetSteamAPIKey` calls `zk.Get` directly, not through the `zkGet` seam. Read-path backend errors are tested with the go-keyring mock instead (`TestGetSteamAPIKeyPassesThroughBackendErrors`).
- Tests use go-keyring's in-memory provider: `zk.MockInit()` (values survive across calls in one test) and `zk.MockInitWithError(err)` (every call fails). Tests that swap seams must not run in parallel and restore the originals with `t.Cleanup`.

Gotchas:

- go-keyring's Windows backend rejects values longer than 2560 bytes (`ErrSetDataTooBig`). `SetSteamAPIKey` does not pre-check length; the error comes back wrapped as `store api key: ...`. Steam keys are far shorter.
- `SetSteamAPIKey` only verifies by read-back. On Linux, a dismissed unlock or create prompt in Secret Service is ignored silently by go-keyring, which is why `ErrNotStored` exists. Windows Credential Manager shows no prompt.

## internal/logging

Routes the standard `log` package to a rotating file. Output goes to stderr before `Init` and after `Fallback`.

### Exported API

| Symbol | Description |
| --- | --- |
| `Init(path string)` | Creates the directory (`0700`) and the file (`0600`), opens a `lumberjack` logger on it, swaps it in as current, closes the previous logger, sets `log` flags, and logs `logging initialized`. |
| `Fallback()` | Closes the current logger and sets it to nil. Later writes go to `os.Stderr`. |
| `Writer() io.Writer` | Returns the writer that the standard logger and `wailslog.go` write through. Resolves the current file on every write. |

### Behavior

- `init()` calls `log.SetOutput(Writer())` once. Output is never re-pointed under the lock.
- All writes take a package mutex, so a write cannot reach a closed file, and `Init` or `Fallback` cannot race a write.
- Rotation (from `lumberjack`): `MaxSize: 5` (MB), `MaxBackups: 5`, `Compress: false`. `MaxAge` is unset, so old backups are kept until the count limit removes them. Rotated files keep the mode of the file they replace (lumberjack copies it).
- Log flags: `LstdFlags | Lmicroseconds | Lshortfile`, set by `Init`. Output is therefore `date time.micro file:line: message`.
- File permissions: directory `0700` and file `0600` on POSIX. Windows ignores POSIX bits.
- `wailslog.go` forwards Wails' logger to `Writer()`, and `App.ensureInitialized` calls `Init`. See [app-service.md](app-service.md).

Gotchas:

- Windows: an open log file blocks removal of its directory. This is why `Init` and `Fallback` close the previous logger. Any test that removes a log directory must call `Fallback` first. `TestFallbackReleasesLogFile` and `TestInitClosesPreviousLogFile` check this.
- `Init` ignores errors from `MkdirAll`, `OpenFile` and `Chmod`. If the file cannot be created, logging falls back to nothing visible: `lumberjack` retries on write, and the `log` package ignores write errors. Check the path if logs are missing.
- Never enable Debug on the Wails logger. Debug logs bound-call arguments, including the key (see `wailslog.go` and `AGENTS.md`).

## internal/fsretry

Wrappers around `os.Rename`, `os.Remove` and `os.RemoveAll`. On Windows, they retry briefly when another process (antivirus, indexer, the cover server or WebView) holds a file.

### Exported API

| Function | Wraps |
| --- | --- |
| `Rename(oldpath, newpath) error` | `os.Rename` |
| `Remove(name) error` | `os.Remove` |
| `RemoveAll(path) error` | `os.RemoveAll` |

### Behavior

- Constants: `maxAttempts = 5` total calls, `retryDelay = 30ms` between them. A transient error therefore costs at most four waits (120 ms) before it is returned.
- `run(policy, op)` calls `op` once. It retries only when `policy.transient(err)` is true and attempts remain. Any other error returns on the first call, exactly like the `os` function.
- Transient classification (`isTransient`):
  - `transient_windows.go` (`//go:build windows`): `ERROR_ACCESS_DENIED` (5), sharing violation (32), lock violation (33). `ERROR_FILE_NOT_FOUND` is not transient. Access denied is retried on purpose: a file held by the cover server or WebView reports it. The cost is the 120 ms above for a truly read-only file.
  - `transient_other.go` (`//go:build !windows`): always false. Every call is a single `os` call.
- `RemoveAll` is safe to retry: `os.RemoveAll` returns nil for missing paths.
- `policy` is a value (attempts, delay, sleep, transient) so tests inject a fake sleep and a fake classifier.

Call sites: `internal/images` (cover temp-file rename and removal), `internal/db` (`writeBackupFile` removes a partial backup copy after close), `app.go` (`removeCachedGameImages` on clear-data), and `internal/update` (the updater's staged folder, leftover files and the startup sweep).

Gotchas:

- Use for files another process may briefly hold open (cover server, WebView, antivirus, indexer).
- Plain `os` calls are fine elsewhere.
- `transient_windows_test.go` is `//go:build windows` and does not compile on Linux CI. The generic retry tests in `fsretry_test.go` run everywhere.

## Single instance

Only one copy of the app runs. `main.go` sets `application.Options.SingleInstance` with `UniqueID` = `applicationID` (`io.github.gjrud.steam-achievement-tracker`, the same constant as the Linux GTK `ApplicationID`). The locking is done by Wails v3.1.0 (`pkg/application/single_instance_*.go`):

| Platform | Lock | Requirement and failure |
| --- | --- | --- |
| Linux | D-Bus well-known name `io.github.gjrud.steam-achievement-tracker.SingleInstance` on the session bus, claimed with "do not queue". | A session bus must be reachable. Without one, `application.New` cannot take the lock and Wails calls its fatal handler: the app exits (status 1) before any window or service starts, and the error goes to stderr, not the log file (`logging.Init` runs after `application.New`). Launched from the desktop there is no terminal, so the app exits silently. The keyring error screen is never reached. |
| Windows | Named mutex `wails-app-io.github.gjrud.steam-achievement-tracker-sim`. | Any `CreateMutex` error counts as "already running". |

A second launch sends its arguments and working directory to the first instance and exits with `ExitCode` (0). The first instance's `OnSecondInstanceLaunch` calls `focusMainWindow` (`main.go`): it focuses the main window when that window is visible (`Focus`, not `Restore`: `Restore` un-maximises the window in Wails v3.1.0). While the Linux window is still hidden (before its page loads, see [window.go](../window.go)) it does nothing. The arguments are not used and not logged. `EncryptionKey` is unset: the payload is not secret and is not used. On Windows the running app may not be allowed to take focus on a second launch; the taskbar button may only flash.

Effects:

- A second launch exits before `ServiceStartup`, so it does not run `CancelRunningSyncRuns` at init and cannot cancel the first instance's sync.

Updater restart (see [updater.md](updater.md#restart)): the helper waits up to 30 s for the old process to exit (`internal/update` relaunch path, the Wails `updater` helper `wait(parentPID)`) before it swaps the executable and starts the new one. The lock is released when the old process exits (the D-Bus name on Linux, the mutex on Windows), so the new process takes the lock. The helper's abort path relaunches the old executable when the old process is still alive after 30 s; that launch is a second instance: it focuses the still-running window and exits, so the user stays on the old version until the next start. Without the lock, that abort path would start a second copy beside the running one. Risk not verified on a real machine: restart on Linux and Windows with the lock held.

Dev builds share the installed app's lock: both use the same `UniqueID`. If the installed app is open, `wails3 dev` hands off to it and exits with status 0.

Manual check: start the app, start it a second time, and confirm the first window comes forward and no second window opens (on Windows its taskbar button may only flash). On Linux, run from a terminal with `DBUS_SESSION_BUS_ADDRESS` unset and confirm the app exits at startup with a D-Bus error on stderr instead of showing the keyring screen.

## Testing these packages

Per-package test coverage is in [testing.md](testing.md#go-test-layout).

Run `go test ./internal/appdata/... ./internal/keyring/... ./internal/logging/... ./internal/fsretry/...`; add `-race` for the logging race test. See [testing.md](testing.md).
