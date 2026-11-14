package steam

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	query := log.query(path)
	if query.Get("include_appinfo") != "true" || query.Get("include_extended_appinfo") != "true" {
		t.Fatalf("query = %v, want include_appinfo and include_extended_appinfo set", query)
	}
	if query.Get("language") != "" {
		t.Fatalf("query language = %q, want unset", query.Get("language"))
	}
	if len(games) != 2 || games[0].CapsuleFilename != "library_600x900.jpg" || games[1].CapsuleFilename != "" {
		t.Fatalf("games = %#v, want trimmed capsule file name for game 10 and empty for game 20", games)
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
	queries  map[string]url.Values
}

func (l *requestLog) record(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[r.URL.Path]++
	l.queries[r.URL.Path] = r.URL.Query()
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

func testSetClient(t *testing.T, responses map[string]fixtureResponse) (*Client, *requestLog, func()) {
	t.Helper()
	log := &requestLog{attempts: map[string]int{}, queries: map[string]url.Values{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
	client := &Client{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()}
	client.retryBackoff = func(int) time.Duration { return 0 }
	return client, log, server.Close
}

// testStatusSequenceClient answers the first len(statuses) requests with those HTTP statuses,
// then serves body with the given x-eresult.
func testStatusSequenceClient(t *testing.T, path string, statuses []int, eresult, body string) (*Client, *atomic.Int32, func()) {
	t.Helper()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	}))
	client := &Client{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()}
	client.retryBackoff = func(int) time.Duration { return 0 }
	return client, &attempts, server.Close
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
