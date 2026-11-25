# Steam client (`internal/steam`)

`internal/steam` is the only package that calls the Steam Web API. It wraps the endpoints used by the app, decodes their JSON, classifies failures, and keeps the Steam Web API key out of error text. It contains no database or image code. Store and CDN asset requests live in `internal/images` (see [Store and asset URLs](#store-and-asset-urls)).

Callers: `syncer.Service` (see [syncer.md](syncer.md)) for library, schema, and unlock data, and `app.go` for profile validation (`ResolveProfileInput` followed by `GetPlayerSummary`). The package is internal; the frontend never calls it directly. See [architecture.md](architecture.md) for its place in the architecture.

Source: `internal/steam/client.go`. Tests and fixtures: `internal/steam/client_test.go`, `internal/steam/testdata/`.

## Client type and construction

```go
type Client struct {
    APIKey     string
    BaseURL    string
    HTTPClient *http.Client

    SkipUnvettedApps       bool // skip_unvetted_apps=true on library requests
    ExcludePlayedFreeGames bool // include_played_free_games=false on library requests

    retryBackoff func(attempt int) time.Duration // unexported; see SetRetryBackoff
    retrySleep   func(ctx context.Context, d time.Duration) error // unexported; nil waits on a timer, tests replace it
}
```

- `New(apiKey string) *Client` returns a client with `BaseURL` set to `https://api.steampowered.com` and an `HTTPClient` with a 30-second timeout.
- The zero value of `SkipUnvettedApps` and `ExcludePlayedFreeGames` means "include": unvetted apps and played free games are requested by default. `app.go` (`newSteamClient`) sets them per profile from the library settings, inverted: `SkipUnvettedApps = !IncludeUnvettedApps`, `ExcludePlayedFreeGames = !IncludePlayedFreeGames`. See [app-service.md](app-service.md).
- `SetRetryBackoff(fn func(attempt int) time.Duration)` replaces the delay before each retry. `attempt` is 1-based. A `nil` fn restores the default. Production code never calls it; tests use it to avoid sleeping.

## Exported API

Every method takes a `context.Context` first. Requests use that context, so cancellation aborts the HTTP call and any pending retry wait.

| Method | Endpoint (path under `BaseURL`) | Parameters sent | Returns | Notes |
| --- | --- | --- | --- | --- |
| `ResolveProfileInput(ctx, input)` | see [Profile resolution](#profile-resolution) | depends on input | `(steamID64 string, error)` | Accepts a SteamID64, a profile URL, a vanity URL or a bare vanity name. Input holding the API key is refused before any request. |
| `ResolveVanityURL(ctx, vanity)` | `/ISteamUser/ResolveVanityURL/v1/` | `key`, `vanityurl` | `(string, error)` | Error is `No Steam profile matches that custom name.` when Steam finds no match; Steam's `message` is the log cause only. A name holding the API key is refused before any request. |
| `GetPlayerSummary(ctx, steamID64)` | `/ISteamUser/GetPlayerSummaries/v2/` | `key`, `steamids` | `(PlayerSummary, error)` | Error `No Steam profile was found for that account.` when no player is returned. |
| `GetOwnedGames(ctx, steamID64)` | `/IPlayerService/GetOwnedGames/v0001/` (up to 3 calls) | see [Library fetch](#library-fetch-getownedgames) | `([]OwnedGame, error)` | Combines the full, purchased and vetted lists. |
| `GetGameAchievements(ctx, appID)` | `/IPlayerService/GetGameAchievements/v1/` | `key`, `appid`, `language=english`, `format=json` | `(GameAchievements, error)` | Uses `x-eresult`. EResult 42 returns an empty value and no error. |
| `GetUserAchievements(ctx, steamID64, appID)` | `/IPlayerService/GetUserAchievements/v1/` | `key`, `steamid`, `appid`, `format=json` | `(UserAchievements, error)` | Uses `x-eresult`. EResult 42 returns empty maps and no error. |
| `GetSchemaForGame(ctx, appID)` | `/ISteamUserStats/GetSchemaForGame/v2/` | `key`, `appid`, `l=english`, `format=json` | `([]SchemaAchievement, error)` | Legacy. Drops entries with an empty name. |
| `GetPlayerAchievements(ctx, steamID64, appID)` | `/ISteamUserStats/GetPlayerAchievements/v1/` | `key`, `steamid`, `appid`, `l=english`, `format=json` | `([]PlayerAchievement, error)` | Legacy. When `success` is false and no achievements came back, the error is the sentence `Steam did not provide achievements for this game.` (Steam's `error` text is its cause, logged only), or `Steam did not return achievements for this game. Try again later.` when Steam sent no error text. `APIName` falls back to `name` when `apiname` is empty. |
| `GetGlobalAchievementPercentages(ctx, appID)` | `/ISteamUserStats/GetGlobalAchievementPercentagesForApp/v0002/` | `gameid`, `format=json` (no key) | `(map[string]float64, error)` | Legacy. Keyed by achievement API name. |

“Legacy” refers to the pre-IPlayerService path, used only as a syncer fallback (see [syncer.md](syncer.md#per-game-refresh)). Planned for removal with the legacy sync path, once Steam documents `GetGameAchievements`.

## Data sources

This table maps each data type to its source endpoint. Parameters are listed in the table above and in the sections below.

| Data | Endpoint | Key sent | Notes |
| --- | --- | --- | --- |
| Profile name and avatar | `ISteamUser/GetPlayerSummaries/v2` | yes | Used during profile validation and stored as `profiles.avatar_url`. |
| Vanity name to SteamID64 | `ISteamUser/ResolveVanityURL/v1` | yes | Used for vanity names and `/id/<name>` URLs. |
| Owned games, names, playtime, library capsule file name, purchased and vetted status | `IPlayerService/GetOwnedGames/v0001` (up to three calls) | yes | Unvetted apps are included unless the profile's **Include unvetted apps** setting is off, and played free games follow **Include played free games**. Any failed or unusable answer fails the run before a game is synced. See [Library fetch](#library-fetch-getownedgames). |
| Achievement schema, sets, global unlock percentages | `IPlayerService/GetGameAchievements/v1` | yes | The main path. One call returns the whole schema of an app, including each achievement's `player_percent_unlocked`. |
| Unlocked achievements of the profile | `IPlayerService/GetUserAchievements/v1` | yes | Main path. Works for the key owner's own profile at any privacy setting. |
| Legacy schema, unlock and percentage data | `ISteamUserStats/GetSchemaForGame/v2`, `ISteamUserStats/GetPlayerAchievements/v1`, `ISteamUserStats/GetGlobalAchievementPercentagesForApp/v0002` | yes (the percentages call sends none) | Fallback only, when `GetGameAchievements` fails with an HTTP status other than 429/500/502/503/504, or with an EResult error, and the game has no stored set data (`Repo.HasSetData` is false; see [syncer.md](syncer.md#per-game-refresh)). Unlike the main path, `GetPlayerAchievements` answers HTTP 403 `Profile is not public` when the profile is private, so for a private profile a fallback game that has achievements fails and appears in Sync errors. |
| Store item assets (library, hero, header and main capsule file names) | `IStoreBrowseService/GetItems/v1` | no | One app per call. Only for stale DLC covers and for base games without a usable owned-games capsule. See [covers.md](covers.md). |
| Cover image files | `https://shared.akamai.steamstatic.com/store_item_assets/` (primary) and `https://shared.steamstatic.com/store_item_assets/` (fallback) | no | Downloaded to the cover cache. See [covers.md](covers.md). |
| Steam avatar image | The `avatarfull` (or `avatarmedium`) URL from `ISteamUser/GetPlayerSummaries/v2` | no | Not fetched by the Go client. The webview loads it as an `<img>` in the profile card. The URL is stored in `profiles.avatar_url`. |

No data is sent to non-Steam services.

### Public types

- `PlayerSummary{SteamID64, DisplayName, AvatarURL}`. `AvatarURL` is `avatarfull`, falling back to `avatarmedium`.
- `OwnedGame{AppID, Name, PlaytimeForever, CapsuleFilename, Purchased, Unvetted}`. `Name` is trimmed; an empty name becomes `App <appid>`. `CapsuleFilename` is the trimmed `capsule_filename` (relative to `steam/apps/<appid>/`, may be empty).
- `SchemaAchievement{APIName, Name}` (legacy).
- `PlayerAchievement{APIName, Achieved, UnlockTime *int64}` (legacy). `UnlockTime` is nil when Steam sends 0.
- `AchievementSet{GroupID, Name, DLCAppID, Order, Archived, DeveloperOnly, Public, TotalAchievements, CompletionAchievements}`. `GroupID` 0 is the base set. `Name` is `localized_name`. `Public` defaults to true when Steam omits `ispublic`.
- `SetAchievement{InternalName, Key, GroupID, Archived, GlobalPercent *float64}`. `Key` is `internal_key`, the id used by `GetUserAchievements`. `GlobalPercent` is the schema's `player_percent_unlocked` (global unlock percentage), nil when absent.
- `GameAchievements{Achievements []SetAchievement, Sets []AchievementSet, SchemaVersion int}`. Achievements without an `internal_name` are dropped.
- `UserAchievements{Unlocked map[int64]*int64, Groups map[int64]bool}`. `Unlocked` maps `internal_key` to unlock time (nil when 0). Locked achievements are absent from the response. `Groups` lists the groupids present in the user response.

### Error types

- `*HTTPStatusError{Path, Code, Status}` for non-2xx responses. `Code` is the HTTP status code; callers classify with `errors.As`, not by message. `Error()` is `Steam API <path> returned <status>`. For non-retryable statuses the trimmed body is appended, cut to 4 KiB (`errorBodyLogLimit`) after key and ID redaction (see [Key and ID redaction](#key-and-id-redaction)). Retryable statuses (429, 500, 502, 503, 504) carry no body text.
- `*EResultError{Path, Code}` for an `x-eresult` header other than 0, 1 or 42.

Other errors are `usererr` values (`internal/usererr`; `Error()` is the text, and `usererr.New` builds them): the app's own user-facing messages, for example `Enter a Steam profile URL, SteamID64 or custom name.`, `No Steam profile was found for that account.`, and `Steam did not return your game list. Your profile's game details may be private.`. Steam's own `message` and `PlayerStats.error` texts are never user-facing: they are wrapped with `usererr.Wrap`, the error text is a fixed sentence (`No Steam profile matches that custom name.`, `Steam did not provide achievements for this game.`), and Steam's text is the cause, kept for the log only (`usererr.LogText`, then `steam.RedactSecrets` when the app logs it). A decode failure is `*badResponseError` (its text is `decode Steam API <path>: ...`, and it unwraps to the decoder error), and an invalid header is also `*badResponseError` (`Steam API <path> returned invalid x-eresult "<value>"`). A failed body read is `*bodyReadError`. Its text is the underlying read error with the key and IDs redacted, and it unwraps to the original error.

### User-facing messages

`steam.UserMessage(err)` (`usermessage.go`) turns an error into one plain sentence for the UI and for stored display text. It finds the typed cause with `errors.As`, so wrapping prefixes such as `achievement schema: ` are dropped. It is an allowlist: only the typed causes below and the app's own user-facing errors (`usererr`) get their text. Any other error gets one generic sentence, so no raw text reaches the UI. Mapping:

| Cause | Message |
| --- | --- |
| `HTTPStatusError` 401 or 403, on `ResolveVanityURL`, `GetPlayerSummaries` or `GetOwnedGames` | Steam rejected the Web API key. Check that the key is correct and still valid, then replace it in Settings. |
| `HTTPStatusError` 403 with a key-check body (`verify your` and `key=`) on any endpoint; a 401 with the same body is not this rule | Same as above. |
| `HTTPStatusError` 401 on `GetUserAchievements`, `GetPlayerAchievements`, `GetGameAchievements` or `GetSchemaForGame` | Same as above. |
| `HTTPStatusError` 403 on `GetUserAchievements` or legacy `GetPlayerAchievements` | Steam did not share achievements for this game. The profile's game details may be private. |
| `HTTPStatusError` 403 on `GetGameAchievements` or `GetSchemaForGame` | Steam refused this request. |
| `HTTPStatusError` 401 or 403 on `GetGlobalAchievementPercentagesForApp` (sends no key) | Steam refused this request. |
| `HTTPStatusError` 401 on any other path | Steam rejected the Web API key. Check that the key is correct and still valid, then replace it in Settings. |
| `HTTPStatusError` 403 on any other path | Steam refused this request. |
| `HTTPStatusError` 400 | Steam could not process the request. |
| `HTTPStatusError` 404 | Steam has no data for this request. |
| `HTTPStatusError` 429 | Steam is limiting requests right now. Try again in a few minutes. |
| `HTTPStatusError` 5xx | Steam is temporarily unavailable. Try again later. |
| other `HTTPStatusError` | Steam returned an unexpected response. The status code is in the log only. |
| `EResultError` | Steam could not provide this data right now. |
| `badResponseError` (body does not decode, or `x-eresult` is not an integer) | Steam sent a response the app could not read. |
| `context.Canceled` (also inside a `*url.Error`) | The request was canceled. |
| `context.DeadlineExceeded`, `*url.Error`, `net.Error`, body read failure (`bodyReadError`) | Could not reach Steam. Check your internet connection and try again. |
| `usererr` (input checks, `No Steam profile was found for that account.`, the `checkOwnedGamesList` messages, the achievements sentence) | Its own text, unchanged. |
| anything else | Something went wrong. See the log file for details. |

Endpoint names in the table are the `Interface.Method` part of the path, so the version segment (`v1`, `v0001`, ...) does not matter.

The mapper never includes an endpoint path, a status line, a body, an eresult, a URL or the key. The raw error text is still logged, and the typed errors keep their `Error()` text, so `errors.As` classification (for example the legacy fallback in `syncer`) is unchanged. Every error that reaches the UI or is stored for display passes through `UserMessage`: the App boundary (`userFacingError`, see [app-service.md](app-service.md#error-boundary)) for bound methods, the per-game warning written by `syncer` (`recordGameFailure`), and the run error of `fail`. A cancelled run stores the same sentence, `The request was canceled.`: the mapper gives it for `context.Canceled` inside a `*url.Error` too, so the request address is never stored.

## Endpoints

All requests use `GET`, with the query string built by `url.Values.Encode()`. The key is sent as a query parameter named `key` except for `GetGlobalAchievementPercentages`. Trailing `/` characters are trimmed from `BaseURL`.

## Profile resolution

`ResolveProfileInput` turns user input into a SteamID64 in this order:

1. Trim whitespace. Empty input returns `Enter a Steam profile URL, SteamID64 or custom name.`
2. API-key guard. If the value contains the API key, return `This looks like your Steam Web API key. Enter your profile URL, SteamID64 or custom name instead.` No request. The match ignores case and is skipped when the key is shorter than 8 characters (`minGuardedKeyLen`).
3. If the value matches `^\d{17}$`, return it unchanged. No request.
4. A scheme-less Steam community link (input starting with `steamcommunity.com/` or `www.steamcommunity.com/`, case-insensitive) gets `https://` prepended. Then, if `url.Parse` succeeds and yields a host, scan the path segments in order:
   - `profiles/<17 digits>` returns that id. A `profiles/` segment not followed by 17 digits does not match, and the scan continues.
   - `id/<vanity>` returns the result of `ResolveVanityURL` for that segment. `ResolveVanityURL` repeats the key guard on the decoded name. An `id/` segment with nothing after it does not match, and the scan continues.
   - If no segment matches, the error is `Enter a profile link like steamcommunity.com/profiles/<number> or steamcommunity.com/id/<name>.`.
5. Otherwise the whole trimmed value is treated as a vanity name and sent to `ResolveVanityURL`.

`ResolveVanityURL` succeeds only when `response.success == 1` and a `steamid` is returned. Otherwise it returns `No Steam profile matches that custom name.`. When Steam sent a `message` (for example `No match`), it is kept, redacted like any message, as the log cause (`usererr.LogText`, then `steam.RedactSecrets` when the app logs it); the UI gets only the sentence. The sentence does not echo the typed input, so a SteamID64 typed as an `/id/` value or a mistyped name never reaches the error text or the UI.

Note: `steamcommunity.com/...` and `www.steamcommunity.com/...` links work without a scheme (step 4). Any other input without a scheme, such as `example.com/id/name`, has no host to `url.Parse`, so it is sent to `ResolveVanityURL` as a literal vanity name and fails. The API-key guard (step 2) runs on the raw input before the scheme is added.

Edge case: empty path segments are not skipped. `https://steamcommunity.com/id//name` yields an empty segment after `id`, so `ResolveVanityURL` is called with an empty vanity name. No client-side check rejects it; the outcome is whatever Steam returns for that request.

## Library fetch (`GetOwnedGames`)

Up to three `GetOwnedGames` requests are made per call. All use `include_appinfo=true`, `include_extended_appinfo=true` (this adds `capsule_filename`), `format=json`, and `steamid`. `language` is deliberately not sent, because with a language Steam returns localized art file names for some games.

| List | `include_played_free_games` | `skip_unvetted_apps` | Made when | Sets |
| --- | --- | --- | --- | --- |
| Full | `!ExcludePlayedFreeGames` | `SkipUnvettedApps` | always | the game list and the `Name`, `PlaytimeForever`, `CapsuleFilename` fields |
| Purchased | `false` | `SkipUnvettedApps` | only when `ExcludePlayedFreeGames` is false | `OwnedGame.Purchased` |
| Vetted | `!ExcludePlayedFreeGames` | `true` | only when `SkipUnvettedApps` is false | `OwnedGame.Unvetted` (`true` when the app is not in this list) |

Combination rules:

- With `ExcludePlayedFreeGames` set, the full list already is the purchased list, so the purchased request is skipped and every game is `Purchased`.
- With `SkipUnvettedApps` set, the library already excludes unvetted apps, so the vetted request is skipped and no game is `Unvetted`.
- Any failing request fails the whole call. Nothing is partially returned.
- `checkOwnedGamesList` rejects unusable answers. A response with neither `games` nor `game_count` is unusable (`Steam did not return your game list...`, `Steam did not return your purchased games...`, `Steam did not return the list of reviewed games...`). A non-zero `game_count` with no games is incomplete (`Steam returned an incomplete game list...`, and the purchased and reviewed variants). A `game_count` of 0 with no games is a valid empty list. This stops a `{"response":{}}` from reading as "nothing purchased" or "nothing vetted".

The returned slice follows the full list's order. Each game's flags come from set membership in the purchased and vetted lists.

## Achievement schema and user state

`GetGameAchievements` (`/IPlayerService/GetGameAchievements/v1/`) returns the whole schema for an app in one response: `schema_version`, an `achievements` array (each with `internal_name`, `internal_key`, `groupid`, `archived`, `player_percent_unlocked`) and a `groups` array (set metadata: `groupid`, `localized_name`, `dlcappid`, `archived`, `developeronly`, `order`, `ispublic`, `total_achievements`, `completion_achievements`).

- Groups are returned as `AchievementSet` values as-is. Set filtering (public, developer-only, archived, empty) is not done here; it is done in the syncer.
- Achievements are returned in the order Steam sends them. Grouping by `GroupID` happens in the syncer.
- `DLCAppID` is non-zero for a DLC-bound set. The client does not check whether the profile owns that DLC; ownership is inferred from the user response (see below).

`GetUserAchievements` (`/IPlayerService/GetUserAchievements/v1/`) returns only unlocked achievements (`internal_key`, `unlocked`, `unlock_time`) and the set of groupids present in the response. A locked achievement is simply absent. A groupid is present in `Groups` when Steam lists it, which the syncer uses for DLC ownership.

### x-eresult handling

For the two IPlayerService achievement endpoints, the client reads the `x-eresult` response header:

- missing or `0`, or `1`: the body is decoded normally;
- `42` (no match): the body is not decoded; the method returns empty values and no error;
- any other value: `EResultError`, not retried;
- a header that is not an integer: `Steam API <path> returned invalid x-eresult "<value>"`.

The syncer treats an EResult 42 on `GetGameAchievements` as an empty schema (game has no achievements). It treats the same code on `GetUserAchievements` as no unlocks, so the entry is written with zero progress.

### Flexible JSON values

The Steam payloads mix numbers and strings. Unexported helper types decode them:

- `flexibleFloat`: number or quoted number; empty or `null` becomes 0.
- `flexibleBool`: `1`, `true` (case-insensitive) are true; `0`, `false`, empty, `null` are false; other numbers are non-zero-is-true; anything else is an error.
- `flexibleInt`: number or quoted number; empty or `null` becomes 0.
- `successValue` for `playerstats.success`: an absent or `null` value counts as success; a bool is its own value; a JSON number counts when it equals 1 (decoded as `float64`); the strings `"1"` and `"true"` (case-insensitive) count. Anything else is false.

## Retries, backoff and limits

`fetchJSON` makes up to 4 attempts per call. A request is retried when:

- the HTTP status is 429, 500, 502, 503 or 504 (`retryableSteamStatus`);
- the transport call itself fails (`HTTPClient.Do` returns an error, including timeouts and connection resets), unless the context is already done;
- reading the response body fails (for example `io.ErrUnexpectedEOF`, a connection reset, or the 30 s `HTTPClient` timeout firing mid-body). The decision uses the request context only: a read error is retried while `ctx.Err() == nil`, whatever the error value, and is not retried once the context is done (including a `context.Canceled` or `context.DeadlineExceeded` that comes from a cancelled context).

Other non-2xx statuses fail immediately with `HTTPStatusError`. A body over the size cap on a 2xx answer fails at once (see [Response size caps](#response-size-caps)); an error status is never failed for its size.

Backoff:

- Before attempts 2, 3 and 4 the client waits `retryDelay(attempt)`.
- Default: `300ms * 2^(attempt-1)` plus random jitter of 0 to 249 ms. Waits are about 300-550 ms, 600-850 ms and 1200-1450 ms.
- `SetRetryBackoff` replaces this. A delay of zero or less skips the wait.
- A 429 `Retry-After` header replaces the backoff for that wait. It is read in both forms: delay-seconds (`Retry-After: 120`) and an HTTP-date. Each value is capped at 60 s (`maxRetryAfter`). A missing, zero, past or unparsable value falls back to the backoff. `Retry-After` is read only on 429; other statuses use the backoff.
- The `Retry-After` waits of one `fetchJSON` call share a total budget of 60 s (`retryAfterBudget`). A wait is cut to what is left of the budget; once the budget is spent, a 429 waits the backoff instead. Example: three 429s with `Retry-After: 60` wait 60 s, then the backoff for attempts 3 and 4. `Retry-After: 40` on two 429s waits 40 s, then 20 s.
- A cancelled context ends the wait with `ctx.Err()`, including during a `Retry-After` wait. Tests replace the wait with the unexported `retrySleep` hook, so they do not sleep.

Worst case for one `fetchJSON` call, leaving out the time each request takes: each of the three waits is either its `Retry-After` or its backoff, never both. The `Retry-After` waits total at most 60 s, so the worst case is 60 s on the first wait, then the backoff for the next two (at most 849 ms and 1449 ms), so under 63 s (about 62.3 s). Each request can also take up to the 30 s `HTTPClient` timeout, so four timed-out attempts add up to 120 s. `GetOwnedGames` makes up to three calls, so its totals are three times these.

After the last attempt, the last error is returned. Retryable statuses therefore surface as `*HTTPStatusError` with the last code.

### Response size caps

Response bodies are read through `io.LimitReader` with one byte past the cap, so an oversize body is detected, not silently truncated:

- 4 MiB (`maxResponseBytes`) for every endpoint except `GetOwnedGames`;
- 64 MiB (`maxOwnedGamesResponseBytes`) for `GetOwnedGames`, because a library with extended app info can exceed 4 MiB. The three list requests each use this cap.
- A `maxBytes` of 0 or less (`fetchOptions`) means `maxResponseBytes`.

The check order in `getJSONBody` is: a body read failure, then a retryable status, then a non-2xx status, then the size check:

- a non-2xx answer (other than the retryable statuses) never fails for its size. It is read only up to `errorBodyWindow` (4 KiB log limit plus the key length and 32 bytes), whatever the cap. When the read reaches that limit, the window may have cut a secret, so the trailing partial UTF-8 rune and the trailing run of token characters are dropped first (`tornTail`). Token characters are letters, digits, `%`, `+`, `-`, `_` and `\`, plus every raw byte of the stored API key, so a key with `.`, `~`, `:`, `/`, `=` or `"` in it is removed whole too. The escaped forms of the key add no byte beyond these. If the strip exposes a partial rune, that is dropped as well. The rest is trimmed and redacted whole, then cut to `errorBodyLogLimit` (4 KiB, rune-safe). A key or SteamID64 that ends inside the window is redacted before the cut, and one the window cuts is already gone, so neither leaves a fragment. The answer is still an `*HTTPStatusError`, so a 403 with an oversize body maps as a 403 and not as an unreadable response;
- a 2xx answer over its cap is a non-retryable `*badResponseError` with text `Steam API <path> response is larger than <n> bytes`. The body is never part of the error. `UserMessage` maps it to `Steam sent a response the app could not read.`

An empty body with a 2xx status is accepted: `decodeJSONBody` returns nil and the target stays zero. See [syncer.md](syncer.md#surprising-behaviour) for what this means for achievement writes.

## Key and ID redaction

Request URLs carry the API key and the profile identifiers, so an error that includes a URL could leak them. `redactText` removes them from the error text the client builds from a request or a response:

- the values of the `key`, `steamid`, `steamids` and `vanityurl` query parameters, in any letter case, are replaced by `REDACTED`. The name must follow a `?` or `&`, an HTML `&amp;` (matched by its `;`) or a JSON `\u0026`. A value ends at the first `&`, `#`, backslash, whitespace, quote or angle bracket, so a URL quoted in an HTML or JSON body does not take the text after it. The regular expression is `(?i)((?:[?&;]|\\u0026)(?:key|steamids?|vanityurl)=)[^&#\\\s"'<>]*` (Go raw-string form; the `\\u0026` alternative matches the literal text `\u0026`), which matches the whole possibly-percent-encoded value. Other parameters (`appid`, `language`, `format`) are kept for debugging;
- a bare SteamID64 (a 17-digit number starting `76561`, pattern `\b76561\d{12}\b`) anywhere in the text is replaced. The prefix is `76561` rather than `7656119` because accounts created later have IDs from `76561200000000000` up;
- the API key is replaced wherever it appears, raw, query-escaped and path-escaped, because the key can still reach error text: keys shorter than `minGuardedKeyLen` skip the input guard, and Steam may echo the request.

Where it applies:

- `redactURLError`: when `HTTPClient.Do` or `http.NewRequestWithContext` fails with a `*url.Error`, a copy is returned with its URL redacted. `Op` and `Err` are kept, so `errors.Is`/`errors.As` and `Timeout()` still see the cause. Other errors pass through unchanged.
- `redactBody`: for non-retryable non-2xx responses, the trimmed body goes through `redactText`. Steam may echo the request URL in the body.
- `ResolveVanityURL`: Steam's `message` text goes through `redactText`. The no-message fallback is a constant and never includes the typed name, so it needs no redaction.
- `GetPlayerAchievements`: Steam's `PlayerStats.error` text goes through `redactText` the same way.

Identifiers are redacted in `redactText`, not only in URL errors, because bodies and Steam's messages can echo the request. Names, app IDs and timestamps are not matched by these patterns, so they stay. A vanity name is removed only where it is a `vanityurl` query value; a name echoed in other text is kept.

`RedactSecrets` is the keyless form for log text whose key is not known. It is the scrub for panic logs (`callBound`), app error logs (`logText` in `app_errors.go`) and the per-game sync failure log (`internal/syncer`). It applies the identifier rules above and also replaces any run of 32 or more hex digits (`(?i)[0-9a-f]{32,}`, the shape of a Steam Web API key, with no word boundary) by `REDACTED`. The whole run is replaced, so a key glued to other hex digits leaves no tail. The rule is a pattern, so a hex value that is not a key also shows `REDACTED` in a log: a 40-hex icon hash, for example, is replaced. That is a false positive, harmless in a log, and it is why `RedactSecrets` is for logs only and never for text the user reads.

`HTTPStatusError.Path` is the endpoint path without the query string. `EResultError`, the x-eresult error and the decode errors carry no request URL. A JSON decode error names the field and the type; for an out-of-range number it also quotes that number, which is not a profile field in any response the client decodes.

The syncer writes the raw error text to the log, so this redaction protects the log and the error the sync returns. Stored display text (`sync_runs.error`, `profile_games.last_error`) is the `UserMessage` sentence, which carries no request address, so the redaction is not what protects those columns. Tests: `TestRedactURLErrorIdentityParams`, `TestTransportErrorHidesIdentifiersAndKeepsCause`, `TestRedactBodyRemovesSteamID64` and `TestVanityFallbackDoesNotEchoTypedInput` in `client_test.go`. The syncer's `TestTransportFailureStoresNoSteamID64OrKeyInSyncText` checks that the returned error keeps the redacted request address, and that `sync_runs.error` and `profile_games.last_error` hold only the user-safe sentence, with no key or SteamID64.

## Store and asset URLs

Store lookups and CDN downloads live in `internal/images`; see [covers.md](covers.md). `OwnedGame.CapsuleFilename` is the input for the library-capsule URL.

## Test fixtures (`internal/steam/testdata/`)

All fixtures are JSON response bodies in the shape Steam returns. They are read with `readFixture` in `client_test.go`. The repository does not record the exact provenance of each file.

| File | Shape it represents | Used by |
| --- | --- | --- |
| `no_schema_game_achievements.json` | `{"response":{}}`: the body Steam sends with EResult 42 (no schema). | `TestGetGameAchievementsEResultHandling` only. `TestMalformedSteamResponses` uses inline bodies. |
| `portal2_game_achievements.json` | Schema with an empty base set (group 0 metadata, no achievements), two named sets (groups 1 and 2), no DLC, no archived achievements, four achievements. | Retry tests (`portal2` body with status sequences), EResult tests (expects 4 achievements), the "empty base set" test. |
| `pws2_game_achievements.json` | Schema with a base set (group 0) and two DLC-bound sets (`dlcappid` 4173830 and 4229450). Has `schema_version` 11. The data is from PowerWash Simulator 2 (base app 2968420 in the syncer tests). | `TestGetGameAchievementsParsesSetsFromPWS2`. |
| `pws2_user_achievements.json` | User response for the base game: 40 unlocked keys, `groups` lists group 0 only. | `TestGetUserAchievementsUnlockedOnlyForPWS2`. |
| `pws2_user_dlc_owned.json` | User response where DLC groups are present: `groups` lists 0, 1 and 2; 42 unlocked keys, including DLC key 777. | `TestGetUserAchievementsDLCOwnedGroupsPresent`. |
| `tf2_game_achievements.json` | Schema with four groups (0, 11, 12, 13) and four archived achievements. Group 13 is one set that is both developer-only and not public. No DLC. | `TestGetGameAchievementsTF2KeepsArchivedAndHiddenSets`. |

Many other tests build bodies inline (for owned-games lists, purchased and vetted lists, player achievements, malformed JSON, and so on).

## Tests and base URL stubbing

`client_test.go` drives the client against `net/http/httptest` servers. Patterns:

- `testSetClient` and `newTestServer` start a server that serves fixed bodies per path and returns a `Client` with `BaseURL: server.URL`, `HTTPClient: server.Client()` and zero retry backoff (`SetRetryBackoff(func(int) time.Duration { return 0 })`).
- `testStatusSequenceClient` answers a sequence of statuses for one path, then a body. It counts attempts per path.
- Transport-error tests point `BaseURL` at `http://steam.invalid` with a custom `http.RoundTripper`, or use an invalid base URL (`://bad base`) to hit the request-build error path.
- `TestNewDefaults` checks that `New` stores the API key and the default base URL.

Syncer tests (`internal/syncer/syncer_test.go`) use the same approach with a larger fake Steam server (`fakeSteamServer`), setting `steam.Client.BaseURL` to the fake server URL and passing a redirecting transport to the image cache. See [testing.md](testing.md).

Production code never sets `BaseURL` except through `New` (the app's `steamBaseURL` override is tests-only; see [app-service.md](app-service.md)).

## Related docs

- [architecture.md](architecture.md): where the client sits in the backend.
- [syncer.md](syncer.md): how the client's results become database state.
- [covers.md](covers.md): store item lookups and CDN fallbacks.
- [testing.md](testing.md): test layout and fakes.
- [app-service.md](app-service.md): per-profile library settings and the test base URL override.
