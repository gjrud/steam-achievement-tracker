# Steam client (`internal/steam`)

`internal/steam` is the only package that talks to the Steam Web API. It wraps the endpoints the app uses, decodes their JSON, classifies failures, and keeps the Steam Web API key out of error text. It has no database or image code. Store and CDN asset requests live in `internal/images` (see [Store and asset URLs](#store-and-asset-urls)).

Callers: `syncer.Service` (see [syncer.md](syncer.md)) for library, schema and unlock data, and `app.go` (profile validation: `ResolveProfileInput` then `GetPlayerSummary`). The package is internal; the frontend never reaches it directly. See [architecture.md](architecture.md) for where it sits.

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
}
```

- `New(apiKey string) *Client` returns a client with `BaseURL` set to `https://api.steampowered.com` and an `HTTPClient` with a 30 second timeout.
- The zero value of `SkipUnvettedApps` and `ExcludePlayedFreeGames` means "include": unvetted apps and played free games are requested by default. `app.go` (`newSteamClient`) sets them per profile from the library settings, inverted: `SkipUnvettedApps = !IncludeUnvettedApps`, `ExcludePlayedFreeGames = !IncludePlayedFreeGames`. See [app-service.md](app-service.md).
- `SetRetryBackoff(fn func(attempt int) time.Duration)` replaces the delay before each retry. `attempt` is 1-based. A `nil` fn restores the default. Production code never calls it; tests use it to avoid sleeping.

## Exported API

All methods take a `context.Context` first. Every request is built with that context, so cancelling it aborts the HTTP call and any pending retry wait.

| Method | Endpoint (path under `BaseURL`) | Parameters sent | Returns | Notes |
| --- | --- | --- | --- | --- |
| `ResolveProfileInput(ctx, input)` | see [Profile resolution](#profile-resolution) | depends on input | `(steamID64 string, error)` | Accepts a SteamID64, a profile URL, a vanity URL or a bare vanity name. |
| `ResolveVanityURL(ctx, vanity)` | `/ISteamUser/ResolveVanityURL/v1/` | `key`, `vanityurl` | `(string, error)` | Error is Steam's `message` text when present, else a generic error. |
| `GetPlayerSummary(ctx, steamID64)` | `/ISteamUser/GetPlayerSummaries/v2/` | `key`, `steamids` | `(PlayerSummary, error)` | Error `Steam profile not found` when no player is returned. |
| `GetOwnedGames(ctx, steamID64)` | `/IPlayerService/GetOwnedGames/v0001/` (up to 3 calls) | see [Library fetch](#library-fetch-getownedgames) | `([]OwnedGame, error)` | Combines the full, purchased and vetted lists. |
| `GetGameAchievements(ctx, appID)` | `/IPlayerService/GetGameAchievements/v1/` | `key`, `appid`, `language=english`, `format=json` | `(GameAchievements, error)` | Uses `x-eresult`. EResult 42 returns an empty value and no error. |
| `GetUserAchievements(ctx, steamID64, appID)` | `/IPlayerService/GetUserAchievements/v1/` | `key`, `steamid`, `appid`, `format=json` | `(UserAchievements, error)` | Uses `x-eresult`. EResult 42 returns empty maps and no error. |
| `GetSchemaForGame(ctx, appID)` | `/ISteamUserStats/GetSchemaForGame/v2/` | `key`, `appid`, `l=english`, `format=json` | `([]SchemaAchievement, error)` | Legacy. Drops entries with an empty name. |
| `GetPlayerAchievements(ctx, steamID64, appID)` | `/ISteamUserStats/GetPlayerAchievements/v1/` | `key`, `steamid`, `appid`, `l=english`, `format=json` | `([]PlayerAchievement, error)` | Legacy. When `success` is false and no achievements came back, the error is Steam's `error` text if present, else `player achievements unavailable`. `APIName` falls back to `name` when `apiname` is empty. |
| `GetGlobalAchievementPercentages(ctx, appID)` | `/ISteamUserStats/GetGlobalAchievementPercentagesForApp/v0002/` | `gameid`, `format=json` (no key) | `(map[string]float64, error)` | Legacy. Keyed by achievement API name. |

Legacy means the pre-IPlayerService path, used only as a fallback by the syncer (see [syncer.md](syncer.md#per-game-refresh)). Remove these methods together with the legacy sync path once Steam documents `GetGameAchievements`.

## Data sources

This table maps each kind of data to the endpoint that provides it. Parameters are in the table above and in the sections below.

| Data | Endpoint | Key sent | Notes |
| --- | --- | --- | --- |
| Profile name and avatar | `ISteamUser/GetPlayerSummaries/v2` | yes | Used during profile validation and stored as `profiles.avatar_url`. |
| Vanity name to SteamID64 | `ISteamUser/ResolveVanityURL/v1` | yes | Used for vanity names and `/id/<name>` URLs. |
| Owned games, names, playtime, library capsule file name, purchased and vetted status | `IPlayerService/GetOwnedGames/v0001` (up to three calls) | yes | Unvetted apps are included unless the profile's **Include unvetted apps** setting is off, and played free games follow **Include played free games**. Any failed or unusable answer fails the run before a game is synced. See [Library fetch](#library-fetch-getownedgames). |
| Achievement schema, sets, global unlock percentages | `IPlayerService/GetGameAchievements/v1` | yes | The main path. One call returns the whole schema of an app, including each achievement's `player_percent_unlocked`. |
| Unlocked achievements of the profile | `IPlayerService/GetUserAchievements/v1` | yes | Main path. Needs the profile's game stats to be public. |
| Legacy schema, unlock and percentage data | `ISteamUserStats/GetSchemaForGame/v2`, `ISteamUserStats/GetPlayerAchievements/v1`, `ISteamUserStats/GetGlobalAchievementPercentagesForApp/v0002` | yes (the percentages call sends none) | Fallback only, when `GetGameAchievements` fails with an error that is not transient or with an EResult error, and the game has no stored set data (`Repo.HasSetData` is false; see [syncer.md](syncer.md#per-game-refresh)). |
| Store item assets (library, hero, header and main capsule file names) | `IStoreBrowseService/GetItems/v1` | no | One app per call. Only for stale DLC covers and for base games without a usable owned-games capsule. See [covers.md](covers.md). |
| Cover image files | `https://shared.akamai.steamstatic.com/store_item_assets/` (primary) and `https://shared.steamstatic.com/store_item_assets/` (fallback) | no | Downloaded to the cover cache. See [covers.md](covers.md). |
| Steam avatar image | The `avatarfull` (or `avatarmedium`) URL from `ISteamUser/GetPlayerSummaries/v2` | no | Not fetched by the Go client. The webview loads it as an `<img>` in the profile card. The URL is stored in `profiles.avatar_url`. |

Nothing is sent to a non-Steam service.

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

- `*HTTPStatusError{Path, Code, Status}` for non-2xx responses. `Code` is the HTTP status code; callers classify with `errors.As`, not by message. `Error()` is `Steam API <path> returned <status>`. For non-retryable statuses the trimmed body is appended (after key redaction). Retryable statuses (429, 500, 503) carry no body text.
- `*EResultError{Path, Code}` for an `x-eresult` header other than 0, 1 or 42.

Other errors are plain `errors.New` or `fmt.Errorf` values, for example `profile input is required`, `Steam profile not found`, `owned games unavailable; profile game details may be private`, `decode Steam API <path>: ...`, and `Steam API <path> returned invalid x-eresult "<value>"`.

## Endpoints

All requests are `GET` with the query string built by `url.Values.Encode()`. The key is always sent as a query parameter named `key` except on `GetGlobalAchievementPercentages`. `BaseURL` is trimmed of trailing `/`.

Store item lookups (`IStoreBrowseService/GetItems`) and CDN image URLs are not in this package.

## Profile resolution

`ResolveProfileInput` turns user input into a SteamID64 in this order:

1. Trim whitespace. Empty input returns `profile input is required`.
2. If the value matches `^\d{17}$`, return it unchanged. No request.
3. If `url.Parse` succeeds and yields a host, scan the path segments in order:
   - `profiles/<17 digits>` returns that id. A `profiles/` segment not followed by 17 digits does not match, and the scan continues.
   - `id/<vanity>` returns the result of `ResolveVanityURL` for that segment. An `id/` segment with nothing after it does not match, and the scan continues.
   - If no segment matches, the error is `Steam profile URL must contain /profiles/{steamid64} or /id/{vanity}`.
4. Otherwise the whole trimmed value is treated as a vanity name and sent to `ResolveVanityURL`.

`ResolveVanityURL` succeeds only when `response.success == 1` and a `steamid` is returned. Otherwise it returns Steam's `message` (for example `No match`) or `could not resolve Steam vanity name "<name>"`.

Note: a URL without a scheme (for example `steamcommunity.com/id/name`) has no host to `url.Parse`, so it is sent to `ResolveVanityURL` as a literal vanity name and fails. Only inputs with `https://` (or another scheme) are parsed as profile URLs. The tests do not cover scheme-less URLs.

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
- `checkOwnedGamesList` rejects unusable answers. A response with neither `games` nor `game_count` is unusable (`owned games unavailable...`, `purchased games list unavailable`, `vetted games list unavailable`). A non-zero `game_count` with no games is incomplete (`... response was incomplete`). A `game_count` of 0 with no games is a valid empty list. This stops a `{"response":{}}` from reading as "nothing purchased" or "nothing vetted".

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

- the HTTP status is 429, 500 or 503 (`retryableSteamStatus`);
- the transport call itself fails (`HTTPClient.Do` returns an error, including timeouts and connection resets).

Statuses 502 and 504 are not retried by this package. Other non-2xx statuses fail immediately with `HTTPStatusError`. A failure to read the response body is returned at once and is not retried.

Backoff:

- Before attempts 2, 3 and 4 the client waits `retryDelay(attempt)`.
- Default: `300ms * 2^(attempt-1)` plus random jitter of 0 to 249 ms. Waits are about 300-550 ms, 600-850 ms and 1200-1450 ms.
- `SetRetryBackoff` replaces this. A delay of zero or less skips the wait.
- A cancelled context ends the wait with `ctx.Err()`.

After the last attempt, the last error is returned. Retryable statuses therefore surface as `*HTTPStatusError` with the last code.

Response bodies are read through `io.LimitReader` with a 4 MiB cap. A larger body is truncated, which then fails JSON decode.

An empty body with a 2xx status is accepted: `decodeJSONBody` returns nil and the target stays zero. See [syncer.md](syncer.md#surprising-behaviour) for what this means for achievement writes.

## Key redaction

The key is in the request URL, so any error that includes the URL could leak it. The package removes it in two functions, both using the regular expression `apiKeyParam`:

- `redactURLError`: when `HTTPClient.Do` or `http.NewRequestWithContext` fails with a `*url.Error`, a copy is returned with the `key=` query value replaced by `REDACTED`. `Op` and `Err` are kept, so `errors.Is`/`errors.As` and `Timeout()` still see the cause. Other errors pass through unchanged.
- `redactBody`: for non-retryable non-2xx responses, the body is trimmed, the literal API key is replaced by `REDACTED`, and any `key=` query value is replaced too. Steam may echo the request URL in the body.
- The regular expression is `([?&]key=)[^&]*`, matching the whole possibly-percent-encoded value.

`HTTPStatusError.Path` and the other error paths never include the query string. `ResolveVanityURL` returns Steam's `message` text verbatim; that text is not passed through `redactBody`.

Callers (`syncer`) also store error text in the database and logs, so this redaction is the only protection for those paths. Warnings saved by the syncer are checked in tests (see [syncer.md](syncer.md#tests)).

## DLC detection

The client does not own a "DLC" concept. The syncer infers it from data this package returns:

- A set is DLC-bound when `AchievementSet.DLCAppID` is non-zero (from `groups[].dlcappid`). Its `DLCAppID` is the image id for the set's cover.
- Ownership: `GetUserAchievements.Groups` lists the groupids the profile has, and `Unlocked` lists unlocked keys. The syncer treats a DLC set as owned when its groupid is present or any of its achievements is unlocked.

See [syncer.md](syncer.md#set-eligibility-and-entries).

## Achievement sets and groups

Set handling is in the syncer. The client's job is to return every group with its flags (`Public`, `DeveloperOnly`, `Archived`, `DLCAppID`, `Order`) and every achievement with its `GroupID` and `Archived` flag. `internal/steam/testdata/tf2_game_achievements.json` (see below) is the fixture for sets that the client returns but the syncer filters.

## Store and asset URLs

This package does not call the store. The store and CDN handling lives in `internal/images`:

- `IStoreBrowseService/GetItems` (`https://api.steampowered.com/IStoreBrowseService/GetItems/v1/`), with the request in an `input_json` query value. It is sent without the API key. Its own loop retries 429, 500, 503 and transport errors up to 4 attempts.
- CDN bases `https://shared.akamai.steamstatic.com/store_item_assets/` (primary) and `https://shared.steamstatic.com/store_item_assets/` (fallback).

See [covers.md](covers.md) for the URL rules and the cover fallback chain. The `OwnedGame.CapsuleFilename` field from this package is the input for the library-capsule URL.

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
