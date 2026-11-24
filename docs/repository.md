# Repository

Developer reference for `internal/repository`. This is the only package that issues SQL against the app database. It owns the dashboard model, tag computation, profile lifecycle and cover-cache rows. Schema and migrations are documented in [database.md](database.md). Callers are the app service (see [app-service.md](app-service.md)) and sync engine (see [syncer.md](syncer.md)). Cover-file handling is documented in [covers.md](covers.md).

## Repository type

```go
type Repository struct { db *sql.DB }
func New(db *sql.DB) *Repository
func (r *Repository) Close() error
```

`New` wraps a `*sql.DB` created by `db.OpenAndMigrate`. The handle has a single connection (see [database.md](database.md#overview)). This has two consequences for package code:

- Never query `r.db` while a transaction or an open `rows` from the same handle is active. Finish reading and close `rows` before issuing the next statement. Existing code follows this rule in `profileGameTags`, `loadGameCards`, `profileAppIDs`, `profileDLCImageAppIDs` and `entriesForImageApp`.
- Helpers that take a `*sql.Tx` (`pruneStaleEntries`, `upsertEntry`, `recomputeGameTagsTx`, ...) are the way to compose work inside one transaction.

### Transaction semantics

Each exported method's behavior is described in the sections below. In summary:

- Single autocommit statement: `DeactivateProfile`, `TouchProfileSynced`, `SaveLibrarySettings`, `SetUpdateChecksEnabled`, `StartSyncRun`, `FinishSyncRun`, `UpdateSyncRunProgress`, `CancelRunningSyncRuns`, `HasSetData`, `HasStoredAchievementData`, and all read methods.
- One transaction: `SaveProfile`, `ClearProfileData` (its reads and deletes), `UpsertOwnedGame`, `SaveGameSync`, `SaveGameWarning` (warning part; `UpsertOwnedGame` runs first in its own transaction), `MarkGamePreviouslyCompleted`, `ToggleMissingAchievementsInDLC`, `DisableMissingOwnedGames`, `DisableProfileGame`, `EnableProfileGame`, `RecomputeGameTags`, `SaveCover`.
- Not atomic across calls: `Dashboard` does its reads in separate statements without a read transaction, so `Summary`, `LatestSyncRun` and the card list can reflect different moments (the card lists themselves come from one `loadGameCards` pass).

## Exported types

| Type | File | Purpose |
| --- | --- | --- |
| `Profile` | `repository.go` | Connected account: `ID`, `SteamID64`, `DisplayName`, `AvatarURL`, `CreatedAt`, `UpdatedAt`, `LastSyncedAt`. |
| `Summary` | `repository.go` | Dashboard totals; see [Summary](#summary). |
| `SyncRun` | `repository.go` | Sync run record: `ID`, `ProfileID`, `StartedAt`, `FinishedAt`, `Status`, `GamesTotal`, `GamesSynced`, `GamesFailed`, `Error`. |
| `GameCard` | `repository.go` | One dashboard entry (game or set) with flags, tags and cover URL. JSON keys are camelCase. |
| `Dashboard` | `repository.go` | `Summary`, `LatestSyncRun`, and the four lists `Suggestions`, `Completed`, `Warnings`, `Disabled`. |
| `GameRecord` | `repository.go` | Owned-game input for `UpsertOwnedGame`: `AppID`, `Name`, `PlaytimeForever`, `Purchased`, `Unvetted`. |
| `AchievementRecord` | `repository.go` | Achievement input: `APIName`, `GlobalPercent`, `Unlocked`, `UnlockTime`, `Archived`. |
| `ProfileGameState` | `repository.go` | State of one entry, including flags; `Exists` is false for history-only reads. |
| `GameSyncUpdate` | `repository.go` | One game's sync: owned facts plus all eligible `Sets`. An empty `Sets` prunes every entry of the game. |
| `SetSyncUpdate` | `repository.go` | One entry (`GroupID`) within a `GameSyncUpdate`, including its achievements. |
| `CoverUpdate` | `repository.go` | Input for `SaveCover`: `ImageAppID`, `Kind`, `FileName`, `Cached` (bool, never a path), `SourceURL`, `Downloaded`. |
| `CoverRecord` | `repository.go` | Stored cover row returned by `GetCover` and `ListCovers`. |
| `LibrarySettings` | `settings.go` | Per-profile library sync options: `IncludeUnvettedApps`, `IncludePlayedFreeGames`. |

Exported helpers in `repository.go`: `EntryKey(appID, groupID) string` (`"<appid>:<groupid>"`), `DisplayName(gameName, setName, groupID) string`, `FormatGameError(appID, logText) string` (log text only, never stored). `DefaultLibrarySettings() LibrarySettings` is in `settings.go`.

`DisplayName` rules: group 0 is the game name. For a set with a name, if the set name already contains the game name (case-insensitive) it is used as is; otherwise `"<game> – <set>"` (en dash). An unnamed set becomes `"<game> – Set <groupid>"`. For a named set, an empty game name yields the trimmed set name alone; an unnamed set with an empty game name yields `" – Set <groupid>"` (leading space).

Constant `almostThereThreshold = 40` (score threshold for the `almost_there` tag).

## Entry model

A profile entry is identified by the key `(profile_id, appid, groupid)` in `profile_games`. `groupid 0` denotes the base game. Other groupids denote achievement sets: named sets of the base game or DLC-bound sets (`game_sets.dlcappid <> 0`). The frontend identifies each card by `EntryKey`.

Cards are built from:

| Source | Provides |
| --- | --- |
| `games` | `Name`, `has_achievements`, `achievement_status` |
| `game_sets` (left join) | `SetName`, `DLCAppID` |
| `profile_games` | counts, completion, suggestion score, sync flags, `dlc_not_owned`, last error |
| `profile_owned_games` (left join on app) | `PlaytimeForever`, `Unvetted` (coalesced to vetted when the row is missing). `purchased` is used only by `Summary`. |
| `profile_game_flags` (left join) | `ManualWasCompleted`, `MissingDLC`, `Disabled`, `DisabledReason` |
| `cover_images` (left join on the effective cover id) | `CoverKind`, `CoverURL` |
| `profile_game_tags` | `Tags` |

`Unvetted` on a card is `profile_owned_games.vetted = 0` for the entry's app. A DLC or named set shares its base app's owned-game row, so it follows the base app.

## Profile lifecycle (`profiles.go`)

### Settings keys

The `settings` table holds app-level keys. Two are used: `active_profile_id` (this section) and `update_checks_enabled` (see [Update check setting](#update-check-setting)).

| Key | Value | Meaning |
| --- | --- | --- |
| `active_profile_id` | decimal `profiles.id` | Active profile. |
| `active_profile_id` | `''` | No active profile (set by `DeactivateProfile` and by deleting the active profile). |
| (no row) | | Also no active profile. |

### Update check setting

`settings.go` holds `update_checks_enabled`, the app-wide key for the startup update check. The methods are `UpdateChecksEnabled() (bool, error)` and `SetUpdateChecksEnabled(enabled bool) error`.

- A missing row reads as on (the default). `"1"` is on and `"0"` is off.
- Any other stored value returns an error with `false`. The app treats that as unreadable and skips the startup check (see [updater.md](updater.md#persistence)).
- `SetUpdateChecksEnabled` upserts only this key. Other settings rows, including `active_profile_id`, are not touched.

Per-profile library options are not in `settings`; they are in `profile_library_settings` (see [Library settings](#library-settings-settingsgo)).

### Methods

- `ActiveProfile() (*Profile, error)`: reads `active_profile_id`. Returns `nil, nil` when the row is missing or empty. When the row holds an id, returns that profile, or `nil` if it no longer exists. It never falls back to another profile.
- `DeactivateProfile() error`: upserts `active_profile_id = ''`. Profile data is untouched. Saving the same SteamID64 again re-activates it.
- `SaveProfile(steamID64, displayName, avatarURL string) (*Profile, error)`: one transaction upserts `profiles` by `steam_id64` (updates name, avatar, `updated_at`; keeps `created_at`), reads the id, and sets `active_profile_id` to it. After commit returns `ActiveProfile()`.
- `TouchProfileSynced(profileID int64) error`: sets `last_synced_at` and `updated_at` to now.
- `ClearProfileData(profileID int64) ([]int64, error)`: see [Profile removal](#profile-removal). Returns the image app ids whose `cover_images` rows were deleted.

### Profile removal

`ClearProfileData(profileID)` follows this sequence:

1. One transaction. Every read below runs on the transaction, never on `r.db`, because the database has one connection (`SetMaxOpenConns(1)`) and a read on `r.db` inside an open transaction waits forever. The reads are: check whether the profile is active (`activeProfile(tx)`); collect every app id the profile references (`profileAppIDs(tx, ...)`: union over `profile_games`, `profile_achievements`, `profile_game_flags`, `profile_game_tags`, `game_snapshots`, `profile_owned_games`); collect the DLC image ids of its sets (`profileDLCImageAppIDs(tx, ...)`). Reading inside the transaction means a sync cannot insert shared rows between the candidate reads and the deletes.
2. Still in the transaction:
   - Delete the profile's rows from `profile_achievements`, `profile_game_flags`, `profile_owned_games`, `profile_game_tags`, `game_snapshots`, `profile_games`, `sync_runs`, `profile_library_settings`.
   - If the profile was active, set `active_profile_id = ''`.
   - Delete the `profiles` row.
   - `deleteOrphanRowsForAppIDs`: delete `achievements` of those apps that no `profile_achievements` row references; delete `game_sets` of those apps that have no `profile_games` rows at all (any profile); delete `games` of those apps that no `profile_games`, `profile_achievements`, `profile_game_flags`, `profile_owned_games`, `profile_game_tags` or `game_snapshots` row references.
   - `deleteOrphanCoverRows` over the union of app ids and DLC image ids: delete `cover_images` rows that no remaining `profile_games` entry displays. An image id is still used when some entry has that app id (DLC entries fall back to the game's cover), or when some entry is bound to it as DLC (`game_sets.dlcappid <> 0`). Both checks are index searches (`orphanCoverRowsSQL`). Returns the deleted ids.
3. Commit.

The caller (`ClearActiveUserData` in `app.go`, documented in [app-service.md](app-service.md)) then removes `<GameImages>/<id>` for the returned ids. Covers still used by another profile remain on disk. Removing the profile also removes its `profile_library_settings` row; re-adding the same SteamID64 starts with `DefaultLibrarySettings()`.

Not removed by `ClearProfileData`: `achievements`/`games` rows still referenced by another profile, and `cover_images` rows still displayed by another profile. Logs and the Steam API key are outside the database (the key is in the system keyring).

## Library settings (`settings.go`)

`LibrarySettings{IncludeUnvettedApps, IncludePlayedFreeGames bool}`. `DefaultLibrarySettings()` returns both `true`; it is the only default source.

- `LibrarySettings(profileID) (LibrarySettings, error)`: reads `profile_library_settings`. Missing row returns the defaults.
- `SaveLibrarySettings(profileID, s) error`: upserts the row for that profile only. Changes apply on the next sync, per the type's doc comment.

## Sync runs (`sync_runs.go`)

- `StartSyncRun(profileID) (int64, error)`: inserts a row with `status = 'running'` and returns its id.
- `FinishSyncRun(id, status, total, synced, failed, errText *string) error`: sets `finished_at`, `status`, counters and `error`. Statuses written by the syncer: `success`, `partial`, `failed`.
- `UpdateSyncRunProgress(id, total, synced, failed) error`: updates counters only.
- `LatestSyncRun(profileID) (*SyncRun, error)`: the highest-id run for the profile, or `nil`.
- `CancelRunningSyncRuns(reason string) error`: sets every `running` row (all profiles) to `canceled`, with `finished_at` now and `error = reason`. Called at startup for runs left by an interrupted process.

## Owned games and sync writes (`games.go`)

### `UpsertOwnedGame(profileID, GameRecord) error`

One transaction: upserts `games(appid, name)`, then upserts `profile_owned_games` with `playtime_forever`, `purchased`, and `vetted = !Unvetted`. Called by `syncer.prepareOwnedGames` once per game of the merged `GetOwnedGames` result, and by `SaveGameWarning`.

### `SaveGameSync(GameSyncUpdate) error`

One transaction for one game. In order:

1. Upsert `games` (name, `has_achievements`, `achievement_status`).
2. Upsert `profile_owned_games` playtime. On insert `purchased` is `0` and `vetted` takes its default `1`. On conflict only playtime and `updated_at` change, so `purchased` and `vetted` set by `UpsertOwnedGame` are kept.
3. `pruneProfileAchievements`: delete this profile's achievements of the app that are not in any set; delete unreferenced `achievements` of the app.
4. `upsertGameSets`: upsert `game_sets` for every set (name, `dlcappid`, `sort_order`). Nothing is deleted: a set Steam stops reporting keeps its row, so `HasSetData` stays true for that app.
5. For each set: upsert `achievements` (`groupid`, `archived`, `global_percent`) and `profile_achievements` (`unlocked`, `unlock_time`).
6. `pruneStaleEntries`: delete `profile_game_tags` and `profile_games` of this app whose `groupid` is not in the new set list. `profile_game_flags` and `game_snapshots` are kept.
7. For each set: upsert `profile_games` (all stats and flags from the update); insert a `game_snapshots` row only when the latest snapshot's (total, unlocked, is_completed) differs from this update or none exists; `recomputeGameTagsTx`.

An empty `Sets` removes all entries of the app (step 6 keeps none). The syncer uses that for games whose eligible sets are empty. `WasCompleted` and `NewlyIncomplete` come from the caller; this method does not derive them.

### `SaveGameWarning(profileID, GameRecord, warning string) error`

Used when a game's sync fails. Calls `UpsertOwnedGame` first (its own transaction), then in one transaction:

- If the profile has entries for the app: sets `sync_warning = 1`, `last_error`, `last_error_at` on all of them. Stats are not changed.
- If there are none: inserts a `groupid 0` placeholder with `sync_warning = 1` and `last_synced_at` NULL.
- Recomputes tags for every affected entry.

### `HasStoredAchievementData(profileID, appID) (bool, error)`

Single read, no transaction. True when the app has an entry with `groupid<>0`, an entry with `total_achievements>0` (both for this profile), or the shared `games.has_achievements=1` row. `games.has_achievements` is cross-profile, so the answer can come from another profile's data. The syncer's empty-schema guard (`saveGameWithoutAchievements`, see [syncer.md](syncer.md)) calls it to tell an empty schema from Steam, which may be a failed answer, from a game that really has none.

### Flag methods

- `MarkGamePreviouslyCompleted(profileID, appID, groupID) error`: toggles `profile_game_flags.manual_was_completed` (insert with 1, or flip on conflict). Then recomputes on `profile_games`:
  - `was_completed = 1` if `is_completed = 1`, or a completed `game_snapshots` row exists for the entry, or the manual flag is set.
  - `newly_incomplete = 1` if `is_completed = 0` and (a completed snapshot exists or the manual flag is set).
  - Recomputes tags. If no `profile_games` row exists, returns `sql.ErrNoRows` and the transaction is rolled back, so the flag change is not kept.
- `ToggleMissingAchievementsInDLC(profileID, appID, groupID) error`: flips `profile_game_flags.missing_achievements_in_dlc` (insert or flip) and runs `recomputeGameTagsTx` in one transaction, so a failed tag write rolls the flip back.
- `DisableProfileGame(profileID, appID, groupID) error`: one transaction: upserts the flag with `disabled = 1`, `disabled_reason = 'manual'`, and deletes that entry's tags. Sibling sets of the same game are not affected.
- `EnableProfileGame(profileID, appID, groupID) error`: one transaction: sets `disabled = 0`, `disabled_reason = NULL` on that entry's flag, then recomputes its tags.
- `DisableMissingOwnedGames(profileID, owned []int64) error`: one transaction, the not-owned sweep.
  - Entries of apps not in `owned` get a flag with `disabled = 1`, `disabled_reason = 'not_owned'`. On conflict a `manual` reason is kept (`notOwnedReasonSQL`), so a re-own does not re-enable a manually disabled entry.
  - Flags with `disabled_reason = 'not_owned'` for apps in `owned` are re-enabled (`disabled = 0`, reason NULL). Manual disables stay.
  - Tags of entries of apps not in `owned` are deleted. With an empty `owned`, every entry is flagged and all tags of the profile are deleted.
  - Tags are not recomputed for re-enabled entries. The next game sync for those apps recomputes them.

## Dashboard (`dashboard.go`)

### Entry points

- `Dashboard(profileID) (Dashboard, error)`: `Summary`, `LatestSyncRun`, then one `loadGameCards` pass, then the four lists are filtered and sorted from that one slice. The lists are consistent with each other. The cards share `Tags` slices and pointer fields when a card appears in two lists. Treat the result as read-only.
- `GameCards(profileID, filter string) ([]GameCard, error)`: one list. Filter values: `suggestions`, `completed`, `warnings`, `disabled`. Any other value means `suggestions`.
- `Summary(profileID) (Summary, error)`: totals (below).
- `ProfileGame(profileID, appID, groupID) (ProfileGameState, error)`: one entry's state. When the entry has no `profile_games` row (pruned, or never synced), falls back to the flags and snapshots (`profileGameHistory`): `WasCompleted` is true if a completed snapshot exists or the manual flag is set; `Exists` stays false.
- `DisabledProfileGameIDs(profileID) (map[int64]bool, error)`: app ids the sync should skip. An app qualifies when every one of its `profile_games` entries has a disabled flag. An app with a `not_owned` flag and no entries also qualifies. Orphan disabled flags (no `profile_games` row, reason not `not_owned`) do not skip the app.

### The four lists

Each list is a subset of the cards; `disabled` is the first filter.

| List | Filter (`includeGameCard`) | Sort (`sortGameCards`) |
| --- | --- | --- |
| Suggestions (`suggestions`, default) | not disabled and not completed | Groups below. |
| Completed (`completed`) | not disabled and `is_completed` | By display name, then `EntryKey`. |
| Warnings (`warnings`) | not disabled and `sync_warning` (may also be completed) | By display name, then `EntryKey`. |
| Disabled (`disabled`) | `disabled` (manual or not-owned) | By display name, then `EntryKey`. |

Suggestion sort groups (category order, lower first):

0. Candidate: a card with achievements, not DLC-not-owned, and not flagged `missing_achievements_in_dlc`.
   - Newly incomplete cards come before others.
   - Among newly incomplete: fewer missing achievements (`total - unlocked`) first, then higher completion, then name.
   - Among the rest: higher `suggestion_score` first (NULL counts as 0), then name.
1. Not owned: `dlc_not_owned` cards. By name.
2. DLC-missing flagged: cards with the `missing_achievements_in_dlc` flag (and not DLC-not-owned). Higher `suggestion_score` first, then name. The newly-incomplete ordering of group 0 does not apply here.
3. No achievements: `total_achievements = 0`, by name. Takes priority over group 1 and 2, so a DLC-not-owned card with zero achievements sits here.

`lessByName` compares lowercased display names, then `EntryKey`, so the order is deterministic.

### Summary

`Summary` counts over `profile_games` joined with `games`, `game_sets`, `profile_game_flags` and `profile_owned_games`, for one profile. "Enabled" means `coalesce(f.disabled, 0) = 0`.

| Field | Definition |
| --- | --- |
| `CompletedSetsCount` | Enabled entries with `is_completed = 1` (base and DLC/named sets each count). |
| `CompletedGamesCount` | Distinct apps whose enabled `groupid 0` entry is completed. |
| `CompletedSetsVettedCount` | As `CompletedSetsCount`, restricted to apps with `vetted = 1` (missing owned row counts as vetted). |
| `CompletedGamesVettedCount` | As `CompletedGamesCount`, restricted to vetted apps. |
| `PurchasedGamesCount` | Distinct enabled apps with `profile_owned_games.purchased = 1`. |
| `OwnedGamesCount` | Distinct enabled apps that have at least one `profile_games` entry. This is not a count of the Steam library. |
| `GamesWithAchievementsCount` | As above, with `games.has_achievements = 1`. |
| `TotalAchievementsUnlocked` | Sum of `unlocked_achievements` over enabled entries whose `dlc_not_owned = 0`. |
| `TotalUnlockedCount` | Same expression as `TotalAchievementsUnlocked`; always equal. Both exported in JSON. |
| `TotalAchievementsAvailable` | Sum of `total_achievements` over enabled, owned entries. |
| `NewlyIncompleteGamesCount` | Count of enabled entries with `newly_incomplete = 1` (entries, not distinct games). |
| `OverallCompletionPercentage` | `TotalAchievementsUnlocked / TotalAchievementsAvailable * 100`, or `0` when available is 0. |

Entries of DLC the profile does not own are left out of the unlocked and available sums. A DLC or named set follows its base app's vetted state.

## Tag model

Tags are stored per entry in `profile_game_tags` and read into `GameCard.Tags`. They are derived data: `recomputeGameTagsTx` deletes the entry's rows and inserts a fresh set from the entry's facts. Tags are computed in `tags.go`.

### Stored tags

| Tag | Kind | Condition | `score` | `reason` |
| --- | --- | --- | --- | --- |
| `completed` | stat | `is_completed = 1` | | `completed` |
| `no_achievements` | stat | `total_achievements = 0` or `has_achievements = 0` | | `no achievements` |
| `untouched` | stat | `playtime_forever <= 0`; not for DLC-not-owned | | `0 playtime` |
| `almost_there` | stat | in progress and score `>= 40`; not for DLC-not-owned | score | completion, missing average, score text |
| `in_progress` | stat | not completed, has achievements, has playtime, score `< 40`; not for DLC-not-owned | score | same text |
| `new_achievements_added` | issue | not completed and `newly_incomplete = 1` | | `previously completed, now incomplete` |
| `missing_achievements_in_dlc` | issue | not completed and `missing_achievements_in_dlc` flag | | `manual DLC-missing flag` |
| `dlc_not_owned` | issue | `dlc_not_owned = 1` | | `DLC for this set is not owned` |
| `missing_cover_art` | issue | no cached cover file for the entry (after DLC fallback) | | `no cached cover image` |
| `sync_warning` | issue | `sync_warning = 1` | | `latest sync warning` |

Notes on the rules:

- The progress score is `suggestion_score` when set. Otherwise `completion*0.5 + missing_avg*0.5` when `missing_avg_unlock` is set, otherwise `completion*0.5`.
- Stat tags are skipped entirely when `sync_warning = 1` and `last_synced_at` is NULL (a placeholder that never synced).
- A DLC-not-owned entry gets only `completed`, `no_achievements` (when applicable) and the issue tags. No `untouched` or progress tag.
- Completed entries never get `new_achievements_added` or `missing_achievements_in_dlc`. The flags remain in storage.
- Disabled entries get no tags at all.
- `missing_cover_art` uses the same effective cover as display (see [covers.md](covers.md#display-url-and-dlc-fallback)).

The UI also shows **Set**, **DLC** and **Unvetted** on cards. These are not stored in `profile_game_tags`. The frontend (`gameTags` in `frontend/src/lib/tag.js`) derives them from the card fields: `set` when `groupId !== 0`, `dlc` when `dlcAppId` is set, `unvetted` when `unvetted` is true.

### When tags are recomputed

- `SaveGameSync`: each entry it writes.
- `SaveGameWarning`: each entry it touches.
- `MarkGamePreviouslyCompleted`, `ToggleMissingAchievementsInDLC`, `EnableProfileGame`: the affected entry (`RecomputeGameTags` or `recomputeGameTagsTx`).
- `SaveCover`: every entry whose image app id equals the saved id, plus every entry of the game with that id as its app id (`entriesForImageApp`, via `entriesForImageAppSQL`). An entry's image app id is the DLC id when its set is bound (`game_sets.dlcappid <> 0`), else the game id.
- `DisableProfileGame` and `DisableMissingOwnedGames`: delete tags directly.
- `pruneStaleEntries` and `ClearProfileData`: delete tags of removed entries.

### Ordering

`profileGameTags` reads each entry's tags in SQL order, fixed by a `CASE` expression:

`new_achievements_added` (0), `completed` (1), `almost_there` (2), `in_progress` (3), `untouched` (4), `no_achievements` (5), `missing_achievements_in_dlc` (6), `dlc_not_owned` (7), `missing_cover_art` (8), `sync_warning` (9). Unknown tags sort last (99), then by name.

This is only the backend read order. The frontend re-sorts card tags: `gameTags` in `frontend/src/lib/tag.js` uses `tagRank`, which puts `completed` first and then follows the `suggestionTagOptions` order. That order differs from the SQL order (for example `dlc_not_owned` precedes `missing_achievements_in_dlc` on a card). Display order is described in [frontend.md](frontend.md).

## Flags

| Flag | Column(s) | Set by | Effect |
| --- | --- | --- | --- |
| Previously completed (manual) | `manual_was_completed` | `MarkGamePreviouslyCompleted` (toggle) | Sets `was_completed` and `newly_incomplete` as in [Flag methods](#flag-methods); also read by `ProfileGame`. |
| Missing achievements in DLC | `missing_achievements_in_dlc` | `ToggleMissingAchievementsInDLC` (toggle) | `missing_achievements_in_dlc` tag (incomplete entries only) and `MissingDLC` on the card. Suggestion group 2. |
| Disabled (manual) | `disabled = 1`, `disabled_reason = 'manual'` | `DisableProfileGame` | Entry leaves Suggestions, Completed and Warnings; shown in Disabled. `ManualDisabled` true. Sync still runs for the app if another entry is enabled. |
| Disabled (not owned) | `disabled = 1`, `disabled_reason = 'not_owned'` | `DisableMissingOwnedGames` | As above. Re-enabled automatically when the app is owned again. |

`EnableProfileGame` clears `disabled` and `disabled_reason` for the entry regardless of reason. The not-owned sweep sets `not_owned` again on its next run if the app is still missing from the library.

## Covers (`covers.go`)

Repository side of the cover cache; the cover rules are in [covers.md](covers.md). `cached` and the effective cover id (`effectiveCoverAppIDSQL`, which applies the DLC fallback) decide the card's cover URL and `missing_cover_art`.

- `SaveCover(CoverUpdate) error`: rejects `ImageAppID <= 0`. One transaction upserts `cover_images`: `source_url` is stored as NULL when empty, `checked_at = now`, and `downloaded_at` is set only when `Downloaded` is true (`coalesce` keeps the previous value otherwise). Then recomputes tags for `entriesForImageApp`, which runs `entriesForImageAppSQL`: a UNION of every entry with app id = the image id and every entry bound to it as DLC (`game_sets.dlcappid`). Both branches use indexes (`idx_profile_games_appid`, `idx_game_sets_dlcappid`); the query plan tests in `covers_lookup_test.go` pin this.
- `GetCover(imageAppID) (CoverRecord, bool, error)`: `ok = false` when no row exists.
- `ListCovers() (map[int64]CoverRecord, error)`: test-only; no production caller.

## Settings notes and other queries

- `HasSetData(appID) (bool, error)` (`sets.go`): true when `game_sets` has a row with `groupid <> 0` for the app, or a `profile_games` entry with `groupid <> 0` exists for it (any profile). The syncer uses it to avoid the legacy single-entry fallback on apps that already have set data.
- Package-level SQL fragments used across files: `effectiveCoverAppIDSQL` (the id whose cover is displayed), `entryBaseFromSQL` (joins for entries).

