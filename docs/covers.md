# Covers

Developer reference for the game cover cache: the download logic in `internal/images` (`images.go`), the sync-side decision in `internal/syncer` (`refreshCover`), cover rows in `internal/repository` (`covers.go`, `cover_images`), display URLs and DLC fallback, and the root package's HTTP handler `CoverService` (`covers.go`).

Related: [syncer.md](syncer.md) (when refresh runs during a sync), [steam-client.md](steam-client.md) (owned-games `capsule_filename`), [repository.md](repository.md) (SQL and tags), [database.md](database.md) (`cover_images` columns), [app-service.md](app-service.md) (service wiring and `ClearActiveUserData`), [platform.md](platform.md) (paths, Windows file rules), [frontend.md](frontend.md) (how cards render the URL), [testing.md](testing.md).

## Components

| Piece | Location | Role |
| --- | --- | --- |
| `images.Cache` | `internal/images/images.go` | Fetches, validates and stores cover files. Holds `appdata.Paths` and an `http.Client` (30 s timeout). |
| `Cache.NeedsRefresh`, `RefreshCover`, `StoreItemAssets` | same | Staleness check, the cover fetch chain, and the store asset lookup. |
| `Service.refreshCover` and helpers | `internal/syncer/syncer.go` | Decides per image id whether to call `images` and what to persist. |
| `repository.SaveCover`, `GetCover`, `ListCovers` | `internal/repository/covers.go` | Reads and writes `cover_images`. `SaveCover` also recomputes tags. |
| `repository.coverURL` | `internal/repository/repository.go` (used by `dashboard.go`) | Builds the served URL. |
| `effectiveCoverAppIDSQL` | `internal/repository/repository.go` (used by `dashboard.go` and `tags.go`) | Picks the displayed cover (DLC fallback). |
| `CoverService` | root `covers.go` | `http.Handler` mounted at `/game-covers`; serves files from disk. |
| `removeCachedGameImages` | root `app.go` | Deletes cover directories of orphaned image ids when a profile is deleted. |

## Cover kinds and file names

There are four kinds. Each is stored in `cover_images.kind` and written to a fixed file name under `<GameImages>/<image_appid>/`.

| Kind | File name | Steam source |
| --- | --- | --- |
| `library` | `library_600x900.jpg` | `library_capsule` from the store, or `capsule_filename` from the owned-games list (base games) |
| `hero` | `hero_capsule.jpg` | `hero_capsule` from the store |
| `header` | `header.jpg` | `header` from the store |
| `main` | `main_capsule.jpg` | `main_capsule` from the store |

The `header` and `main` capsules are landscape images; the UI renders these kinds differently (see the comment in `images.go`).

The file name is fixed per kind. The file always uses the `.jpg` extension, even when Steam serves PNG or WebP (see [Validation](#download-validation)). `''` as kind and file name means "checked, no cover". The file name set is exported as `images.LibraryFileName`, `HeroFileName`, `HeaderFileName`, `MainFileName`. `CoverService` whitelists exactly these four.

`images.KindLibrary`, `KindHero`, `KindHeader`, `KindMain` are the kind constants.

## Constants

| Constant | Value | Meaning |
| --- | --- | --- |
| `CoverRefreshInterval` | 30 days | A found cover (any kind) is trusted for this long before a recheck. |
| `MissingCoverRecheckInterval` | 7 days | A check that found no cover (kind `''`) is trusted for this long. |
| `maxCoverBytes` | 5 MiB (`5 << 20`) | Largest accepted cover download. |
| HTTP client timeout | 30 s | `images.New` client. |
| `storeItemsBatchSize` | 100 | Max app ids per store request. The syncer passes one id at a time. |
| Store request attempts | 4 | Attempts 0 to 3. |
| Store retry backoff | 300 ms, 600 ms, 1200 ms | Before attempts 1, 2, 3 (`storeItemsRetryDelay`). Cancelled by context. |
| Store response read limit | 4 MiB | Body limit for store responses. |
| Store `country_code` | `US` | Fixed in the store request. |

Asset hosts (`images.go`):

- Primary: `https://shared.akamai.steamstatic.com/store_item_assets/`
- Fallback: `https://shared.steamstatic.com/store_item_assets/`
- Store lookup: `https://api.steampowered.com/IStoreBrowseService/GetItems/v1/`

## Sync-side decision (`refreshCover`)

Runs after a successful game sync: for a game without achievements only the game id (`saveGameWithoutAchievements`, `syncer.go`); otherwise `coverImageAppIDs` (the game id, then each distinct DLC id). Nothing runs when `Service.Images` is nil (`syncer.go`). Failures are logged (`refresh cover <id>: ...`) and never fail the game sync.

1. Read the stored state (`storedCover` -> `images.Stored{Kind, FileName, CheckedAt}`). `CheckedAt` is parsed from RFC3339; an unparseable value counts as never checked.
2. `NeedsRefresh(imageAppID, prev, now)`. If false, return with no network I/O.
3. Base game with an owned-games capsule (`imageAppID == game.AppID && game.CapsuleFilename != ""`):
   - `RefreshCover` with `ItemAssets{AppID, LibraryCapsule: capsule_filename}` and no store lookup.
   - If it downloaded, save and return.
   - If it returned an error, return the error. No store lookup, nothing saved.
   - Otherwise fall through to step 4 (the owned capsule was not found or no file was downloaded).
4. Store lookup: `lookupItemAssets` -> `StoreItemAssets([id])`. A missing item gives empty assets. An error returns the error (nothing saved). Then `RefreshCover` with those assets and `prev`. If it returns an error, or a result with `Skipped` set, nothing is saved (`if err != nil || res.Skipped { return err }`). `Skipped` is a defensive guard: the same staleness check already ran in step 2, so it is practically unreachable. Otherwise `saveCover`.

Thus, a base game with a working owned-games capsule requires no store request. A stale DLC cover always requires one store request. A base game with no capsule, or whose capsule URL is not found, requires one store request.

`saveCover` maps `images.Result` to `repository.CoverUpdate` (`Kind`, `FileName`, `Cached` set from `Result.Path != ""`, `SourceURL`, `Downloaded`). `Result.Path` stays in memory and is never persisted. `SaveCover` always writes `checked_at = now`, and writes `downloaded_at = now` only when `Downloaded` is true.

Note: when the owned capsule is not found but a cached file exists, `RefreshCover` keeps that file and returns `Downloaded = false`. The syncer then still performs the store lookup and a second `RefreshCover`.

## Staleness (`NeedsRefresh`)

Returns true (refresh required) in these cases, in order:

1. Never checked: `CheckedAt` is zero.
2. Stored kind is `''` and the last check is at least `MissingCoverRecheckInterval` (7 days) old.
3. Stored kind is set and the last check is at least `CoverRefreshInterval` (30 days) old.
4. Stored kind does not match the kind implied by the stored file name (`kindForFileName`).
5. The stored file is missing from disk, or is not a regular file.

Otherwise it returns false. A "not found" result is rechecked after 7 days, and a found cover after 30 days or when the file goes missing.

`Stored` holds only `Kind`, `FileName` and `CheckedAt`; `source_url` and `downloaded_at` are not inputs. A cover whose source changed on Steam is not refreshed until the 30-day window ends.

## Fetch chain (`RefreshCover`)

The chain tries candidates in priority order (`coverCandidates`): library, hero, header, main. A candidate is tried only when Steam lists that asset (non-empty value); candidates without a listed value produce no URLs and are skipped. For each listed candidate, URLs are tried in order: primary host, then fallback host. The first valid image response wins.

Steps:

1. `imageAppID <= 0` is an error (`invalid image app id`). No request is made.
2. `NeedsRefresh` false: return the stored state with `Skipped = true`, `Path` pointing at the existing file name. No network I/O.
3. `fetchFirstCover`: for each candidate and URL, `fetchCover`. The result is one of:
   - Valid image response (2xx, allowed content type, length within limit): use it.
   - 404: `(nil, nil)`, try the next URL.
   - Other non-2xx, transport error, bad content type, or `Content-Length` over the limit: error, remember it, try the next URL.
4. No response found:
   - If a cover file already exists for this image id (`existingCover`: the stored file name first, then every known file name, checked as regular files on disk), return it as kept, with `Downloaded = false`, `CheckedAt = now`, `SourceURL` = last URL tried.
   - Else if any error was recorded, return that error (and the last source). The caller saves nothing, so the check is retried on the next sync.
   - Else return an empty result with `CheckedAt = now` and no error. This records "checked, no cover".
5. Response found: `writeCover` (below). Then `removeOtherCovers` deletes the other three known file names for this id. Result: kind and file name of the candidate, `Path`, `SourceURL`, `Downloaded = true`, `Changed` when kind or file name differs from the input. `Changed` is informational; the syncer does not read it.

The “keep the previous cover on failure” rule means a transient failure does not blank a previously downloaded cover. Only a non-found result with no file on disk records an empty state.

## Asset URLs and mirrors

`assetURL(assets, file)` builds the primary URL:

- Format: `assets.AssetURLFormat` (Steam's `asset_url_format`, leading `/` trimmed). When Steam sent none, the default `steam/apps/<appid>/${FILENAME}` is used.
- `${FILENAME}` is replaced by the listed capsule value. The format must contain the placeholder.
- Both the format and the file value are rejected (empty URL, no candidates) when they contain a backslash, `://`, or a `..` path segment (`unsafeAssetPath`).
- Result: `primaryCoverURLBase + <format with file>`.

`capsuleCandidateURLs` returns `[primary, fallback]` from `mirrorSourceURLs`. The fallback is the same path on `shared.steamstatic.com`. Duplicate URLs are dropped (`uniqueStrings`).

The owned-games capsule uses the default format: `primary/steam/apps/<appid>/<capsule_filename>`. Store lookups use Steam's own `asset_url_format` and the listed capsule value.

The app never guesses file names. It requests only the owned-games `capsule_filename` (trimmed by the Steam client) and store-listed assets.

## Store item lookup (`StoreItemAssets`)

- Input ids are de-duplicated, and ids `<= 0` are dropped. Ids are split into batches of 100.
- Request: `GET https://api.steampowered.com/IStoreBrowseService/GetItems/v1/?input_json=<json>`. The same form-encoded `input_json` is also sent as the request body with `Content-Type: application/x-www-form-urlencoded`. The JSON asks for `ids`, `country_code: "US"` and `include_assets: true`.
- Retry: transport errors, HTTP 429, 500 and 503 are retried, up to 4 attempts in total. Other non-2xx responses are not retried. A request-build error and a response body read error are not retried (`doStoreItemsRequest` returns `retry = false` for them). A JSON decode error happens in `fetchStoreItemAssets` after a successful request and is returned at once, without retry.
- Items with `success != 1` are omitted. `appid` falls back to `id` when zero.
- A batch error returns the partial map collected so far along with the error.

The syncer uses only single-id calls, so the batch behaviour is not exercised in production.

## Download validation

Applied to every candidate response in `fetchCover`, before the body is read:

- Status: 2xx accepted; 404 is "not here" (the next URL is tried); any other status is an error. Redirects are followed by the default client.
- Content type: after lowercasing and stripping parameters, must be exactly `image/jpeg`, `image/png` or `image/webp`. Anything else is an error. The body is not sniffed.
- `Content-Length` over `maxCoverBytes` is an error. A missing or unknown length (`-1`) is not rejected before the read; the read limit in `writeCover` applies.

While writing (`writeCover`):

- Read at most `maxCoverBytes + 1` bytes. More than `maxCoverBytes` is an error. This covers chunked responses with no declared length.

Failures discard the body. No magic-byte check exists: the chosen kind and file name are the only type information, and the file is always named `.jpg`.

## Atomic write and permissions

`writeCover(imageAppID, fileName, body)`:

1. `MkdirAll(<GameImages>/<id>, 0700)`.
2. Create `<file>.tmp` with `O_WRONLY|O_CREATE|O_TRUNC`, mode `0600`.
3. Copy the limited body into it. Close the file.
4. On copy, size or close error: `fsretry.Remove` the temp file and return the error. The temp file is removed after `Close` because Windows cannot delete an open file.
5. `fsretry.Rename(tmp, final)`. The final file replaces any earlier file of the same name. The temp file is removed if the rename fails.
6. `Chmod 0600` on the final file (best effort; Windows ignores it).

There is no `fsync` before the rename. A crash between create and rename can leave `<file>.tmp` behind. Nothing removes leftover temp files; the cover tests check that none remain after failed downloads.

Directories: `appdata.Ensure` creates `GameImages` with mode `0700` at startup. `writeCover` creates per-id directories with `0700`.

`fsretry` (`Rename`, `Remove`, `RemoveAll`) retries briefly on Windows sharing and lock errors, because the cover server may still hold a file open. Elsewhere it is one OS call.

## Cache layout

Default paths (`appdata.Resolve`):

- Linux: `~/.cache/steam-achievement-tracker/images/games/`
- Windows: `%LocalAppData%\cache\steam-achievement-tracker\images\games\`
- Overrides: `XDG_CACHE_HOME` on both platforms.

Under that root: `<appid>/<file>`, with the file names above. The directory name is the image app id, so a DLC set's cover sits under the DLC id, and a base game's under the game id. A `.tmp` file is transient.

The cache is disposable. Deleting it removes the files; the next sync downloads them again. The `cover_images` rows still point at the missing files, so `NeedsRefresh` sees the missing file (rule 5) and refetches.

## Database state

`cover_images` (see [database.md](database.md#cover_images)): one row per image app id, with `kind`, `file_name`, `cached`, `source_url`, `checked_at`, `downloaded_at`. `SaveCover` upserts it and recomputes tags for the entries returned by `entriesForImageApp` (owning image id equal to the saved id, plus every entry of the game with that app id).

- `cached` is `1` when a file was returned by `images` at save time (`images.Result.Path` non-empty). Only that flag is used for display and tags; the absolute path is never stored (the database must not carry the OS user name). The served URL is built from `file_name`. Before migration 2 the column was `path`; it is dropped there (see [database.md](database.md#migrations)).
- `downloaded_at` changes only on a new download. It is the cache-busting value in the URL.
- A kept file or a "not found" result does not change `downloaded_at`.

## Display: URL and DLC fallback

Repository (`coverURL`): empty when `cached` is `0`, `file_name` is empty, or the image id is `<= 0`. Otherwise:

```text
/game-covers/<image_appid>/<file_name>[?v=<downloaded_at>]
```

The query string is added only when `downloaded_at` is set.

Which image id is displayed for an entry (`effectiveCoverAppIDSQL`):

- Non-DLC entries (`game_sets.dlcappid = 0` or no set row): the game's id.
- DLC-bound entries: the DLC id, when `cover_images` has a row for it with `cached = 1`. Otherwise the game's id.

So a DLC set without its own cover shows the parent game's cover, and a DLC cover saved later takes over at the next read. The `missing_cover_art` tag uses the same rule, so a DLC set counts as covered when its parent is.

When the displayed image URL is empty, `GameCard.svelte` renders a placeholder: the first letter of the game title and a small **Missing cover** label. The image is loaded with `loading="lazy"`.

Ownership for refresh and orphan cleanup uses a different rule (`imageAppIDSQL`): the DLC id for DLC-bound entries, else the game id. This decides which cover row an entry writes to, not what it shows.

## Serving (`CoverService`)

`CoverService` (root package) is a Wails service with `Route: "/game-covers"`, registered in `main.go`. It is not bound to the frontend. Wails strips the route prefix before calling `ServeHTTP`, so the request `/game-covers/<appid>/<file>` reaches the handler as `/<appid>/<file>`.

`ServeHTTP` rules:

- Only `GET` is served. Other methods get 404 (`http.NotFound`).
- The path must start with `/`. Otherwise 404. This guards against the route matching `/game-covers123/...` by raw prefix.
- Exactly two path segments: `<appid>/<file>`. `<file>` must be one of the four cover file names (`servableCoverFile`).
- `<appid>` must parse as a positive integer. Otherwise 404.
- The cache root is read under the app mutex from the app's paths; if it is empty, `appdata.Resolve()` is used.
- The file is served with `http.ServeFile` (Content-Type from the extension, Last-Modified, Range support). No `Cache-Control` header is set. Missing files give 404 from `ServeFile`.

Because the file name is whitelisted and the id is numeric, the handler cannot reach outside `<GameImages>/<id>/`.

## Deletion

- `ClearProfileData` deletes `cover_images` rows that no remaining profile entry displays, and returns their ids. A row is kept when another entry of any profile still has the id as its displayed or owning image, or when the game's own id is the entry's app (so DLC entries that fall back still keep the game cover). See [repository.md](repository.md#profile-removal).
- `ClearActiveUserData(deleteProfileData=true)` then calls `removeCachedGameImages(GameImages, ids)`, which `RemoveAll`s `<GameImages>/<id>` for each returned id (skipping ids `<= 0`). Failure is returned to the caller, after the database change has already committed.
- `DeactivateProfile` (Remove profile with data kept) deletes nothing from the cache.
- `ids` whose directory exists but have no `cover_images` row are not removed by this path.

## Failure behaviour

| Situation | Result | Stored state |
| --- | --- | --- |
| Owned capsule 404, store finds nothing, no file | Kind `''`, `checked_at` now | Saved. Recheck after 7 days. |
| Any candidate download error, nothing on disk | Error returned | Nothing saved. Retried next sync. |
| Store lookup error (transport, decode, non-retried status) | Error returned | Nothing saved. Retried next sync. |
| Download error but a previous file exists | Previous file kept (`Downloaded = false`) | `checked_at` updated, kind and file kept. |
| Download succeeds but local write fails | Error from `writeCover` | Nothing saved (`RefreshCover` returns the error, the syncer does not save). |
| Stored file deleted from disk | `NeedsRefresh` true | Refetched on next sync. |
| Oversized or wrong content type | Candidate skipped as error | As for any candidate error. |
| Cancelled sync context, no file on disk | Context error | Nothing saved. |
| Cancelled sync context, previous file on disk | Context error from the store lookup | Nothing saved. |

Cover failures are logged by the syncer and never fail a game sync or the sync run status. Log lines pass the error through `redact.Paths` (`internal/redact`), which reduces the file paths of `*fs.PathError` and `*os.LinkError` to base names, so the log does not carry the user's profile directory. The cache-removal error from `removeCachedGameImages` (app.go) has its cause reduced by `redact.Paths` before it is wrapped. The bound method that returns it (`ClearActiveUserData`) logs the error once through `userFacingError` (`app_errors.go`), and the UI gets only the generic sentence, never the wrapped text. Use `redact.Paths` for any new log line or UI message that can carry a cache or image file error.

## Notes

- `mirrorSourceURLs` rewrites `cdn.cloudflare.steamstatic.com` URLs. Asset URLs always begin with the akamai base (`assetURL`), so that rewrite never matches in production. The fallback host is derived from the same path.
- The store retry set (`storeItemsRetryStatus`: 429, 500, 503) is the same set as the Steam client's `retryableSteamStatus`. The syncer's `transientSteamStatus` (429, 500, 502, 503, 504) is not a retry set. It only decides whether a `GetGameAchievements` failure may fall back to the legacy API.
- `ItemAssets.Type` and `ItemAssets.ParentAppID` are filled by `storeItemsAssets` but nothing in production reads them. Only tests check them.
