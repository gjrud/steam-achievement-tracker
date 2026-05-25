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

func New(apiKey string) *Client {
	return &Client{APIKey: apiKey, BaseURL: defaultBaseURL, HTTPClient: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) ResolveProfileInput(ctx context.Context, input string) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", errors.New("profile input is required")
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
			return "", errors.New(payload.Response.Message)
		}
		return "", fmt.Errorf("could not resolve Steam vanity name %q", vanity)
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

func (c *Client) GetOwnedGames(ctx context.Context, steamID64 string) ([]OwnedGame, error) {
	var payload struct {
		Response struct {
			GameCount *int `json:"game_count"`
			Games     []struct {
				AppID           int64  `json:"appid"`
				Name            string `json:"name"`
				PlaytimeForever int    `json:"playtime_forever"`
			} `json:"games"`
		} `json:"response"`
	}
	q := url.Values{"key": {c.APIKey}, "steamid": {steamID64}, "include_appinfo": {"true"}, "include_played_free_games": {"true"}, "format": {"json"}}
	if err := c.getJSON(ctx, "/IPlayerService/GetOwnedGames/v0001/", q, &payload); err != nil {
		return nil, err
	}
	if payload.Response.Games == nil && payload.Response.GameCount == nil {
		return nil, errors.New("owned games unavailable; profile game details may be private")
	}
	if payload.Response.GameCount != nil && *payload.Response.GameCount > 0 && len(payload.Response.Games) == 0 {
		return nil, errors.New("owned games response was incomplete")
	}
	games := make([]OwnedGame, 0, len(payload.Response.Games))
	for _, g := range payload.Response.Games {
		name := strings.TrimSpace(g.Name)
		if name == "" {
			name = fmt.Sprintf("App %d", g.AppID)
		}
		games = append(games, OwnedGame{AppID: g.AppID, Name: name, PlaytimeForever: g.PlaytimeForever})
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
			return nil, errors.New(payload.PlayerStats.Error)
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

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, target any) error {
	endpoint := c.endpoint(path, q)
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if err := c.waitRetry(ctx, attempt); err != nil {
			return err
		}
		body, retry, err := c.getJSONBody(ctx, path, endpoint)
		if err != nil {
			lastErr = err
			if retry {
				continue
			}
			return err
		}
		return decodeJSONBody(path, body, target)
	}
	return lastErr
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

func (c *Client) getJSONBody(ctx context.Context, path, endpoint string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, true, err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if readErr != nil {
		return nil, false, readErr
	}
	if retryableSteamStatus(resp.StatusCode) {
		return nil, true, fmt.Errorf("Steam API %s returned %s", path, resp.Status)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("Steam API %s returned %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, false, nil
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
