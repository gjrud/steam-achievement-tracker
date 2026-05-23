package steam

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGetOwnedGamesPrivateProfilesAndFallbackNames(t *testing.T) {
	client, closeServer := testClient(t, map[string]string{
		"/IPlayerService/GetOwnedGames/v0001/": `{"response":{"game_count":2,"games":[{"appid":10,"name":"","playtime_forever":5},{"appid":20,"name":"Named Game","playtime_forever":9}]}}`,
	})
	defer closeServer()

	games, err := client.GetOwnedGames(context.Background(), "11111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 || games[0].Name != "App 10" || games[1].Name != "Named Game" || games[1].PlaytimeForever != 9 {
		t.Fatalf("games = %#v, want fallback app name and parsed playtime", games)
	}

	client, closeServer = testClient(t, map[string]string{
		"/IPlayerService/GetOwnedGames/v0001/": `{"response":{}}`,
	})
	defer closeServer()
	if _, err := client.GetOwnedGames(context.Background(), "11111111111111111"); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("private owned games err = %v, want privacy error", err)
	}
}

func TestGetPlayerAchievementsFlexibleValuesAndNameFallback(t *testing.T) {
	client, closeServer := testClient(t, map[string]string{
		"/ISteamUserStats/GetPlayerAchievements/v1/": `{"playerstats":{"success":"1","achievements":[{"name":"NAME_FALLBACK","achieved":"true","unlocktime":"123"},{"apiname":"API_FALSE","achieved":0,"unlocktime":0}]}}`,
	})
	defer closeServer()

	achievements, err := client.GetPlayerAchievements(context.Background(), "11111111111111111", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(achievements) != 2 {
		t.Fatalf("len = %d, want 2", len(achievements))
	}
	if achievements[0].APIName != "NAME_FALLBACK" || !achievements[0].Achieved || achievements[0].UnlockTime == nil || *achievements[0].UnlockTime != 123 {
		t.Fatalf("first achievement = %#v, want name fallback, true, unlock time", achievements[0])
	}
	if achievements[1].APIName != "API_FALSE" || achievements[1].Achieved || achievements[1].UnlockTime != nil {
		t.Fatalf("second achievement = %#v, want false without unlock time", achievements[1])
	}
}

func TestGetPlayerAchievementsReturnsSteamErrorWhenNoAchievements(t *testing.T) {
	client, closeServer := testClient(t, map[string]string{
		"/ISteamUserStats/GetPlayerAchievements/v1/": `{"playerstats":{"success":false,"error":"Profile is private"}}`,
	})
	defer closeServer()

	_, err := client.GetPlayerAchievements(context.Background(), "11111111111111111", 10)
	if err == nil || err.Error() != "Profile is private" {
		t.Fatalf("err = %v, want Steam error", err)
	}
}

func TestGetGlobalAchievementPercentagesStringNumbers(t *testing.T) {
	client, closeServer := testClient(t, map[string]string{
		"/ISteamUserStats/GetGlobalAchievementPercentagesForApp/v0002/": `{"achievementpercentages":{"achievements":[{"name":"ACH_STRING","percent":"12.34"},{"name":"ACH_NUMBER","percent":56.78}]}}`,
	})
	defer closeServer()

	values, err := client.GetGlobalAchievementPercentages(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if values["ACH_STRING"] != 12.34 || values["ACH_NUMBER"] != 56.78 {
		t.Fatalf("values = %#v, want parsed string and number percentages", values)
	}
}

func TestGetSchemaForGameDropsBlankAchievementNames(t *testing.T) {
	client, closeServer := testClient(t, map[string]string{
		"/ISteamUserStats/GetSchemaForGame/v2/": `{"game":{"availableGameStats":{"achievements":[{"name":"","displayName":"Blank"},{"name":"ACH_OK","displayName":"Visible"}]}}}`,
	})
	defer closeServer()

	achievements, err := client.GetSchemaForGame(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(achievements) != 1 || achievements[0].APIName != "ACH_OK" || achievements[0].Name != "Visible" {
		t.Fatalf("achievements = %#v, want only nonblank API name", achievements)
	}
}

func TestGetOwnedGamesRetriesTransientSteamStatuses(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/IPlayerService/GetOwnedGames/v0001/" {
			http.NotFound(w, r)
			return
		}
		if attempts.Add(1) < 3 {
			http.Error(w, "try later", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"game_count":1,"games":[{"appid":10,"name":"Recovered","playtime_forever":7}]}}`))
	}))
	defer server.Close()
	client := &Client{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()}
	client.retryBackoff = func(int) time.Duration { return 0 }

	games, err := client.GetOwnedGames(context.Background(), "11111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 || len(games) != 1 || games[0].Name != "Recovered" {
		t.Fatalf("attempts=%d games=%#v, want retry then recovered game", attempts.Load(), games)
	}
}

func testClient(t *testing.T, responses map[string]string) (*Client, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := responses[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	client := &Client{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()}
	return client, server.Close
}
