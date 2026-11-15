# Steam Achievement Tracker

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/gjrud/steam-achievement-tracker)](https://goreportcard.com/report/github.com/gjrud/steam-achievement-tracker)
[![Go Version](https://img.shields.io/github/go-mod/go-version/gjrud/steam-achievement-tracker)](go.mod)

Steam Achievement Tracker is a Linux desktop app for tracking Steam achievement completion from real Steam data. It syncs a public Steam profile, stores progress locally, highlights games worth finishing next, and keeps sync warnings visible instead of silently losing old data.

This app was born for personal use and was entirely vibecoded. I have entry-level knowledge of Go, which I often usen for personal or work related cli tools, but the Svelte/JavaScript frontend is far outside my own area of expertise.

## Features

- **Steam profile setup** from SteamID64, `steamcommunity.com/profiles/...` URLs, `steamcommunity.com/id/...` URLs, or vanity names.
- **Secret Service-only API key storage** for the Steam Web API key; no environment variable or plaintext fallback is used.
- **Startup and manual sync** against Steam Web API data.
- **Dashboard header** with counters for all games, purchased games, completed games, completed sets, and unlocked achievements, plus a profile card with the latest sync status.
- **Not completed view** with ranked suggestions, search, and tag filters.
- **Completed games view** for achievement sets with full completion, including completed named and DLC sets.
- **Sync errors view** for games that failed refresh while preserving prior local data.
- **Disabled games view** for manually disabled games or games no longer owned by the active profile.
- **Achievement sets**: each achievement set of a game (base game, named sets, DLC sets) is its own card, completed and disabled independently.
- **Per-set actions** to mark a set as previously completed, flag missing achievements as DLC-related, disable a set, or re-enable it.
- **Local cover cache** using Steam library capsule images taken from the owned-games list; the store API is only queried for DLC sets and for base games without a listed cover, with hero, header and main capsule fallbacks. DLC sets without any image of their own show the parent game's cover.
- **Profile-scoped data clear** that removes the active profile, tracked games, owned-game playtime and purchase status, achievements, sync history, manual flags, and cached covers while keeping the Secret Service key and logs.
- **Local SQLite storage** with migration backups, WAL mode, and app-local log rotation.

## Requirements

### Compiled version

- Linux desktop session.
- GTK/WebKit runtime libraries compatible with the build. This project uses Wails with the `webkit2_41` build tag, so release builds target GTK3 + WebKit2GTK 4.1 + libsoup 3 runtime libraries.
- Linux Secret Service provider, such as GNOME Keyring or another compatible keyring service.
- `secret-tool` from libsecret tools to store the Steam Web API key.
- Steam Web API key.
- Network access to Steam Web API and Steam static asset hosts.
- Public Steam profile game details for the profile being tracked.

Package names differ by distribution. On Debian/Ubuntu-style systems, look for packages similar to `libgtk-3-0`, `libwebkit2gtk-4.1-0`, `libsoup-3.0-0`, `gnome-keyring`, and `libsecret-tools`.

### Development version

- Go 1.26.2 or newer.
- Node.js frontend tooling required by Wails.
- Wails v2 CLI. Example:

  ```sh
  go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.1
  ```

- Linux build dependencies for Wails and CGO: C compiler, `pkg-config`, GTK3 development headers, WebKit2GTK 4.1 development headers, and libsoup 3 development headers.
- SQLite CGO support through `github.com/mattn/go-sqlite3` dependencies, normally covered by the compiler and system SQLite/CGO toolchain.
- Same Secret Service, Steam Web API key, network, and public profile requirements as the compiled version for real sync testing.

Use `wails doctor` to check local Wails dependencies.

## Install

### 1. Install runtime dependencies

Install GTK/WebKit runtime libraries, a Secret Service provider, and `secret-tool` for your distribution.

### 2. Install the app binary and desktop icon

Download the release artifact for Linux, then install the executable and Linux desktop metadata:

```sh
./scripts/install-linux-user.sh install ./steam-achievement-tracker
```

If you build from source, the production binary is created at `build/bin/steam-achievement-tracker`; install it with:

```sh
wails build
./scripts/install-linux-user.sh install
```

The script installs:

- `~/.local/bin/steam-achievement-tracker`
- `~/.local/share/applications/steam-achievement-tracker.desktop`
- hicolor app icons under `~/.local/share/icons/hicolor/`

Launch from the app menu after install so the window manager can associate the window with the desktop icon.

To uninstall the binary, desktop entry, and icons:

```sh
./scripts/install-linux-user.sh uninstall
```

Uninstall keeps `~/.steam-achievement-tracker` and the Secret Service API key.

### 3. Store the Steam Web API key

Store the key in Secret Service:

Generate a Steam Web API key from Steam's developer page: <https://steamcommunity.com/dev/apikey>.

```sh
secret-tool store \
  --label="Steam Achievement Tracker - Steam Web API Key" \
  service steam-achievement-tracker \
  username steam-web-api-key
```

Paste the Steam Web API key when prompted by `secret-tool`. The app reads only this Secret Service item.

### 4. Launch

```sh
steam-achievement-tracker
```

If the API key is missing, the app opens to the key setup screen and shows the same `secret-tool` command.

## Build & run

Wails handles frontend dependency installation and frontend builds through the hooks in `wails.json`.

Run the desktop app in development mode from the repository root:

```sh
wails dev
```

Build the production app from the repository root:

```sh
wails build
```

The production binary is written to:

```text
build/bin/steam-achievement-tracker
```

## Testing

Run frontend tests:

```sh
cd frontend
npm test
```

Run backend tests from a clean tree by building the frontend first, because `main.go` embeds `frontend/dist`:

```sh
cd frontend
npm run build
cd ..
go test ./...
```

Run both:

```sh
cd frontend && npm test && npm run build
cd ..
go test ./...
```

Run the Go tests with the race detector (several tests, such as the asset handler and sync cancellation, only catch regressions under `-race`):

```sh
go test -race ./...
```

### Test suite layout

- Go tests drive package APIs against real SQLite and a real image cache; only Steam HTTP endpoints are faked. `app_test.go` covers the Wails-bound orchestration with `HOME` set to a temp dir and the `go-keyring` mock backend.
- Tests that need real retry paths without sleeping use `steam.Client.SetRetryBackoff`.
- Frontend tests (vitest) cover sync polling with fake timers, error banners for rejected Wails calls, keyboard/modal flows, and a drift check between `frontend/src/test/wails-app-mock.js` and the generated `frontend/wailsjs` bindings (skipped when the bindings are absent; regenerate with `wails generate module`).
- There is no coverage threshold. Coverage numbers overstate safety at the orchestration and async-UI boundaries, so use mutation testing as a diagnostic instead.

### Mutation testing

[gremlins](https://github.com/go-gremlins/gremlins) is configured in `.gremlins.yaml` (2 workers, timeout coefficient 20, `frontend/`, `wailsjs`, and `main.go` excluded). Run it per package, because whole-module runs report false timeouts:

```sh
gremlins unleash ./internal/<pkg> -o tmp/gremlins/<pkg>.json
```

Distrust any run with more than about 5% timed-out mutants; rerun with a higher coefficient first. Gremlins is a diagnostic only and must not be a CI gate.

## Usage

### Set up the API key

Before sync can run, store a Steam Web API key in Secret Service using the command in [Install](#3-store-the-steam-web-api-key). If the key is missing, the app shows the setup command and a **Retry key lookup** button. Store the key, then click **Retry key lookup**.

![API key setup](docs/setup-apikey.gif)

### Connect a Steam profile

On first launch with a valid API key and no active profile, enter one of:

- SteamID64, such as `7656119...`
- Profile URL, such as `https://steamcommunity.com/profiles/<steamid64>`
- Vanity URL, such as `https://steamcommunity.com/id/<name>`
- Vanity name by itself

Click **Validate**. The app resolves the profile and shows the Steam display name/avatar. Click **Save and sync** to save it as the active profile and start a background sync. Profile game details must be public.

![Connect a Steam profile](docs/connect-profile.gif)

### Sync achievements

Sync starts after saving a profile and again on app startup when a profile and API key exist. Use **Sync now** to start a manual refresh; the button changes to **Syncing…** while a sync is running.

Sync behavior:

- Owned games, names, playtime, purchase status, achievements, and cover images are loaded from Steam. Playtime and purchase status are stored per profile.
- Games no longer returned by Steam for the active profile are disabled for that profile.
- Disabled games are skipped, including manually disabled games and games disabled because Steam no longer reports them as owned.
- Each achievement set of a game becomes its own dashboard entry, and archived achievements are excluded from completion counts.
- DLC sets are kept even when the profile does not own the DLC. They show with the **Not owned** tag (see Achievement sets).
- Per-game sync runs with concurrency limit `4`.
- Steam API requests retry transient `429`, `500`, and `503` responses.
- Failed per-game refreshes keep prior local data when available and appear in **Sync errors**.
- If any per-game refresh fails, the sync run is marked `partial`.
- A manually disabled game stays manually disabled if Steam stops reporting it and later reports it again; only the automatic "no longer owned" disable is lifted.
- A cancelled sync (for example on app shutdown) ends the run as `failed` with no per-game warnings; games interrupted mid-sync keep their previously synced data.
- Steam error text shown in the UI, saved as warnings, or written to logs never contains the Steam Web API key; the `key=` query value is replaced with `REDACTED`.

### Read the dashboard

The header shows the app title, the profile card, and five counters in three labeled groups. Counters are plain figures, not cards, and everything in the header and the game grid scales with the window size.

- **Library** group:
  - **All games**: count excluding disabled games. This includes played free-to-play games.
  - **Purchased**: count excluding disabled games. This counts the games in the account's purchased list, so played free-to-play games are not included. Steam's purchased list may also include free license grants.
- **Completed** group:
  - **Completed games**: count excluding disabled games. A game counts when its base achievement set is completed. A game with only a completed DLC set does not count, and neither does a game whose base set is not listed for the profile (for example, a game whose achievements all belong to named or DLC sets).
  - **Completed sets**: count excluding disabled games and sets. Each completed set (base, named, or DLC) counts separately.
  - When at least one counted game or set is unvetted (see **Include unvetted apps** in [Library sync options](#library-sync-options)), the counter reads `<vetted> (<total>)`, with the vetted count in green, for example `12 (15)`. A set follows the vetted state of its game. Each counter decides on its own, so one can show both numbers while the other shows only its total. When nothing counted is unvetted, only the total is shown.
- **Unlocked** group:
  - **Achievements**: unlocked achievement count, excluding disabled games.

The profile card shows the avatar, the profile name, and the **Sync now** button. Below them, a status line shows the current or latest sync (a colored dot: blue while running, green on success, amber for partial or failed, grey when no sync has run yet). The date and time of the sync (`Started <date>` while it runs) sit on their own line under the status, followed by its per-game progress.

The main tabs are **Not completed**, **Completed**, **Sync errors**, **Disabled**, and **Settings**, drawn as folder tabs with the active tab raised. Below 1180 px wide the header stacks (title, profile card, one row of counters, tabs) and the tabs wrap without a baseline.

### Achievement sets

Steam groups achievements into sets. A game with several sets (for example Team Fortress 2 classes, or a game with DLC) shows **one card per set** instead of one card for the whole game:

- A set is completed on its own. A completed set moves to **Completed** while its unfinished siblings stay in **Not completed**.
- Sets are named after the game when Steam does not name them, for example `Team Fortress 2 – Scout`. The card title shows the set name, a **Set** tag marks every set other than the game's base set, and a **DLC** tag marks sets that belong to a DLC. A card lists its tags in the same order as the filter chips (see [Tags and sorting](#tags-and-sorting)), so **Set** and **DLC** appear after the progress tags and the other grey tags, and before the red issue tags (**Not owned**, **Achievements in DLC**, **Missing cover art**, **Sync error**).
- **DLC** sets (sets tied to a DLC app) are shown whether or not you own the DLC. A set for DLC you do not own is tagged **Not owned**, sits in **Not completed** with no progress tags, and is left out of the overall achievement totals, so it does not lower your overall completion percentage. Once you own the DLC, the next sync removes the tag and counts the set normally.
- Achievements marked **archived** by Steam are not counted toward a set's total or completion. Locked archived achievements never block completion.
- Set cards show the same playtime as their game, because Steam reports playtime per game.
- Sets without achievements are skipped. A game whose base set is empty (all achievements live in named sets) shows only its named sets.

Search matches the game name, so searching for a game lists all of its set cards.

A base game's cover is the library capsule named in the owned-games list (`capsule_filename`), downloaded from the Steam asset host without any store request. The store API is asked for a single app only when a cover is stale and either the cover belongs to a DLC set, or the owned-games list has no capsule for the base game or its file does not exist. A sync where every stale cover is a base game with a working owned-games capsule makes no store requests; each stale DLC cover, and each base game without a usable capsule, costs one store request.

Cover art for a DLC set (and for a base game that falls back to the store) uses this order:

1. the DLC's Steam library capsule
2. the DLC's Steam hero capsule (cropped to the 2:3 card shape)
3. the DLC's Steam header image (landscape; shown whole on a blurred backdrop instead of cropped)
4. the DLC's Steam main capsule (last resort, landscape, rendered like the header)
5. for a DLC set only: the parent game's cover, when the DLC has no cached image at all (the store page is unavailable, lists no images, or the lookup or download failed)
6. a placeholder with the first letter and **Missing cover art** when neither the DLC nor the parent game has a cover

A store-based capsule is only downloaded when Steam's store API lists it for that app; the app never guesses file names, and only builds the base game's `steam/apps/<appid>/<capsule_filename>` path from the owned-games list. If the cover download or the store lookup fails during a sync and no cover is cached yet, nothing is saved and the cover is retried on the next sync instead of being marked as missing. A cover that the store lists as absent is rechecked after 7 days.

Custom (non-DLC) named sets always use the base game's cover. A DLC set uses its own cover whenever one is cached, and only falls back to the base game's cover while it has none. The fallback is applied when cards are displayed, so a DLC cover that is found later replaces it automatically. The DLC cover is still retried on the normal schedule (next sync, or after 7 days when the store lists it as absent). Right after upgrading, a DLC set may keep a stale **Missing cover art** tag until its game is synced again.

Disable, re-enable, and the manual flags apply to one set card at a time. Disabling one set of a game leaves its sibling sets active.

### Use the Not completed view

This view lists non-disabled games that are not currently complete. Cards show cover art when cached, completion progress, playtime, tags, and the average unlock rate of missing achievements when Steam provides it.

![Not completed view](docs/not-completed.gif)

Suggestion tags include **New achievements added**, **Almost there**, **In progress**, **Untouched**, **Unvetted**, **No achievements**, **Set**, **DLC**, **Not owned**, **Achievements in DLC**, **Missing cover art**, and **Sync error**. Use the search box to filter by title and the tag chips to filter the **Not completed** and **Completed** lists. Each chip shows how many cards match it.

#### Tags and sorting

The **Not completed** list is sorted to surface likely finishable games first:

1. games with achievements that are not flagged as DLC-related, with **New achievements added** games first
2. DLC sets you do not own (**Not owned**), sorted by title; these never outrank a real candidate
3. games flagged **Achievements in DLC**, sorted by suggestion score
4. games with no Steam achievements, sorted by title

A set that is both **Not owned** and flagged **Achievements in DLC** is listed in group 2.

The tag filter chips and the tags shown on each card use the same order: **New achievements added**, **Almost there**, **In progress**, **Untouched**, **Unvetted**, **No achievements**, **Set**, **DLC**, **Not owned**, **Achievements in DLC**, **Missing cover art**, **Sync error**. The grey informational tags (**Untouched**, **Unvetted**, **No achievements**, **Set**, **DLC**) sit together right after the progress tags. On a completed card, **Completed** comes first, then the rest in that order. Tags the app does not recognize, if any, are shown last in their stored order. The chips appear in both **Not completed** and **Completed**, and each view lists only the tags that at least one of its cards carries. The active tag chip resets whenever you switch views; search still applies.

Within active achievement games, the app sorts by suggestion score, highest first. Suggestion score is based on completion percentage and, when Steam global achievement percentages are available, the average unlock rate of the achievements you are missing. Games with a score of `40` or higher get the **Almost there** tag; other started games get **In progress**.

Tag meanings:

- **New achievements added**: the game was previously completed, but now has locked achievements. Completed entries never show this tag.
- **Almost there**: the suggestion score is at least `40`.
- **In progress**: the game has progress, but its suggestion score is below the **Almost there** threshold.
- **Untouched**: the game has no recorded playtime.
- **Unvetted**: Steam has not vetted the app yet (a new app that Steam hides from the owned-games list unless **Include unvetted apps** is on). Shown on every card of such a game, including its set and DLC cards, and filterable. It also appears on completed cards. The tag clears on the next sync after Steam vets the app. A game that left the library (for example after turning **Include unvetted apps** off) is disabled as no longer owned and keeps its last known state, so it may still show the tag in **Disabled**.
- **No achievements**: Steam reports no achievements for the game.
- **Set**: the card is any achievement set other than the game's base set. Shown on every such card and filterable. A DLC set carries both **Set** and **DLC**.
- **DLC**: the set belongs to a DLC app. Shown on every DLC set card and filterable.
- **Not owned**: the set belongs to a DLC you do not own. It shows no progress tags, since it cannot be progressed.
- **Achievements in DLC**: remaining achievements are manually flagged as DLC-related. Completed entries never show this tag.
- **Missing cover art**: no cover image is cached locally. A DLC set counts as having a cover when its parent game's cover is cached.
- **Sync error**: the latest per-game refresh failed.

### Review completed games

The **Completed** tab lists non-disabled games where all known achievements are unlocked. Use search to filter by title, and the tag chips to narrow the list to cards carrying a given tag (for example **Set**, **DLC**, or **Missing cover art**). Right-click a card to change local flags or disable the game.

<img src="docs/completed.png" alt="Completed games view" width="640">

### Handle sync errors

The **Sync errors** tab lists non-disabled games with current per-game sync warnings. Previous successful data is shown when available; otherwise the row says there is no previous sync data. Use the **Disable** button to hide a noisy game, or right-click the row to mark it previously completed, flag DLC-missing achievements, or disable it.

<img src="docs/sync-error.png" alt="Sync errors view" width="640">

### Disable or re-enable games

Right-click a game card in **Not completed** or **Completed**, or a row in **Sync errors**, to open the game action menu. **Disable game** hides it from active game views and summary totals, and skips it in later syncs. On a set card the action reads **Disable set** and affects only that set. Use the **Disabled** tab to review disabled games and click **Re-enable**.

A game whose sets are all disabled is skipped during sync. Because of that, new DLC sets of such a game are only discovered after you re-enable at least one of its sets.

![Disable and re-enable games](docs/disable-renable.gif)

### Manually flag games

In **Not completed**, right-click a game card and use:

- **Mark as previously completed** when a game was complete before Steam added more achievements. This adds the **New achievements added** tag while the game is incomplete.
- **Flag missing achievements in DLC** when remaining achievements belong to DLC or content you do not plan to count. This adds the **Achievements in DLC** tag.

These flags are mainly for incomplete games that need local context. They are profile-scoped, stored locally, and can be cleared from the same right-click menu. The same actions are also available from **Completed** cards and **Sync errors** rows when needed.

![Manually flag games](docs/flag-games.gif)

### Library sync options

Open **Settings** to choose what a sync of the active profile loads. Settings are stored per profile: each profile has its own values, and a new profile starts with both options on:

- **Include unvetted apps**: includes new Steam apps that Steam hides until they are vetted. With it on, each sync also asks Steam for the vetted list, so the app can tell which games are unvetted: those get the **Unvetted** tag and are counted separately in the **Completed** counters. With it off, every game is vetted.
- **Include played free games**: includes free-to-play games you played but did not buy. Off limits the library to purchased games.

Changes apply on the next sync and do not start one. Turning an option off removes the games it excluded from the library on that sync, so they may be marked as no longer owned or disabled.

### Clear active user data

Open **Settings** and click **Clear data**. The app asks you to type the shown profile identifier before clearing. This removes the active profile, tracked games, owned-game playtime and purchase status, achievement data, sync history, manual flags, profile-scoped tags, library sync settings, and cached covers for that profile. It does not remove the Secret Service API key or logs. Re-adding the profile starts with both library sync options on.

![Clear active user data](docs/clear-data.gif)

## Data

All app-owned files live under `~/.steam-achievement-tracker` unless noted. Directories are created with mode `0700`; local DB, backup, image, and log files are kept at `0600` where the app controls permissions.

### Stored Files

| Path | Created by (process/major function) | Purpose |
| --- | --- | --- |
| `~/.steam-achievement-tracker/` | Startup initialization | App data root. |
| `~/.steam-achievement-tracker/steam-achievement-tracker.db` | DB open/migration | SQLite database for profiles, games, achievements, flags, tags, snapshots, and sync history. |
| `~/.steam-achievement-tracker/steam-achievement-tracker.db-wal` | SQLite WAL mode | SQLite write-ahead log while database is active. |
| `~/.steam-achievement-tracker/steam-achievement-tracker.db-shm` | SQLite WAL mode | SQLite shared-memory file while database is active. |
| `~/.steam-achievement-tracker/backups/` | DB migration | Backup directory. |
| `~/.steam-achievement-tracker/backups/steam-achievement-tracker-<unix-ns>.db` | DB migration before schema changes | Point-in-time SQLite DB backup created before migrating an existing user DB. |
| `~/.steam-achievement-tracker/cache/` | Startup initialization | Cache root. |
| `~/.steam-achievement-tracker/cache/images/games/` | Startup initialization / cover cache | Game cover cache root. |
| `~/.steam-achievement-tracker/cache/images/games/<appid>/library_600x900.jpg` | Cover sync | Cached Steam library capsule image for one app (or DLC app id for DLC sets). |
| `~/.steam-achievement-tracker/cache/images/games/<appid>/hero_capsule.jpg` | Cover sync | Cached Steam hero capsule image, used when a DLC has no library capsule. |
| `~/.steam-achievement-tracker/cache/images/games/<appid>/header.jpg` | Cover sync | Cached Steam header image, the fallback when neither the library nor the hero capsule is available. |
| `~/.steam-achievement-tracker/cache/images/games/<appid>/main_capsule.jpg` | Cover sync | Cached Steam main capsule image, the last fallback when the library capsule, hero capsule and header are all unavailable. |
| `~/.steam-achievement-tracker/logs/` | Logging initialization | Log directory. |
| `~/.steam-achievement-tracker/logs/app.log` | Logging initialization | Current application log. |
| `~/.steam-achievement-tracker/logs/app.log*` | Log rotation | Rotated logs; max size is 5 MB with up to 5 backups. |
| Secret Service item `service=steam-achievement-tracker`, `username=steam-web-api-key` | User via `secret-tool` | Steam Web API key. Stored outside the app data directory by the desktop keyring. |

### steam-achievement-tracker.db Schema

| Table | Columns | Notes |
| --- | --- | --- |
| `settings` | `key` PK, `value` | App settings. Stores `active_profile_id`. |
| `schema_migrations` | `version` PK, `name`, `applied_at` | Applied DB migrations. Current schema version is `14`. |
| `game_sets` | `appid`, `groupid`, `name`, `dlcappid`, `sort_order`, `updated_at`; PK (`appid`, `groupid`) | Achievement set catalog per game. Group `0` is the base set; `dlcappid` ties a set to a DLC app. |
| `cover_images` | `image_appid` PK, `kind`, `file_name`, `path`, `source_url`, `checked_at`, `downloaded_at` | Cover image per image app id (the game, or the DLC for DLC sets). `kind` is `library`, `hero`, `header` or `main`. |
| `profiles` | `id` PK, `steam_id64` unique, `display_name`, `avatar_url`, `created_at`, `updated_at`, `last_synced_at` | Steam profiles known to the app. |
| `games` | `appid` PK, `name`, `playtime_forever`, `purchased`, `has_achievements`, `achievement_status`, `disabled`, `disabled_reason`, `cover_path`, `cover_source_url`, `cover_checked_at`, `cover_downloaded_at`, `created_at`, `updated_at` | Shared Steam app metadata and cover cache metadata. `playtime_forever` and `purchased` are legacy columns (migrations 11 and 12): app code no longer reads or writes them; per-profile values are in `profile_owned_games`. `disabled`/`disabled_reason` are legacy/global fields; current manual disables are profile-scoped. |
| `profile_games` | `profile_id`, `appid`, `groupid`, `total_achievements`, `unlocked_achievements`, `completion_percent`, `missing_avg_unlock`, `suggestion_score`, `is_completed`, `was_completed`, `newly_incomplete`, `sync_warning`, `last_error`, `last_error_at`, `last_synced_at`, `dlc_not_owned`; PK (`profile_id`, `appid`, `groupid`) | Per-profile entry progress and latest warning state. `dlc_not_owned` is `1` for a DLC-bound set the profile does not own. |
| `profile_owned_games` | `profile_id`, `appid`, `playtime_forever`, `purchased`, `vetted`, `updated_at`; PK (`profile_id`, `appid`) | Per-profile playtime, purchase status and vetted status for each owned game (migrations 12 and 14). `purchased` is `1` when the game is in the profile's purchased list. `vetted` is `0` when Steam returns the app only with unvetted apps included; it defaults to `1`. |
| `profile_library_settings` | `profile_id` PK (FK `profiles`), `include_unvetted_apps`, `include_played_free_games`, `updated_at` | Per-profile library sync options (migration 13). `1` or `0`; a profile with no row uses both on. Removed with its profile by Clear data. |
| `achievements` | `appid`, `apiname`, `global_percent`, `updated_at`; PK (`appid`, `apiname`) | Per-game achievement catalog with global unlock percentage. |
| `profile_achievements` | `profile_id`, `appid`, `apiname`, `unlocked`, `unlock_time`, `updated_at`; PK (`profile_id`, `appid`, `apiname`) | Per-profile achievement unlock state. |
| `game_snapshots` | `id` PK, `profile_id`, `appid`, `total_achievements`, `unlocked_achievements`, `completion_percent`, `missing_avg_unlock`, `is_completed`, `synced_at` | Historical per-sync progress snapshots. |
| `sync_runs` | `id` PK, `profile_id`, `started_at`, `finished_at`, `status`, `games_total`, `games_synced`, `games_failed`, `error` | Sync run history and progress. Status values include `running`, `success`, `partial`, and `failed`. |
| `profile_game_flags` | `profile_id`, `appid`, `manual_was_completed`, `missing_achievements_in_dlc`, `disabled`, `disabled_reason`, `created_at`, `updated_at`; PK (`profile_id`, `appid`) | Manual, profile-scoped game flags. Indexed by (`profile_id`, `disabled`). |
| `profile_game_tags` | `profile_id`, `appid`, `tag`, `score`, `reason`, `computed_at`; PK (`profile_id`, `appid`, `tag`) | Computed profile-scoped tags used by suggestion filters. Indexed by (`profile_id`, `tag`). |

Migration 11 adds `games.purchased`. Migration 12 creates `profile_owned_games` and copies each profile entry's legacy `games.playtime_forever` and `games.purchased` values into it. The legacy purchased flag was global, so migration 12 copies it to every profile that has the game, even a profile that did not buy it, until that profile's next sync corrects it.

Migration 13 creates `profile_library_settings`. It does not backfill rows: every profile starts with both library sync options on until it saves its own values.

Migration 14 adds `profile_owned_games.vetted` (default `1`). Existing rows count as vetted until the profile's next sync sets the real value.

```mermaid
erDiagram
  SETTINGS {
    text key PK
    text value
  }
  SCHEMA_MIGRATIONS {
    integer version PK
    text name
    text applied_at
  }
  PROFILES {
    integer id PK
    text steam_id64 UK
    text display_name
    text avatar_url
    text created_at
    text updated_at
    text last_synced_at
  }
  GAMES {
    integer appid PK
    text name
    integer playtime_forever
    integer purchased
    integer has_achievements
    text achievement_status
    integer disabled
    text disabled_reason
    text cover_path
    text cover_source_url
    text cover_checked_at
    text cover_downloaded_at
    text created_at
    text updated_at
  }
  PROFILE_GAMES {
    integer profile_id PK,FK
    integer appid PK,FK
    integer total_achievements
    integer unlocked_achievements
    real completion_percent
    real missing_avg_unlock
    real suggestion_score
    integer is_completed
    integer was_completed
    integer newly_incomplete
    integer sync_warning
    text last_error
    text last_error_at
    text last_synced_at
    integer dlc_not_owned
  }
  PROFILE_OWNED_GAMES {
    integer profile_id PK,FK
    integer appid PK,FK
    integer playtime_forever
    integer purchased
    integer vetted
    text updated_at
  }
  ACHIEVEMENTS {
    integer appid PK,FK
    text apiname PK
    real global_percent
    text updated_at
  }
  PROFILE_ACHIEVEMENTS {
    integer profile_id PK,FK
    integer appid PK,FK
    text apiname PK,FK
    integer unlocked
    integer unlock_time
    text updated_at
  }
  GAME_SNAPSHOTS {
    integer id PK
    integer profile_id FK
    integer appid FK
    integer total_achievements
    integer unlocked_achievements
    real completion_percent
    real missing_avg_unlock
    integer is_completed
    text synced_at
  }
  SYNC_RUNS {
    integer id PK
    integer profile_id FK
    text started_at
    text finished_at
    text status
    integer games_total
    integer games_synced
    integer games_failed
    text error
  }
  PROFILE_GAME_FLAGS {
    integer profile_id PK,FK
    integer appid PK,FK
    integer manual_was_completed
    integer missing_achievements_in_dlc
    integer disabled
    text disabled_reason
    text created_at
    text updated_at
  }
  PROFILE_GAME_TAGS {
    integer profile_id PK,FK
    integer appid PK,FK
    text tag PK
    real score
    text reason
    text computed_at
  }
  PROFILE_LIBRARY_SETTINGS {
    integer profile_id PK,FK
    integer include_unvetted_apps
    integer include_played_free_games
    text updated_at
  }

  PROFILES ||--o{ PROFILE_GAMES : tracks
  GAMES ||--o{ PROFILE_GAMES : appears_in
  PROFILES ||--o{ PROFILE_OWNED_GAMES : owns
  GAMES ||--o{ PROFILE_OWNED_GAMES : owned_in
  GAMES ||--o{ ACHIEVEMENTS : has
  PROFILES ||--o{ PROFILE_ACHIEVEMENTS : unlocks
  ACHIEVEMENTS ||--o{ PROFILE_ACHIEVEMENTS : recorded_as
  PROFILES ||--o{ GAME_SNAPSHOTS : snapshots
  GAMES ||--o{ GAME_SNAPSHOTS : snapshotted
  PROFILES ||--o{ SYNC_RUNS : syncs
  PROFILES ||--o{ PROFILE_GAME_FLAGS : flags
  GAMES ||--o{ PROFILE_GAME_FLAGS : flagged
  PROFILES ||--o{ PROFILE_GAME_TAGS : tags
  GAMES ||--o{ PROFILE_GAME_TAGS : tagged
  PROFILES ||--o| PROFILE_LIBRARY_SETTINGS : configures
```

### Data sources

| URL/API/source | Description |
| --- | --- |
| `https://api.steampowered.com/ISteamUser/ResolveVanityURL/v1/` | Resolves Steam vanity names and `/id/<name>` profile URLs to SteamID64. Requires the API key. |
| `https://api.steampowered.com/ISteamUser/GetPlayerSummaries/v2/` | Loads profile display name and avatar URL for setup/confirmation. Requires the API key. |
| `https://api.steampowered.com/IPlayerService/GetOwnedGames/v0001/` | Loads the public owned game list, app names, playtime, the library cover file name (`include_extended_appinfo`), and the purchased list for the active profile. Unvetted apps are included by default (`skip_unvetted_apps=false`; `true` when that profile's **Include unvetted apps** is off; each profile uses its own setting from `profile_library_settings`). Called three times per sync by default: once with `include_played_free_games=true` (full library), once with `include_played_free_games=false` (purchased list), and once with `skip_unvetted_apps=true` and the same `include_played_free_games` value as the full-library call (vetted list; a game missing from it is unvetted). With **Include played free games** off, the full list and the purchased list are one call with `include_played_free_games=false`, and every game counts as purchased. With **Include unvetted apps** off, the vetted-list call is skipped and every game counts as vetted. A failure or unusable response from any call fails the sync run before any game is synced. Requires the API key and public game details. |
| `https://api.steampowered.com/ISteamUserStats/GetSchemaForGame/v2/` | Loads each game's achievement schema. Requires the API key. |
| `https://api.steampowered.com/ISteamUserStats/GetPlayerAchievements/v1/` | Loads per-profile unlocked achievements and unlock times for a game. Requires the API key and accessible profile/game stats. |
| `https://api.steampowered.com/ISteamUserStats/GetGlobalAchievementPercentagesForApp/v0002/` | Loads global achievement unlock percentages used for rarity/missing-average calculations. |
| `https://api.steampowered.com/IStoreBrowseService/GetItems/v1/` | Loads Steam store item assets (library, hero, header and main capsule source paths) for one app at a time. Called only for stale DLC set covers and for base games whose owned-games cover is missing. |
| `https://shared.akamai.steamstatic.com/store_item_assets/...` | Primary Steam static asset host for cached game cover downloads. |
| `https://shared.steamstatic.com/store_item_assets/...` | Fallback Steam static asset host for cached game cover downloads. |
| Steam avatar URLs returned by `GetPlayerSummaries` | Displayed in the app for the active profile; URL is stored in `profiles.avatar_url`. |

The app does not send data to any non-Steam service. Steam requests include the Steam Web API key where required by Steam endpoints.
