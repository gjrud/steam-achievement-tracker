# In-app updater

This document describes the in-app updater: how it finds a release, how a user-approved update is downloaded, verified, staged and applied, and what can go wrong. The user-facing rules are in the [user guide](../README.md#updates). The Go code is `internal/update` (the service, the GitHub provider setup, the phase rules, the errors), `internal/version` (the version stamp), and `app_update.go` (the bound methods). The frontend is covered in [frontend.md](frontend.md#update-banner-and-settings).

The updater wraps the framework updater from Wails v3.1.0 (`pkg/updater`, provider `github`). The service never uses the framework's `CheckAndInstall` or `CheckInterval`, because they download without asking. The framework's own UI is off (`updater.WindowNone`). The frontend never subscribes to the framework's `wails:updater:*` events: Go listens to three of them, and the frontend hears only the app's own `update:changed` event.

## Install targets

| Install | Behavior |
| --- | --- |
| Windows portable exe | Updated in place. |
| Windows per-user installed exe (`%LocalAppData%\Programs\Steam Achievement Tracker\`) | Updated in place. "Apps & Features" keeps the old version (see [Known limitations](#known-limitations)). |
| Linux AppImage | Updated in place, when the process runs from that AppImage. |
| Linux deb, rpm, Arch package, or any other Linux copy | Notify only. The banner shows the version and the release link. Install is never offered. |
| Other platforms | The check fails: `matchAsset` finds no asset, so the provider returns an error. No supported build reaches this. |

A build without a valid version stamp (dev builds, Docker builds, untagged local builds) has no updater: no request is made and no update UI shows. See [Version stamp](#version-stamp).

## Version stamp

- `internal/version` declares `tag`, a package-level string with no initializer. The production build tasks set it with `-ldflags -X`:

  ```text
  -X github.com/gjrud/steam-achievement-tracker/internal/version.tag=sat-version:<version>;
  ```

  The `-X` is part of the single `-ldflags` string in the production branch of `build/linux/Taskfile.yml` (`build:native`) and `build/windows/Taskfile.yml` (`build`). The DEV branch has no `-X`. `<version>` is `PKG_VERSION` from the root `Taskfile.yml`, which has no leading `v`.
- The Linux task adds the flag only when `PKG_VERSION` is set. The Windows build fails its precondition when `PKG_VERSION` is empty (non-DEV builds).
- `version.Current()` reads the stamp. `ParseStamp` needs the `sat-version:` prefix and the `;` suffix, and then `Valid` must accept the rest. `Valid` requires the shape `X.Y.Z` with an optional `-pre` part and no `v` prefix (checked with a regular expression, then `semver.IsValid("v"+v)`). Anything else leaves the updater unavailable. Empty, unlabeled, unterminated, `v`-prefixed and partial versions such as `1.2` are all invalid.
- `IsPrerelease` is true when the version has a `-`. Pre-releases are never offered (see [Check](#check)).
- The stamp is a plain string in the binary. Without `-trimpath`, Go records the `-ldflags` string in the build info, and that string contains the marker, so a wrong `-X` path would still pass the release's `grep -aqF`. `-trimpath` leaves it out, so a match proves the variable was set (see [build-and-release.md](build-and-release.md#version-stamping-and-marker-checks)).

To test the updater on a local build, stamp it with an old version: `VERSION=0.0.1` before `wails3 task windows:build` on Windows. On Linux, a plain `wails3 build` binary is notify-only (never Install), and its check fails until the first release that ships the updater (see [Acceptance checks](#acceptance-checks-without-automated-tests)). A stamp equal to or newer than the latest release shows "up to date".

## Wiring

- `main.go` creates the service after `application.New` and before the window: `update.New(update.Deps{...})`, then `backend.setUpdater`, then `updates.Init()`. The dependencies are the framework updater (`app.Updater`), the app lifetime context, `app.Event.On` (for the framework events Go listens to), the settings reader (`backend.updateChecksEnabled`, which the service caches), and `OnChanged`, which emits `update:changed`.
- `Init` reads the stamp. With a valid stamp it builds one stock GitHub provider and calls the framework `Updater.Init`. Its configuration is `CurrentVersion` (the stamp), `Providers` (the provider), and `Window: WindowNone`. It has no `CheckInterval` and no `PublicKey`. If `Init` fails, the service is unavailable and logs `update: unavailable: other` once, after logging is up. No secret or key is involved.
- `App.ServiceStartup` runs `StartupCheck` only after `ensureInitialized` succeeds. When init fails, the startup check is skipped (there is no database to read the toggle from), and the update UI does not show on the setup or init-error screens.
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

The GitHub provider requests `GET /repos/{owner}/{repo}/releases/latest`. A 404 counts as "no release". The provider also fetches `SHA256SUMS` when a newer release exists. A check therefore costs up to two API requests. The download goes to `api.github.com/repos/{owner}/{repo}/releases/assets/{id}` with `Accept: application/octet-stream`, and GitHub redirects it to its storage host. The framework strips `Authorization` on a host change; the app sends no token.

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

There is no total timeout, so a large asset on a slow link is not cut off. Cancel and app shutdown end a download. A check has its own timeout (`checkTimeout`, 60 s) that covers both of its API requests. The framework's default client has a 30 s total timeout that would fail large downloads, so the app passes its own client.

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
| `downloading`, `verifying`, `installing` | download fails | `error` | Download, verification or folder sentence. |
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
3. The phase must allow it (`available` or `error`). The phase moves to `downloading`, the install context is created, and the cancel flag is reset.
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
| Windows amd64 | the asset whose name ends with `-windows-amd64-portable.exe` (for example `steam-achievement-tracker-<version>-windows-amd64-portable.exe`). The installer and any package are never chosen. |
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
| `check-failed` | check or re-check fails (network, API, bad version) | `update: check failed: check-failed` (manual), `update: startup check failed: check-failed` (startup), or `update: install: check-failed` (re-check during Install) | "Could not check for updates. Try again later." |
| `cancelled` | the user cancelled an install, or the app shut down during one | `update: install: cancelled` (a category only, not a failure) | no sentence |
| `not-writable` | the staging step returns a permission error (`fs.ErrPermission`) | `update: install: not-writable` | "The app folder is not writable, so the update cannot be installed here. Download the new version from the release page." |
| `download-failed` | download, checksum or staging fails for any other reason | `update: install: download-failed` | "The update could not be downloaded or verified. Try again." |
| `restart-failed` | `Restart` returns an error | `update: restart: restart-failed` | "The app could not restart into the update. The update is still ready, so you can try Restart again." |
| `other` | anything else, including a failed `Updater.Init` and the staged-folder removal at shutdown | `update: unavailable: other`, `update: remove staged update: other` | "The update failed. Try again later." |

Precedence for a download or install step: our own cancel flag or a cancelled context wins (`cancelled`); then a permission error (`not-writable`); then `download-failed`. The framework's error text is never read beyond the `fs.ErrPermission` check. A cancel after `DownloadAndInstall` returned nil, or during verification and installation (which the framework does not check the context in), ends in `ready`.

Other sentences the update service returns (not categories): unavailable ("Updates are not available in this build."), busy, ready-first, not-ready, check-first.

The snapshot carries the current error sentence in `error`. The frontend also shows the sentence of a rejected call. A failed startup check shows nothing.

Settings read errors log `update: settings unreadable: update checks off` once per load. The app also logs `update: unavailable: other` when `Init` fails.

Log lines never contain a full path. Framework messages such as `ErrHelperNotReady` text, which can carry paths, are not logged or returned.

## Shutdown and restart safety

`App.ServiceShutdown` (`app.go`) runs in this order:

1. Cancels the app lifetime context. Checks and installs derive from it, so they stop.
2. `Service.Shutdown`: marks the service stopped (so no more `update:changed` events), cancels a running install, unsubscribes from framework events, and, if the phase is `ready`, moves it to `idle`, drops the latest release, and removes the staged folder. Nothing is removed while `restarting`, because the helper owns that folder.
3. Cancels any running sync.
4. Waits for syncs for at most `syncShutdownWait` (10 s), then logs a warning and continues. The wait is bounded so that a restart (which the helper gives 30 s to exit) cannot time out and relaunch the old version.
5. Closes the database.

`Shutdown` never waits on an operation. The staged folder is removed by `fsretry.RemoveAll` on `filepath.Dir(Updater.DownloadedPath())`, and only when the directory name starts with `wails-update-`.

Consequences:

- A restart cancels a running sync. The sync normally ends in time and its run is recorded as `failed` (see [architecture.md](architecture.md#shutdown)). If it outlives the 10 s bound, the database closes first, so its run row stays `running`. The next start marks it `canceled` (shown as "Sync canceled"). The "Restart now" banner says so.
- A restart does not lose data: the database closes after the bounded wait, and the new process opens it normally.
- Closing the app without applying a staged update removes the staged folder, so no full-size copy of the update stays next to the exe or the AppImage.

## Staging and leftover files

The framework creates a `wails-update-*` directory next to the target (the exe or the AppImage). Several files can be left behind:

| Path | Left when |
| --- | --- |
| `<target>.bak` (next to the AppImage on Linux, next to the exe on Windows) | the helper's copy of the previous version. The successful path deletes it, and a restore renames it back. It stays when the helper stops mid-update or a restore fails. |
| `<exe>.old.<UnixNano>` (Windows, next to the exe) | every successful Windows update while the old file is still in use. A later update's helper removes them when Windows allows. |
| a `wails-update-*` directory next to the target | a helper failure, a crash or a kill during an update. A staged update that was never applied is removed at shutdown. |
| `wails-update-<pid>.log` in the OS temp directory | every update attempt that starts the helper. It holds full paths, usually including the OS user name, and the framework never removes it. |

There is no startup sweep. The NSIS uninstaller deletes `<exe>.old.*` and `<exe>.bak` in the install folder before its non-recursive `RMDir` (see [platform.md](platform.md#updater-leftovers)). A leftover `wails-update-*` directory inside the install folder would make that `RMDir` fail, and the install folder stays.

## Known limitations

- Update authenticity is not checked. The update is integrity-checked against a sums file from the same release.
- Only part of the swap and relaunch can be checked before the first release that ships the updater (see [Acceptance checks](#acceptance-checks-without-automated-tests)). The rest, including the AppImage, is tested by that first real update. A bug there reaches users, who would reinstall by hand.
- No rollback for a new version that starts and then crashes. Reinstall the same or a newer version. An older build refuses a database stamped with a newer schema (`internal/db`), so do not reinstall an older version after a newer one has run.
- Windows installed copies show the old version in "Apps & Features" after an in-app update. The registry entry is not changed.
- Portable exes keep their versioned download name (`...-<old>-windows-amd64-portable.exe`), so their name shows the old version. The AppImage asset name has no version, so it is unaffected unless the user renamed the file.
- Unauthenticated GitHub rate limit: 60 requests per hour per IP. A check costs up to two requests. An Install costs a re-check (up to two) plus the download (one).
- Installs into a folder that is not writable (for example a machine-wide Windows install) fail at download with the not-writable sentence and the release link. There is no probe before the download.
- Linux non-AppImage installs, including packages in `/usr/local/bin`, are notify-only.
- Builds without a valid stamp have no updater.
- Versions released before the updater shipped cannot update themselves. They must be replaced once by hand.
- Only stable releases are offered.

## Acceptance checks without automated tests

Some behavior depends on a real host, a real release, or timing. These are checked by hand, on a local build stamped with an old version (for example `VERSION=0.0.1`) against the current release:

1. Windows portable and per-user installed: check, Install with progress, Cancel, verify, Restart now, and the new version runs.
2. A failed restart: build with `HelperReadyTimeout: time.Nanosecond` (temporary, not committed). Restart fails and the app keeps running. The phase returns to `ready`, the staged folder stays, and Restart now is accepted again (it fails again with the same build). Reverting the change needs a rebuild, which loses the staged update, so download it again with Install.
3. Closing the app while `ready` removes the staged folder. "Restart now" does not remove it.
4. Cancel during the download ends in `available`. A second Install click during the download is refused at once. A late Install click after `ready` is refused and the staged payload is kept.
5. A hanging download (for example a throttled network) can be cancelled by the user.
6. A folder that is not writable shows the not-writable sentence and the release link, and the app keeps working.
7. A restart while a sync runs: the database is intact after restart. Shutdown finishes within the bound also with a sync that never ends.
8. On the first release that ships the updater: the AppImage swap and relaunch, and the rollback when the new version cannot start.
9. Stable and pre-release tags: a `-rc` release is never offered.

Every Install-dependent check (items 1 to 7) is Windows-only until the first release that ships the updater. The current release has no `steam-achievement-tracker-linux-x86_64.AppImage`, so on Linux the check fails with "Could not check for updates. Try again later." until a release renames the AppImage. After that release, Linux can be tested with `VERSION=0.0.1 wails3 package`, then running `bin/steam-achievement-tracker-x86_64.AppImage`. The matcher checks release asset names, not the local file name. Item 8 covers the AppImage swap and the rollback.

Code-review checks from the issue, not run by hand: AC 7, a failed `Updater.Init` leaves the updater unavailable and is logged once (`TestInitFailureLogsOnceAfterLoggingIsUp` covers the logging); AC 9, the bindings never return the raw updater error, and no log line holds a full path.

## Tests

| Test file | Covers |
| --- | --- |
| `internal/version/version_test.go` | Stamp parsing, valid and invalid versions, pre-release detection. |
| `internal/update/match_test.go` | `matchAsset` against the real release asset names (portable vs setup, packages, source tarball, `SHA256SUMS`); AppImage target resolution, including an inherited `APPIMAGE`; host and install gate rules (nil verification, wrong digest length); release page URL. |
| `internal/update/phase_test.go` | Phase guards, cancel rule, framework event guard, refusal sentences. |
| `internal/update/errors_test.go` | Category precedence and cause-less sentences. |
| `internal/update/client_test.go` | Provider against `httptest` through `githubConfig(base, client)`: the AppImage and portable selection with digest verification, missing checksum (notify-only), checksum fetch failure, no release, not newer, no asset for the platform. Also the provider config and the HTTP client without a total timeout. |
| `internal/update/service_test.go` | The service with a fake environment (`fakeEnv`): unavailable without stamp (no log), refusals never reach the framework, pre-release screening (`TestScreenRelease`), the startup toggle read once, settings unreadable, init failure logged once, cancel and shutdown, concurrency with state. |
| `internal/update/staging_test.go` | The staged-folder path (`stagingDirFor`): only `wails-update-*` directories are removed. |
| `app_update_test.go` | Bound-method refusals without an updater give one sentence; the toggle persists; `GetUpdateState` reads no settings; the shutdown sync bound; a quiet shutdown. |

Frontend: `App.update.test.js`, `lib/update.test.js`, `lib/components/UpdateBanner.test.js` and `UpdateSettings.test.js` use the mocked bindings and `frontend/src/test/update-fixtures.js`. No test calls GitHub.
