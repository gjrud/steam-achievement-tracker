# In-app updater

This document describes the in-app updater: how it finds a release, how a user-approved update is downloaded, verified, staged and applied, and what can go wrong. The user-facing rules are in the [README](../README.md#updates). The Go code is `internal/update` (the service, the GitHub provider setup, the phase rules, the errors), `internal/version` (the version stamp), and `app_update.go` (the bound methods). The frontend is covered in [frontend.md](frontend.md#update-banner-and-settings).

The updater wraps the framework updater from Wails v3.1.0 (`pkg/updater`, provider `github`). The service never uses the framework's `CheckAndInstall` or `CheckInterval`, because they download without asking. The framework's own UI is off (`updater.WindowNone`). The frontend never subscribes to the framework's `wails:updater:*` events: Go listens to three of them, and the frontend hears only the app's own `update:changed` event.

## Install targets

| Install | Behavior |
| --- | --- |
| Windows portable exe | Updated in place. |
| Windows per-user installed exe (`%LocalAppData%\Programs\Steam Achievement Tracker\`) | Updated in place. The app sets the "Apps & Features" version at its next start (see [After an update](#after-an-update)). |
| Linux AppImage | Updated in place, when the process runs from that AppImage. |
| Linux deb, rpm, Arch package, or any other Linux copy | Notify only. The banner shows the version and the release link. Install is never offered. |
| Other platforms | The check fails: `matchAsset` finds no asset, so the provider returns an error. No supported build reaches this. |

A build without a valid version stamp (dev builds, Docker builds, untagged local builds) has no updater: no request is made and no update UI shows. See [Version stamp](#version-stamp).

## Version stamp

- The production build tasks set `internal/version.tag` to `sat-version:<PKG_VERSION>;` through `-ldflags -X`. The flags and the release marker check are in [build-and-release.md](build-and-release.md#version-stamping-and-marker-checks).
- `version.Current()` reads the stamp. `ParseStamp` needs the `sat-version:` prefix and the `;` suffix, and then `Valid` must accept the rest. `Valid` requires the shape `X.Y.Z` with an optional `-pre` part and no `v` prefix (checked with a regular expression, then `semver.IsValid("v"+v)`). Anything else leaves the updater unavailable. Empty, unlabeled, unterminated, `v`-prefixed and partial versions such as `1.2` are all invalid.
- `IsPrerelease` is true when the version has a `-`. Pre-releases are never offered (see [Check](#check)).

## Local test build

To test the updater locally, stamp a build with an old version. On Windows: `VERSION=0.0.1` (PowerShell: `$env:VERSION = "0.0.1"`) then `wails3 task windows:build`. On Linux: `VERSION=0.0.1 wails3 package`, then run `bin/steam-achievement-tracker-x86_64.AppImage`. A plain `wails3 build` binary is notify-only (never Install). A stamp equal to or newer than the latest release shows "up to date".

## Wiring

- `main.go` creates the service after `application.New` and before the window: `update.New(update.Deps{...})`, then `backend.setUpdater`, then `updates.Init()`. The dependencies are the framework updater (`app.Updater`), the app lifetime context, `app.Event.On` (for the framework events Go listens to), the settings reader (`backend.updateChecksEnabled`, which the service caches), and `OnChanged`, which emits `update:changed`.
- `Init` reads the stamp. With a valid stamp it builds one stock GitHub provider and calls the framework `Updater.Init`. Its configuration is `CurrentVersion` (the stamp), `Providers` (the provider), and `Window: WindowNone`. It has no `CheckInterval` and no `PublicKey`. If `Init` fails, the service is unavailable and logs `update: unavailable: other` once, after logging is up. No secret or key is involved.
- `App.ServiceStartup` runs `StartupCheck` only after `ensureInitialized` succeeds. When init fails, the startup check is skipped (there is no database to read the toggle from), and the update UI does not show on the setup or init-error screens.
- `App.ServiceStartup` calls `startUpdateHousekeeping` (which calls `RunPostUpdate`) right after `logUpdateInitFailure` and before the init-error return. The post-update work therefore runs even when the database fails to open. See [After an update](#after-an-update).
- `App.ServiceShutdown` calls `stopUpdates` (`Shutdown`) before it waits for syncs (see [Shutdown and restart safety](#shutdown-and-restart-safety)).

### Provider configuration

`githubConfig` (`internal/update/client.go`) sets:

| Field | Value |
| --- | --- |
| `Repository` | `gjrud/steam-achievement-tracker` |
| `Prerelease` | `false` |
| `ChecksumAsset` | `SHA256SUMS` |
| `AssetMatcher` | `matchAsset` (below) |
| `HTTPClient` | the app's client (below) |
| `BaseURL` | empty in production; tests set it to an `httptest` server |

The GitHub provider requests `GET /repos/{owner}/{repo}/releases/latest`. A 404 counts as "no release". The provider also fetches `SHA256SUMS` when a newer release exists. A check therefore costs up to two API requests. The download goes to `api.github.com/repos/{owner}/{repo}/releases/assets/{id}` with `Accept: application/octet-stream`, and GitHub redirects it to its storage host. The provider strips `Authorization` on a host change for the `SHA256SUMS` and asset requests; the app sends no token.

## Network

The updater sends ordinary HTTPS requests to two hosts:

- `api.github.com`. A check asks for the latest release. When a newer one exists it also downloads `SHA256SUMS`, which `api.github.com` redirects to a GitHub file host. Install downloads the release file the same way.
- The GitHub file host that those redirects go to. GitHub chooses this host.

Each request carries Go's default `User-Agent`, the `Accept` and `X-GitHub-Api-Version` headers from the provider, and nothing else from the app. There is no app version, no install or user identifier, no account, and no telemetry. The proxy comes from the environment (`http.ProxyFromEnvironment`). With the updater unavailable, or with the startup check off, the app makes no request at startup. No release asset is downloaded until the user clicks Install.

The app's client (`newHTTPClient`) has these limits:

| Setting | Value |
| --- | --- |
| Dial timeout | 15 s |
| TLS handshake timeout | 15 s |
| Response header timeout | 30 s |
| Total timeout | none |
| Idle connection timeout | 90 s |
| Redirects | `https` only, at most 10 requests, i.e. 9 redirects, Go's default (`checkRedirect`). A redirect to plain `http`, or one that would be the 11th request, fails the request. The first request may be any scheme, and the stubs in tests use `http`. |
| Response body | at most 512 MiB (`maxDownloadBytes`) for every response: the release JSON, `SHA256SUMS` and the asset. A declared length over the cap is refused before any byte is read. A body that grows past it fails the read. Exactly the cap is allowed. |

There is no total timeout, so a large asset on a slow link is not cut off. Cancel and app shutdown end a download. A check has its own timeout (`checkTimeout`, 60 s) that covers both of its API requests. The framework's default client has a 30 s total timeout that would fail large downloads, so the app passes its own client.

The size cap and the redirect rule are the client's own policy, and they apply to every request the client makes: the release JSON and `SHA256SUMS` of a check (and of an Install's re-check), and the asset download. For `SHA256SUMS` and the asset, the provider's redirect wrapper first strips `Authorization` on a host change, then calls the client's redirect check; the release JSON request uses the client directly. A refused redirect or an over-cap body ends that operation as a failure. On a check or a re-check it is the `check-failed` category, and during an Install's re-check the phase goes to `available` with the check sentence, not to `error`. On the asset download it is the `download-failed` category (see [Errors and logging](#errors-and-logging)).

## Phases

`Phase` (`internal/update/phase.go`) is what the UI shows: `idle`, `checking`, `up-to-date`, `available`, `downloading`, `verifying`, `installing`, `ready`, `restarting`, `error`.

The phase is the only exclusion between operations. A transition is made under one short state lock (`Service.mu`). The network, framework and filesystem work runs outside that lock. Nothing waits on a running operation, so `Shutdown` and the framework event listener never block behind a download.

Transitions:

| From | Operation | To | Notes |
| --- | --- | --- | --- |
| `idle`, `up-to-date`, `available`, `error` | manual check | `checking` | |
| `idle` | startup check | `checking` | Only from `idle`. Otherwise dropped silently. |
| `checking` | check finds nothing newer, or only a pre-release | `up-to-date` | |
| `checking` | check finds a newer stable release | `available` | Carries the latest release and its gate result. |
| `checking` | startup check fails | `idle` | Quiet: no banner, no `error`. |
| `checking` | manual check fails | `error` | Shows the check sentence with "Try again". |
| `available` | Install (version the user saw) | `downloading` | Set before the re-check so Cancel is visible during it. |
| `error` | Install (after a failed download) | `downloading` | Retry. |
| `downloading` | framework `verifying`, then `installing` | `verifying`, `installing` | Framework events, see below. |
| `downloading`, `verifying`, `installing` | framework finishes | `ready` | The payload is staged. A cancel during verification or installation that the framework completes also ends here. |
| `downloading`, `verifying`, `installing` | download fails | `error` | Download, verification or folder sentence. A failed download leaves no staged folder: the framework removes it (see [Errors and logging](#errors-and-logging)). |
| `downloading`, `verifying`, `installing` | app shutdown during `verifying` or `installing`, then the framework finishes | `idle` | The framework does not stop these steps, so the install ends with success. Its staged folder is removed and the phase is not `ready` (see [Shutdown and restart safety](#shutdown-and-restart-safety)). |
| `downloading` | cancel stops the download | `available` | Cancel never sets `error`. A cancel during verification or installation is not stopped by the framework and ends in `ready` (row above). |
| `downloading` | re-check finds nothing newer | `up-to-date` | The cached release is cleared. This also applies when Cancel was pressed, as long as the re-check itself completed. |
| `downloading` | re-check finds a newer or unsafe release, a Cancel that aborts the re-check or came after it found a release, or a failed re-check | `available` | A failed re-check also sets the check sentence in the snapshot's `error` field. |
| `ready` | Restart | `restarting` | Set before the framework's `Restart`, because `Restart` runs shutdown. |
| `restarting` | `Restart` fails | `ready` | Retry allowed. |
| `restarting` | `Restart` succeeds | `restarting` | The app is closing. Nothing else runs. |
| `ready` | app shutdown | `idle` | Staged folder removed (see below). |

`canStart` (`phase.go`) decides which operations may begin. Everything else is refused at once with one sentence:

- While a check, download, verification, installation or restart runs: "Another update step is already running."
- While `ready`: "An update is ready. Restart the app to apply it first." (for Check and Install).
- For Restart outside `ready`: "There is no downloaded update to restart into."

`Cancel` only sets a flag and cancels the install context. It never changes the phase. It is a no-op outside `downloading`, `verifying` and `installing`.

### Framework events

Go subscribes to three framework events only: `wails:updater:download-progress`, `wails:updater:verifying` and `wails:updater:installing` (the `updater.Event*` constants). The frontend does not subscribe to them. `EventError` is not consumed. Events reach Go listeners asynchronously and unordered, and these carry no attempt id, so `frameworkEvent` accepts them forward-only:

- progress only while `downloading` (it updates the byte count);
- `verifying` only from `downloading`;
- `installing` only from `downloading` or `verifying`.

Anything else is dropped. The result of the operation itself sets every terminal phase (see below), so a late framework error event cannot change the outcome.

## Check

`CheckNow` (manual) and `StartupCheck` (background) use the same `runCheck`.

- The startup check starts when the app starts (`ServiceStartup`), after `startupDelay` (5 s), and only when the toggle is on and the updater is available. It runs only if the phase is still `idle` then. Otherwise it is dropped silently. It stops when the app shuts down.
- `Updater.Check` finds the newest release. `screenRelease` then drops a pre-release (it reads as "up to date", so `-rc` tags are never offered) and treats a tag that is not `X.Y.Z[-pre]` as a check failure.
- `latestFrom` builds the snapshot entry and runs the install gate (below). The entry has the version, the asset size and the release page URL (`https://github.com/gjrud/steam-achievement-tracker/releases/tag/v<version>`, built from the validated version).

## Install

`InstallUpdate(version)` takes the version the user saw. In order:

1. The updater must be available. Host refusals (package installs, unsupported platforms) return their reason without changing the phase.
2. A notify-only release (the gate, below) returns its reason without changing the phase.
3. The phase must allow it (`available` or `error`). From `idle` or `up-to-date`, or with no known release, Install returns "Check for updates first." and changes nothing. Otherwise the phase moves to `downloading`, the install context is created, and the cancel flag is reset.
4. The install re-runs the check on the install context (with `checkTimeout`, so Cancel aborts it). If the newest release is not the version the user saw, or it is notify-only, or Cancel was pressed during the re-check, nothing is downloaded: the phase returns to `available` with the newest release as found. When the version changed, the banner says so, so the user can review it again. A failed re-check returns to `available` with the check sentence in the snapshot's `error` field. If nothing newer exists, the phase becomes `up-to-date`, even if Cancel was pressed, as long as the re-check itself completed. A Cancel that aborts the re-check returns to `available`.
5. `Updater.DownloadAndInstall` downloads, verifies and stages the payload. Success moves to `ready`. A cancel moves to `available`. Any other failure moves to `error`.

The framework's `DownloadAndInstall` installs the `pending` release stored by the last `Check`, so the re-check in step 4 is required.

### Install gate

`installRefusal(verification, env)` decides whether a release may be installed in place. It is checked in this order:

1. The host (`hostRefusal`): Windows is always allowed. Linux is allowed only when `appImageTarget` returns the AppImage. Other platforms are refused.
2. The release verification: `Verification` must not be nil, `DigestAlgo` must be `sha256`, and the digest must be 32 bytes.

A refusal makes the release notify-only. The banner then shows the reason and the release link instead of Install. The reasons are fixed sentences: no verified checksum, installed from a package, or not available on this platform.

`appImageTarget` reproduces the framework's Linux rule (`pkg/updater/spawn.go`). It returns `$APPIMAGE` only when `$APPDIR` and `$APPIMAGE` are both set, the running executable is under `$APPDIR`, and `$APPIMAGE` is a regular file. An `APPIMAGE` variable inherited by a process that is not an AppImage run fails the `$APPDIR` check, so installs stay off.

### Asset matching

`matchAsset` (`match.go`) picks the one asset to check and install:

| Platform | Asset |
| --- | --- |
| Windows amd64 | the asset whose name ends with `-windows-amd64-portable.exe`: `steam-achievement-tracker-windows-amd64-portable.exe` from the first release after v1.2.1 on (no version in the name), and `steam-achievement-tracker-<version>-windows-amd64-portable.exe` in earlier releases. The suffix matches both. The installer and any package are never chosen. |
| Linux amd64 | exactly `steam-achievement-tracker-linux-x86_64.AppImage`. The release job renames the AppImage to this name before it writes `SHA256SUMS`. |
| Anything else | none (-1) |

On Linux the AppImage is always the matched asset, even for a package install. A package install can still learn that a newer release exists, and then it is notify-only.

## Trust model

The updater checks integrity only.

- `SHA256SUMS` is published in the same release as the binary. It detects a corrupted or truncated download. It does not detect a replaced release, because whoever can replace the binary can replace the sums too.
- The framework checks the SHA-256 of the download against the `SHA256SUMS` line for the matched asset. If the sums file is missing, or does not list the asset, the framework sets no verification and the install gate refuses it (fail-closed in this app; the framework alone would install unverified).
- No signature is checked. The releases carry no signature, and `PublicKey` is not set. Setting it would reject every release.
- Build provenance attestations (`gh attestation verify`) remain a manual check for users.
- Windows binaries are not code-signed. The app swaps the exe without the Mark-of-the-Web, so SmartScreen is unlikely to warn; Windows Defender may delay the new file while it scans.

## Persistence

The only stored value is the `update_checks_enabled` key in the `settings` table (see [database.md](database.md#settings) and [repository.md](repository.md#update-check-setting)). There is no migration, and no update state is stored: the phase, the latest release and any "Later" dismissal live in memory and are gone after a restart.

The service caches the toggle, so `GetUpdateState` never reads the database:

- `LoadChecksEnabled` reads the setting once, when the database first opens (`ensureInitialized`). An unreadable setting is logged once as `update: settings unreadable: update checks off`, and the toggle reads as off.
- `SetUpdateChecksEnabled` (binding) stores the value first, and only then changes the cached toggle, so the snapshot and the stored value agree.
- A startup check does not read the setting again when it was already loaded.

## Restart

`RestartToUpdate` applies the staged payload. Only `ready` is accepted.

1. The phase moves to `restarting` and subscribers are notified, before the framework's `Restart` runs. The framework's `Restart` calls host quit, which runs `ServiceShutdown` (through the Wails shutdown chain) before `Restart` returns, so the phase must already be `restarting` at that point.
2. `Updater.Restart` hands the staging directory to a helper process, starts it, and waits up to 30 s for its ready signal. It then quits the app. The helper waits up to 30 s for the app to exit and then swaps the executable (the AppImage file on Linux, the exe on Windows).
3. The helper starts the new version. If the new process cannot be started, the helper restores the previous version from its backup (`<target>.bak`). On Windows the old exe is first renamed to `<exe>.old.<UnixNano>` and removed later.
4. A failed `Restart` (the helper did not start or did not signal ready) returns the phase to `ready` with the restart sentence. The app keeps running with the update staged, and the user can retry.

A new version that starts and then crashes is not rolled back (see [Known limitations](#known-limitations)).

## Errors and logging

Every updater failure is written to the log as one category and returned to the UI as a sentence without a cause. The raw error text (which may contain full paths or the OS user name) is never logged or returned.

Categories (`errors.go`):

| Category | When | Log line | UI sentence |
| --- | --- | --- | --- |
| `check-failed` | check or re-check fails (network, API, bad version), including an over-cap or refused-redirect response for the release JSON or `SHA256SUMS` | `update: check failed: check-failed` (manual), `update: startup check failed: check-failed` (startup), or `update: install: check-failed` (re-check during Install) | "Could not check for updates. Try again later." |
| `cancelled` | the user cancelled an install, or the app shut down during one | `update: install: cancelled` (a category only, not a failure) | no sentence |
| `not-writable` | the staging step returns a permission error (`fs.ErrPermission`) | `update: install: not-writable` | "The app folder is not writable, so the update cannot be installed here. Download the new version from the release page." |
| `download-failed` | download, checksum or staging fails for any other reason, including an asset response over the 512 MiB cap (`errTooLarge`) and a refused redirect on the asset download | `update: install: download-failed` | "The update could not be downloaded or verified. Try again." |
| `restart-failed` | `Restart` returns an error | `update: restart: restart-failed` | "The app could not restart into the update. The update is still ready, so you can try Restart again." |
| `other` | anything else, including a failed `Updater.Init` and the staged-folder removal at shutdown | `update: unavailable: other`, `update: remove staged update: other` | `The update failed. Try again later.` (`msgOther`). No current path shows it: a failed `Init` gives the unavailable sentence, and the shutdown removal shows nothing. |

Precedence for a download or install step: our own cancel flag or a cancelled context wins (`cancelled`); then a permission error (`not-writable`); then `download-failed`. The framework's error text is never read beyond the `fs.ErrPermission` check. A cancel after `DownloadAndInstall` returned nil, or during verification and installation (which the framework does not check the context in), ends in `ready`.

A failed download needs no cleanup from the app. The framework removes its own staging folder (`wails-update-*`, with the partial file in it) before `DownloadAndInstall` returns an error, for any download error: a network failure, an over-cap body, a refused redirect or a failed checksum. The app's code never removes a partial file during a download.

Other sentences the update service returns (not categories): unavailable ("Updates are not available in this build."), busy, ready-first, not-ready, check-first.

The snapshot carries the current error sentence in `error`. The frontend also shows the sentence of a rejected call. A failed startup check shows nothing.

Settings read errors log `update: settings unreadable: update checks off` once per load. The app also logs `update: unavailable: other` when `Init` fails.

The post-update work (see [After an update](#after-an-update)) logs only when it does something or fails, and never a path or raw error text:

| Log line | When |
| --- | --- |
| `update: installed version registered` | The Apps & Features version was written. |
| `update: version sync failed: other` | The registry read or write failed. |
| `update: cleanup: removed <n> leftovers` | The sweep removed `<n>` files or folders. |
| `update: cleanup: <n> leftovers could not be removed` | `<n>` removals failed, for example a file still held open. |

Log lines never contain a full path. Framework messages such as `ErrHelperNotReady` text, which can carry paths, are not logged or returned.

## Shutdown and restart safety

`App.ServiceShutdown` (`app.go`) runs in this order:

1. Cancels the app lifetime context. Checks and installs derive from it, so they stop.
2. `Service.Shutdown`: marks the service stopped (so no more `update:changed` events), sets the shutdown flag, cancels a running install, unsubscribes from framework events, and, if the phase is `ready`, moves it to `idle`, drops the latest release, and removes the staged folder. Nothing is removed while `restarting`, because the helper owns that folder. A shutdown during an install's `verifying` or `installing` step does not wait: the install itself ends the same way (see below).
3. Cancels any running sync.
4. Waits for syncs for at most `syncShutdownWait` (10 s), then logs a warning and continues. The wait is bounded so that a restart (which the helper gives 30 s to exit) cannot time out and relaunch the old version.
5. Closes the database.

`Shutdown` never waits on an operation. The staged folder is removed by `fsretry.RemoveAll` on `filepath.Dir(Updater.DownloadedPath())`, and only when the directory name starts with `wails-update-`.

The framework does not check the install context while it verifies and installs, so a shutdown can land in that step. Shutdown then sees no `ready` phase and removes nothing, and the install ends with the framework's success. The success path (`finishStaged`) reads the shutdown flag under the same state lock as its transition, so the two orders are both covered: a shutdown before the transition makes the install end in `idle`, not `ready`, and removes the folder after the lock is released; a shutdown after it finds `ready` and removes the folder. A Cancel during verification or installation sets the cancel flag, but the framework does not stop those steps: if it completes them, the install still ends in `ready` and keeps the folder. A Cancel after `ready` is a no-op. The shutdown test runs the real framework against a local stub (`TestShutdownDuringFinaliseRemovesStagedUpdate`).

Consequences:

- A restart cancels a running sync. The sync normally ends in time and its run is recorded as `failed` (see [architecture.md](architecture.md#shutdown)). If it outlives the 10 s bound, the database closes first, so its run row stays `running`. The next start marks it `canceled` (shown as "Sync canceled"). The "Restart now" banner says so.
- A restart does not lose data: the database closes after the bounded wait, and the new process opens it normally.
- Closing the app without applying a staged update removes the staged folder, so no full-size copy of the update stays next to the exe or the AppImage. This includes closing the app while an install is still verifying or installing: that folder is removed when the install finishes, not left for the startup sweep.

## After an update

`RunPostUpdate` (`internal/update/postupdate.go`) runs on every start of a stamped build, from `ServiceStartup`. It does nothing when the updater is unavailable (dev, Docker and untagged builds, tests). It starts one goroutine per process. No update state is stored: each start works from what is on disk and in the registry.

- Apps & Features (Windows, per-user installed copy only). At once, on every start, the app reads the HKCU uninstall entry that the installer writes: `Software\Microsoft\Windows\CurrentVersion\Uninstall\gjrudSteam Achievement Tracker` (64-bit view). It writes `DisplayVersion` only when the stored value differs from the running version and `DisplayIcon` is the running exe. The value written is the numeric core of the running version. A pre-release part is dropped, as the installer drops it (`1.2.3-rc.1` writes `1.2.3`). A missing key (portable exe) or another install's entry is left alone. The key name must match `UNINST_KEY_NAME` in `build/windows/nsis/wails_tools.nsh`. The installer deletes the whole key on uninstall. The app writes no other registry value.
- Leftover files. Five seconds after startup, or never if the app shuts down first. See [Staging and leftover files](#staging-and-leftover-files).

## Staging and leftover files

The framework creates a `wails-update-*` directory next to the target (the exe or the AppImage). Several files can be left behind:

| Path | Left when |
| --- | --- |
| `<target>.bak` (next to the AppImage on Linux, next to the exe on Windows) | the helper's copy of the previous version. The successful path deletes it, and a restore renames it back. It stays when the helper stops mid-update or a restore fails. |
| `<exe>.old.<UnixNano>` (Windows, next to the exe) | every successful Windows update while the old file is still in use. A later update's helper, or the app at its next start, removes them when Windows allows. |
| a `wails-update-*` directory next to the target | a helper failure, a crash or a kill during an update. A staged update that was never applied is removed at shutdown. |
| `wails-update-<pid>.log` in the OS temp directory | every update attempt that starts the helper. It holds full paths, usually including the OS user name. The framework never removes it; the app does at a later start, once the log is more than 24 hours old (see below). |

The startup sweep (`sweepLeftovers`) runs once per start, five seconds after startup (`cleanupDelay`). The delay lets the helper finish its own cleanup of `.bak` and the staging folder first. Each candidate is checked before it is removed:

- `<target>.bak`: a regular file or symlink, never a directory, last modified more than 24 hours ago (`leftoverMinAge`).
- on Windows only, `<exe>.old.<digits>` regular files next to the exe. The `<exe>.old.` part matches in any letter case, and the rest must be one or more ASCII digits; any other name stays. There is no age limit: Windows keeps the file until the old process has exited, and its mtime is the old build's time.
- `wails-update-*` folders next to the target. A folder is removed only when it is flat (regular files only, no subfolders and no symlinks) and neither the folder nor any file in it was changed in the last 24 hours. It is never the folder that holds the staged payload, and none is removed while an update is downloading, verifying, installing, ready or restarting.
- `wails-update-<digits>.log` regular files in the OS temp directory (the name must match exactly), once the log was last written more than 24 hours ago (`leftoverMinAge`). A log from a failed update is therefore removed at the first start more than 24 hours after it was written; a restart sooner keeps it. The sweep skips the temp logs while the phase is `restarting`, because that log belongs to the live helper.

The age and shape checks cover another app that shares the target folder and an updater helper still running from a previous start. The app itself runs as a single instance (see [platform.md](platform.md#single-instance)). A concurrent update writes its files continuously, so its in-flight `.bak` and staging folder are young and are left alone. A folder that fails a check is skipped silently, and so is a file that vanished before its removal, or a temp log that another user owns (sticky `/tmp`). Those are not failures.

The sweep never removes the target and never removes a tree. For a staging folder that passes the checks, it lists the folder's entries once, removes each listed file by name, then removes the folder with a non-recursive remove. A file added after the listing makes that last remove fail, so the folder and the new file stay and the failure is counted. If a listed file cannot be removed, the folder is not attempted. The sweep does nothing on a Linux package install (no target). Its log lines are counts only (see [Errors and logging](#errors-and-logging)). Nothing is logged when nothing is found.

The NSIS uninstaller deletes `.artifact` and `*-windows-amd64-portable.exe` from each leftover `wails-update-*` directory inside the Windows install folder, then removes the directory with a non-recursive `RMDir`. Any other entry (file or subfolder) keeps that directory, and so keeps the install folder (see [platform.md](platform.md#updater-leftovers)). While the app stays installed, the startup sweep removes such a folder at a later start, once it is flat and unchanged for 24 hours (rules above).

## Known limitations

- Update authenticity is not checked. The update is integrity-checked against a sums file from the same release.
- No rollback for a new version that starts and then crashes. Reinstall the same or a newer version. An older build refuses a database stamped with a newer schema (`internal/db`), so do not reinstall an older version after a newer one has run.
- A machine-wide (HKLM) install is not supported by the updater, so its Apps & Features entry is never changed.
- The first release after v1.2.1 names the portable exe without a version (`steam-achievement-tracker-windows-amd64-portable.exe`), so a self-updated file keeps a stable name. A portable file that already carries a version in its name (`...-<old>-windows-amd64-portable.exe`) keeps that name after an update, because the updater never renames the file. Rename it by hand once (close the app first; renaming breaks taskbar pins and shortcuts that point to the old name). Data, settings and the API key are not tied to the file name.
- Unauthenticated GitHub rate limit: 60 requests per hour per IP. A check costs up to two requests. An Install costs a re-check (up to two) plus the download (one).
- Installs into a folder that is not writable (for example a machine-wide Windows install) fail at download with the not-writable sentence and the release link. There is no probe before the download.
- Linux non-AppImage installs, including packages in `/usr/local/bin`, are notify-only.
- Builds without a valid stamp have no updater.
- Versions before 1.1.0 have no updater; replace them once by hand.
- Only stable releases are offered.

## Manual checks

Some behavior depends on a real host, a real release, or timing. These are checked by hand, on a local build ([Local test build](#local-test-build)) stamped with an old version against the current release:

1. Windows portable and per-user installed: check, Install with progress, Cancel, verify, Restart now, and the new version runs.
2. A failed restart: build with `HelperReadyTimeout: time.Nanosecond` (temporary, not committed). Restart fails and the app keeps running. The phase returns to `ready`, the staged folder stays, and Restart now is accepted again (it fails again with the same build). Reverting the change needs a rebuild, which loses the staged update, so download it again with Install.
3. Closing the app while `ready` removes the staged folder. "Restart now" does not remove it.
4. Cancel during the download ends in `available`. A second Install click during the download is refused at once. A late Install click after `ready` is refused and the staged payload is kept.
5. A hanging download (for example a throttled network) can be cancelled by the user.
6. A folder that is not writable shows the not-writable sentence and the release link, and the app keeps working.
7. A restart while a sync runs: the database is intact after restart. Shutdown finishes within the bound also with a sync that never ends.
8. AppImage swap and relaunch, and the rollback when the new version cannot start.
9. Stable and pre-release tags: a `-rc` release is never offered.
10. Per-user installed copy: after the new version's first start, Apps & Features shows it; `<exe>.old.*` is gone about 5 s later.

Linux Install checks need the AppImage build above. The matcher checks release asset names, not the local file name.

## Tests

The test files and what they cover are listed in [testing.md](testing.md#internalupdate).
