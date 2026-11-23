# Developer documentation

These docs are for developers: readers who want to understand the app's internals, change the code, build the app, or cut a release. Nothing here is needed to use the app; end users only need the [root README](../README.md). Information about the code, build, or release belongs here.

## Index

- [architecture.md](architecture.md): application layers; startup and shutdown order; `sync:changed` event flow; binding flow; error and secret handling; concurrency; and package map.
- [app-service.md](app-service.md): root package files (`main.go`, `window.go`, `window_csd_gtk4.go`, `window_csd_other.go`, `app.go`, `app_errors.go`, `app_update.go`, `covers.go`, `wailslog.go`); every bound method; lifecycle hooks; profile validation; state assembly; remove-profile behavior; cover handler; and frontend binding contract.
- [updater.md](updater.md): the in-app updater: install targets, version stamp, GitHub provider and network use, phases, consent and the install gate, errors and log categories, trust model, restart and shutdown safety, leftover files, known limitations, and manual checks.
- [syncer.md](syncer.md): the full sync run: owned games, the per-game worker pool, achievement sets, progress, and final status.
- [steam-client.md](steam-client.md): the Steam Web API client: endpoints, retries, error redaction, and profile resolution.
- [database.md](database.md): SQLite opening, WAL mode, migrations, pre-migration backups, and the schema.
- [repository.md](repository.md): the SQL access layer: profiles, games, sets, flags, tags, sync runs, settings, and dashboard assembly.
- [covers.md](covers.md): cover sources, the fallback order, the on-disk cache layout, and refresh rules.
- [platform.md](platform.md): per-OS paths, the system keyring, logging and rotation, and Windows specifics.
- [frontend.md](frontend.md): the Svelte 5 UI, its state and refresh logic, and the component layout.
- [build-and-release.md](build-and-release.md): prerequisites, local build commands, Wails and Task build files, packaging, CI, the release workflow, and the procedure for cutting a release.
- [testing.md](testing.md): the Go and frontend test suites, test isolation helpers, and the diagnostic tools.

## Recommended reading order

1. [../README.md](../README.md): what the app does and how users use it, including the sync and dashboard behavior. Skim it first.
2. [architecture.md](architecture.md): the overall design and the role of each package.
3. [app-service.md](app-service.md): the Go entry points the frontend calls.
4. [database.md](database.md) and [repository.md](repository.md): where state lives and how it is read and written.
5. [syncer.md](syncer.md) and [steam-client.md](steam-client.md): how data gets in.
6. [covers.md](covers.md), [platform.md](platform.md) and [updater.md](updater.md): images, paths, keyring, logging, and the in-app updater.
7. [frontend.md](frontend.md): how the UI consumes the state.
8. [testing.md](testing.md) and [build-and-release.md](build-and-release.md): how to verify and ship a change.

## Conventions

- If a doc and the code disagree, the code is authoritative; update the doc in the same change.
- The root [README.md](../README.md) is the only user-facing document: overview, requirements, install, usage, updates, data and privacy, troubleshooting. Nothing under `docs/` is for end users, and the README has no developer internals beyond a short development overview. Developer docs link to the README for user-facing behavior instead of repeating it. When a change alters what users see, update the README and the matching doc here in the same change.
- Function, type, file and constant names are cited without line numbers, because line numbers drift.
- Generated files (`frontend/bindings/`, `frontend/dist/`) are not documented line by line. Regenerate them rather than editing them.
- Secrets never appear in examples. The Steam Web API key is written as `<key>` or described by name only.
