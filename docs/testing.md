# Testing

Test strategy, layout, helpers and commands. The user guide ([../README.md](../README.md)) has no testing section.
This file lists what each suite covers and how to add tests.

Strategy in one paragraph: Go tests drive the package APIs against real SQLite databases and a
real on-disk image cache, and fake only the Steam HTTP endpoints. Isolation comes from temp
directories, not from mocks of the filesystem. Frontend tests run in jsdom against a mocked
Wails binding layer and a mocked runtime; drift checks keep those mocks in step with the Go
backend. Mutation testing (gremlins) is a local diagnostic, not a gate.

## Commands

| Goal | Command | Notes |
| --- | --- | --- |
| Frontend tests | `cd frontend && npm test` | `vitest run`, jsdom. No build needed. |
| Backend tests | `go test ./...` | The root package embeds `frontend/dist`, so create it first (see below). |
| Race detector | `go test -race ./...` | Some tests fail only under `-race` (see below). |
| Vet | `go vet ./...` | CI runs it on both legs. |
| One package | `go test ./internal/repository -run TestName -v` | Standard `go test` filtering. |
| Benchmark | `go test ./internal/repository -run '^$' -bench BenchmarkDashboard -benchmem` | Diagnostic only. |
| Mutation | `gremlins unleash ./internal/<pkg> -o tmp/gremlins/<pkg>.json` | Run per package. Gremlins is an external tool and is not in `go.mod`. |

Creating `frontend/dist` for a clean tree:

```sh
cd frontend && npm ci && npm run build && cd ..
go test ./...
```

Only the root package (`package main`, `main.go` embeds `all:frontend/dist`) needs `dist` to
exist. `go test ./internal/...` works on a clean tree.

The Linux CI leg also runs `wails3 generate bindings` before the frontend tests. The frontend
mock-drift checks need the generated files (see below).

## Go test layout

No Go test calls `t.Parallel`. Tests that change process environment (XDG variables, keyring seams,
package variables) rely on that. Do not add `t.Parallel` to them.

### Root package (`package main`)

| File | Covers |
| --- | --- |
| `app_test.go` | The Wails-bound `App` methods: `GetAppState` (key and profile presence, the key never returned), init failures, sync single-flight guards and lifecycle, baseline and run tracking, profile save and validation (SteamID, vanity name, player not found, HTTP errors), clear-data combinations of profile data and key, game actions (disable, enable, previously completed, DLC toggle), cached image removal, `SaveAPIKey` and `RetryAPIKey`, cancel-and-wait. |
| `app_events_test.go` | The `sync:changed` event sequence: events at start, after each write and at the end, and the final event reporting the idle state. An early exit (no API key) still emits the start and end events, `[true false]` (`TestStartSyncEarlyExitEmitsStartAndEnd`). Also an emitter that is absent. |
| `covers_test.go` | `CoverService` routing: prefix stripping, rejection of non-positive app ids, paths without a leading slash, invalid cover routes, serving of each cover kind, and a race test that reads app paths while `ensureInitialized` runs. |
| `service_lifecycle_test.go` | `ServiceStartup` init failure (returns nil, reports the error), `ServiceShutdown` cancels the lifetime context, and `startSync` after shutdown returns `errShuttingDown`. A zero-value `App` has a background context that `ServiceShutdown` does not cancel (`TestAppLiteralContextIsBackground`). |
| `wailslog_test.go` | Forwarding of Wails' `slog` output to the app log, and that Debug is disabled. |

Helpers in this package: `openTestApp` (isolates directories, registers `logging.Fallback` and
service shutdown in cleanup, and runs `ensureInitialized`), `newTestApp` (mocks the keyring, then
calls `openTestApp`), `newTestAppWithKey` (same, with a key already stored), `newSteamStub` (an
`httptest` server that records requests). Tests also set the `steamBaseURL` field of `App` (tests only) to point
sync and validation at the stub.

### internal/appdata

`appdata_test.go`: path layout from `Isolate`, no directory creation by `Resolve`, `0700` creation
and tightening by `Ensure`, idempotence, and errors when a path component is a file.
`appdatatest` itself has no tests; it is a helper.

### internal/config

No tests. It is constants only.

### internal/db

| File | Covers |
| --- | --- |
| `db_test.go` | Fresh schema and defaults, no-op backup skipping, backup of an unversioned database with tables, backup contents match the original. |
| `db_gap_test.go` | Migration failure rolls back to the previous schema, a newer schema is rejected, a failure while recording the version rolls back, a failed migration releases the connection, and file modes of the database and backups. |
| `dsn_test.go` | SQLite URI building: escaping, query parameters kept, and a path with special characters opens correctly. |

### internal/fsretry

`fsretry_test.go` (all platforms): retry counts, the error returned after the last attempt,
non-transient errors returning immediately, success on the first try, and that
`Rename`, `Remove` and `RemoveAll` match the `os` results. `transient_windows_test.go` is
`//go:build windows`; it runs only on the Windows CI leg. It checks which Win32 codes count as transient.

### internal/images

| File | Covers |
| --- | --- |
| `images_test.go` | Cover refresh chains (library capsule, hero, main, header), fallbacks and mirror hosts, stored-file reuse, stale cover replacement with deletion of old files, fresh-state skipping of the network, content-type rejection, download size limits. Uses `routeTransport` and `statusTransport` (custom `http.RoundTripper`) and `testCache`. |
| `cover_guard_test.go` | Cache constants, client construction (paths and timeout), guards for non-positive app ids (no requests), URL building, content-type and byte-limit boundaries, and removal of other covers. |
| `store_items_test.go` | Steam store asset lookups: batch boundaries, partial results on a batch error, retries of retryable statuses and transport errors up to four attempts in total, no retry for other statuses, cancellation during backoff, and id de-duplication. Uses `storeScriptTransport`. |

### internal/keyring

`keyring_test.go` uses go-keyring's in-memory mock (`zk.MockInit`, `zk.MockInitWithError`). It covers
round-trip with trimming, overwrite, invalid input rejected without storing, read-back mismatch
(`ErrNotStored`), backend error pass-through, the service and user names, and that no error text
contains the key. The `zkSet`, `zkGet` and `zkDelete` package variables are swapped with
`t.Cleanup` restores.

### internal/logging

`logging_test.go`: private file and directory modes (POSIX only), stderr fallback after `Fallback`,
a race between concurrent `Writer().Write` and `Init`/`Fallback`, and closing of the previous logger
on `Init` and `Fallback` (needed for Windows directory removal). Tests call `Fallback` in cleanup
before removing a temp log directory.

### internal/repository

| File | Covers |
| --- | --- |
| `repository_test.go` | Clear-profile scoping and orphaned cover ids, DLC cover precedence, sync warnings and preserved snapshots, tags (computed, cached, cleared when disabled), previously completed flag, suggestion ordering, completed and suggestion splits, set naming, summaries, vetted counts, disabled and not-owned rules, and game-card cover URL cache busting. |
| `repository_gap_test.go` | Stale entry pruning, per-profile summaries, tag scope, card tag order, null suggestion scores, sort table, flag independence, profile updates, sync progress and cancel, and cover saving edge cases. |
| `dashboard_test.go` | Dashboard list contents against card lists, error propagation from both queries, and `BenchmarkDashboard`. |
| `profiles_active_test.go` | Active profile selection after deactivate, clear and save. |
| `settings_test.go` | Library settings defaults, per-profile isolation and cleanup. |
| `sets_test.go` | `HasSetData`. |

The benchmark seeds 250 base apps. Every second app has a DLC set and every fifth has an extra
set, giving 425 entries. The seed is fixed so runs compare the same rows.

### internal/steam

`client_test.go` drives the Steam client through `httptest` servers (`newTestServer` returns a
client and a close function). It covers owned games (private profiles, purchased and vetted
lists, the request parameters), achievements (schema, player and global percentages, set parsing
from the fixtures in `testdata/`), EResult and HTTP status errors, transient status retries,
redaction of the API key in transport and status errors, malformed JSON, profile resolution, and
the retry schedule. Fixtures in `testdata/` are sample Steam responses for PWS2 (game and user
achievements, DLC ownership), Portal 2, TF2, and a game without a schema.

Retry tests call `client.SetRetryBackoff(func(int) time.Duration { return 0 })` to avoid real
sleeps. The same method with `nil` restores the default delay, and a test checks the default
bounds per attempt. Cancellation during backoff is tested with a long delay.

### internal/syncer

| File | Covers |
| --- | --- |
| `syncer_test.go` | Full sync flows against `fakeSteamServer` (built with `newFakeSteamServer`). The fake serves owned games, game and user achievements, and store items. A `redirectTransport` sends Steam URLs to it. Covers partial failure, recovery, cancellation, the API-key redaction in warnings, disabled and no-longer-owned games, concurrency limits, set entries, DLC ownership changes, covers (base and DLC, stale and fresh, store fallback), and the legacy fallback rules. |
| `syncer_changed_test.go` | The `Service` change notification (`changed`) fires at start, progress and finish, also when writes fail, and not when the run cannot start. It is nil-safe. |
| `syncer_entries_test.go` | `buildSetEntries` rules: completion boundary, missing average, synthesized sets in group order. |
| `syncer_faults_test.go` | Database write failures are logged and the sync continues: upserts, disable-missing, warnings, finish, progress, profile touch, and cover refresh. |
| `syncer_retry_test.go` | Owned-games failure fails the run and keeps existing data; retryable game status exhaustion gives a partial run; retries that succeed. |

`openTestRepository` opens a real repository under isolated paths. Fixtures (`storeFixture`,
`groupFixture`, `achFixture`, `gameFailure`) are built inline.

## Isolation helpers

### appdatatest (`internal/appdata/appdatatest`)

- `Isolate(t)`: points `XDG_DATA_HOME`, `XDG_CACHE_HOME` and `XDG_STATE_HOME` at subdirectories of
  `t.TempDir()`, reloads `xdg`, and restores everything at cleanup.
- `Block(t)`: points them at a regular file, so `appdata.Ensure` fails. Returns the path of the
  blocking file. Used to test the startup-error path.

Never set `HOME` for app paths. Windows does not use it for these paths, and tests should pass on
both platforms.

### Keyring mock

Every test that touches the key calls `zk.MockInit()` (go-keyring's in-memory provider) first.
`zk.MockInitWithError(err)` makes every call fail. Keyring seams are package variables that tests
swap and restore in cleanup.

### Steam stubs

- `httptest.NewServer` plus a handler, for client and app tests (`newSteamStub`, `newTestServer`,
  and the app events test).
- Custom `http.RoundTripper` types (`routeTransport`, `statusTransport`, `storeScriptTransport`,
  `redirectTransport`) for image and store tests, where the code builds absolute URLs.
- `fakeSteamServer` for full sync flows (in `syncer_test.go`).
- `steam.Client.SetRetryBackoff` replaces the retry delay, so retry tests run without sleeping.
- `App.steamBaseURL` (tests only) redirects the app's sync and validation clients to a stub.

### Logging

Any test that initializes logging (`logging.Init` through `ensureInitialized`) registers
`t.Cleanup(logging.Fallback)` before the temp directory is removed. Cleanups run last-in
first-out, so `Fallback` must be registered after the `TempDir` or `Isolate` call.

## Race detector

`go test -race` is part of the CI race check (the Linux and Windows legs both run it). Some regressions appear only
under `-race`, and the tests below are written to catch them:

- `TestCoverServiceReadsPathsSafelyWhileInitializing` reads the app paths while
  `ensureInitialized` writes them. Its comment says that without the race detector the
  unsynchronized read usually passes silently.
- `TestWriteRacingInitAndFallback` (logging) hammers `Writer` while `Init` and `Fallback` swap the
  logger. It mainly guards against a deadlock (60 s timeout) and against writes reaching a released
  file. `-race` adds detection of data races in the logger swap.
- Sync cancellation: the cancellation paths are exercised in `internal/syncer/syncer_test.go` by
  `TestFullSyncCancellationPersistsFailedRun` and `TestFullSyncCancelAfterAllGamesDispatchedFailsRun`, which cancel with
  four workers in flight. The App-side sync state is covered by `app_test.go`
  `TestSyncNowStartsSyncAndRejectsSecondStart` and by `app_events_test.go`
  `TestSyncEmitsChangedEventsEndingAfterFlagClears`.

## Benchmark

`BenchmarkDashboard` (`internal/repository/dashboard_test.go`) times `Repository.Dashboard` on
the synthetic 425-entry dataset. It logs the entry count and reports allocations. It is a local
diagnostic. It has no threshold and CI does not run it.

## Mutation testing

`.gremlins.yaml` settings:

- `workers: 2`
- `timeout-coefficient: 20` (the suite takes about 0.3 s per package; the default timeouts give
  false timeouts under load)
- `exclude-files`: `^frontend/`, `bindings`, `^main\.go$`

Run it per package, because whole-module runs report false timeouts. Write the report to
`tmp/gremlins/<pkg>.json` (`tmp/` is git-ignored). A run where more than about 5% of mutants time out should be rerun
with a higher coefficient before it is trusted. Gremlins results are not a gate and no coverage threshold exists.

## Frontend tests

Config: `vite.config.js` sets `test.environment: 'jsdom'` and `test.setupFiles:
['./src/test/setup.js']`. In test mode, two aliases replace the runtime and the generated bindings
(`wails-app-mock.js` and `wails-runtime-mock.js`). See [frontend.md](frontend.md) for the mock
APIs.

Suites:

| File | Covers |
| --- | --- |
| `src/App.test.js` | Setup states, API key flows, profile validation and save, dashboard views and tag filters, search, card covers, Wails call failures in each action, previously completed flag, keyboard shortcuts, clear-data dialog, library settings. |
| `src/App.sync.test.js` | Event-driven refresh with fake timers: one read per event, coalesced bursts, follow-up reads, reads overtaken by actions, the 2 s retry while a sync runs, retry cancellation on success and unmount, unsubscribe on unmount. |
| `src/App.incremental.test.js` | Incremental grid with a controllable `IntersectionObserver` stub (`stubIntersectionObserver`): batch size 48, resets, spacer height, viewport-sized growth, focus handoff without scrolling. |
| `src/lib/format.test.js` | Formatting helpers. |
| `src/lib/sync.test.js` | `syncBand` for every run status and the announce text. |
| `src/lib/tag.test.js` | Tag derivation, order, tones, labels, and count consistency between `tagCountsFromGames` and the per-tag reference. |
| `src/lib/components/*.test.js` | `ApiKeyForm`, `ClearDataConfirmModal`, `ProfileCard` (fake timers for the 30 s refresh), `SettingsView`, `SummaryStats`. |
| `src/test/wails-bindings.test.js` | Drift checks (below). |

Shared helpers (`src/test/app-fixtures.js`): state builders (`baseState`, `dashboardState`,
`game`, `manyGames`, `syncRun`, `syncingState`, `finishedState`, `missingKeyState`,
`profileSetupState`), `emitSyncChanged` (fires `sync:changed` through the runtime mock), and
`settle`/`advance` for fake timers.

Rules when writing frontend tests:

- `setup.js` resets runtime event listeners and the Browser mock after each test. Tests that
  change backend handlers must call `__resetWailsMocks()` in `afterEach` (the App test files do).
- Under fake timers, use `advance()` and `settle()`. `waitFor` and `userEvent` rely on timers
  that the fake clock never fires; drive those tests with `fireEvent`. `app-fixtures.js` says the
  same.

### Drift checks (`wails-bindings.test.js`)

The checks keep the hand-written mock in step with the Go backend:

1. Every mock export has a generated binding with the same argument count. The mock must spell
   out its parameters (no rest parameters).
2. Every generated binding has a mock export.
3. Every name `App.svelte` imports from the bindings exists in the generated file.
4. `syncChangedEvent` is the same string in `app.go` and `App.svelte`.

The generated-bindings tests need `frontend/bindings`, which is git-ignored. With `CI` set to `1`
or `true` (GitHub Actions sets it), a missing file fails the suite. Locally, the bindings tests are
skipped when the file is absent. Generate it with `wails3 generate bindings` from the repository
root.

After changing an exported `App` method: regenerate the bindings, then update
`frontend/src/test/wails-app-mock.js` (explicit parameters, same order, no rest arguments).

## CI behavior

Defined in `.github/workflows/ci.yml` (see [build-and-release.md](build-and-release.md)).

- Linux leg: Node 22 and `npm ci`, Wails CLI, placeholder `frontend/dist`, `wails3 generate
  bindings`, `npm run build`, `npm test` (so the drift checks run with bindings present), `go vet
  ./...`, `go test -race ./...`.
- Windows leg: placeholder `frontend/dist`, `CGO_ENABLED=1`, `go vet ./...`, `go test -race ./...`,
  and a production build smoke test. It does not run frontend tests and does not generate
  bindings. The root package still compiles because of the placeholder.
- The required status check is the `test` job, which passes only when both legs succeed.
- The release workflow reuses CI (`workflow_call`), so a release runs the same tests.

## Adding tests

Go:

- Use `appdatatest.Isolate(t)` for any test that uses the repository, the cover cache, or logs.
  Call `t.Cleanup(logging.Fallback)` after isolation if `Init` runs.
- For keyring use, call `zk.MockInit()` first. Swap seams only through the helpers in the test
  file, and do not call `t.Parallel`.
- For Steam, use `newTestServer` or `newSteamStub` and call `SetRetryBackoff` to skip sleeps.
  Never let a test reach the real Steam API.
- For repository behavior, use `testRepositoryPath` (in the `internal/repository` tests) or
  `openTestRepository` (in the `internal/syncer` tests) so the database is real and temporary.
- Keep the key out of expected error text. Existing tests check that a key never appears in an
  error (`TestSaveAPIKeyErrorsNeverContainTheKey`, `TestTransportErrorDoesNotLeakAPIKey`); follow
  that pattern for any new error path that touches the key.
- Put cross-platform file mode checks behind `runtime.GOOS != "windows"`.

Frontend:

- Add pure logic to `src/lib/*.js` with a matching `*.test.js`. Test components with
  `@testing-library/svelte`.
- For a new backend call, add the export to `wails-app-mock.js` with explicit parameters, add a
  handler default that rejects, and add a case in `App.test.js` or a component test.
- For a new `sync:changed` behavior, use `emitSyncChanged()` and the fake-timer helpers.
- For grid changes, use the `INITIAL`/`STEP` exports and the `IntersectionObserver` stub in
  `App.incremental.test.js`.

Before opening a pull request, run `npm test` in `frontend/`, and `go vet ./...` and
`go test -race ./...` with `dist` present.
