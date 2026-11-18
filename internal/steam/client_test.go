package steam

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/usererr"
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

func TestGetOwnedGamesRequestsExtendedAppInfoAndTrimsCapsuleFilename(t *testing.T) {
	const path = "/IPlayerService/GetOwnedGames/v0001/"
	client, log, closeServer := testSetClient(t, map[string]fixtureResponse{
		path: {body: `{"response":{"game_count":2,"games":[{"appid":10,"name":"Capsule Game","playtime_forever":1,"capsule_filename":"  library_600x900.jpg \n"},{"appid":20,"name":"No Capsule","playtime_forever":2}]}}`},
	})
	defer closeServer()

	games, err := client.GetOwnedGames(context.Background(), testSteamID)
	if err != nil {
		t.Fatal(err)
	}
	queries := log.queryHistory(path)
	if len(queries) != 3 {
		t.Fatalf("GetOwnedGames requests = %d, want 3 (full list, purchased list and vetted list)", len(queries))
	}
	wantSkip := []string{"false", "false", "true"}
	for i, query := range queries {
		if query.Get("include_appinfo") != "true" || query.Get("include_extended_appinfo") != "true" {
			t.Fatalf("request %d query = %v, want include_appinfo and include_extended_appinfo set", i, query)
		}
		if query.Get("language") != "" {
			t.Fatalf("request %d query language = %q, want unset", i, query.Get("language"))
		}
		if query.Get("skip_unvetted_apps") != wantSkip[i] {
			t.Fatalf("request %d skip_unvetted_apps = %q, want %q", i, query.Get("skip_unvetted_apps"), wantSkip[i])
		}
	}
	if len(games) != 2 || games[0].CapsuleFilename != "library_600x900.jpg" || games[1].CapsuleFilename != "" {
		t.Fatalf("games = %#v, want trimmed capsule file name for game 10 and empty for game 20", games)
	}
}

// testOwnedClient serves GetOwnedGames by the include_played_free_games value: "true" gets full, "false"
// gets purchased. Each answer is a raw body; statuses, when set, overrides the answer with that HTTP status.
func testOwnedClient(t *testing.T, full, purchased string, purchasedStatus int) (*Client, *requestLog, func()) {
	t.Helper()
	const path = "/IPlayerService/GetOwnedGames/v0001/"
	log := &requestLog{attempts: map[string]int{}, queries: map[string]url.Values{}, history: map[string][]url.Values{}}
	client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		log.record(r)
		if r.URL.Query().Get("include_played_free_games") == "false" {
			if purchasedStatus != 0 {
				http.Error(w, "purchased unavailable", purchasedStatus)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(purchased))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(full))
	})
	return client, log, closeServer
}

func TestGetOwnedGamesMarksPurchasedFromSecondList(t *testing.T) {
	const path = "/IPlayerService/GetOwnedGames/v0001/"
	full := `{"response":{"game_count":3,"games":[{"appid":10,"name":"Bought","playtime_forever":1},{"appid":20,"name":"Played Free","playtime_forever":2},{"appid":30,"name":"Bought Too","playtime_forever":3}]}}`
	purchased := `{"response":{"game_count":2,"games":[{"appid":30,"name":"Bought Too"},{"appid":10,"name":"Bought"}]}}`
	client, log, closeServer := testOwnedClient(t, full, purchased, 0)
	defer closeServer()

	games, err := client.GetOwnedGames(context.Background(), testSteamID)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 3 {
		t.Fatalf("games = %#v, want the full list of 3", games)
	}
	wantPurchased := map[int64]bool{10: true, 20: false, 30: true}
	for _, g := range games {
		if g.Purchased != wantPurchased[g.AppID] {
			t.Fatalf("game %d Purchased = %v, want %v", g.AppID, g.Purchased, wantPurchased[g.AppID])
		}
	}
	queries := log.queryHistory(path)
	if len(queries) != 3 {
		t.Fatalf("requests = %d, want 3 (full, purchased, vetted)", len(queries))
	}
	if queries[0].Get("include_played_free_games") != "true" || queries[1].Get("include_played_free_games") != "false" {
		t.Fatalf("include_played_free_games = %q then %q, want true then false",
			queries[0].Get("include_played_free_games"), queries[1].Get("include_played_free_games"))
	}
}

func TestGetOwnedGamesPurchasedListFailureFailsCall(t *testing.T) {
	full := `{"response":{"game_count":1,"games":[{"appid":10,"name":"Bought","playtime_forever":1}]}}`
	client, log, closeServer := testOwnedClient(t, full, "", http.StatusNotFound)
	defer closeServer()

	games, err := client.GetOwnedGames(context.Background(), testSteamID)
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusNotFound {
		t.Fatalf("err = %#v, want *HTTPStatusError 404 from the purchased list", err)
	}
	if games != nil {
		t.Fatalf("games = %#v, want nil when the call fails", games)
	}
	if n := len(log.queryHistory("/IPlayerService/GetOwnedGames/v0001/")); n != 2 {
		t.Fatalf("requests = %d, want 2 (full then purchased, not retried on 404)", n)
	}
}

func TestGetOwnedGamesFullListFailureSkipsPurchasedList(t *testing.T) {
	client, log, closeServer := testOwnedClient(t, `{"response":{}}`, `{"response":{}}`, 0)
	defer closeServer()

	if _, err := client.GetOwnedGames(context.Background(), testSteamID); err == nil || !strings.Contains(err.Error(), "private") {
		t.Fatalf("err = %v, want privacy error from the full list", err)
	}
	if n := len(log.queryHistory("/IPlayerService/GetOwnedGames/v0001/")); n != 1 {
		t.Fatalf("requests = %d, want 1 (purchased list skipped after full-list failure)", n)
	}
}

func TestGetOwnedGamesSkipUnvettedAppsParam(t *testing.T) {
	const path = "/IPlayerService/GetOwnedGames/v0001/"
	full := `{"response":{"game_count":1,"games":[{"appid":10,"name":"Game","playtime_forever":1}]}}`
	// Default (skip=false): full, purchased and vetted requests; the vetted one is the only skip=true request.
	// SkipUnvettedApps set: only the full and purchased requests, both with skip=true, and no vetted request.
	tests := []struct {
		skip     bool
		wantSkip []string
	}{
		{skip: false, wantSkip: []string{"false", "false", "true"}},
		{skip: true, wantSkip: []string{"true", "true"}},
	}
	for _, tc := range tests {
		client, log, closeServer := testOwnedClient(t, full, full, 0)
		client.SkipUnvettedApps = tc.skip
		if _, err := client.GetOwnedGames(context.Background(), testSteamID); err != nil {
			t.Fatal(err)
		}
		queries := log.queryHistory(path)
		if len(queries) != len(tc.wantSkip) {
			t.Fatalf("skip=%v requests = %d, want %d", tc.skip, len(queries), len(tc.wantSkip))
		}
		for i, query := range queries {
			if got := query.Get("skip_unvetted_apps"); got != tc.wantSkip[i] {
				t.Fatalf("skip=%v request %d skip_unvetted_apps = %q, want %q", tc.skip, i, got, tc.wantSkip[i])
			}
		}
		closeServer()
	}
}

// With ExcludePlayedFreeGames the full list is requested as purchased only, so no purchased-list request is made
// and every game is Purchased. The vetted request still runs, with the same include_played_free_games=false.
// testOwnedClient serves include_played_free_games=false from its purchased body, so that body carries the library here.
func TestGetOwnedGamesExcludePlayedFreeGamesSkipsPurchasedRequest(t *testing.T) {
	const path = "/IPlayerService/GetOwnedGames/v0001/"
	library := `{"response":{"game_count":2,"games":[{"appid":10,"name":"Bought","playtime_forever":1},{"appid":30,"name":"Bought Too","playtime_forever":3}]}}`
	client, log, closeServer := testOwnedClient(t, `{"response":{}}`, library, 0)
	defer closeServer()
	client.ExcludePlayedFreeGames = true

	games, err := client.GetOwnedGames(context.Background(), testSteamID)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 {
		t.Fatalf("games = %#v, want 2", games)
	}
	for _, g := range games {
		if !g.Purchased || g.Unvetted {
			t.Fatalf("game %d Purchased=%v Unvetted=%v, want true and false", g.AppID, g.Purchased, g.Unvetted)
		}
	}
	queries := log.queryHistory(path)
	if len(queries) != 2 {
		t.Fatalf("requests = %d, want 2 (full library and vetted list, no purchased-list request)", len(queries))
	}
	for i, query := range queries {
		if got := query.Get("include_played_free_games"); got != "false" {
			t.Fatalf("request %d include_played_free_games = %q, want false", i, got)
		}
	}
	if got := queries[1].Get("skip_unvetted_apps"); got != "true" {
		t.Fatalf("vetted request skip_unvetted_apps = %q, want true", got)
	}
}

// With ExcludePlayedFreeGames the full list is the purchased list, but the vetted list still decides Unvetted: a game
// missing from it is Unvetted while still Purchased, and only the full and vetted requests run.
func TestGetOwnedGamesExcludePlayedFreeGamesMarksUnvettedMissingFromVettedList(t *testing.T) {
	const path = "/IPlayerService/GetOwnedGames/v0001/"
	library := `{"response":{"game_count":2,"games":[{"appid":10,"name":"Bought","playtime_forever":1},{"appid":30,"name":"Bought Unvetted","playtime_forever":3}]}}`
	vetted := `{"response":{"game_count":1,"games":[{"appid":10,"name":"Bought"}]}}`
	client, log, closeServer := testOwnedRouteClient(t, func(q url.Values) (int, string) {
		if q.Get("skip_unvetted_apps") == "true" {
			return 0, vetted
		}
		return 0, library
	})
	defer closeServer()
	client.ExcludePlayedFreeGames = true

	games, err := client.GetOwnedGames(context.Background(), testSteamID)
	if err != nil {
		t.Fatal(err)
	}
	wantUnvetted := map[int64]bool{10: false, 30: true}
	if len(games) != 2 {
		t.Fatalf("games = %#v, want 2", games)
	}
	for _, g := range games {
		if !g.Purchased {
			t.Fatalf("game %d Purchased = false, want true", g.AppID)
		}
		if g.Unvetted != wantUnvetted[g.AppID] {
			t.Fatalf("game %d Unvetted = %v, want %v", g.AppID, g.Unvetted, wantUnvetted[g.AppID])
		}
	}
	queries := log.queryHistory(path)
	if len(queries) != 2 {
		t.Fatalf("requests = %d, want 2 (full library and vetted list)", len(queries))
	}
	for i, query := range queries {
		if got := query.Get("include_played_free_games"); got != "false" {
			t.Fatalf("request %d include_played_free_games = %q, want false", i, got)
		}
	}
}

// testOwnedRouteClient answers each GetOwnedGames request with route(query): a non-zero status is an HTTP error,
// otherwise the body is served. It returns the request log like testOwnedClient.
func testOwnedRouteClient(t *testing.T, route func(q url.Values) (int, string)) (*Client, *requestLog, func()) {
	t.Helper()
	const path = "/IPlayerService/GetOwnedGames/v0001/"
	log := &requestLog{attempts: map[string]int{}, queries: map[string]url.Values{}, history: map[string][]url.Values{}}
	client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		log.record(r)
		status, body := route(r.URL.Query())
		if status != 0 {
			http.Error(w, "owned games unavailable", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	return client, log, closeServer
}

// The vetted list (skip_unvetted_apps=true) marks Unvetted: an app in it is vetted, one missing from it is unvetted,
// and a played-free app can be vetted too.
func TestGetOwnedGamesMarksVettedFromThirdList(t *testing.T) {
	const path = "/IPlayerService/GetOwnedGames/v0001/"
	full := `{"response":{"game_count":3,"games":[{"appid":10,"name":"Bought","playtime_forever":1},{"appid":20,"name":"Played Free Vetted","playtime_forever":2},{"appid":30,"name":"Unvetted","playtime_forever":3}]}}`
	purchased := `{"response":{"game_count":1,"games":[{"appid":10,"name":"Bought"}]}}`
	vetted := `{"response":{"game_count":2,"games":[{"appid":10,"name":"Bought"},{"appid":20,"name":"Played Free Vetted"}]}}`
	client, log, closeServer := testOwnedRouteClient(t, func(q url.Values) (int, string) {
		switch {
		case q.Get("skip_unvetted_apps") == "true":
			return 0, vetted
		case q.Get("include_played_free_games") == "false":
			return 0, purchased
		default:
			return 0, full
		}
	})
	defer closeServer()

	games, err := client.GetOwnedGames(context.Background(), testSteamID)
	if err != nil {
		t.Fatal(err)
	}
	wantUnvetted := map[int64]bool{10: false, 20: false, 30: true}
	if len(games) != 3 {
		t.Fatalf("games = %#v, want the full list of 3", games)
	}
	for _, g := range games {
		if g.Unvetted != wantUnvetted[g.AppID] {
			t.Fatalf("game %d Unvetted = %v, want %v", g.AppID, g.Unvetted, wantUnvetted[g.AppID])
		}
	}
	queries := log.queryHistory(path)
	if len(queries) != 3 {
		t.Fatalf("requests = %d, want 3", len(queries))
	}
	if queries[2].Get("skip_unvetted_apps") != "true" || queries[2].Get("include_played_free_games") != "true" {
		t.Fatalf("vetted request query = %v, want skip_unvetted_apps=true and include_played_free_games=true", queries[2])
	}
}

// With SkipUnvettedApps the library already excludes unvetted apps, so no game is Unvetted without a vetted request.
func TestGetOwnedGamesSkipUnvettedAppsMarksAllVetted(t *testing.T) {
	full := `{"response":{"game_count":2,"games":[{"appid":10,"name":"One","playtime_forever":1},{"appid":20,"name":"Two","playtime_forever":2}]}}`
	client, log, closeServer := testOwnedClient(t, full, full, 0)
	defer closeServer()
	client.SkipUnvettedApps = true

	games, err := client.GetOwnedGames(context.Background(), testSteamID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range games {
		if g.Unvetted {
			t.Fatalf("game %d Unvetted = true, want false when SkipUnvettedApps is set", g.AppID)
		}
	}
	if n := len(log.queryHistory("/IPlayerService/GetOwnedGames/v0001/")); n != 2 {
		t.Fatalf("requests = %d, want 2 (no vetted-list request)", n)
	}
}

// The vetted list is validated like the other lists. A bare response or an incomplete list is an error, and so is
// an HTTP failure, which is not retried on 404. An explicit empty list (game_count 0) is a valid answer: every game
// is then unvetted. A non-empty vetted list marks only the games it lists as vetted.
func TestGetOwnedGamesVettedListValidation(t *testing.T) {
	full := `{"response":{"game_count":1,"games":[{"appid":10,"name":"Game","playtime_forever":1}]}}`
	tests := []struct {
		name         string
		status       int
		vetted       string
		wantErr      string
		wantUnvetted bool // Unvetted for game 10 when no error
	}{
		{name: "count zero and empty list marks every game unvetted", vetted: `{"response":{"game_count":0,"games":[]}}`, wantUnvetted: true},
		{name: "count zero without games key marks every game unvetted", vetted: `{"response":{"game_count":0}}`, wantUnvetted: true},
		{name: "bare response is unavailable", vetted: `{"response":{}}`, wantErr: "Steam did not return the list of reviewed games. Try again later."},
		{name: "count positive and empty list is incomplete", vetted: `{"response":{"game_count":1,"games":[]}}`, wantErr: "Steam returned an incomplete list of reviewed games. Try again later."},
		{name: "http 404 fails the call", status: http.StatusNotFound, wantErr: "returned 404 Not Found"},
		{name: "listed game is vetted", vetted: `{"response":{"game_count":1,"games":[{"appid":10,"name":"Game"}]}}`, wantUnvetted: false},
		{name: "unlisted game is unvetted", vetted: `{"response":{"game_count":1,"games":[{"appid":99,"name":"Other"}]}}`, wantUnvetted: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, log, closeServer := testOwnedRouteClient(t, func(q url.Values) (int, string) {
				if q.Get("skip_unvetted_apps") == "true" {
					return tc.status, tc.vetted
				}
				return 0, full
			})
			defer closeServer()

			games, err := client.GetOwnedGames(context.Background(), testSteamID)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want text %q", err, tc.wantErr)
				}
				if games != nil {
					t.Fatalf("games = %#v, want nil when the call fails", games)
				}
				if n := len(log.queryHistory("/IPlayerService/GetOwnedGames/v0001/")); n != 3 {
					t.Fatalf("requests = %d, want 3 (full, purchased, then vetted failure)", n)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want no error for a valid empty vetted list", err)
			}
			if len(games) != 1 || games[0].Unvetted != tc.wantUnvetted {
				t.Fatalf("games = %#v, want one game with Unvetted=%v", games, tc.wantUnvetted)
			}
		})
	}
}

// The purchased list is validated like the full list. An explicit empty list (game_count 0) is valid; a bare
// `{"response":{}}` or an incomplete list is an error, so a glitch cannot read as "no purchases" and clear
// every purchased flag.
func TestGetOwnedGamesPurchasedListEmptiness(t *testing.T) {
	full := `{"response":{"game_count":1,"games":[{"appid":10,"name":"Played Free","playtime_forever":1}]}}`
	tests := []struct {
		name      string
		purchased string
		wantErr   string
	}{
		{name: "count zero and empty list is a valid empty list", purchased: `{"response":{"game_count":0,"games":[]}}`},
		{name: "count zero without games key is a valid empty list", purchased: `{"response":{"game_count":0}}`},
		{name: "bare response is unavailable", purchased: `{"response":{}}`, wantErr: "Steam did not return your purchased games. Try again later."},
		{name: "count positive and empty list is incomplete", purchased: `{"response":{"game_count":2,"games":[]}}`, wantErr: "Steam returned an incomplete list of purchased games. Try again later."},
		{name: "count positive without games key is incomplete", purchased: `{"response":{"game_count":1}}`, wantErr: "Steam returned an incomplete list of purchased games. Try again later."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, _, closeServer := testOwnedClient(t, full, tc.purchased, 0)
			defer closeServer()

			games, err := client.GetOwnedGames(context.Background(), testSteamID)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want no error for a valid empty purchased list", err)
			}
			if len(games) != 1 || games[0].Purchased {
				t.Fatalf("games = %#v, want one game not purchased", games)
			}
		})
	}
}

func TestGetOwnedGamesPurchasedListRetriesTransientStatus(t *testing.T) {
	const path = "/IPlayerService/GetOwnedGames/v0001/"
	var purchasedAttempts atomic.Int32
	full := `{"response":{"game_count":1,"games":[{"appid":10,"name":"Bought","playtime_forever":1}]}}`
	purchased := `{"response":{"game_count":1,"games":[{"appid":10,"name":"Bought"}]}}`
	client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("include_played_free_games") == "false" {
			if purchasedAttempts.Add(1) == 1 {
				http.Error(w, "try later", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte(purchased))
			return
		}
		_, _ = w.Write([]byte(full))
	})
	defer closeServer()

	games, err := client.GetOwnedGames(context.Background(), testSteamID)
	if err != nil {
		t.Fatal(err)
	}
	if purchasedAttempts.Load() != 2 || len(games) != 1 || !games[0].Purchased {
		t.Fatalf("purchased attempts=%d games=%#v, want retry then purchased game", purchasedAttempts.Load(), games)
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
	if err == nil || err.Error() != "Steam did not provide achievements for this game." {
		t.Fatalf("err = %v, want the fixed sentence", err)
	}
	// Steam's own text is kept for the log only.
	if got := usererr.Raw(err).Error(); got != "Profile is private" {
		t.Fatalf("log cause = %q, want Steam's message", got)
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
	// Full list: two 503s then success (3 requests). Purchased list: one success (4th request). Vetted list: one success (5th).
	if attempts.Load() != 5 || len(games) != 1 || games[0].Name != "Recovered" {
		t.Fatalf("attempts=%d games=%#v, want retry then recovered game", attempts.Load(), games)
	}
}

const (
	gameAchievementsPath = "/IPlayerService/GetGameAchievements/v1/"
	userAchievementsPath = "/IPlayerService/GetUserAchievements/v1/"
	testSteamID          = "11111111111111111"
)

type fixtureResponse struct {
	body    string
	eresult string // sent as x-eresult when non-empty
}

type requestLog struct {
	mu       sync.Mutex
	attempts map[string]int
	queries  map[string]url.Values   // query of the latest request per path
	history  map[string][]url.Values // queries of every request per path, in order
}

func (l *requestLog) record(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[r.URL.Path]++
	l.queries[r.URL.Path] = r.URL.Query()
	l.history[r.URL.Path] = append(l.history[r.URL.Path], r.URL.Query())
}

// queryHistory returns the query of every request to path, in arrival order.
func (l *requestLog) queryHistory(path string) []url.Values {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]url.Values(nil), l.history[path]...)
}

func (l *requestLog) attemptCount(path string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.attempts[path]
}

func (l *requestLog) query(path string) url.Values {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.queries[path]
}

// newTestServer serves handler over httptest and returns a Client pointed at it with zero retry
// backoff, plus the server's close func. The API key is "test-key".
func newTestServer(t *testing.T, handler http.HandlerFunc) (*Client, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	client := &Client{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()}
	client.SetRetryBackoff(func(int) time.Duration { return 0 })
	return client, server.Close
}

func testSetClient(t *testing.T, responses map[string]fixtureResponse) (*Client, *requestLog, func()) {
	t.Helper()
	log := &requestLog{attempts: map[string]int{}, queries: map[string]url.Values{}, history: map[string][]url.Values{}}
	client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		log.record(r)
		resp, ok := responses[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if resp.eresult != "" {
			w.Header().Set("x-eresult", resp.eresult)
		}
		_, _ = w.Write([]byte(resp.body))
	})
	return client, log, closeServer
}

// testStatusSequenceClient answers the first len(statuses) requests with those HTTP statuses,
// then serves body with the given x-eresult.
func testStatusSequenceClient(t *testing.T, path string, statuses []int, eresult, body string) (*Client, *atomic.Int32, func()) {
	t.Helper()
	var attempts atomic.Int32
	client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		n := int(attempts.Add(1))
		if n <= len(statuses) {
			http.Error(w, "try later", statuses[n-1])
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if eresult != "" {
			w.Header().Set("x-eresult", eresult)
		}
		_, _ = w.Write([]byte(body))
	})
	return client, &attempts, closeServer
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func findSetAchievement(t *testing.T, achievements []SetAchievement, name string) SetAchievement {
	t.Helper()
	for _, a := range achievements {
		if a.InternalName == name {
			return a
		}
	}
	t.Fatalf("achievement %q not found", name)
	return SetAchievement{}
}

func TestGetGameAchievementsParsesSetsFromPWS2(t *testing.T) {
	client, log, closeServer := testSetClient(t, map[string]fixtureResponse{
		gameAchievementsPath: {body: readFixture(t, "pws2_game_achievements.json"), eresult: "1"},
	})
	defer closeServer()

	game, err := client.GetGameAchievements(context.Background(), 2968420)
	if err != nil {
		t.Fatal(err)
	}
	if game.SchemaVersion != 11 || len(game.Achievements) != 60 || len(game.Sets) != 3 {
		t.Fatalf("schema=%d achievements=%d sets=%d, want 11/60/3", game.SchemaVersion, len(game.Achievements), len(game.Sets))
	}
	perGroup := map[int64]int{}
	for _, a := range game.Achievements {
		perGroup[a.GroupID]++
	}
	if perGroup[0] != 40 || perGroup[1] != 10 || perGroup[2] != 10 {
		t.Fatalf("per-group counts = %#v, want 40/10/10", perGroup)
	}
	base := game.Sets[0]
	if base.GroupID != 0 || base.DLCAppID != 0 || base.TotalAchievements != 40 || base.CompletionAchievements != 40 || !base.Public {
		t.Fatalf("base set = %#v, want group 0, 40/40, public", base)
	}
	adventure := game.Sets[1]
	if adventure.GroupID != 1 || adventure.DLCAppID != 4173830 || adventure.Name != "PowerWash Simulator 2 - Adventure Time Pack" ||
		adventure.TotalAchievements != 10 || adventure.CompletionAchievements != 10 || adventure.Archived || adventure.DeveloperOnly || !adventure.Public {
		t.Fatalf("adventure set = %#v", adventure)
	}
	first := game.Achievements[0]
	if first.InternalName != "TROPHY_1" || first.Key != 512 || first.GroupID != 0 || first.Archived || first.GlobalPercent == nil || *first.GlobalPercent != 93.9 {
		t.Fatalf("first achievement = %#v, want TROPHY_1 key 512 group 0 at 93.9%%", first)
	}
	query := log.query(gameAchievementsPath)
	if query.Get("appid") != "2968420" || query.Get("language") != "english" || query.Get("key") != "test-key" {
		t.Fatalf("query = %v, want appid, language=english and key", query)
	}
}

func TestGetGameAchievementsPortal2EmptyBaseSet(t *testing.T) {
	client, _, closeServer := testSetClient(t, map[string]fixtureResponse{
		gameAchievementsPath: {body: readFixture(t, "portal2_game_achievements.json"), eresult: "1"},
	})
	defer closeServer()

	game, err := client.GetGameAchievements(context.Background(), 620)
	if err != nil {
		t.Fatal(err)
	}
	if len(game.Achievements) != 4 || len(game.Sets) != 3 {
		t.Fatalf("achievements=%d sets=%d, want 4/3", len(game.Achievements), len(game.Sets))
	}
	if game.Sets[0].GroupID != 0 || game.Sets[0].TotalAchievements != 0 || game.Sets[0].CompletionAchievements != 0 {
		t.Fatalf("base set = %#v, want empty group 0", game.Sets[0])
	}
	for _, a := range game.Achievements {
		if a.GroupID == 0 {
			t.Fatalf("achievement %#v assigned to empty group 0", a)
		}
	}
	if game.Sets[1].Name != "Cooperative" || game.Sets[2].Name != "Single Player" {
		t.Fatalf("set names = %q / %q", game.Sets[1].Name, game.Sets[2].Name)
	}
}

func TestGetGameAchievementsTF2KeepsArchivedAndHiddenSets(t *testing.T) {
	client, _, closeServer := testSetClient(t, map[string]fixtureResponse{
		gameAchievementsPath: {body: readFixture(t, "tf2_game_achievements.json"), eresult: "1"},
	})
	defer closeServer()

	game, err := client.GetGameAchievements(context.Background(), 440)
	if err != nil {
		t.Fatal(err)
	}
	if len(game.Achievements) != 12 || len(game.Sets) != 4 {
		t.Fatalf("achievements=%d sets=%d, want 12/4", len(game.Achievements), len(game.Sets))
	}
	archived := 0
	for _, a := range game.Achievements {
		if a.Archived {
			archived++
			if a.GroupID != 11 {
				t.Fatalf("archived achievement in group %d, want 11", a.GroupID)
			}
		}
	}
	if archived != 4 {
		t.Fatalf("archived achievements = %d, want 4", archived)
	}
	replay := game.Sets[1]
	if replay.GroupID != 11 || replay.Name != "Replay" || replay.TotalAchievements != 8 || replay.CompletionAchievements != 4 || replay.Archived {
		t.Fatalf("replay set = %#v, want total 8 completion 4", replay)
	}
	mvm := game.Sets[2]
	if mvm.GroupID != 12 || mvm.DLCAppID != 0 || mvm.Name != "Mann vs. Machine" || !mvm.Public {
		t.Fatalf("custom set = %#v, want non-DLC public set", mvm)
	}
	dev := game.Sets[3]
	if dev.GroupID != 13 || !dev.DeveloperOnly || dev.Public {
		t.Fatalf("developer set = %#v, want developeronly and not public", dev)
	}
	if got := findSetAchievement(t, game.Achievements, "ACH_REPLAY_OLD_4"); got.GlobalPercent != nil {
		t.Fatalf("missing player_percent_unlocked parsed as %v, want nil", *got.GlobalPercent)
	}
	if got := findSetAchievement(t, game.Achievements, "ACH_MVM_1"); got.GlobalPercent == nil || *got.GlobalPercent != 3.2 {
		t.Fatalf("ACH_MVM_1 percent = %#v, want 3.2", got.GlobalPercent)
	}
}

func TestGetGameAchievementsEResultHandling(t *testing.T) {
	portal := readFixture(t, "portal2_game_achievements.json")
	noSchema := readFixture(t, "no_schema_game_achievements.json")
	tests := []struct {
		name    string
		eresult string
		body    string
		wantAch int
		wantErr string
	}{
		{name: "ok decodes", eresult: "1", body: portal, wantAch: 4},
		{name: "missing header decodes", eresult: "", body: portal, wantAch: 4},
		{name: "no match is empty without error", eresult: "42", body: noSchema, wantAch: 0},
		{name: "other eresult is error", eresult: "2", body: portal, wantErr: "eresult 2"},
		{name: "invalid eresult is error", eresult: "abc", body: portal, wantErr: "invalid x-eresult"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, log, closeServer := testSetClient(t, map[string]fixtureResponse{
				gameAchievementsPath: {body: tc.body, eresult: tc.eresult},
			})
			defer closeServer()

			game, err := client.GetGameAchievements(context.Background(), 620)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(game.Achievements) != tc.wantAch {
				t.Fatalf("achievements = %d, want %d", len(game.Achievements), tc.wantAch)
			}
			if got := log.attemptCount(gameAchievementsPath); got != 1 {
				t.Fatalf("attempts = %d, want 1 (eresult is not retried)", got)
			}
		})
	}
}

func TestGetGameAchievementsPlayerPercentParsing(t *testing.T) {
	client, _, closeServer := testSetClient(t, map[string]fixtureResponse{
		gameAchievementsPath: {eresult: "1", body: `{"response":{"achievements":[
			{"internal_name":"ACH_STRING","internal_key":1,"groupid":0,"player_percent_unlocked":"93.9"},
			{"internal_name":"ACH_NUMBER","internal_key":2,"groupid":0,"player_percent_unlocked":12.5},
			{"internal_name":"ACH_ZERO","internal_key":3,"groupid":0,"player_percent_unlocked":"0.0"},
			{"internal_name":"ACH_NULL","internal_key":4,"groupid":0,"player_percent_unlocked":null},
			{"internal_name":"ACH_MISSING","internal_key":5,"groupid":0}
		]}}`},
	})
	defer closeServer()

	game, err := client.GetGameAchievements(context.Background(), 620)
	if err != nil {
		t.Fatal(err)
	}
	if got := findSetAchievement(t, game.Achievements, "ACH_STRING"); got.GlobalPercent == nil || *got.GlobalPercent != 93.9 {
		t.Fatalf("string percent = %#v, want 93.9", got.GlobalPercent)
	}
	if got := findSetAchievement(t, game.Achievements, "ACH_NUMBER"); got.GlobalPercent == nil || *got.GlobalPercent != 12.5 {
		t.Fatalf("numeric percent = %#v, want 12.5", got.GlobalPercent)
	}
	if got := findSetAchievement(t, game.Achievements, "ACH_ZERO"); got.GlobalPercent == nil || *got.GlobalPercent != 0 {
		t.Fatalf("zero percent = %#v, want non-nil 0", got.GlobalPercent)
	}
	if got := findSetAchievement(t, game.Achievements, "ACH_NULL"); got.GlobalPercent != nil {
		t.Fatalf("null percent = %v, want nil", *got.GlobalPercent)
	}
	if got := findSetAchievement(t, game.Achievements, "ACH_MISSING"); got.GlobalPercent != nil {
		t.Fatalf("missing percent = %v, want nil", *got.GlobalPercent)
	}
}

func TestGetGameAchievementsInvalidPercentIsError(t *testing.T) {
	client, _, closeServer := testSetClient(t, map[string]fixtureResponse{
		gameAchievementsPath: {eresult: "1", body: `{"response":{"achievements":[{"internal_name":"ACH_BAD","internal_key":1,"player_percent_unlocked":"abc"}]}}`},
	})
	defer closeServer()

	if _, err := client.GetGameAchievements(context.Background(), 620); err == nil {
		t.Fatal("err = nil, want decode error for non-numeric percent")
	}
}

func TestGetGameAchievementsDropsBlankNamesAndDefaultsPublic(t *testing.T) {
	client, _, closeServer := testSetClient(t, map[string]fixtureResponse{
		gameAchievementsPath: {eresult: "1", body: `{"response":{"groups":[{"groupid":0,"total_achievements":1,"completion_achievements":1}],
			"achievements":[{"internal_name":"","internal_key":1,"groupid":0},{"internal_name":"ACH_OK","internal_key":2,"groupid":0}]}}`},
	})
	defer closeServer()

	game, err := client.GetGameAchievements(context.Background(), 620)
	if err != nil {
		t.Fatal(err)
	}
	if len(game.Achievements) != 1 || game.Achievements[0].InternalName != "ACH_OK" || game.Achievements[0].Key != 2 {
		t.Fatalf("achievements = %#v, want only ACH_OK", game.Achievements)
	}
	if len(game.Sets) != 1 || !game.Sets[0].Public {
		t.Fatalf("sets = %#v, want one public set when ispublic is absent", game.Sets)
	}
}

func TestGetUserAchievementsUnlockedOnlyForPWS2(t *testing.T) {
	client, log, closeServer := testSetClient(t, map[string]fixtureResponse{
		userAchievementsPath: {body: readFixture(t, "pws2_user_achievements.json"), eresult: "1"},
	})
	defer closeServer()

	user, err := client.GetUserAchievements(context.Background(), testSteamID, 2968420)
	if err != nil {
		t.Fatal(err)
	}
	if len(user.Unlocked) != 40 {
		t.Fatalf("unlocked = %d, want 40", len(user.Unlocked))
	}
	if unlock, ok := user.Unlocked[512]; !ok || unlock == nil || *unlock != 1774123910 {
		t.Fatalf("unlock for key 512 = %v (ok=%v), want 1774123910", unlock, ok)
	}
	if _, ok := user.Unlocked[776]; ok {
		t.Fatal("DLC key 776 unlocked in base-only response, want absent")
	}
	if len(user.Groups) != 1 || !user.Groups[0] {
		t.Fatalf("groups = %#v, want only group 0", user.Groups)
	}
	query := log.query(userAchievementsPath)
	if query.Get("steamid") != testSteamID || query.Get("appid") != "2968420" || query.Get("key") != "test-key" {
		t.Fatalf("query = %v", query)
	}
}

func TestGetUserAchievementsSkipsLockedEntries(t *testing.T) {
	client, _, closeServer := testSetClient(t, map[string]fixtureResponse{
		userAchievementsPath: {eresult: "1", body: `{"response":{"achievements":[
			{"internal_key":10,"unlocked":true,"unlock_time":0},
			{"internal_key":11,"unlocked":false,"unlock_time":0},
			{"internal_key":12,"unlocked":"1","unlock_time":"77"}],
			"groups":[{"groupid":0}]}}`},
	})
	defer closeServer()

	user, err := client.GetUserAchievements(context.Background(), testSteamID, 620)
	if err != nil {
		t.Fatal(err)
	}
	if len(user.Unlocked) != 2 {
		t.Fatalf("unlocked = %#v, want keys 10 and 12 only", user.Unlocked)
	}
	if unlock, ok := user.Unlocked[10]; !ok || unlock != nil {
		t.Fatalf("key 10 = %v (ok=%v), want present with nil unlock time", unlock, ok)
	}
	if _, ok := user.Unlocked[11]; ok {
		t.Fatal("locked key 11 present in unlocked map")
	}
	if unlock := user.Unlocked[12]; unlock == nil || *unlock != 77 {
		t.Fatalf("key 12 = %v, want 77", unlock)
	}
}

func TestGetUserAchievementsDLCOwnedGroupsPresent(t *testing.T) {
	client, _, closeServer := testSetClient(t, map[string]fixtureResponse{
		userAchievementsPath: {body: readFixture(t, "pws2_user_dlc_owned.json"), eresult: "1"},
	})
	defer closeServer()

	user, err := client.GetUserAchievements(context.Background(), testSteamID, 2968420)
	if err != nil {
		t.Fatal(err)
	}
	if len(user.Groups) != 3 || !user.Groups[0] || !user.Groups[1] || !user.Groups[2] {
		t.Fatalf("groups = %#v, want 0, 1 and 2", user.Groups)
	}
	if len(user.Unlocked) != 42 {
		t.Fatalf("unlocked = %d, want 42", len(user.Unlocked))
	}
	if unlock := user.Unlocked[777]; unlock == nil || *unlock != 1775100002 {
		t.Fatalf("DLC key 777 unlock = %v, want 1775100002", unlock)
	}
}

func TestGetUserAchievementsEResultHandling(t *testing.T) {
	t.Run("no match is empty without error", func(t *testing.T) {
		client, log, closeServer := testSetClient(t, map[string]fixtureResponse{
			userAchievementsPath: {body: `{"response":{}}`, eresult: "42"},
		})
		defer closeServer()

		user, err := client.GetUserAchievements(context.Background(), testSteamID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if user.Unlocked == nil || user.Groups == nil || len(user.Unlocked) != 0 || len(user.Groups) != 0 {
			t.Fatalf("user = %#v, want empty non-nil maps", user)
		}
		if log.attemptCount(userAchievementsPath) != 1 {
			t.Fatalf("attempts = %d, want 1", log.attemptCount(userAchievementsPath))
		}
	})
	t.Run("other eresult is error", func(t *testing.T) {
		client, _, closeServer := testSetClient(t, map[string]fixtureResponse{
			userAchievementsPath: {body: `{"response":{}}`, eresult: "2"},
		})
		defer closeServer()

		if _, err := client.GetUserAchievements(context.Background(), testSteamID, 1); err == nil || !strings.Contains(err.Error(), "eresult 2") {
			t.Fatalf("err = %v, want eresult 2 error", err)
		}
	})
}

func TestGetGameAchievementsRetriesTransientStatusesUnchanged(t *testing.T) {
	client, attempts, closeServer := testStatusSequenceClient(t, gameAchievementsPath, []int{http.StatusTooManyRequests, http.StatusServiceUnavailable}, "1", readFixture(t, "portal2_game_achievements.json"))
	defer closeServer()

	game, err := client.GetGameAchievements(context.Background(), 620)
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 || len(game.Achievements) != 4 {
		t.Fatalf("attempts=%d achievements=%d, want 3 and 4", attempts.Load(), len(game.Achievements))
	}
}

func TestGetUserAchievementsGivesUpAfterFourRetryableAttempts(t *testing.T) {
	client, attempts, closeServer := testStatusSequenceClient(t, userAchievementsPath, []int{503, 503, 503, 503}, "1", `{"response":{}}`)
	defer closeServer()

	if _, err := client.GetUserAchievements(context.Background(), testSteamID, 620); err == nil {
		t.Fatal("err = nil, want error after retries are exhausted")
	}
	if attempts.Load() != 4 {
		t.Fatalf("attempts = %d, want 4", attempts.Load())
	}
}

func TestGetGameAchievementsNonRetryableStatusFailsImmediately(t *testing.T) {
	client, attempts, closeServer := testStatusSequenceClient(t, gameAchievementsPath, []int{http.StatusBadRequest}, "1", readFixture(t, "portal2_game_achievements.json"))
	defer closeServer()

	if _, err := client.GetGameAchievements(context.Background(), 620); err == nil {
		t.Fatal("err = nil, want HTTP 400 error")
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestGetGameAchievementsTypedHTTPStatusError(t *testing.T) {
	tests := []struct {
		name         string
		statuses     []int
		wantCode     int
		wantAttempts int32
		wantText     string
	}{
		{name: "non-retryable 404 fails once", statuses: []int{http.StatusNotFound}, wantCode: 404, wantAttempts: 1, wantText: "Steam API " + gameAchievementsPath + " returned 404 Not Found: try later"},
		{name: "bad gateway is not retried", statuses: []int{http.StatusBadGateway}, wantCode: 502, wantAttempts: 1, wantText: "Steam API " + gameAchievementsPath + " returned 502 Bad Gateway: try later"},
		{name: "gateway timeout is not retried", statuses: []int{http.StatusGatewayTimeout}, wantCode: 504, wantAttempts: 1, wantText: "Steam API " + gameAchievementsPath + " returned 504 Gateway Timeout: try later"},
		{name: "retryable 503 exhausted", statuses: []int{503, 503, 503, 503}, wantCode: 503, wantAttempts: 4, wantText: "Steam API " + gameAchievementsPath + " returned 503 Service Unavailable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, attempts, closeServer := testStatusSequenceClient(t, gameAchievementsPath, tc.statuses, "1", readFixture(t, "portal2_game_achievements.json"))
			defer closeServer()

			_, err := client.GetGameAchievements(context.Background(), 620)
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("err = %#v, want *HTTPStatusError", err)
			}
			if statusErr.Code != tc.wantCode || statusErr.Path != gameAchievementsPath {
				t.Fatalf("status error = %#v, want code %d on %s", statusErr, tc.wantCode, gameAchievementsPath)
			}
			if err.Error() != tc.wantText {
				t.Fatalf("error text = %q, want %q", err.Error(), tc.wantText)
			}
			if attempts.Load() != tc.wantAttempts {
				t.Fatalf("attempts = %d, want %d", attempts.Load(), tc.wantAttempts)
			}
		})
	}
}

func TestGetGameAchievementsTypedEResultError(t *testing.T) {
	client, _, closeServer := testSetClient(t, map[string]fixtureResponse{
		gameAchievementsPath: {body: `{"response":{}}`, eresult: "2"},
	})
	defer closeServer()

	_, err := client.GetGameAchievements(context.Background(), 620)
	var eresultErr *EResultError
	if !errors.As(err, &eresultErr) {
		t.Fatalf("err = %#v, want *EResultError", err)
	}
	if eresultErr.Code != 2 || err.Error() != "Steam API "+gameAchievementsPath+" returned eresult 2" {
		t.Fatalf("eresult error = %#v (%q), want code 2 with unchanged text", eresultErr, err.Error())
	}
}

func testClient(t *testing.T, responses map[string]string) (*Client, func()) {
	t.Helper()
	return newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, ok := responses[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

// roundTripFunc adapts a function to http.RoundTripper for deterministic transport failures.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTransportErrorDoesNotLeakAPIKey(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("boom: connection refused")
	})}
	client := &Client{APIKey: "test-key", BaseURL: "http://steam.invalid", HTTPClient: httpClient}
	client.retryBackoff = func(int) time.Duration { return 0 }

	_, err := client.GetOwnedGames(context.Background(), testSteamID)
	if err == nil {
		t.Fatal("err = nil, want transport error")
	}
	text := err.Error()
	if strings.Contains(text, "test-key") {
		t.Fatalf("error leaks API key: %q", text)
	}
	if !strings.Contains(text, "key=REDACTED") || !strings.Contains(text, "/IPlayerService/GetOwnedGames/v0001/") {
		t.Fatalf("error = %q, want redacted key and request path kept", text)
	}
}

func TestHTTPStatusErrorBodyRedactsAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden: https://api.steampowered.com/x?key=test-key&steamid=1 and &amp;key=test-key", http.StatusForbidden)
	}))
	defer server.Close()
	client := &Client{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()}
	client.retryBackoff = func(int) time.Duration { return 0 }

	_, err := client.GetOwnedGames(context.Background(), testSteamID)
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusForbidden {
		t.Fatalf("err = %#v, want 403 *HTTPStatusError", err)
	}
	text := err.Error()
	if strings.Contains(text, "test-key") {
		t.Fatalf("error leaks API key: %q", text)
	}
	if !strings.Contains(text, "key=REDACTED") {
		t.Fatalf("error = %q, want redacted key in body", text)
	}
}

// A non-2xx body can echo a SteamID64 as a JSON field or inside a request URL. Both forms must be removed.
func TestRedactBodyRemovesSteamID64(t *testing.T) {
	c := &Client{APIKey: testLongKey}
	tests := []struct{ name, in, want string }{
		{name: "json field", in: `{"error":"bad request","steamid":"` + realSteamID + `"}`, want: `{"error":"bad request","steamid":"REDACTED"}`},
		{name: "query value", in: "see https://h/x?steamid=" + realSteamID + "&appid=440", want: "see https://h/x?steamid=REDACTED&appid=440"},
		{name: "bare in text", in: "player " + realSteamID + " not found", want: "player REDACTED not found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := c.redactBody(tc.in)
			if got != tc.want {
				t.Fatalf("redactBody(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.Contains(got, realSteamID) {
				t.Fatalf("redactBody kept the SteamID64: %q", got)
			}
		})
	}
}

func TestHTTPStatusErrorBodyRedactsSteamID64(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden","steamid":"` + realSteamID + `","request":"https://h/x?steamid=` + realSteamID + `&appid=440"}`))
	}))
	defer server.Close()
	client := &Client{APIKey: testLongKey, BaseURL: server.URL, HTTPClient: server.Client()}
	client.retryBackoff = func(int) time.Duration { return 0 }

	_, err := client.GetOwnedGames(context.Background(), testSteamID)
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.Code != http.StatusForbidden {
		t.Fatalf("err = %#v, want 403 *HTTPStatusError", err)
	}
	if strings.Contains(err.Error(), realSteamID) {
		t.Fatalf("error leaks SteamID64: %q", err.Error())
	}
	if !strings.Contains(err.Error(), `"steamid":"REDACTED"`) || !strings.Contains(err.Error(), "appid=440") {
		t.Fatalf("error = %q, want the body kept with the SteamID64 redacted", err.Error())
	}
}

func TestRedactURLError(t *testing.T) {
	sentinel := errors.New("dial tcp 127.0.0.1:1: connection refused")
	tests := []struct {
		name   string
		rawURL string
		want   string
	}{
		{name: "key first", rawURL: "https://api.steampowered.com/x/?key=test-key&steamid=1", want: "https://api.steampowered.com/x/?key=REDACTED&steamid=REDACTED"},
		{name: "key middle", rawURL: "https://h/x?steamid=1&key=test-key&format=json", want: "https://h/x?steamid=REDACTED&key=REDACTED&format=json"},
		{name: "key last", rawURL: "https://h/x?appid=1&key=test-key", want: "https://h/x?appid=1&key=REDACTED"},
		{name: "key only param", rawURL: "https://h/x?key=test-key", want: "https://h/x?key=REDACTED"},
		{name: "empty key value", rawURL: "https://h/x?key=&a=1", want: "https://h/x?key=REDACTED&a=1"},
		{name: "url-encoded key value", rawURL: "https://h/x?key=a%2Bb%3Dc&x=1", want: "https://h/x?key=REDACTED&x=1"},
		{name: "no key param", rawURL: "https://h/x?appid=1", want: "https://h/x?appid=1"},
		{name: "similar param name kept", rawURL: "https://h/x?monkey=banana&appid=1", want: "https://h/x?monkey=banana&appid=1"},
		{name: "unparsable URL text", rawURL: "://bad host?key=test-key", want: "://bad host?key=REDACTED"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := &url.Error{Op: "Get", URL: tc.rawURL, Err: sentinel}
			got := (&Client{APIKey: "test-key"}).redactURLError(orig)
			var ue *url.Error
			if !errors.As(got, &ue) {
				t.Fatalf("got %#v, want *url.Error", got)
			}
			if ue.URL != tc.want {
				t.Fatalf("URL = %q, want %q", ue.URL, tc.want)
			}
			if ue.Op != "Get" || !errors.Is(got, sentinel) {
				t.Fatalf("Op/Err not preserved: %#v", ue)
			}
			if orig.URL != tc.rawURL {
				t.Fatalf("original error mutated: URL = %q, want %q", orig.URL, tc.rawURL)
			}
		})
	}
}

func TestRedactURLErrorPassThroughAndPlainErrors(t *testing.T) {
	c := &Client{APIKey: "test-key"}
	if got := c.redactURLError(nil); got != nil {
		t.Fatalf("nil error became %v", got)
	}
	plain := errors.New("connection reset")
	if got := c.redactURLError(plain); got != plain {
		t.Fatalf("non-url.Error changed: %v", got)
	}
}

// realSteamID is a SteamID64 in the shape Steam issues. No error text may contain it.
const realSteamID = "76561198000000001"

func TestRedactURLErrorIdentityParams(t *testing.T) {
	sentinel := errors.New("dial tcp 127.0.0.1:1: connection refused")
	tests := []struct {
		name   string
		rawURL string
		want   string
	}{
		{name: "steamid with key and appid", rawURL: "https://h/x?key=test-key&steamid=" + realSteamID + "&appid=440", want: "https://h/x?key=REDACTED&steamid=REDACTED&appid=440"},
		{name: "steamids with key", rawURL: "https://h/x?key=test-key&steamids=" + realSteamID, want: "https://h/x?key=REDACTED&steamids=REDACTED"},
		{name: "vanityurl with key and format", rawURL: "https://h/x?format=json&key=test-key&vanityurl=gabelogan", want: "https://h/x?format=json&key=REDACTED&vanityurl=REDACTED"},
		{name: "uppercase names", rawURL: "https://h/x?KEY=test-key&SteamID=" + realSteamID + "&STEAMIDS=" + realSteamID + "&VanityURL=gabelogan", want: "https://h/x?KEY=REDACTED&SteamID=REDACTED&STEAMIDS=REDACTED&VanityURL=REDACTED"},
		{name: "percent-encoded vanity value", rawURL: "https://h/x?vanityurl=a%26steamid%3D" + realSteamID + "&appid=440", want: "https://h/x?vanityurl=REDACTED&appid=440"},
		{name: "appid and timestamps kept", rawURL: "https://h/x?appid=2968420&unlocktime=1700000000&language=english&steamid=" + realSteamID, want: "https://h/x?appid=2968420&unlocktime=1700000000&language=english&steamid=REDACTED"},
		{name: "similar names kept", rawURL: "https://h/x?steamidx=1&steamidlist=2&monkey=banana&appid=440", want: "https://h/x?steamidx=1&steamidlist=2&monkey=banana&appid=440"},
		{name: "bare SteamID64 in text", rawURL: "https://h/profiles/" + realSteamID + "/x?appid=440", want: "https://h/profiles/REDACTED/x?appid=440"},
		{name: "clan id kept", rawURL: "https://h/x?clanid=103582791429521408", want: "https://h/x?clanid=103582791429521408"},
		{name: "clansteamid and monkey kept, html separators", rawURL: "https://h/x?clansteamid=1&amp;monkey=banana&amp;appid=440", want: "https://h/x?clansteamid=1&amp;monkey=banana&amp;appid=440"},
		// HTML-escaped separators: '&amp;' is matched by its ';'.
		{name: "html escaped vanityurl, appid kept", rawURL: "https://h/x?format=json&amp;vanityurl=gabelogan&amp;appid=5", want: "https://h/x?format=json&amp;vanityurl=REDACTED&amp;appid=5"},
		{name: "plain ampersand vanityurl, appid kept", rawURL: "https://h/x?format=json&vanityurl=gabelogan&appid=5", want: "https://h/x?format=json&vanityurl=REDACTED&appid=5"},
		// JSON-escaped separators: Go's encoding/json writes '&' as \u0026 in a body, and the value stops at the backslash.
		{name: "json escaped vanityurl, appid kept", rawURL: `{"url":"https://h/x?format=json\u0026vanityurl=gabelogan\u0026appid=5"}`, want: `{"url":"https://h/x?format=json\u0026vanityurl=REDACTED\u0026appid=5"}`},
		// The key value differs from the client's APIKey, so only the parameter pattern can redact it (the literal
		// key replacement would not match). The steamid value (12345) is not SteamID64-shaped, so the bare-ID rule cannot mask it. This pins the \u0026 prefix alternative for steamid and key.
		{name: "json escaped steamid and key", rawURL: `{"url":"https://h/x?appid=5\u0026steamid=12345\u0026key=other-key"}`, want: `{"url":"https://h/x?appid=5\u0026steamid=REDACTED\u0026key=REDACTED"}`},
		{name: "html escaped key other than client key", rawURL: "https://h/x?appid=5&amp;key=other-key&amp;appid=6", want: "https://h/x?appid=5&amp;key=REDACTED&amp;appid=6"},
		// A value ends at '&', '#', whitespace, a quote or an angle bracket, so text after a URL quoted in a body survives.
		{name: "json value kept after", rawURL: `{"url":"https://h/x?vanityurl=gabelogan","appid":440}`, want: `{"url":"https://h/x?vanityurl=REDACTED","appid":440}`},
		{name: "html attribute value kept after", rawURL: `<a href="https://h/x?key=test-key">open</a> and steamid text`, want: `<a href="https://h/x?key=REDACTED">open</a> and steamid text`},
		{name: "whitespace ends value", rawURL: "see https://h/x?steamid=" + realSteamID + " next", want: "see https://h/x?steamid=REDACTED next"},
		{name: "fragment ends value", rawURL: "https://h/x?steamids=" + realSteamID + "#top", want: "https://h/x?steamids=REDACTED#top"},
		{name: "single quote ends value", rawURL: "'https://h/x?vanityurl=gabelogan'", want: "'https://h/x?vanityurl=REDACTED'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := &url.Error{Op: "Get", URL: tc.rawURL, Err: sentinel}
			got := (&Client{APIKey: "test-key"}).redactURLError(orig)
			var ue *url.Error
			if !errors.As(got, &ue) {
				t.Fatalf("got %#v, want *url.Error", got)
			}
			if ue.URL != tc.want {
				t.Fatalf("URL = %q, want %q", ue.URL, tc.want)
			}
			if ue.Op != "Get" || !errors.Is(got, sentinel) {
				t.Fatalf("Op/Err not preserved: %#v", ue)
			}
			if strings.Contains(got.Error(), realSteamID) || strings.Contains(got.Error(), "test-key") || strings.Contains(got.Error(), "other-key") || strings.Contains(got.Error(), "steamid=12345") {
				t.Fatalf("error leaks identifiers: %q", got.Error())
			}
		})
	}
}

// Real requests through a transport that times out. Every error in the chain must be free of the key and the
// SteamIDs/vanity name, and errors.Is(DeadlineExceeded) and Timeout() must still hold after redaction.
func TestTransportErrorHidesIdentifiersAndKeepsCause(t *testing.T) {
	tests := []struct {
		name string
		call func(c *Client) error
	}{
		{name: "owned games steamid", call: func(c *Client) error {
			_, err := c.GetOwnedGames(context.Background(), realSteamID)
			return err
		}},
		{name: "player summary steamids", call: func(c *Client) error {
			_, err := c.GetPlayerSummary(context.Background(), realSteamID)
			return err
		}},
		{name: "user achievements steamid", call: func(c *Client) error {
			_, err := c.GetUserAchievements(context.Background(), realSteamID, 440)
			return err
		}},
		{name: "vanity lookup", call: func(c *Client) error {
			_, err := c.ResolveVanityURL(context.Background(), "gabelogan")
			return err
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, context.DeadlineExceeded
			})}
			client := &Client{APIKey: "test-key", BaseURL: "http://steam.invalid", HTTPClient: httpClient}
			client.retryBackoff = func(int) time.Duration { return 0 }

			err := tc.call(client)
			if err == nil {
				t.Fatal("err = nil, want transport error")
			}
			for e := err; e != nil; e = errors.Unwrap(e) {
				for _, secret := range []string{"test-key", realSteamID, "gabelogan"} {
					if strings.Contains(e.Error(), secret) {
						t.Fatalf("%T in chain leaks %q: %q", e, secret, e.Error())
					}
				}
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("errors.Is(err, DeadlineExceeded) = false for %v", err)
			}
			var ue *url.Error
			if !errors.As(err, &ue) || !ue.Timeout() {
				t.Fatalf("err = %#v, want *url.Error with Timeout() true", err)
			}
			if !strings.Contains(err.Error(), "REDACTED") {
				t.Fatalf("error = %q, want redacted query values", err.Error())
			}
		})
	}
}

// The no-match fallback (Steam sends no message) must not echo the typed name, whatever it is: a SteamID64 typed as
// a /id/ value, or an ordinary name. The error is the same constant for every input.
func TestVanityFallbackDoesNotEchoTypedInput(t *testing.T) {
	const typo = "gabelogan-typo"
	inputs := map[string]string{
		"steamid64 in /id/ URL": "https://steamcommunity.com/id/" + realSteamID,
		"name in /id/ URL":      "https://steamcommunity.com/id/" + typo,
		"bare name":             typo,
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			client, closeServer := testClient(t, map[string]string{"/ISteamUser/ResolveVanityURL/v1/": `{"response":{"success":42}}`})
			defer closeServer()
			client.APIKey = "test-key"
			_, err := client.ResolveProfileInput(context.Background(), input)
			if err == nil {
				t.Fatal("err = nil, want no-match error")
			}
			if err.Error() != errNoVanityMatch.Error() {
				t.Fatalf("err = %q, want constant %q", err.Error(), errNoVanityMatch.Error())
			}
			for _, echoed := range []string{realSteamID, typo} {
				if strings.Contains(err.Error(), echoed) {
					t.Fatalf("err = %q, echoes typed input %q", err.Error(), echoed)
				}
			}
		})
	}
}

func TestRedactTextRemovesKeyValueEverywhere(t *testing.T) {
	const key = "AB12+/=cd"
	c := &Client{APIKey: key}
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "raw", in: "typed " + key + " here", want: "typed REDACTED here"},
		{name: "query escaped", in: "?vanityurl=" + url.QueryEscape(key), want: "?vanityurl=REDACTED"},
		{name: "path escaped", in: "/id/" + url.PathEscape(key), want: "/id/REDACTED"},
		{name: "key param and vanity", in: "?key=" + url.QueryEscape(key) + "&vanityurl=" + url.QueryEscape(key), want: "?key=REDACTED&vanityurl=REDACTED"},
		{name: "other case", in: "typed " + strings.ToLower(key) + " here", want: "typed REDACTED here"},
		{name: "lower-case percent hex", in: "/id/" + strings.ToLower(url.PathEscape(key)), want: "/id/REDACTED"},
		{name: "unrelated text kept", in: "https://h/x?appid=1", want: "https://h/x?appid=1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.redactText(tc.in); got != tc.want {
				t.Fatalf("redactText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if got := (&Client{}).redactText("plain"); got != "plain" {
		t.Fatalf("empty key changed text: %q", got)
	}
}

// RedactSecrets works without a known key: it removes any 32-hex run (the key shape, any case, with no word boundary),
// the identifier query values and bare SteamID64s, and keeps the rest of the text, including a goroutine stack's
// addresses (at most 16 hex digits).
func TestRedactSecrets(t *testing.T) {
	const key = "0123456789ABCDEF0123456789ABCDEF"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "raw key", in: "keyring: " + key, want: "keyring: REDACTED"},
		{name: "key glued to letters", in: "keyKEY" + key + "x", want: "keyKEYREDACTEDx"},
		{name: "lower-case key", in: strings.ToLower(key), want: "REDACTED"},
		{name: "escaped key", in: url.QueryEscape(strings.ToLower(key)), want: "REDACTED"},
		{name: "key param", in: "?key=" + key + "&appid=10", want: "?key=REDACTED&appid=10"},
		{name: "bare steamid64", in: "id 76561198000000000 here", want: "id REDACTED here"},
		{name: "steamids param", in: "?steamids=76561198000000000", want: "?steamids=REDACTED"},
		{name: "short hex kept", in: "0x4b2c1d+0x1a3 deadbeef", want: "0x4b2c1d+0x1a3 deadbeef"},
		{name: "plain text kept", in: "goroutine 1 [running]: main.f(0xc000012345)", want: "goroutine 1 [running]: main.f(0xc000012345)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactSecrets(tc.in); got != tc.want {
				t.Fatalf("RedactSecrets(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// testLongKey looks like a real key (32 hex characters) and does not repeat, so partial matches show up.
const testLongKey = "9F3C1A7E5B2D8046A1C7E3B9D5F20846"

// shortTestKey is below minGuardedKeyLen, so the input guard is off and only redaction is under test.
const shortTestKey = "abc-key"

// A user can paste the API key into the profile field. ResolveProfileInput rejects it before any request.
func TestResolveProfileInputRejectsAPIKey(t *testing.T) {
	var hits atomic.Int32
	client, closeServer := vanityTestClient(t, &hits)
	defer closeServer()
	client.APIKey = testLongKey
	inputs := map[string]string{
		"bare":            testLongKey,
		"lower case":      strings.ToLower(testLongKey),
		"mixed case":      "9f3c1a7e5b2d8046A1C7E3B9D5F20846",
		"padded":          "  " + testLongKey + "  ",
		"profile URL":     "https://steamcommunity.com/id/" + testLongKey,
		"URL without id":  "https://steamcommunity.com/" + testLongKey,
		"embedded in url": "https://steamcommunity.com/id/gabe?key=" + testLongKey,
		// The path is decoded after the raw-input check, so a percent-encoded letter hides the key from it
		// (an encoded digit would not: %39 still ends in "9"). ResolveVanityURL must catch the decoded name.
		"percent-encoded letter, lower case": "https://steamcommunity.com/id/9%66" + strings.ToLower(testLongKey)[2:],
		"percent-encoded letter, upper case": "https://steamcommunity.com/id/9%46" + testLongKey[2:],
	}
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			_, err := client.ResolveProfileInput(context.Background(), input)
			if !errors.Is(err, errAPIKeyAsProfileInput) {
				t.Fatalf("err = %v, want errAPIKeyAsProfileInput", err)
			}
			if strings.Contains(strings.ToLower(err.Error()), strings.ToLower(testLongKey)) {
				t.Fatalf("error echoes the key: %q", err.Error())
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("vanity lookups = %d, want 0 (no request for key input)", hits.Load())
	}
	if _, err := client.ResolveProfileInput(context.Background(), "gabelogan"); err != nil {
		t.Fatalf("ordinary input rejected: %v", err)
	}
}

// A key too short for the guard (or any input it misses) still must not leak: a transport failure embeds
// the request URL, where the typed name rides in vanityurl.
func TestResolveVanityURLTransportErrorDoesNotLeakAPIKey(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("boom: connection refused")
	})}
	client := &Client{APIKey: shortTestKey, BaseURL: "http://steam.invalid", HTTPClient: httpClient}
	client.retryBackoff = func(int) time.Duration { return 0 }

	_, err := client.ResolveVanityURL(context.Background(), shortTestKey)
	if err == nil {
		t.Fatal("err = nil, want transport error")
	}
	if strings.Contains(err.Error(), shortTestKey) {
		t.Fatalf("error leaks API key: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "vanityurl=REDACTED") {
		t.Fatalf("error = %q, want vanityurl redacted", err.Error())
	}
}

// The fallback error is a constant that carries no input. Steam's own message may echo the key, so it must be redacted.
func TestResolveVanityURLNoMatchErrorsDoNotLeakAPIKey(t *testing.T) {
	tests := []struct {
		name, key, vanity, body, wantText string
		// wantCause is the redacted text the log gets (usererr.Raw); the UI sentence is wantText.
		wantCause string
	}{
		{name: "fallback typed name equals key", key: shortTestKey, vanity: shortTestKey, body: `{"response":{"success":42}}`, wantText: "No Steam profile matches that custom name."},
		{name: "steam message echoes key", key: testLongKey, vanity: "someone", body: `{"response":{"success":42,"message":"No match for ` + testLongKey + `"}}`, wantText: "No Steam profile matches that custom name.", wantCause: "No match for REDACTED"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, closeServer := testClient(t, map[string]string{"/ISteamUser/ResolveVanityURL/v1/": tc.body})
			defer closeServer()
			client.APIKey = tc.key
			_, err := client.ResolveVanityURL(context.Background(), tc.vanity)
			if err == nil || err.Error() != tc.wantText {
				t.Fatalf("err = %v, want the sentence %q", err, tc.wantText)
			}
			if strings.Contains(err.Error(), tc.key) || strings.Contains(usererr.Raw(err).Error(), tc.key) {
				t.Fatalf("err = %v carries the API key", err)
			}
			if tc.wantCause != "" && usererr.Raw(err).Error() != tc.wantCause {
				t.Fatalf("log cause = %q, want %q", usererr.Raw(err).Error(), tc.wantCause)
			}
		})
	}
}

func TestGetPlayerAchievementsSteamErrorRedactsAPIKey(t *testing.T) {
	client, closeServer := testClient(t, map[string]string{
		"/ISteamUserStats/GetPlayerAchievements/v1/": `{"playerstats":{"success":false,"error":"bad key ` + testLongKey + `"}}`,
	})
	defer closeServer()
	client.APIKey = testLongKey
	_, err := client.GetPlayerAchievements(context.Background(), testSteamID, 10)
	if err == nil || strings.Contains(err.Error(), testLongKey) || strings.Contains(usererr.Raw(err).Error(), testLongKey) || !strings.Contains(usererr.Raw(err).Error(), "REDACTED") {
		t.Fatalf("err = %v, want a sentence and a redacted log cause without the API key", err)
	}
}

func TestInputContainsAPIKeyThreshold(t *testing.T) {
	tests := []struct {
		name, key, input string
		want             bool
	}{
		{name: "empty key matches nothing", key: "", input: "gabelogan", want: false},
		{name: "short key is not guarded", key: shortTestKey, input: shortTestKey, want: false},
		{name: "long key at threshold", key: "12345678", input: "x12345678y", want: true},
		{name: "long key absent", key: testLongKey, input: "gabelogan", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := (&Client{APIKey: tc.key}).inputContainsAPIKey(tc.input); got != tc.want {
				t.Fatalf("inputContainsAPIKey = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequestBuildErrorDoesNotLeakAPIKey(t *testing.T) {
	client := &Client{APIKey: "test-key", BaseURL: "://bad base", HTTPClient: &http.Client{}}
	client.retryBackoff = func(int) time.Duration { return 0 }

	_, err := client.GetOwnedGames(context.Background(), testSteamID)
	if err == nil {
		t.Fatal("err = nil, want URL parse error")
	}
	if strings.Contains(err.Error(), "test-key") || !strings.Contains(err.Error(), "key=REDACTED") {
		t.Fatalf("error = %q, want redacted key", err.Error())
	}
}

func TestTransportErrorKeepsCancellationChain(t *testing.T) {
	started := make(chan struct{}, 16)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})
	client := &Client{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()}
	client.retryBackoff = func(int) time.Duration { return 0 }

	checkURLError := func(t *testing.T, err error) {
		t.Helper()
		var ue *url.Error
		if !errors.As(err, &ue) {
			t.Fatalf("err = %#v, want *url.Error in chain", err)
		}
		if ue.Op != "Get" || ue.Err == nil {
			t.Fatalf("url.Error Op/Err not preserved: %#v", ue)
		}
		if strings.Contains(err.Error(), "test-key") || !strings.Contains(ue.URL, "key=REDACTED") {
			t.Fatalf("url.Error URL = %q, want redacted key", ue.URL)
		}
	}

	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-started
			cancel()
		}()
		_, err := client.GetOwnedGames(ctx, testSteamID)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want errors.Is context.Canceled", err)
		}
		checkURLError(t, err)
	})

	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := client.GetOwnedGames(ctx, testSteamID)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want errors.Is context.DeadlineExceeded", err)
		}
		checkURLError(t, err)
		var ue *url.Error
		if errors.As(err, &ue) && !ue.Timeout() {
			t.Fatalf("url.Error.Timeout() = false, want true for deadline")
		}
	})
}

// testHangUpClient closes the TCP connection without a response for the first hangUps requests,
// then serves a one-game owned-games response.
func testHangUpClient(t *testing.T, hangUps int32) (*Client, *atomic.Int32, func()) {
	t.Helper()
	var hits atomic.Int32
	client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= hangUps {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"game_count":1,"games":[{"appid":10,"name":"Back","playtime_forever":1}]}}`))
	})
	return client, &hits, closeServer
}

func TestRetryableStatusesRetriedUntilSuccess(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			client, attempts, closeServer := testStatusSequenceClient(t, gameAchievementsPath, []int{status, status}, "1", readFixture(t, "portal2_game_achievements.json"))
			defer closeServer()

			game, err := client.GetGameAchievements(context.Background(), 620)
			if err != nil {
				t.Fatal(err)
			}
			if attempts.Load() != 3 || len(game.Achievements) != 4 {
				t.Fatalf("attempts=%d achievements=%d, want 3 and 4", attempts.Load(), len(game.Achievements))
			}
		})
	}
}

func TestRetryableStatusesGiveUpAfterFourAttempts(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			client, attempts, closeServer := testStatusSequenceClient(t, gameAchievementsPath, []int{status, status, status, status}, "1", readFixture(t, "portal2_game_achievements.json"))
			defer closeServer()

			_, err := client.GetGameAchievements(context.Background(), 620)
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) || statusErr.Code != status {
				t.Fatalf("err = %#v, want *HTTPStatusError with code %d", err, status)
			}
			if attempts.Load() != 4 {
				t.Fatalf("attempts = %d, want 4", attempts.Load())
			}
		})
	}
}

func TestTransportErrorsAreRetried(t *testing.T) {
	t.Run("hang up once then succeeds", func(t *testing.T) {
		client, hits, closeServer := testHangUpClient(t, 1)
		defer closeServer()

		games, err := client.GetOwnedGames(context.Background(), testSteamID)
		if err != nil {
			t.Fatal(err)
		}
		// Full list: one hang-up then success (2 requests). Purchased list and vetted list: one request each.
		if hits.Load() != 4 || len(games) != 1 || games[0].Name != "Back" {
			t.Fatalf("hits=%d games=%#v, want 4 requests and game Back", hits.Load(), games)
		}
	})
	t.Run("hang up on every attempt gives up after four", func(t *testing.T) {
		client, hits, closeServer := testHangUpClient(t, 100)
		defer closeServer()

		_, err := client.GetOwnedGames(context.Background(), testSteamID)
		if _, ok := errors.AsType[*url.Error](err); !ok {
			t.Fatalf("err = %#v, want *url.Error from transport", err)
		}
		if hits.Load() != 4 {
			t.Fatalf("hits = %d, want 4", hits.Load())
		}
		if !strings.Contains(err.Error(), "key=REDACTED") {
			t.Fatalf("err = %q, want redacted key", err.Error())
		}
	})
}

func TestRetryBackoffCalledWithRetryAttemptNumbers(t *testing.T) {
	client, attempts, closeServer := testStatusSequenceClient(t, gameAchievementsPath, []int{503, 500, 429}, "1", readFixture(t, "portal2_game_achievements.json"))
	defer closeServer()

	var got []int
	client.SetRetryBackoff(func(attempt int) time.Duration {
		got = append(got, attempt)
		return 0
	})
	if _, err := client.GetGameAchievements(context.Background(), 620); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 4 || fmt.Sprint(got) != "[1 2 3]" {
		t.Fatalf("attempts=%d backoff attempts=%v, want 4 and [1 2 3]", attempts.Load(), got)
	}
}

func TestDefaultRetryDelayBoundsPerAttempt(t *testing.T) {
	// Default backoff: 300ms doubling per retry, plus jitter in [0, 250ms).
	want := map[int]time.Duration{
		1: 300 * time.Millisecond,
		2: 600 * time.Millisecond,
		3: 1200 * time.Millisecond,
		4: 2400 * time.Millisecond,
	}
	c := New("test-key")
	for attempt := 1; attempt <= 4; attempt++ {
		base := want[attempt]
		seen := map[time.Duration]bool{}
		for range 5000 {
			d := c.retryDelay(attempt)
			if d < base || d >= base+250*time.Millisecond {
				t.Fatalf("attempt %d delay = %v, want in [%v, %v)", attempt, d, base, base+250*time.Millisecond)
			}
			seen[d] = true
		}
		if len(seen) < 2 {
			t.Fatalf("attempt %d: jitter produced one value, want variation", attempt)
		}
	}

	c.SetRetryBackoff(func(int) time.Duration { return 0 })
	c.SetRetryBackoff(nil)
	if d := c.retryDelay(1); d < 300*time.Millisecond || d >= 550*time.Millisecond {
		t.Fatalf("after SetRetryBackoff(nil) delay = %v, want default 300ms plus jitter", d)
	}
}

func TestSetRetryBackoffReplacesDelay(t *testing.T) {
	c := New("test-key")
	c.SetRetryBackoff(func(attempt int) time.Duration { return time.Duration(attempt) * time.Second })
	for attempt := 1; attempt <= 3; attempt++ {
		if d := c.retryDelay(attempt); d != time.Duration(attempt)*time.Second {
			t.Fatalf("attempt %d delay = %v, want %v", attempt, d, time.Duration(attempt)*time.Second)
		}
	}
}

func TestRetryBackoffWaitsAndHonorsCancellation(t *testing.T) {
	t.Run("waits the injected delay before retrying", func(t *testing.T) {
		client, attempts, closeServer := testStatusSequenceClient(t, gameAchievementsPath, []int{503}, "1", readFixture(t, "portal2_game_achievements.json"))
		defer closeServer()
		client.SetRetryBackoff(func(int) time.Duration { return 80 * time.Millisecond })

		start := time.Now()
		if _, err := client.GetGameAchievements(context.Background(), 620); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
			t.Fatalf("elapsed = %v, want at least the 80ms backoff", elapsed)
		}
		if attempts.Load() != 2 {
			t.Fatalf("attempts = %d, want 2", attempts.Load())
		}
	})
	t.Run("cancel during backoff returns context error promptly", func(t *testing.T) {
		client, attempts, closeServer := testStatusSequenceClient(t, gameAchievementsPath, []int{503, 503, 503, 503}, "1", readFixture(t, "portal2_game_achievements.json"))
		defer closeServer()
		client.SetRetryBackoff(func(int) time.Duration { return 10 * time.Second })

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		time.AfterFunc(20*time.Millisecond, cancel)

		start := time.Now()
		_, err := client.GetGameAchievements(ctx, 620)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("elapsed = %v, want prompt return instead of waiting out the 10s backoff", elapsed)
		}
		if attempts.Load() > 1 {
			t.Fatalf("attempts = %d, want no retry after cancel", attempts.Load())
		}
	})
}

func TestSuccessStatusRangeIs2xx(t *testing.T) {
	const body = `{"response":{"game_count":0,"games":[]}}`
	tests := []struct {
		code    int
		wantErr bool
	}{
		{199, true},
		{200, false},
		{299, false},
		{300, true},
	}
	for _, tc := range tests {
		t.Run(strconv.Itoa(tc.code), func(t *testing.T) {
			var hits atomic.Int32
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				hits.Add(1)
				return &http.Response{
					StatusCode: tc.code,
					Status:     strconv.Itoa(tc.code),
					Header:     http.Header{},
					Body:       io.NopCloser(strings.NewReader(body)),
				}, nil
			})}
			client := &Client{APIKey: "test-key", BaseURL: "http://steam.invalid", HTTPClient: httpClient}
			client.SetRetryBackoff(func(int) time.Duration { return 0 })

			_, err := client.GetOwnedGames(context.Background(), testSteamID)
			if !tc.wantErr {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) || statusErr.Code != tc.code {
				t.Fatalf("err = %#v, want *HTTPStatusError with code %d", err, tc.code)
			}
			if hits.Load() != 1 {
				t.Fatalf("hits = %d, want 1 (non-retryable)", hits.Load())
			}
		})
	}
}

func TestMalformedSteamResponses(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string // exact text, or prefix when wantPrefix is set
		prefix  bool
	}{
		{name: "invalid JSON with 200", body: `{"response":`, wantErr: "decode Steam API /IPlayerService/GetOwnedGames/v0001/", prefix: true},
		{name: "empty body with 200", body: "", wantErr: "Steam did not return your game list. Your profile's game details may be private."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			})
			defer closeServer()

			_, err := client.GetOwnedGames(context.Background(), testSteamID)
			if err == nil {
				t.Fatal("err = nil, want error")
			}
			if tc.prefix && !strings.HasPrefix(err.Error(), tc.wantErr) {
				t.Fatalf("err = %q, want prefix %q", err.Error(), tc.wantErr)
			}
			if !tc.prefix && err.Error() != tc.wantErr {
				t.Fatalf("err = %q, want %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestGetOwnedGamesIncompleteResponse(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantCount int
		wantErr   string
	}{
		{name: "count zero and empty list is an empty library", body: `{"response":{"game_count":0,"games":[]}}`, wantCount: 0},
		{name: "count zero without games key", body: `{"response":{"game_count":0}}`, wantCount: 0},
		{name: "count positive and empty list is incomplete", body: `{"response":{"game_count":2,"games":[]}}`, wantErr: "Steam returned an incomplete game list. Try again later."},
		{name: "count positive without games key is incomplete", body: `{"response":{"game_count":1}}`, wantErr: "Steam returned an incomplete game list. Try again later."},
		{name: "count positive with games is complete", body: `{"response":{"game_count":1,"games":[{"appid":10,"name":"A","playtime_forever":0}]}}`, wantCount: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, closeServer := testClient(t, map[string]string{"/IPlayerService/GetOwnedGames/v0001/": tc.body})
			defer closeServer()

			games, err := client.GetOwnedGames(context.Background(), testSteamID)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(games) != tc.wantCount {
				t.Fatalf("games = %d, want %d", len(games), tc.wantCount)
			}
		})
	}
}

func TestPlayerAchievementsBoolFlexibility(t *testing.T) {
	tests := []struct {
		raw     string
		want    bool
		wantErr bool
	}{
		{raw: `1`, want: true},
		{raw: `"1"`, want: true},
		{raw: `true`, want: true},
		{raw: `"true"`, want: true},
		{raw: `"TRUE"`, want: true},
		{raw: `2`, want: true},
		{raw: `-1`, want: true},
		{raw: `0`, want: false},
		{raw: `"0"`, want: false},
		{raw: `false`, want: false},
		{raw: `"false"`, want: false},
		{raw: `""`, want: false},
		{raw: `null`, want: false},
		{raw: `"abc"`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			body := `{"playerstats":{"success":true,"achievements":[{"apiname":"A","achieved":` + tc.raw + `}]}}`
			client, closeServer := testClient(t, map[string]string{"/ISteamUserStats/GetPlayerAchievements/v1/": body})
			defer closeServer()

			achievements, err := client.GetPlayerAchievements(context.Background(), testSteamID, 10)
			if tc.wantErr {
				if err == nil {
					t.Fatal("err = nil, want decode error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(achievements) != 1 || achievements[0].Achieved != tc.want {
				t.Fatalf("achieved = %#v, want %v", achievements, tc.want)
			}
		})
	}
}

func TestPlayerAchievementsSuccessFlag(t *testing.T) {
	tests := []struct {
		raw    string
		wantOK bool
	}{
		{raw: `true`, wantOK: true},
		{raw: `1`, wantOK: true},
		{raw: `"1"`, wantOK: true},
		{raw: `"true"`, wantOK: true},
		{raw: `"TRUE"`, wantOK: true},
		{raw: `null`, wantOK: true},
		{raw: `false`, wantOK: false},
		{raw: `0`, wantOK: false},
		{raw: `2`, wantOK: false},
		{raw: `"0"`, wantOK: false},
		{raw: `"yes"`, wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			client, closeServer := testClient(t, map[string]string{
				"/ISteamUserStats/GetPlayerAchievements/v1/": `{"playerstats":{"success":` + tc.raw + `}}`,
			})
			defer closeServer()

			achievements, err := client.GetPlayerAchievements(context.Background(), testSteamID, 10)
			if tc.wantOK {
				if err != nil || len(achievements) != 0 {
					t.Fatalf("achievements=%v err=%v, want empty and no error", achievements, err)
				}
				return
			}
			if err == nil || err.Error() != "Steam did not return achievements for this game. Try again later." {
				t.Fatalf("err = %v, want %q", err, "Steam did not return achievements for this game. Try again later.")
			}
		})
	}
}

// vanityTestClient answers ResolveVanityURL: "gabelogan" resolves, any other name is no match.
func vanityTestClient(t *testing.T, hits *atomic.Int32) (*Client, func()) {
	t.Helper()
	return newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ISteamUser/ResolveVanityURL/v1/" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("vanityurl") == "gabelogan" {
			_, _ = w.Write([]byte(`{"response":{"success":1,"steamid":"76561197960287930"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"success":42,"message":"No match"}}`))
	})
}

func TestResolveProfileInput(t *testing.T) {
	const (
		profileID  = "76561198000000001"
		vanityID   = "76561197960287930"
		profileErr = "Enter a profile link like steamcommunity.com/profiles/<number> or steamcommunity.com/id/<name>."
	)
	tests := []struct {
		name       string
		input      string
		want       string
		wantErr    string
		wantVanity int32
	}{
		{name: "bare SteamID64", input: profileID, want: profileID},
		{name: "SteamID64 trimmed", input: "  " + profileID + "\n", want: profileID},
		{name: "profile URL", input: "https://steamcommunity.com/profiles/" + profileID, want: profileID},
		{name: "profile URL trailing slash", input: "https://steamcommunity.com/profiles/" + profileID + "/", want: profileID},
		{name: "profile URL with query", input: "https://steamcommunity.com/profiles/" + profileID + "/?tab=games", want: profileID},
		{name: "vanity URL", input: "https://steamcommunity.com/id/gabelogan", want: vanityID, wantVanity: 1},
		{name: "vanity URL trailing slash", input: "https://steamcommunity.com/id/gabelogan/", want: vanityID, wantVanity: 1},
		{name: "vanity URL with query", input: "https://steamcommunity.com/id/gabelogan?l=english", want: vanityID, wantVanity: 1},
		{name: "bare vanity name", input: "gabelogan", want: vanityID, wantVanity: 1},
		{name: "empty input", input: "", wantErr: "Enter a Steam profile URL, SteamID64 or custom name."},
		{name: "whitespace input", input: "   ", wantErr: "Enter a Steam profile URL, SteamID64 or custom name."},
		{name: "URL without profile or id", input: "https://steamcommunity.com/", wantErr: profileErr},
		{name: "URL with groups path", input: "https://steamcommunity.com/groups/foo", wantErr: profileErr},
		{name: "profiles without id", input: "https://steamcommunity.com/profiles", wantErr: profileErr},
		{name: "profiles with 3-digit id", input: "https://steamcommunity.com/profiles/123", wantErr: profileErr},
		{name: "id without vanity", input: "https://steamcommunity.com/id", wantErr: profileErr},
		{name: "16 digits is not SteamID64", input: "7656119800000000", wantErr: "No Steam profile matches that custom name.", wantVanity: 1},
		{name: "18 digits is not SteamID64", input: "765611980000000001", wantErr: "No Steam profile matches that custom name.", wantVanity: 1},
		{name: "unknown bare vanity", input: "not a profile!", wantErr: "No Steam profile matches that custom name.", wantVanity: 1},
		{name: "unparsable URL falls back to vanity", input: "https://[::1", wantErr: "No Steam profile matches that custom name.", wantVanity: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int32
			client, closeServer := vanityTestClient(t, &hits)
			defer closeServer()

			got, err := client.ResolveProfileInput(context.Background(), tc.input)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			} else if got != tc.want {
				t.Fatalf("id = %q, want %q", got, tc.want)
			}
			if hits.Load() != tc.wantVanity {
				t.Fatalf("vanity lookups = %d, want %d", hits.Load(), tc.wantVanity)
			}
		})
	}
}

func TestResolveVanityURL(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    string
		wantErr string
	}{
		{name: "success", body: `{"response":{"success":1,"steamid":"76561197960287930"}}`, want: "76561197960287930"},
		{name: "no match with message", body: `{"response":{"success":42,"message":"No match"}}`, wantErr: "No Steam profile matches that custom name."},
		{name: "no match without message", body: `{"response":{"success":42}}`, wantErr: "No Steam profile matches that custom name."},
		{name: "success without steamid", body: `{"response":{"success":1}}`, wantErr: "No Steam profile matches that custom name."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, closeServer := testClient(t, map[string]string{"/ISteamUser/ResolveVanityURL/v1/": tc.body})
			defer closeServer()

			got, err := client.ResolveVanityURL(context.Background(), "nobody")
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("id = %q err = %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestGetPlayerSummaryFieldsAndNotFound(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    PlayerSummary
		wantErr string
	}{
		{
			name: "full avatar preferred",
			body: `{"response":{"players":[{"steamid":"76561198000000001","personaname":"Gabe","avatarfull":"https://img/full.jpg","avatarmedium":"https://img/med.jpg"}]}}`,
			want: PlayerSummary{SteamID64: "76561198000000001", DisplayName: "Gabe", AvatarURL: "https://img/full.jpg"},
		},
		{
			name: "medium avatar when full is empty",
			body: `{"response":{"players":[{"steamid":"76561198000000001","personaname":"Gabe","avatarfull":"","avatarmedium":"https://img/med.jpg"}]}}`,
			want: PlayerSummary{SteamID64: "76561198000000001", DisplayName: "Gabe", AvatarURL: "https://img/med.jpg"},
		},
		{name: "empty players list", body: `{"response":{"players":[]}}`, wantErr: "No Steam profile was found for that account."},
		{name: "players key missing", body: `{"response":{}}`, wantErr: "No Steam profile was found for that account."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var query url.Values
			client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/ISteamUser/GetPlayerSummaries/v2/" {
					http.NotFound(w, r)
					return
				}
				query = r.URL.Query()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			})
			defer closeServer()

			got, err := client.GetPlayerSummary(context.Background(), testSteamID)
			if query.Get("steamids") != testSteamID || query.Get("key") != "test-key" {
				t.Fatalf("query = %v, want steamids and key", query)
			}
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("summary = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	c := New("abc")
	if c.APIKey != "abc" || c.BaseURL != "https://api.steampowered.com" {
		t.Fatalf("client = %#v, want key abc and default base URL", c)
	}
	if c.HTTPClient == nil || c.HTTPClient.Timeout != 30*time.Second {
		t.Fatalf("HTTPClient = %#v, want 30s timeout", c.HTTPClient)
	}
	if c.retryBackoff != nil {
		t.Fatal("New must leave the default retry backoff in place")
	}
}
