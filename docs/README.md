# Developer documentation

These documents are for people who change the code. The user guide, which covers installing, using and releasing the
app, is [../README.md](../README.md).

## Index

- [architecture.md](architecture.md): layers, startup and shutdown order, the `sync:changed` event flow, the binding flow, error and secret handling, concurrency, and the package map.
- [app-service.md](app-service.md): the root package (`main.go`, `app.go`, `covers.go`, `wailslog.go`): every bound method, lifecycle hooks, profile validation, state assembly, remove-profile behavior, the cover handler, and the frontend binding contract.
- [syncer.md](syncer.md): the full sync run: owned games, the per-game worker pool, achievement sets, progress, and final status.
- [steam-client.md](steam-client.md): the Steam Web API client: endpoints, retries, error redaction, and profile resolution.
- [database.md](database.md): SQLite opening, WAL mode, migrations, pre-migration backups, and the schema.
- [repository.md](repository.md): the SQL access layer: profiles, games, sets, flags, tags, sync runs, settings, and dashboard assembly.
- [covers.md](covers.md): cover sources, the fallback order, the on-disk cache layout, and refresh rules.
- [platform.md](platform.md): per-OS paths, the system keyring, logging and rotation, and Windows specifics.
- [frontend.md](frontend.md): the Svelte 5 UI, its state and refresh logic, and the component layout.
- [build-and-release.md](build-and-release.md): Wails and Task build commands, packaging, CI, and the release workflow.
- [testing.md](testing.md): the Go and frontend test suites, test isolation helpers, and the diagnostic tools.

## Recommended reading order

1. [../README.md](../README.md): what the app does, from the user side. Skim it first.
2. [architecture.md](architecture.md): the big picture and where each package fits.
3. [app-service.md](app-service.md): the Go entry points the frontend calls.
4. [database.md](database.md) and [repository.md](repository.md): where state lives and how it is read and written.
5. [syncer.md](syncer.md) and [steam-client.md](steam-client.md): how data gets in.
6. [covers.md](covers.md) and [platform.md](platform.md): images, paths, keyring, and logging.
7. [frontend.md](frontend.md): how the UI consumes the state.
8. [testing.md](testing.md) and [build-and-release.md](build-and-release.md): how to verify and ship a change.

## Conventions

- These docs describe the code as it was when they were written. When a doc and the code disagree, the code wins, and
  the doc should be fixed in the same change.
- The user guide, [../README.md](../README.md), is for end users and maintainers of releases. Behavior that matters to
  users is described there. Developer docs refer to it instead of repeating it.
- The code is the source of truth. Function, type, file and constant names are cited without line numbers, because
  line numbers drift.
- Generated files (`frontend/bindings/`, `frontend/dist/`) are not documented line by line. Regenerate them rather than
  editing them.
- Secrets never appear in examples. The Steam Web API key is written as `<key>` or described by name only.
