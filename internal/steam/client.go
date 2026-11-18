package steam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.steampowered.com"

type Client struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client

	// SkipUnvettedApps sends skip_unvetted_apps=true, hiding unvetted (new) apps from GetOwnedGames.
	// The zero value (false) requests them, which is the default library behavior.
	SkipUnvettedApps bool
	// ExcludePlayedFreeGames asks GetOwnedGames for purchased games only (include_played_free_games=false)
	// and skips the second purchased-list request. The zero value (false) includes played free games.
	ExcludePlayedFreeGames bool

	retryBackoff func(attempt int) time.Duration
}

type PlayerSummary struct {
	SteamID64   string `json:"steamId64"`
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrl"`
}

type OwnedGame struct {
	AppID           int64
	Name            string
	PlaytimeForever int
	// CapsuleFilename is the library capsule file name Steam lists for the game (trimmed). It is
	// relative to steam/apps/<appid>/ and may be empty.
	CapsuleFilename string
	// Purchased is true when the game is in the account's purchased list. Games present only because they
	// were played for free are false. With Client.ExcludePlayedFreeGames set, the library is the purchased
	// list, so every game is true.
	Purchased bool
	// Unvetted is true when the app is not in Steam's vetted list. Unvetted (new) apps are returned only when
	// Client.SkipUnvettedApps is unset; with it set, the library already excludes them, so no game is Unvetted
	// and no extra request is made. The zero value (false) means vetted.
	Unvetted bool
}

type SchemaAchievement struct {
	APIName string
	Name    string
}

type PlayerAchievement struct {
	APIName    string
	Achieved   bool
	UnlockTime *int64
}

// AchievementSet is one achievement set (groupid) of a game from IPlayerService/GetGameAchievements.
// GroupID 0 is the base game set. DLCAppID is non-zero when the set is bound to a DLC.
type AchievementSet struct {
	GroupID                int64
	Name                   string
	DLCAppID               int64
	Order                  int
	Archived               bool
	DeveloperOnly          bool
	Public                 bool
	TotalAchievements      int
	CompletionAchievements int
}

// SetAchievement is one achievement from IPlayerService/GetGameAchievements.
// Key is the internal_key used by GetUserAchievements.
type SetAchievement struct {
	InternalName  string
	Key           int64
	GroupID       int64
	Archived      bool
	GlobalPercent *float64
}

// GameAchievements is the schema of a game: all achievements (every set) and set metadata.
// An empty value (no Achievements, no Sets) means Steam reported no schema (EResult 42).
type GameAchievements struct {
	Achievements  []SetAchievement
	Sets          []AchievementSet
	SchemaVersion int
}

// UserAchievements lists the unlocked achievements of one player. Locked achievements are absent.
type UserAchievements struct {
	Unlocked map[int64]*int64 // internal_key -> unlock_time (nil when Steam sent 0)
	Groups   map[int64]bool   // groupids present in the user response
}

func New(apiKey string) *Client {
	return &Client{APIKey: apiKey, BaseURL: defaultBaseURL, HTTPClient: &http.Client{Timeout: 30 * time.Second}}
}

// SetRetryBackoff replaces the delay before each retry attempt (attempt is 1-based). A nil fn restores
// the default exponential backoff with jitter. Callers in tests use it to exercise retryable statuses
// without sleeping; production code does not call it.
func (c *Client) SetRetryBackoff(fn func(attempt int) time.Duration) {
	c.retryBackoff = fn
}

// errAPIKeyAsProfileInput is returned without sending a request when the profile input, or the vanity name
// decoded from it, holds the API key.
var errAPIKeyAsProfileInput = errors.New("this looks like your Steam Web API key; enter your profile URL, SteamID64 or custom name instead")

// errNoVanityMatch is the ResolveVanityURL fallback when Steam sends no message. It is a constant on purpose: the
// typed name is never echoed, so a SteamID64 typed as a /id/ value cannot reach logs, the UI or the sync history.
var errNoVanityMatch = errors.New("no Steam profile matches that custom name")

// minGuardedKeyLen skips the input guard for keys too short to be real (real keys are 32 characters),
// where a substring match would reject ordinary input.
const minGuardedKeyLen = 8

// inputContainsAPIKey reports whether input holds the API key, ignoring case.
func (c *Client) inputContainsAPIKey(input string) bool {
	if len(c.APIKey) < minGuardedKeyLen {
		return false
	}
	return strings.Contains(strings.ToLower(input), strings.ToLower(c.APIKey))
}

func (c *Client) ResolveProfileInput(ctx context.Context, input string) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", errors.New("profile input is required")
	}
	if c.inputContainsAPIKey(value) {
		return "", errAPIKeyAsProfileInput
	}
	if steamID64Regexp.MatchString(value) {
		return value, nil
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		parts := pathParts(parsed.Path)
		for i, part := range parts {
			switch part {
			case "profiles":
				if i+1 < len(parts) && steamID64Regexp.MatchString(parts[i+1]) {
					return parts[i+1], nil
				}
			case "id":
				if i+1 < len(parts) {
					return c.ResolveVanityURL(ctx, parts[i+1])
				}
			}
		}
		return "", errors.New("Steam profile URL must contain /profiles/{steamid64} or /id/{vanity}")
	}
	return c.ResolveVanityURL(ctx, value)
}

func (c *Client) ResolveVanityURL(ctx context.Context, vanity string) (string, error) {
	// Every key-bearing input ends up here after URL decoding, so the guard repeats on the decoded name.
	if c.inputContainsAPIKey(vanity) {
		return "", errAPIKeyAsProfileInput
	}
	var payload struct {
		Response struct {
			Success int    `json:"success"`
			SteamID string `json:"steamid"`
			Message string `json:"message"`
		} `json:"response"`
	}
	q := url.Values{"key": {c.APIKey}, "vanityurl": {vanity}}
	if err := c.getJSON(ctx, "/ISteamUser/ResolveVanityURL/v1/", q, &payload); err != nil {
		return "", err
	}
	if payload.Response.Success != 1 || payload.Response.SteamID == "" {
		if payload.Response.Message != "" {
			return "", errors.New(c.redactText(payload.Response.Message))
		}
		return "", errNoVanityMatch
	}
	return payload.Response.SteamID, nil
}

func (c *Client) GetPlayerSummary(ctx context.Context, steamID64 string) (PlayerSummary, error) {
	var payload struct {
		Response struct {
			Players []struct {
				SteamID      string `json:"steamid"`
				PersonaName  string `json:"personaname"`
				AvatarFull   string `json:"avatarfull"`
				AvatarMedium string `json:"avatarmedium"`
			} `json:"players"`
		} `json:"response"`
	}
	q := url.Values{"key": {c.APIKey}, "steamids": {steamID64}}
	if err := c.getJSON(ctx, "/ISteamUser/GetPlayerSummaries/v2/", q, &payload); err != nil {
		return PlayerSummary{}, err
	}
	if len(payload.Response.Players) == 0 {
		return PlayerSummary{}, errors.New("Steam profile not found")
	}
	p := payload.Response.Players[0]
	avatar := p.AvatarFull
	if avatar == "" {
		avatar = p.AvatarMedium
	}
	return PlayerSummary{SteamID64: p.SteamID, DisplayName: p.PersonaName, AvatarURL: avatar}, nil
}

// ownedGamesResponse is the IPlayerService/GetOwnedGames payload. GameCount is nil when Steam omits it.
type ownedGamesResponse struct {
	Response struct {
		GameCount *int `json:"game_count"`
		Games     []struct {
			AppID           int64  `json:"appid"`
			Name            string `json:"name"`
			PlaytimeForever int    `json:"playtime_forever"`
			CapsuleFilename string `json:"capsule_filename"`
		} `json:"games"`
	} `json:"response"`
}

// getOwnedGamesList requests one GetOwnedGames list. includePlayedFree=true asks for the full library
// (purchased games plus games played for free); false asks only for the games the account purchased.
// skipUnvetted is sent as skip_unvetted_apps. Callers pass Client.SkipUnvettedApps for the library lists, so
// by default Steam hides nothing (skip_unvetted_apps=false); the vetted list passes true.
func (c *Client) getOwnedGamesList(ctx context.Context, steamID64 string, includePlayedFree, skipUnvetted bool) (ownedGamesResponse, error) {
	var payload ownedGamesResponse
	// include_extended_appinfo adds capsule_filename. language is deliberately not set: with a language,
	// Steam returns localized art file names for some games.
	q := url.Values{"key": {c.APIKey}, "steamid": {steamID64}, "include_appinfo": {"true"}, "include_extended_appinfo": {"true"}, "include_played_free_games": {strconv.FormatBool(includePlayedFree)}, "skip_unvetted_apps": {strconv.FormatBool(skipUnvetted)}, "format": {"json"}}
	if err := c.getJSON(ctx, "/IPlayerService/GetOwnedGames/v0001/", q, &payload); err != nil {
		return ownedGamesResponse{}, err
	}
	return payload, nil
}

// checkOwnedGamesList rejects an unusable GetOwnedGames answer. A response with neither games nor game_count
// is unusable; so is a non-zero game_count with no games. An explicit game_count of 0 with an empty or missing
// games list is a valid empty library.
func checkOwnedGamesList(r ownedGamesResponse, unavailable, incomplete string) error {
	if r.Response.Games == nil && r.Response.GameCount == nil {
		return errors.New(unavailable)
	}
	if r.Response.GameCount != nil && *r.Response.GameCount > 0 && len(r.Response.Games) == 0 {
		return errors.New(incomplete)
	}
	return nil
}

// GetOwnedGames returns the library. It makes up to three requests:
//  1. the full list (played free games included, skip_unvetted_apps per Client.SkipUnvettedApps);
//  2. the purchased list (played-free games excluded) used to set OwnedGame.Purchased;
//  3. only when SkipUnvettedApps is unset: the vetted list (skip_unvetted_apps=true) used to set OwnedGame.Unvetted.
//
// Any failing request fails the call. With ExcludePlayedFreeGames set, the full list is requested without
// played free games, which already is the purchased list: request 2 is skipped and every game is Purchased.
// With SkipUnvettedApps set, the library already excludes unvetted apps: request 3 is skipped and no game is Unvetted.
// An explicit empty vetted list (game_count 0) is a valid answer: every game is then Unvetted. Unusable answers
// (a bare `{"response":{}}`, an incomplete list) fail the call via checkOwnedGamesList.
func (c *Client) GetOwnedGames(ctx context.Context, steamID64 string) ([]OwnedGame, error) {
	full, err := c.getOwnedGamesList(ctx, steamID64, !c.ExcludePlayedFreeGames, c.SkipUnvettedApps)
	if err != nil {
		return nil, err
	}
	if err := checkOwnedGamesList(full, "owned games unavailable; profile game details may be private", "owned games response was incomplete"); err != nil {
		return nil, err
	}
	purchasedAll := c.ExcludePlayedFreeGames
	purchased := make(map[int64]bool)
	if !purchasedAll {
		purchasedList, err := c.getOwnedGamesList(ctx, steamID64, false, c.SkipUnvettedApps)
		if err != nil {
			return nil, err
		}
		// Checked like the full list: a glitched or throttled `{"response":{}}` must not read as "no purchases",
		// which would clear every purchased flag. A genuinely empty purchased list still carries game_count.
		if err := checkOwnedGamesList(purchasedList, "purchased games list unavailable", "purchased games response was incomplete"); err != nil {
			return nil, err
		}
		for _, g := range purchasedList.Response.Games {
			purchased[g.AppID] = true
		}
	}
	allVetted := c.SkipUnvettedApps
	vetted := make(map[int64]bool)
	if !allVetted {
		// Same played-free setting as the full list, so the vetted membership covers every game in it.
		vettedList, err := c.getOwnedGamesList(ctx, steamID64, !c.ExcludePlayedFreeGames, true)
		if err != nil {
			return nil, err
		}
		// Checked like the other lists: an unusable answer must not read as "nothing vetted".
		if err := checkOwnedGamesList(vettedList, "vetted games list unavailable", "vetted games response was incomplete"); err != nil {
			return nil, err
		}
		for _, g := range vettedList.Response.Games {
			vetted[g.AppID] = true
		}
	}
	games := make([]OwnedGame, 0, len(full.Response.Games))
	for _, g := range full.Response.Games {
		name := strings.TrimSpace(g.Name)
		if name == "" {
			name = fmt.Sprintf("App %d", g.AppID)
		}
		games = append(games, OwnedGame{
			AppID:           g.AppID,
			Name:            name,
			PlaytimeForever: g.PlaytimeForever,
			CapsuleFilename: strings.TrimSpace(g.CapsuleFilename),
			Purchased:       purchasedAll || purchased[g.AppID],
			Unvetted:        !allVetted && !vetted[g.AppID],
		})
	}
	return games, nil
}

func (c *Client) GetSchemaForGame(ctx context.Context, appID int64) ([]SchemaAchievement, error) {
	var payload struct {
		Game struct {
			AvailableGameStats struct {
				Achievements []struct {
					Name        string `json:"name"`
					DisplayName string `json:"displayName"`
				} `json:"achievements"`
			} `json:"availableGameStats"`
		} `json:"game"`
	}
	q := url.Values{"key": {c.APIKey}, "appid": {strconv.FormatInt(appID, 10)}, "l": {"english"}, "format": {"json"}}
	if err := c.getJSON(ctx, "/ISteamUserStats/GetSchemaForGame/v2/", q, &payload); err != nil {
		return nil, err
	}
	ach := make([]SchemaAchievement, 0, len(payload.Game.AvailableGameStats.Achievements))
	for _, a := range payload.Game.AvailableGameStats.Achievements {
		if a.Name != "" {
			ach = append(ach, SchemaAchievement{APIName: a.Name, Name: a.DisplayName})
		}
	}
	return ach, nil
}

func (c *Client) GetPlayerAchievements(ctx context.Context, steamID64 string, appID int64) ([]PlayerAchievement, error) {
	var payload struct {
		PlayerStats struct {
			Success      any    `json:"success"`
			Error        string `json:"error"`
			Achievements []struct {
				APIName    string       `json:"apiname"`
				Name       string       `json:"name"`
				Achieved   flexibleBool `json:"achieved"`
				UnlockTime flexibleInt  `json:"unlocktime"`
			} `json:"achievements"`
		} `json:"playerstats"`
	}
	q := url.Values{"key": {c.APIKey}, "steamid": {steamID64}, "appid": {strconv.FormatInt(appID, 10)}, "l": {"english"}, "format": {"json"}}
	if err := c.getJSON(ctx, "/ISteamUserStats/GetPlayerAchievements/v1/", q, &payload); err != nil {
		return nil, err
	}
	if !successValue(payload.PlayerStats.Success) && len(payload.PlayerStats.Achievements) == 0 {
		if payload.PlayerStats.Error != "" {
			return nil, errors.New(c.redactText(payload.PlayerStats.Error))
		}
		return nil, errors.New("player achievements unavailable")
	}
	ach := make([]PlayerAchievement, 0, len(payload.PlayerStats.Achievements))
	for _, a := range payload.PlayerStats.Achievements {
		apiName := a.APIName
		if apiName == "" {
			apiName = a.Name
		}
		var unlock *int64
		if a.UnlockTime > 0 {
			v := int64(a.UnlockTime)
			unlock = &v
		}
		ach = append(ach, PlayerAchievement{APIName: apiName, Achieved: bool(a.Achieved), UnlockTime: unlock})
	}
	return ach, nil
}

func (c *Client) GetGlobalAchievementPercentages(ctx context.Context, appID int64) (map[string]float64, error) {
	var payload struct {
		AchievementPercentages struct {
			Achievements []struct {
				Name    string        `json:"name"`
				Percent flexibleFloat `json:"percent"`
			} `json:"achievements"`
		} `json:"achievementpercentages"`
	}
	q := url.Values{"gameid": {strconv.FormatInt(appID, 10)}, "format": {"json"}}
	if err := c.getJSON(ctx, "/ISteamUserStats/GetGlobalAchievementPercentagesForApp/v0002/", q, &payload); err != nil {
		return nil, err
	}
	values := make(map[string]float64, len(payload.AchievementPercentages.Achievements))
	for _, a := range payload.AchievementPercentages.Achievements {
		values[a.Name] = float64(a.Percent)
	}
	return values, nil
}

// EResult values returned in the x-eresult header by IPlayerService endpoints.
const (
	eresultOK      = 1
	eresultNoMatch = 42
)

// HTTPStatusError is a non-2xx Steam response. Code is the HTTP status code. Error() text is
// unchanged from the earlier string errors; callers classify with errors.As, not by message.
type HTTPStatusError struct {
	Path    string
	Code    int
	Status  string // resp.Status, e.g. "404 Not Found"
	body    string // trimmed response body, shown only for non-retryable statuses
	hasBody bool
}

func (e *HTTPStatusError) Error() string {
	if e.hasBody {
		return fmt.Sprintf("Steam API %s returned %s: %s", e.Path, e.Status, e.body)
	}
	return fmt.Sprintf("Steam API %s returned %s", e.Path, e.Status)
}

// EResultError is an IPlayerService x-eresult other than 0, 1 and 42. Code is the EResult value.
type EResultError struct {
	Path string
	Code int
}

func (e *EResultError) Error() string {
	return fmt.Sprintf("Steam API %s returned eresult %d", e.Path, e.Code)
}

// GetGameAchievements returns the achievement schema and set metadata for a game.
// EResult 42 (no schema) yields an empty GameAchievements and no error.
func (c *Client) GetGameAchievements(ctx context.Context, appID int64) (GameAchievements, error) {
	var payload struct {
		Response struct {
			SchemaVersion int `json:"schema_version"`
			Achievements  []struct {
				InternalName          string         `json:"internal_name"`
				InternalKey           int64          `json:"internal_key"`
				GroupID               int64          `json:"groupid"`
				Archived              bool           `json:"archived"`
				PlayerPercentUnlocked *flexibleFloat `json:"player_percent_unlocked"`
			} `json:"achievements"`
			Groups []struct {
				GroupID                int64  `json:"groupid"`
				LocalizedName          string `json:"localized_name"`
				DLCAppID               int64  `json:"dlcappid"`
				Archived               bool   `json:"archived"`
				DeveloperOnly          bool   `json:"developeronly"`
				Order                  int    `json:"order"`
				IsPublic               *bool  `json:"ispublic"`
				TotalAchievements      int    `json:"total_achievements"`
				CompletionAchievements int    `json:"completion_achievements"`
			} `json:"groups"`
		} `json:"response"`
	}
	q := url.Values{"key": {c.APIKey}, "appid": {strconv.FormatInt(appID, 10)}, "language": {"english"}, "format": {"json"}}
	code, err := c.getJSONWithEResult(ctx, "/IPlayerService/GetGameAchievements/v1/", q, &payload)
	if err != nil {
		return GameAchievements{}, err
	}
	if code == eresultNoMatch {
		return GameAchievements{}, nil
	}
	result := GameAchievements{
		SchemaVersion: payload.Response.SchemaVersion,
		Achievements:  make([]SetAchievement, 0, len(payload.Response.Achievements)),
		Sets:          make([]AchievementSet, 0, len(payload.Response.Groups)),
	}
	for _, a := range payload.Response.Achievements {
		if a.InternalName == "" {
			continue
		}
		ach := SetAchievement{InternalName: a.InternalName, Key: a.InternalKey, GroupID: a.GroupID, Archived: a.Archived}
		if a.PlayerPercentUnlocked != nil {
			v := float64(*a.PlayerPercentUnlocked)
			ach.GlobalPercent = &v
		}
		result.Achievements = append(result.Achievements, ach)
	}
	for _, g := range payload.Response.Groups {
		public := true
		if g.IsPublic != nil {
			public = *g.IsPublic
		}
		result.Sets = append(result.Sets, AchievementSet{
			GroupID:                g.GroupID,
			Name:                   g.LocalizedName,
			DLCAppID:               g.DLCAppID,
			Order:                  g.Order,
			Archived:               g.Archived,
			DeveloperOnly:          g.DeveloperOnly,
			Public:                 public,
			TotalAchievements:      g.TotalAchievements,
			CompletionAchievements: g.CompletionAchievements,
		})
	}
	return result, nil
}

// GetUserAchievements returns the unlocked achievements and present groupids of a player for one game.
// Locked achievements are not listed by Steam. EResult 42 yields empty maps and no error.
func (c *Client) GetUserAchievements(ctx context.Context, steamID64 string, appID int64) (UserAchievements, error) {
	var payload struct {
		Response struct {
			Achievements []struct {
				InternalKey int64        `json:"internal_key"`
				Unlocked    flexibleBool `json:"unlocked"`
				UnlockTime  flexibleInt  `json:"unlock_time"`
			} `json:"achievements"`
			Groups []struct {
				GroupID int64 `json:"groupid"`
			} `json:"groups"`
		} `json:"response"`
	}
	q := url.Values{"key": {c.APIKey}, "steamid": {steamID64}, "appid": {strconv.FormatInt(appID, 10)}, "format": {"json"}}
	result := UserAchievements{Unlocked: map[int64]*int64{}, Groups: map[int64]bool{}}
	code, err := c.getJSONWithEResult(ctx, "/IPlayerService/GetUserAchievements/v1/", q, &payload)
	if err != nil {
		return UserAchievements{}, err
	}
	if code == eresultNoMatch {
		return result, nil
	}
	for _, a := range payload.Response.Achievements {
		if !bool(a.Unlocked) {
			continue
		}
		var unlock *int64
		if a.UnlockTime > 0 {
			v := int64(a.UnlockTime)
			unlock = &v
		}
		result.Unlocked[a.InternalKey] = unlock
	}
	for _, g := range payload.Response.Groups {
		result.Groups[g.GroupID] = true
	}
	return result, nil
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, target any) error {
	_, err := c.fetchJSON(ctx, path, q, target, false)
	return err
}

// getJSONWithEResult is getJSON plus x-eresult header handling: 0/1 decode the body,
// 42 returns (42, nil) without decoding, any other value is an error.
func (c *Client) getJSONWithEResult(ctx context.Context, path string, q url.Values, target any) (int, error) {
	return c.fetchJSON(ctx, path, q, target, true)
}

func (c *Client) fetchJSON(ctx context.Context, path string, q url.Values, target any, checkEResult bool) (int, error) {
	endpoint := c.endpoint(path, q)
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if err := c.waitRetry(ctx, attempt); err != nil {
			return 0, err
		}
		body, eresultHeader, retry, err := c.getJSONBody(ctx, path, endpoint)
		if err != nil {
			lastErr = err
			if retry {
				continue
			}
			return 0, err
		}
		if !checkEResult {
			return 0, decodeJSONBody(path, body, target)
		}
		code, err := parseEResult(path, eresultHeader)
		if err != nil {
			return 0, err
		}
		switch code {
		case 0, eresultOK:
			return code, decodeJSONBody(path, body, target)
		case eresultNoMatch:
			return code, nil
		default:
			return code, &EResultError{Path: path, Code: code}
		}
	}
	return 0, lastErr
}

// parseEResult reads the x-eresult header. A missing header is treated as 0 (not reported).
func parseEResult(path, header string) (int, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, nil
	}
	code, err := strconv.Atoi(header)
	if err != nil {
		return 0, fmt.Errorf("Steam API %s returned invalid x-eresult %q", path, header)
	}
	return code, nil
}

func (c *Client) endpoint(path string, q url.Values) string {
	base := strings.TrimRight(c.BaseURL, "/")
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return base + path + separator + q.Encode()
}

func (c *Client) waitRetry(ctx context.Context, attempt int) error {
	if attempt == 0 {
		return nil
	}
	d := c.retryDelay(attempt)
	if d <= 0 {
		return nil
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// getJSONBody performs one GET. It returns the body, the x-eresult header value (may be empty),
// whether the failure is retryable, and the error.
func (c *Client) getJSONBody(ctx context.Context, path, endpoint string) ([]byte, string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", false, c.redactURLError(err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, "", true, c.redactURLError(err)
	}
	eresult := resp.Header.Get("x-eresult")
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if readErr != nil {
		return nil, "", false, readErr
	}
	if retryableSteamStatus(resp.StatusCode) {
		return nil, "", true, &HTTPStatusError{Path: path, Code: resp.StatusCode, Status: resp.Status}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", false, &HTTPStatusError{Path: path, Code: resp.StatusCode, Status: resp.Status, body: c.redactBody(strings.TrimSpace(string(body))), hasBody: true}
	}
	return body, eresult, false, nil
}

// queryIdentityParam matches the query parameters of a Steam request URL that carry the API key or a profile
// identifier: key, steamid, steamids and vanityurl, in any letter case. The name follows a '?' or '&', an HTML
// escaped '&amp;' (matched by ';'), or a JSON escaped '\u0026'. The value runs up to the next '&', '#', '\',
// whitespace, quote or angle bracket, so a request URL quoted in an HTML or JSON body does not take the text after
// it. The whole (possibly percent-encoded) value is matched, so only the value is replaced and the name, like appid,
// is kept.
var queryIdentityParam = regexp.MustCompile(`(?i)((?:[?&;]|\\u0026)(?:key|steamids?|vanityurl)=)[^&#\\\s"'<>]*`)

// steamID64Text matches a bare SteamID64 (an individual account in universe 1) anywhere in text. The prefix is
// 76561, not 7656119: accounts created later have IDs from 76561200000000000 up. The \b word boundaries are
// required: an ID glued to a letter, digit or underscore is not matched, so an ID inside percent-encoded JSON
// (%2276561...%22) is missed. All current requests send IDs only in plain steamid/steamids params, which
// queryIdentityParam covers. A future request that embeds IDs in encoded JSON needs an explicit rule. Appids and
// unix times are never 17 digits, so they are not matched.
var steamID64Text = regexp.MustCompile(`\b76561\d{12}\b`)

// redactText removes the API key and profile identifiers from text that may carry a request URL, a response body
// or user input. It replaces the values of the key, steamid, steamids and vanityurl query parameters and any bare
// SteamID64. The API key is also replaced wherever it appears, in raw, query-escaped and path-escaped form, because
// a user can type the key as profile input and it then travels in other parameters and in messages that echo it.
// Identifiers are redacted here rather than only in URL errors because the other callers carry them too: a
// response body can echo the request URL (TestHTTPStatusErrorBodyRedactsAPIKey models this), Steam's message can
// echo the input, and the vanity fallback echoes a typed /id/ value that may be a SteamID64. Names, appids and
// timestamps are not matched by these patterns and are kept.
func (c *Client) redactText(s string) string {
	s = queryIdentityParam.ReplaceAllString(s, "${1}REDACTED")
	s = steamID64Text.ReplaceAllString(s, "REDACTED")
	if c.APIKey == "" {
		return s
	}
	for _, form := range []string{url.QueryEscape(c.APIKey), url.PathEscape(c.APIKey), c.APIKey} {
		s = regexp.MustCompile("(?i)"+regexp.QuoteMeta(form)).ReplaceAllString(s, "REDACTED")
	}
	return s
}

// redactURLError redacts a copy of the URL of a *url.Error (as returned by Client.Do and
// NewRequestWithContext) with redactText, so the key and the SteamID64/vanity query values are gone. Op and Err
// are kept so errors.Is/As and Timeout() still see the original cause. Other errors, including nil, are returned
// unchanged.
func (c *Client) redactURLError(err error) error {
	urlErr, ok := err.(*url.Error)
	if !ok {
		return err
	}
	redacted := *urlErr
	redacted.URL = c.redactText(urlErr.URL)
	return &redacted
}

// redactBody removes the API key and profile identifiers from a non-2xx response body, which may echo the request.
func (c *Client) redactBody(body string) string {
	return c.redactText(body)
}

func retryableSteamStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

func decodeJSONBody(path string, body []byte, target any) error {
	if len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode Steam API %s: %w", path, err)
	}
	return nil
}

func (c *Client) retryDelay(attempt int) time.Duration {
	if c.retryBackoff != nil {
		return c.retryBackoff(attempt)
	}
	d := time.Duration(300*(1<<uint(attempt-1))) * time.Millisecond
	d += time.Duration(rand.Intn(250)) * time.Millisecond
	return d
}

func pathParts(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

var steamID64Regexp = regexp.MustCompile(`^\d{17}$`)

func successValue(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case bool:
		return v
	case float64:
		return v == 1
	case string:
		return v == "1" || strings.EqualFold(v, "true")
	default:
		return false
	}
}

type flexibleFloat float64

func (f *flexibleFloat) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	text = strings.Trim(text, `"`)
	if text == "" || text == "null" {
		*f = 0
		return nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return err
	}
	*f = flexibleFloat(value)
	return nil
}

type flexibleBool bool

func (b *flexibleBool) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	text = strings.Trim(text, `"`)
	switch strings.ToLower(text) {
	case "1", "true":
		*b = true
	case "", "0", "false", "null":
		*b = false
	default:
		value, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return err
		}
		*b = value != 0
	}
	return nil
}

type flexibleInt int64

func (i *flexibleInt) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	text = strings.Trim(text, `"`)
	if text == "" || text == "null" {
		*i = 0
		return nil
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}
	*i = flexibleInt(value)
	return nil
}
