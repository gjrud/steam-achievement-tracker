package syncer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	dbinit "github.com/gjrud/steam-achievement-tracker/internal/db"
	"github.com/gjrud/steam-achievement-tracker/internal/images"
	"github.com/gjrud/steam-achievement-tracker/internal/repository"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
)

func TestFullSyncRecordsPartialFailureAndKeepsSuccessfulGame(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{
		{AppID: 10, Name: "Good Game", PlaytimeForever: 120},
		{AppID: 20, Name: "Broken Game", PlaytimeForever: 5},
	})
	fake.schema[10] = []string{"ACH_ONE", "ACH_TWO"}
	fake.player[10] = map[string]bool{"ACH_ONE": true, "ACH_TWO": false}
	fake.global[10] = map[string]float64{"ACH_ONE": 80, "ACH_TWO": 30}
	fake.schema[20] = []string{"ACH_FAIL"}
	fake.playerError[20] = "player achievements unavailable"

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 2, synced: 1, failed: 1, hasError: true})
	requireLatestSyncRun(t, repo, profile.ID, expectedSyncResult{status: "partial", total: 2, synced: 1, failed: 1, hasError: true})

	requireSummaryCounts(t, repo, profile.ID, summaryCounts{owned: 2, achievements: 2, unlocked: 1})
	requireWarningCard(t, repo, profile.ID, 20, "player achievements unavailable")
	requireGoodGameState(t, repo, profile.ID, 10)
}

func TestFullSyncClearsWarningAfterRecovery(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 20, Name: "Recovered Game", PlaytimeForever: 45}})
	fake.schema[20] = []string{"ACH_ONE", "ACH_TWO"}
	fake.playerError[20] = "temporary Steam failure"

	first := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, first, expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})
	requireWarningCard(t, repo, profile.ID, 20, "temporary Steam failure")

	fake.playerError[20] = ""
	fake.player[20] = map[string]bool{"ACH_ONE": true, "ACH_TWO": true}
	fake.global[20] = map[string]float64{"ACH_ONE": 90, "ACH_TWO": 40}
	second := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, second, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	requireRecoveredGameState(t, repo, profile.ID, 20)
	requireNoWarningCards(t, repo, profile.ID)
	requireCompletedCard(t, repo, profile.ID, 20)
}

func TestFullSyncCancellationPersistsFailedRun(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	owned := make([]steam.OwnedGame, 20)
	for i := range owned {
		appID := int64(2000 + i)
		owned[i] = steam.OwnedGame{AppID: appID, Name: fmt.Sprintf("Game %d", appID), PlaytimeForever: 1}
	}
	fake := newFakeSteamServer(t, owned)
	fake.delay = 50 * time.Millisecond
	for _, game := range owned {
		fake.schema[game.AppID] = []string{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(ctx, *profile)
	if err == nil || err != context.Canceled {
		t.Fatalf("err = %v, want context canceled", err)
	}
	if result.Status != "failed" || result.Error == nil || result.GamesTotal != len(owned) {
		t.Fatalf("result = %#v, want failed canceled sync with total games", result)
	}
	run, err := repo.LatestSyncRun(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Status != "failed" || run.Error == nil || !strings.Contains(*run.Error, "context canceled") {
		t.Fatalf("sync run = %#v, want persisted failed cancellation", run)
	}
}

func TestFullSyncSkipsManuallyDisabledGames(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	seedGameSync(t, repo, profile.ID, 99)
	if err := repo.DisableProfileGame(profile.ID, 99, 0); err != nil {
		t.Fatal(err)
	}
	fake := newFakeSteamServer(t, []steam.OwnedGame{
		{AppID: 99, Name: "Disabled Game", PlaytimeForever: 10},
		{AppID: 100, Name: "Enabled Game", PlaytimeForever: 20},
	})
	fake.schema[100] = []string{"ACH"}
	fake.player[100] = map[string]bool{"ACH": true}
	fake.global[100] = map[string]float64{"ACH": 75}

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(context.Background(), *profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.GamesTotal != 1 || result.GamesSynced != 1 || result.GamesFailed != 0 {
		t.Fatalf("result = %#v, want only enabled game synced", result)
	}
	if got := fake.count("game", 99); got != 0 {
		t.Fatalf("disabled game achievement calls = %d, want 0", got)
	}
	disabled, err := repo.GameCards(profile.ID, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled) != 1 || disabled[0].AppID != 99 || !disabled[0].ManualDisabled {
		t.Fatalf("disabled cards = %#v, want manually disabled game only", disabled)
	}
}

func TestFullSyncDisablesNoLongerOwnedGames(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	seedGameSync(t, repo, profile.ID, 50)
	seedGameSync(t, repo, profile.ID, 60)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 60, Name: "Still Owned", PlaytimeForever: 30}})
	fake.schema[60] = []string{}

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(context.Background(), *profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.GamesTotal != 1 || result.GamesSynced != 1 || result.GamesFailed != 0 {
		t.Fatalf("result = %#v, want only still-owned game synced", result)
	}
	disabled, err := repo.GameCards(profile.ID, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled) != 1 || disabled[0].AppID != 50 || disabled[0].DisabledReason == nil || *disabled[0].DisabledReason != "not_owned" || len(disabled[0].Tags) != 0 {
		t.Fatalf("disabled cards = %#v, want not-owned game with cleared tags", disabled)
	}
	completed, err := repo.GameCards(profile.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 0 {
		t.Fatalf("completed cards = %#v, want disabled old completed game hidden", completed)
	}
}

func TestFullSyncLimitsConcurrencyAndPersistsProgress(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	owned := make([]steam.OwnedGame, 12)
	for i := range owned {
		appID := int64(1000 + i)
		owned[i] = steam.OwnedGame{AppID: appID, Name: fmt.Sprintf("Game %d", appID), PlaytimeForever: i + 1}
	}
	fake := newFakeSteamServer(t, owned)
	fake.delay = 20 * time.Millisecond
	for _, game := range owned {
		fake.schema[game.AppID] = []string{}
	}

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: len(owned), synced: len(owned), failed: 0})
	requireConcurrentRange(t, fake, 2, 4)
	requireLatestSyncRun(t, repo, profile.ID, expectedSyncResult{status: "success", total: len(owned), synced: len(owned), failed: 0})
	requireSummaryCounts(t, repo, profile.ID, summaryCounts{owned: len(owned)})
}

func TestFullSyncWritesOneEntryPerEligibleSet(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 300, Name: "Set Game", PlaytimeForever: 5}})
	fake.gameBody[300] = gameJSON(
		[]groupFixture{{id: 0}, {id: 1, name: "Owned DLC", dlc: 301}, {id: 2, name: "Unowned DLC", dlc: 302}},
		[]achFixture{
			{name: "BASE_1", key: 1, group: 0}, {name: "BASE_2", key: 2, group: 0},
			{name: "DLC1_1", key: 3, group: 1}, {name: "DLC2_1", key: 4, group: 2},
		},
	)
	fake.userBody[300] = userJSON([]int64{1, 2}, []int64{0, 1})

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	entries := cardsForApp(allCards(t, repo, profile.ID), 300)
	if len(entries) != 3 {
		t.Fatalf("entries for app 300 = %#v, want base + owned DLC set + unowned DLC set", entries)
	}
	base := findCard(t, entries, 0)
	if base.TotalAchievements != 2 || base.UnlockedAchievements != 2 || !base.IsCompleted {
		t.Fatalf("base entry = %#v, want completed 2/2", base)
	}
	owned := findCard(t, entries, 1)
	if owned.TotalAchievements != 1 || owned.UnlockedAchievements != 0 || owned.DLCAppID != 301 || owned.EntryKey != "300:1" || owned.DLCNotOwned {
		t.Fatalf("owned DLC entry = %#v, want 0/1 with DLC 301 and not flagged", owned)
	}
	unowned := findCard(t, entries, 2)
	if !unowned.DLCNotOwned || unowned.DLCAppID != 302 || unowned.TotalAchievements != 1 || !hasTag(unowned.Tags, "dlc_not_owned") {
		t.Fatalf("unowned DLC entry = %#v, want DLC 302 flagged DLCNotOwned with dlc_not_owned tag", unowned)
	}
	// Unowned DLC achievements stay out of the available total: base 2 + owned DLC 1.
	requireSummaryCounts(t, repo, profile.ID, summaryCounts{owned: 1, achievements: 3, unlocked: 2})
}

func TestFullSyncDLCNotOwnedFlipsWhenDLCIsBought(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 320, Name: "Bought Later", PlaytimeForever: 30}})
	fake.gameBody[320] = gameJSON(
		[]groupFixture{{id: 0}, {id: 1, name: "Later DLC", dlc: 321}},
		[]achFixture{{name: "BASE", key: 1, group: 0}, {name: "DLC", key: 2, group: 1}},
	)
	fake.userBody[320] = userJSON([]int64{1}, []int64{0})

	runFullSyncOK(t, repo, fake, profile)
	before := findCard(t, cardsForApp(allCards(t, repo, profile.ID), 320), 1)
	if !before.DLCNotOwned || !hasTag(before.Tags, "dlc_not_owned") {
		t.Fatalf("DLC set before purchase = %#v, want DLCNotOwned with tag", before)
	}
	for _, tag := range []string{"in_progress", "almost_there", "untouched"} {
		if hasTag(before.Tags, tag) {
			t.Fatalf("unowned DLC tags = %v, want no %s", before.Tags, tag)
		}
	}

	fake.userBody[320] = userJSON([]int64{1}, []int64{0, 1})
	runFullSyncOK(t, repo, fake, profile)
	after := findCard(t, cardsForApp(allCards(t, repo, profile.ID), 320), 1)
	if after.DLCNotOwned || hasTag(after.Tags, "dlc_not_owned") {
		t.Fatalf("DLC set after purchase = %#v, want owned without dlc_not_owned", after)
	}
}

func hasTag(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

func TestFullSyncStoresNoAchievementsForGameWithoutSchema(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 1, Name: "No Schema", PlaytimeForever: 0}})
	fake.schema[1] = []string{}

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	cards := cardsForApp(allCards(t, repo, profile.ID), 1)
	if len(cards) != 1 || cards[0].GroupID != 0 || cards[0].HasAchievements || cards[0].AchievementStatus != "no_achievements" {
		t.Fatalf("cards = %#v, want one group-0 no_achievements entry", cards)
	}
	if fake.count("user", 1) != 0 {
		t.Fatalf("user achievement calls = %d, want none without schema", fake.count("user", 1))
	}
}

func TestFullSyncHasNoEntriesWhenNoSetIsEligible(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 400, Name: "Hidden Only", PlaytimeForever: 0}})
	fake.gameBody[400] = gameJSON(
		[]groupFixture{{id: 0, hidden: true}},
		[]achFixture{{name: "HIDDEN", key: 1, group: 0}},
	)

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	if cards := cardsForApp(allCards(t, repo, profile.ID), 400); len(cards) != 0 {
		t.Fatalf("cards = %#v, want no entries when no set is eligible", cards)
	}
	state, err := repo.ProfileGame(profile.ID, 400, 0)
	if err != nil {
		t.Fatal(err)
	}
	if state.Exists {
		t.Fatalf("state = %#v, want no entry", state)
	}
}

func TestFullSyncLegacyFallbackOnNonTransientFailure(t *testing.T) {
	cases := map[string]gameFailure{
		"http 404":  {status: http.StatusNotFound},
		"eresult 2": {eresult: "2"},
	}
	for name, failure := range cases {
		t.Run(name, func(t *testing.T) {
			repo := testRepository(t)
			profile := testProfile(t, repo)
			fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 10, Name: "Legacy Game", PlaytimeForever: 3}})
			fake.schema[10] = []string{"ACH_ONE", "ACH_TWO"}
			fake.player[10] = map[string]bool{"ACH_ONE": true, "ACH_TWO": false}
			fake.global[10] = map[string]float64{"ACH_ONE": 80, "ACH_TWO": 30}
			fake.gameFailure[10] = failure

			result := runFullSyncOK(t, repo, fake, profile)
			requireSyncResult(t, result, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
			if fake.count("game", 10) != 1 || fake.count("schema", 10) != 1 {
				t.Fatalf("calls game=%d schema=%d, want one new and one legacy schema call", fake.count("game", 10), fake.count("schema", 10))
			}
			state := requireProfileGame(t, repo, profile.ID, 10, 0)
			if state.TotalAchievements != 2 || state.UnlockedAchievements != 1 {
				t.Fatalf("legacy state = %#v, want one group-0 entry 1/2", state)
			}
		})
	}
}

func TestFullSyncNewlyIncompleteWhenSetGainsAchievements(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 800, Name: "Growing Game", PlaytimeForever: 9}})
	base := []groupFixture{{id: 0}, {id: 1, name: "Side Quests"}}
	fake.gameBody[800] = gameJSON(base, []achFixture{
		{name: "MAIN_1", key: 1, group: 0}, {name: "MAIN_2", key: 2, group: 0},
		{name: "SIDE_1", key: 3, group: 1}, {name: "SIDE_2", key: 4, group: 1},
	})
	fake.userBody[800] = userJSON([]int64{1, 2, 3, 4}, []int64{0, 1})
	runFullSyncOK(t, repo, fake, profile)
	if state := requireProfileGame(t, repo, profile.ID, 800, 1); !state.IsCompleted {
		t.Fatalf("side set state = %#v, want completed before growth", state)
	}

	fake.gameBody[800] = gameJSON(base, []achFixture{
		{name: "MAIN_1", key: 1, group: 0}, {name: "MAIN_2", key: 2, group: 0},
		{name: "SIDE_1", key: 3, group: 1}, {name: "SIDE_2", key: 4, group: 1}, {name: "SIDE_3", key: 5, group: 1},
	})
	runFullSyncOK(t, repo, fake, profile)
	side := requireProfileGame(t, repo, profile.ID, 800, 1)
	if side.IsCompleted || !side.NewlyIncomplete || !side.WasCompleted || side.TotalAchievements != 3 {
		t.Fatalf("side set state = %#v, want newly incomplete 2/3", side)
	}
	main := requireProfileGame(t, repo, profile.ID, 800, 0)
	if !main.IsCompleted || main.NewlyIncomplete {
		t.Fatalf("base set state = %#v, want still completed and not newly incomplete", main)
	}
}

func TestFullSyncPrunesEntryThatStopsBeingEligible(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 810, Name: "Pruned Game", PlaytimeForever: 9}})
	fake.gameBody[810] = gameJSON(
		[]groupFixture{{id: 0}, {id: 1, name: "Extra"}},
		[]achFixture{{name: "A", key: 1, group: 0}, {name: "B", key: 2, group: 1}},
	)
	runFullSyncOK(t, repo, fake, profile)
	if state := requireProfileGame(t, repo, profile.ID, 810, 1); !state.Exists {
		t.Fatalf("extra set state = %#v, want entry before archive", state)
	}

	fake.gameBody[810] = gameJSON(
		[]groupFixture{{id: 0}, {id: 1, name: "Extra", archived: true}},
		[]achFixture{{name: "A", key: 1, group: 0}, {name: "B", key: 2, group: 1}},
	)
	runFullSyncOK(t, repo, fake, profile)
	if state := requireProfileGame(t, repo, profile.ID, 810, 1); state.Exists {
		t.Fatalf("extra set state = %#v, want pruned after archive", state)
	}
	if state := requireProfileGame(t, repo, profile.ID, 810, 0); !state.Exists {
		t.Fatalf("base state = %#v, want kept", state)
	}
}

func TestFullSyncFetchesDLCCoverOnlyWhenStale(t *testing.T) {
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 700, Name: "Cover Game", PlaytimeForever: 10}})
	fake.gameBody[700] = gameJSON(
		[]groupFixture{{id: 0}, {id: 1, name: "Cover DLC", dlc: 701}},
		[]achFixture{{name: "BASE", key: 1, group: 0}, {name: "DLC", key: 2, group: 1}},
	)
	fake.userBody[700] = userJSON([]int64{1}, []int64{0, 1})
	fake.images["700/library_600x900.jpg"] = true
	fake.images["701/hashed/hero_capsule.jpg"] = true
	fake.store[701] = storeFixture{hero: "hashed/hero_capsule.jpg"}
	svc := serviceWithImages(t, repo, fake, paths)

	first := runServiceOK(t, svc, profile)
	requireSyncResult(t, first, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	if got := fake.count("store", 701); got != 1 {
		t.Fatalf("store lookups for DLC after first sync = %d, want 1", got)
	}
	cover, ok, err := repo.GetCover(701)
	if err != nil || !ok {
		t.Fatalf("DLC cover = %#v ok=%v err=%v, want stored hero cover", cover, ok, err)
	}
	if cover.Kind != images.KindHero || cover.FileName != images.HeroFileName || cover.DownloadedAt == nil {
		t.Fatalf("DLC cover = %#v, want downloaded hero cover", cover)
	}
	dlcCard := findCard(t, cardsForApp(allCards(t, repo, profile.ID), 700), 1)
	if !strings.HasPrefix(dlcCard.CoverURL, "/game-covers/701/hero_capsule.jpg?v=") {
		t.Fatalf("DLC card cover url = %q, want hero cover of DLC", dlcCard.CoverURL)
	}

	runServiceOK(t, svc, profile)
	if got := fake.count("store", 701); got != 1 {
		t.Fatalf("store lookups for DLC after fresh second sync = %d, want still 1", got)
	}
}

func TestFullSyncFetchesCoverForUnownedDLCSet(t *testing.T) {
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 710, Name: "Unowned DLC Game", PlaytimeForever: 10}})
	fake.gameBody[710] = gameJSON(
		[]groupFixture{{id: 0}, {id: 1, name: "Locked DLC", dlc: 711}},
		[]achFixture{{name: "BASE", key: 1, group: 0}, {name: "DLC", key: 2, group: 1}},
	)
	fake.userBody[710] = userJSON([]int64{1}, []int64{0})
	fake.images["710/library_600x900.jpg"] = true
	fake.store[711] = storeFixture{hero: "hashed/hero_capsule.jpg"}
	fake.images["711/hashed/hero_capsule.jpg"] = true

	result := runServiceOK(t, serviceWithImages(t, repo, fake, paths), profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	// The unowned DLC set is shown, so its cover is fetched like any other DLC set's.
	if got := fake.count("store", 711); got != 1 {
		t.Fatalf("store lookups for unowned DLC = %d, want 1", got)
	}
	cards := cardsForApp(allCards(t, repo, profile.ID), 710)
	if len(cards) != 2 {
		t.Fatalf("cards = %#v, want base entry and unowned DLC entry", cards)
	}
	dlcCard := findCard(t, cards, 1)
	if !dlcCard.DLCNotOwned || !strings.HasPrefix(dlcCard.CoverURL, "/game-covers/711/hero_capsule.jpg?v=") {
		t.Fatalf("unowned DLC card = %#v, want flagged card with hero cover", dlcCard)
	}
}

func TestShouldFallbackToLegacy(t *testing.T) {
	transport := &url.Error{Op: "Get", URL: "https://api.steampowered.com/", Err: errors.New("connection reset")}
	status := func(code int) error {
		return &steam.HTTPStatusError{Path: "/x", Code: code, Status: fmt.Sprintf("%d %s", code, http.StatusText(code))}
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"http 404", status(http.StatusNotFound), true},
		{"http 403 wrapped", fmt.Errorf("achievement schema: %w", status(http.StatusForbidden)), true},
		{"http 400", status(http.StatusBadRequest), true},
		{"http 429 exhausted", status(http.StatusTooManyRequests), false},
		{"http 500 exhausted", status(http.StatusInternalServerError), false},
		{"http 502 not retried", status(http.StatusBadGateway), false},
		{"http 503 exhausted", status(http.StatusServiceUnavailable), false},
		{"http 504 not retried", status(http.StatusGatewayTimeout), false},
		{"eresult 2", &steam.EResultError{Path: "/x", Code: 2}, true},
		{"invalid eresult header", errors.New("Steam API /x returned invalid x-eresult \"abc\""), false},
		{"decode error", errors.New("decode Steam API /x: invalid character"), false},
		{"untyped text is not classified", errors.New("Steam API /x returned 404 Not Found"), false},
		{"untyped eresult text is not classified", errors.New("Steam API /x returned eresult 2"), false},
		{"transport", transport, false},
		{"context canceled", fmt.Errorf("achievement schema: %w", context.Canceled), false},
		{"deadline", context.DeadlineExceeded, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldFallbackToLegacy(tc.err); got != tc.want {
				t.Fatalf("shouldFallbackToLegacy(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestFullSyncKeepsSetEntriesWhenNewAPIFailsForAppWithSetData(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 820, Name: "Set Keeper", PlaytimeForever: 3}})
	fake.gameBody[820] = gameJSON(
		[]groupFixture{{id: 0}, {id: 1, name: "Extra"}},
		[]achFixture{{name: "A", key: 1, group: 0}, {name: "B", key: 2, group: 1}},
	)
	fake.userBody[820] = userJSON([]int64{1, 2}, []int64{0, 1})
	requireSyncResult(t, runFullSyncOK(t, repo, fake, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})

	// A non-transient failure must not reach the legacy API (it would prune the extra set).
	fake.gameFailure[820] = gameFailure{status: http.StatusNotFound}
	requireSyncResult(t, runFullSyncOK(t, repo, fake, profile), expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})
	if got := fake.count("schema", 820); got != 0 {
		t.Fatalf("legacy schema calls = %d, want 0 for app with set data", got)
	}
	if state := requireProfileGame(t, repo, profile.ID, 820, 1); !state.Exists || !state.SyncWarning {
		t.Fatalf("extra set state = %#v, want kept with warning", state)
	}
	if state := requireProfileGame(t, repo, profile.ID, 820, 0); !state.Exists || state.TotalAchievements != 1 {
		t.Fatalf("base state = %#v, want kept with 1 achievement", state)
	}
}

func TestFullSyncBadGatewayFailsWithoutLegacyFallback(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 830, Name: "Gateway Game", PlaytimeForever: 3}})
	fake.schema[830] = []string{"ACH_ONE"}
	fake.gameFailure[830] = gameFailure{status: http.StatusBadGateway}

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})
	if got := fake.count("schema", 830); got != 0 {
		t.Fatalf("legacy schema calls = %d, want 0 for 502", got)
	}
	// The failure path only records a warning; no achievement data is written from the legacy API.
	if state := requireProfileGame(t, repo, profile.ID, 830, 0); state.TotalAchievements != 0 || !state.SyncWarning {
		t.Fatalf("state = %#v, want warning-only entry with no achievements after 502", state)
	}
}

func TestBuildSetEntriesPowerWashSimulatorBaseOnly(t *testing.T) {
	game := pws2Game()
	user := steam.UserAchievements{Unlocked: unlockedKeys(keysBetween(1, 40)...), Groups: groupSet(0)}

	entries := buildSetEntries(2968420, game, user)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want base plus both unowned DLC sets", len(entries))
	}
	entry := entries[0]
	if entry.GroupID != 0 || entry.ImageAppID != 2968420 || entry.stats.total != 40 || entry.stats.unlocked != 40 || entry.DLCNotOwned {
		t.Fatalf("entry = %#v, want base 40/40 and not flagged", entry)
	}
	if !setSyncUpdate(entry, repository.ProfileGameState{}).IsCompleted || entry.stats.completion != 100 {
		t.Fatalf("entry = %#v, want complete at 100%%", entry)
	}
	for _, unowned := range entries[1:] {
		if !unowned.DLCNotOwned || unowned.stats.total != 10 || unowned.stats.unlocked != 0 {
			t.Fatalf("unowned DLC entry = %#v, want DLCNotOwned 0/10", unowned)
		}
	}
}

func TestBuildSetEntriesPowerWashSimulatorDLCOwned(t *testing.T) {
	game := pws2Game()
	user := steam.UserAchievements{Unlocked: unlockedKeys(keysBetween(1, 40)...), Groups: groupSet(0, 1, 2)}

	entries := buildSetEntries(2968420, game, user)
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3 entries with both DLC sets owned", len(entries))
	}
	if entries[1].GroupID != 1 || entries[1].ImageAppID != 4173830 || entries[1].stats.total != 10 || entries[1].stats.unlocked != 0 {
		t.Fatalf("DLC 1 entry = %#v, want image 4173830, 0/10", entries[1])
	}
	if entries[1].DLCNotOwned || entries[2].DLCNotOwned {
		t.Fatalf("owned DLC entries = %#v, %#v, want neither flagged", entries[1], entries[2])
	}
	if entries[2].ImageAppID != 4229450 {
		t.Fatalf("DLC 2 entry image = %d, want 4229450", entries[2].ImageAppID)
	}
}

func TestBuildSetEntriesPowerWashSimulatorDLCNotOwnedIsShownFlagged(t *testing.T) {
	game := pws2Game()
	user := steam.UserAchievements{Unlocked: unlockedKeys(keysBetween(1, 40)...), Groups: groupSet(0, 1)}

	entries := buildSetEntries(2968420, game, user)
	if len(entries) != 3 || entries[1].GroupID != 1 || entries[2].GroupID != 2 {
		t.Fatalf("entries = %#v, want base, owned DLC 1 and unowned DLC 2", entries)
	}
	if entries[1].DLCNotOwned || !entries[2].DLCNotOwned {
		t.Fatalf("DLC flags = %v, %v; want owned DLC 1 unflagged, unowned DLC 2 flagged", entries[1].DLCNotOwned, entries[2].DLCNotOwned)
	}
	if entries[2].ImageAppID != 4229450 || entries[2].stats.total != 10 {
		t.Fatalf("unowned DLC entry = %#v, want image 4229450 with 10 achievements", entries[2])
	}
}

func TestBuildSetEntriesPortal2HasNoBaseEntry(t *testing.T) {
	game := steam.GameAchievements{
		Sets: []steam.AchievementSet{
			{GroupID: 0, Public: true},
			{GroupID: 1, Name: "Cooperative", Public: true, Order: 1},
			{GroupID: 2, Name: "Single Player", Public: true, Order: 2},
		},
		Achievements: append(achievementsOf(1, 1, 22), achievementsOf(2, 23, 29)...),
	}

	entries := buildSetEntries(620, game, steam.UserAchievements{Groups: groupSet(0, 1, 2)})
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 named sets", len(entries))
	}
	for _, entry := range entries {
		if entry.GroupID == 0 {
			t.Fatalf("entries = %#v, want no group-0 entry", entries)
		}
	}
	if entries[0].stats.total != 22 || entries[1].stats.total != 29 {
		t.Fatalf("totals = %d, %d; want 22, 29", entries[0].stats.total, entries[1].stats.total)
	}
}

func TestBuildSetEntriesTF2ReplayCountsOnlyNonArchived(t *testing.T) {
	replay := achievementsOf(11, 101, 8)
	for i := 4; i < 8; i++ {
		replay[i].Archived = true
	}
	game := steam.GameAchievements{
		Sets: []steam.AchievementSet{
			{GroupID: 1, Name: "Team Fortress 2 – Scout", Public: true},
			{GroupID: 11, Name: "Replay", Public: true, Order: 11},
		},
		Achievements: append(achievementsOf(1, 1, 5), replay...),
	}
	user := steam.UserAchievements{Unlocked: unlockedKeys(101, 102, 103, 104), Groups: groupSet(1, 11)}

	entries := buildSetEntries(440, game, user)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want Scout and Replay", len(entries))
	}
	replayEntry := entries[1]
	if replayEntry.GroupID != 11 || replayEntry.stats.total != 4 || replayEntry.stats.unlocked != 4 {
		t.Fatalf("replay entry = %#v, want 4/4", replayEntry)
	}
	if !setSyncUpdate(replayEntry, repository.ProfileGameState{}).IsCompleted {
		t.Fatalf("replay entry = %#v, want completed with archived locked achievements", replayEntry)
	}
	if len(replayEntry.stats.records) != 8 {
		t.Fatalf("replay records = %d, want all 8 achievements stored", len(replayEntry.stats.records))
	}
}

func TestBuildSetEntriesSkipsHiddenDeveloperOnlyAndArchivedSets(t *testing.T) {
	var achs []steam.SetAchievement
	achs = append(achs, achievementsOf(3, 1, 2)...)               // hidden set
	achs = append(achs, achievementsOf(4, 3, 2)...)               // developer-only set
	achs = append(achs, achievementsOf(5, 5, 2)...)               // archived set
	achs = append(achs, archivedCopy(achievementsOf(6, 7, 2))...) // every achievement archived
	achs = append(achs, achievementsOf(7, 9, 1)...)               // visible set
	achs = append(achs, achievementsOf(8, 10, 1)...)              // DLC set the player does not own
	game := steam.GameAchievements{
		Sets: []steam.AchievementSet{
			{GroupID: 3, Name: "Hidden", Public: false},
			{GroupID: 4, Name: "Dev", Public: true, DeveloperOnly: true},
			{GroupID: 5, Name: "Archived set", Public: true, Archived: true},
			{GroupID: 6, Name: "All archived", Public: true},
			{GroupID: 7, Name: "Visible", Public: true},
			{GroupID: 8, Name: "Unowned DLC", Public: true, DLCAppID: 999},
		},
		Achievements: achs,
	}
	user := steam.UserAchievements{Groups: groupSet(3, 4, 5, 6, 7)}

	entries := buildSetEntries(500, game, user)
	if len(entries) != 2 || entries[0].GroupID != 7 || entries[1].GroupID != 8 {
		t.Fatalf("entries = %#v, want visible set 7 and unowned DLC set 8", entries)
	}
	if entries[0].DLCNotOwned || !entries[1].DLCNotOwned {
		t.Fatalf("DLC flags = %v, %v; want set 7 unflagged, set 8 flagged", entries[0].DLCNotOwned, entries[1].DLCNotOwned)
	}
}

func TestBuildSetEntriesNoSchemaHasNoEntries(t *testing.T) {
	if entries := buildSetEntries(1, steam.GameAchievements{}, steam.UserAchievements{}); len(entries) != 0 {
		t.Fatalf("entries = %#v, want none for empty schema", entries)
	}
}

func TestBuildSetEntriesDLCOwnedByUnlockedAchievementWithoutGroupFlag(t *testing.T) {
	game := pws2Game()
	// Groups lists only the base set, but the player unlocked one DLC 1 achievement (key 41).
	user := steam.UserAchievements{Unlocked: unlockedKeys(append(keysBetween(1, 40), 41)...), Groups: groupSet(0)}

	entries := buildSetEntries(2968420, game, user)
	if len(entries) != 3 || entries[1].GroupID != 1 || entries[2].GroupID != 2 {
		t.Fatalf("entries = %#v, want base, DLC 1 (owned via unlocked achievement) and unowned DLC 2", entries)
	}
	if entries[1].DLCNotOwned || !entries[2].DLCNotOwned {
		t.Fatalf("DLC flags = %v, %v; want DLC 1 unflagged, DLC 2 flagged", entries[1].DLCNotOwned, entries[2].DLCNotOwned)
	}
	if entries[1].stats.unlocked != 1 || entries[1].stats.total != 10 {
		t.Fatalf("DLC 1 stats = %#v, want 1/10", entries[1].stats)
	}
}

func TestBuildSetEntriesDLCNotOwnedWithoutUnlocksOrGroupFlag(t *testing.T) {
	game := pws2Game()
	// Group 2 is owned through its group flag; group 1 has no flag and no unlock, so it is not owned.
	user := steam.UserAchievements{Unlocked: unlockedKeys(keysBetween(1, 40)...), Groups: groupSet(0, 2)}

	entries := buildSetEntries(2968420, game, user)
	if len(entries) != 3 {
		t.Fatalf("entries = %#v, want base plus both DLC sets", entries)
	}
	if !entries[1].DLCNotOwned || entries[2].DLCNotOwned {
		t.Fatalf("DLC flags = %v, %v; want DLC 1 flagged, DLC 2 unflagged", entries[1].DLCNotOwned, entries[2].DLCNotOwned)
	}
}

func TestBuildSetEntriesSynthesizesDefaultSetForMissingMetadata(t *testing.T) {
	game := steam.GameAchievements{
		Sets: []steam.AchievementSet{{GroupID: 0, Name: "", Public: true}},
		Achievements: []steam.SetAchievement{
			{InternalName: "BASE_1", Key: 1, GroupID: 0},
			{InternalName: "NEW_9", Key: 9, GroupID: 9},
			{InternalName: "NEW_5", Key: 5, GroupID: 5},
			{InternalName: "NEW_5B", Key: 6, GroupID: 5},
		},
	}
	entries := buildSetEntries(77, game, steam.UserAchievements{Unlocked: unlockedKeys(5), Groups: groupSet(0)})
	if len(entries) != 3 {
		t.Fatalf("entries = %#v, want base plus synthesized groups 5 and 9", entries)
	}
	if entries[0].GroupID != 0 || entries[1].GroupID != 5 || entries[2].GroupID != 9 {
		t.Fatalf("group order = %d, %d, %d; want 0, 5, 9", entries[0].GroupID, entries[1].GroupID, entries[2].GroupID)
	}
	synth := entries[1]
	if synth.Name != "" || synth.DLCAppID != 0 || synth.ImageAppID != 77 || synth.stats.total != 2 || synth.stats.unlocked != 1 {
		t.Fatalf("synthesized entry = %#v, want unnamed base-like set 1/2 for app 77", synth)
	}
}

func TestBuildSetEntriesNullSetsSynthesizesBaseSet(t *testing.T) {
	game := steam.GameAchievements{
		Achievements: []steam.SetAchievement{{InternalName: "ONLY", Key: 1, GroupID: 0}, {InternalName: "TWO", Key: 2, GroupID: 0}},
	}
	entries := buildSetEntries(55, game, steam.UserAchievements{Unlocked: unlockedKeys(1)})
	if len(entries) != 1 {
		t.Fatalf("entries = %#v, want one synthesized group-0 entry", entries)
	}
	entry := entries[0]
	if entry.GroupID != 0 || entry.ImageAppID != 55 || entry.stats.total != 2 || entry.stats.unlocked != 1 {
		t.Fatalf("entry = %#v, want group 0 for app 55 at 1/2", entry)
	}
}

func TestBuildSetEntriesMissingAverageUsesCountableLockedOnly(t *testing.T) {
	game := steam.GameAchievements{
		Sets: []steam.AchievementSet{{GroupID: 0, Public: true}},
		Achievements: []steam.SetAchievement{
			{InternalName: "A", Key: 1, GlobalPercent: pct(50)},
			{InternalName: "B", Key: 2, GlobalPercent: pct(10)},
			{InternalName: "C", Key: 3},
			{InternalName: "D", Key: 4, GlobalPercent: pct(90), Archived: true},
		},
	}
	entries := buildSetEntries(42, game, steam.UserAchievements{Unlocked: unlockedKeys(1), Groups: groupSet(0)})
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	stats := entries[0].stats
	if stats.total != 3 || stats.unlocked != 1 {
		t.Fatalf("stats = %#v, want 1/3", stats)
	}
	if stats.missingAvg == nil || *stats.missingAvg != 10 {
		t.Fatalf("missing average = %v, want 10", stats.missingAvg)
	}
	wantScore := stats.completion*0.5 + 10*0.5
	if stats.score != wantScore {
		t.Fatalf("score = %v, want %v", stats.score, wantScore)
	}
}

func TestSetSyncUpdateNewlyIncompleteWhenEntryGainsAchievements(t *testing.T) {
	prev := repository.ProfileGameState{Exists: true, TotalAchievements: 4, UnlockedAchievements: 4, IsCompleted: true, WasCompleted: true}

	grown := setSyncUpdate(setEntry{GroupID: 1, stats: achievementStats{total: 5, unlocked: 4}}, prev)
	if grown.IsCompleted || !grown.NewlyIncomplete || !grown.WasCompleted {
		t.Fatalf("grown update = %#v, want newly incomplete and was completed", grown)
	}
	same := setSyncUpdate(setEntry{GroupID: 1, stats: achievementStats{total: 4, unlocked: 3}}, prev)
	if same.NewlyIncomplete {
		t.Fatalf("same-size update = %#v, want not newly incomplete (total did not grow)", same)
	}
}

func TestCoverImageAppIDsIncludesEachDLCOnce(t *testing.T) {
	entries := []setEntry{
		{GroupID: 0, ImageAppID: 1},
		{GroupID: 1, ImageAppID: 501},
		{GroupID: 2, ImageAppID: 501},
		{GroupID: 3, ImageAppID: 1},
	}
	got := coverImageAppIDs(1, entries)
	if len(got) != 2 || got[0] != 1 || got[1] != 501 {
		t.Fatalf("image app ids = %v, want [1 501]", got)
	}
}

type expectedSyncResult struct {
	status   string
	total    int
	synced   int
	failed   int
	hasError bool
}

type summaryCounts struct {
	owned        int
	achievements int
	unlocked     int
}

func runFullSyncOK(t *testing.T, repo *repository.Repository, fake *fakeSteamServer, profile *repository.Profile) Result {
	t.Helper()
	return runServiceOK(t, &Service{Repo: repo, Steam: fake.client}, profile)
}

func runServiceOK(t *testing.T, svc *Service, profile *repository.Profile) Result {
	t.Helper()
	result, err := svc.FullSync(context.Background(), *profile)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requireSyncResult(t *testing.T, result Result, want expectedSyncResult) {
	t.Helper()
	if result.Status != want.status {
		t.Fatalf("status = %q, want %q; result = %#v", result.Status, want.status, result)
	}
	if result.GamesTotal != want.total {
		t.Fatalf("games total = %d, want %d; result = %#v", result.GamesTotal, want.total, result)
	}
	if result.GamesSynced != want.synced {
		t.Fatalf("games synced = %d, want %d; result = %#v", result.GamesSynced, want.synced, result)
	}
	if result.GamesFailed != want.failed {
		t.Fatalf("games failed = %d, want %d; result = %#v", result.GamesFailed, want.failed, result)
	}
	requireErrorPresence(t, result.Error, want.hasError, result)
}

func requireErrorPresence(t *testing.T, errText *string, want bool, context any) {
	t.Helper()
	if want && errText == nil {
		t.Fatalf("error = nil, want error; context = %#v", context)
	}
	if !want && errText != nil {
		t.Fatalf("error = %q, want nil; context = %#v", *errText, context)
	}
}

func requireLatestSyncRun(t *testing.T, repo *repository.Repository, profileID int64, want expectedSyncResult) {
	t.Helper()
	run, err := repo.LatestSyncRun(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil {
		t.Fatal("sync run = nil, want latest run")
	}
	got := Result{Status: run.Status, GamesTotal: run.GamesTotal, GamesSynced: run.GamesSynced, GamesFailed: run.GamesFailed, Error: run.Error}
	requireSyncResult(t, got, want)
}

func requireSummaryCounts(t *testing.T, repo *repository.Repository, profileID int64, want summaryCounts) {
	t.Helper()
	summary, err := repo.Summary(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.OwnedGamesCount != want.owned {
		t.Fatalf("owned games count = %d, want %d", summary.OwnedGamesCount, want.owned)
	}
	if summary.TotalAchievementsAvailable != want.achievements {
		t.Fatalf("total achievements = %d, want %d", summary.TotalAchievementsAvailable, want.achievements)
	}
	if summary.TotalAchievementsUnlocked != want.unlocked {
		t.Fatalf("unlocked achievements = %d, want %d", summary.TotalAchievementsUnlocked, want.unlocked)
	}
}

func requireWarningCard(t *testing.T, repo *repository.Repository, profileID int64, appID int64, message string) {
	t.Helper()
	warnings := requireGameCards(t, repo, profileID, "warnings")
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want one warning", warnings)
	}
	card := warnings[0]
	if card.AppID != appID {
		t.Fatalf("warning appID = %d, want %d", card.AppID, appID)
	}
	if !card.SyncWarning {
		t.Fatalf("warning card = %#v, want sync warning", card)
	}
	requireStringPtrContains(t, card.LastError, message, card)
}

func requireGoodGameState(t *testing.T, repo *repository.Repository, profileID int64, appID int64) {
	t.Helper()
	state := requireProfileGame(t, repo, profileID, appID, 0)
	if !state.Exists {
		t.Fatalf("good game state = %#v, want existing state", state)
	}
	if state.TotalAchievements != 2 {
		t.Fatalf("good total achievements = %d, want 2", state.TotalAchievements)
	}
	if state.UnlockedAchievements != 1 {
		t.Fatalf("good unlocked achievements = %d, want 1", state.UnlockedAchievements)
	}
	if state.SyncWarning {
		t.Fatalf("good game state = %#v, want no warning", state)
	}
}

func requireRecoveredGameState(t *testing.T, repo *repository.Repository, profileID int64, appID int64) {
	t.Helper()
	state := requireProfileGame(t, repo, profileID, appID, 0)
	if !state.Exists {
		t.Fatalf("recovered state = %#v, want existing state", state)
	}
	if state.SyncWarning {
		t.Fatalf("recovered state = %#v, want no warning", state)
	}
	if state.TotalAchievements != 2 {
		t.Fatalf("recovered total achievements = %d, want 2", state.TotalAchievements)
	}
	if state.UnlockedAchievements != 2 {
		t.Fatalf("recovered unlocked achievements = %d, want 2", state.UnlockedAchievements)
	}
	if !state.IsCompleted {
		t.Fatalf("recovered state = %#v, want completed", state)
	}
}

func requireProfileGame(t *testing.T, repo *repository.Repository, profileID, appID, groupID int64) repository.ProfileGameState {
	t.Helper()
	state, err := repo.ProfileGame(profileID, appID, groupID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func requireNoWarningCards(t *testing.T, repo *repository.Repository, profileID int64) {
	t.Helper()
	warnings := requireGameCards(t, repo, profileID, "warnings")
	if len(warnings) != 0 {
		t.Fatalf("warnings after recovery = %#v, want cleared warning", warnings)
	}
}

func requireCompletedCard(t *testing.T, repo *repository.Repository, profileID int64, appID int64) {
	t.Helper()
	completed := requireGameCards(t, repo, profileID, "completed")
	if len(completed) != 1 {
		t.Fatalf("completed after recovery = %#v, want one completed card", completed)
	}
	if completed[0].AppID != appID {
		t.Fatalf("completed appID = %d, want %d", completed[0].AppID, appID)
	}
	if completed[0].LastError != nil {
		t.Fatalf("completed after recovery = %#v, want no error", completed)
	}
}

func requireGameCards(t *testing.T, repo *repository.Repository, profileID int64, filter string) []repository.GameCard {
	t.Helper()
	cards, err := repo.GameCards(profileID, filter)
	if err != nil {
		t.Fatal(err)
	}
	return cards
}

// allCards returns every entry of the profile once (suggestions, completed, warnings and disabled views).
func allCards(t *testing.T, repo *repository.Repository, profileID int64) []repository.GameCard {
	t.Helper()
	seen := make(map[string]bool)
	var out []repository.GameCard
	for _, filter := range []string{"", "completed", "warnings", "disabled"} {
		for _, card := range requireGameCards(t, repo, profileID, filter) {
			if !seen[card.EntryKey] {
				seen[card.EntryKey] = true
				out = append(out, card)
			}
		}
	}
	return out
}

func cardsForApp(cards []repository.GameCard, appID int64) []repository.GameCard {
	var out []repository.GameCard
	for _, card := range cards {
		if card.AppID == appID {
			out = append(out, card)
		}
	}
	return out
}

func findCard(t *testing.T, cards []repository.GameCard, groupID int64) repository.GameCard {
	t.Helper()
	for _, card := range cards {
		if card.GroupID == groupID {
			return card
		}
	}
	t.Fatalf("no entry with groupid %d in %#v", groupID, cards)
	return repository.GameCard{}
}

func requireStringPtrContains(t *testing.T, got *string, want string, context any) {
	t.Helper()
	if got == nil {
		t.Fatalf("string = nil, want %q; context = %#v", want, context)
	}
	if !strings.Contains(*got, want) {
		t.Fatalf("string = %q, want substring %q; context = %#v", *got, want, context)
	}
}

func requireConcurrentRange(t *testing.T, fake *fakeSteamServer, min, max int) {
	t.Helper()
	got := fake.maxConcurrent()
	if got < min {
		t.Fatalf("max concurrent per-game requests = %d, want at least %d", got, min)
	}
	if got > max {
		t.Fatalf("max concurrent per-game requests = %d, want at most %d", got, max)
	}
}

// storeFixture is the store item assets served for one app id.
type storeFixture struct {
	library string
	hero    string
}

// gameFailure makes GetGameAchievements fail with an HTTP status or an x-eresult header.
type gameFailure struct {
	status  int
	eresult string
}

type fakeSteamServer struct {
	server      *httptest.Server
	client      *steam.Client
	schema      map[int64][]string
	player      map[int64]map[string]bool
	global      map[int64]map[string]float64
	playerError map[int64]string // GetUserAchievements (and legacy player) answers 403/error with this text
	gameFailure map[int64]gameFailure
	gameBody    map[int64]string // raw GetGameAchievements body override
	userBody    map[int64]string // raw GetUserAchievements body override
	store       map[int64]storeFixture
	images      map[string]bool // served store_item_assets paths relative to steam/apps/
	delay       time.Duration
	mu          sync.Mutex
	counts      map[string]int
	active      int
	maxActive   int
}

func newFakeSteamServer(t *testing.T, owned []steam.OwnedGame) *fakeSteamServer {
	t.Helper()
	f := &fakeSteamServer{
		schema:      make(map[int64][]string),
		player:      make(map[int64]map[string]bool),
		global:      make(map[int64]map[string]float64),
		playerError: make(map[int64]string),
		gameFailure: make(map[int64]gameFailure),
		gameBody:    make(map[int64]string),
		userBody:    make(map[int64]string),
		store:       make(map[int64]storeFixture),
		images:      make(map[string]bool),
		counts:      make(map[string]int),
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/IPlayerService/GetOwnedGames/v0001/":
			f.increment("owned", 0)
			fmt.Fprint(w, ownedGamesJSON(owned))
		case "/IPlayerService/GetGameAchievements/v1/":
			f.serveGameAchievements(t, w, r)
		case "/IPlayerService/GetUserAchievements/v1/":
			f.serveUserAchievements(t, w, r)
		case "/ISteamUserStats/GetSchemaForGame/v2/":
			appID := int64Query(t, r, "appid")
			done := f.enterPerGameRequest()
			defer done()
			f.increment("schema", appID)
			fmt.Fprint(w, schemaJSON(f.schema[appID]))
		case "/ISteamUserStats/GetPlayerAchievements/v1/":
			appID := int64Query(t, r, "appid")
			done := f.enterPerGameRequest()
			defer done()
			f.increment("player", appID)
			if msg := f.playerError[appID]; msg != "" {
				fmt.Fprintf(w, `{"playerstats":{"success":false,"error":%q}}`, msg)
				return
			}
			fmt.Fprint(w, playerJSON(f.player[appID]))
		case "/ISteamUserStats/GetGlobalAchievementPercentagesForApp/v0002/":
			appID := int64Query(t, r, "gameid")
			done := f.enterPerGameRequest()
			defer done()
			f.increment("global", appID)
			fmt.Fprint(w, globalJSON(f.global[appID]))
		case "/IStoreBrowseService/GetItems/v1/":
			f.serveStoreItems(t, w, r)
		default:
			if rel, ok := strings.CutPrefix(r.URL.Path, "/store_item_assets/steam/apps/"); ok && f.images[rel] {
				w.Header().Set("Content-Type", "image/jpeg")
				fmt.Fprint(w, "jpeg-bytes")
				return
			}
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	f.client = &steam.Client{APIKey: "test-key", BaseURL: f.server.URL, HTTPClient: f.server.Client()}
	return f
}

func (f *fakeSteamServer) serveGameAchievements(t *testing.T, w http.ResponseWriter, r *http.Request) {
	appID := int64Query(t, r, "appid")
	done := f.enterPerGameRequest()
	defer done()
	f.increment("game", appID)
	if fail, ok := f.gameFailure[appID]; ok {
		if fail.eresult != "" {
			w.Header().Set("x-eresult", fail.eresult)
			fmt.Fprint(w, `{"response":{}}`)
			return
		}
		http.Error(w, "unsupported", fail.status)
		return
	}
	body := f.gameBody[appID]
	if body == "" {
		if len(f.schema[appID]) == 0 {
			w.Header().Set("x-eresult", "42")
			fmt.Fprint(w, `{"response":{}}`)
			return
		}
		body = gameJSON([]groupFixture{{id: 0}}, defaultAchievements(f.schema[appID], f.global[appID]))
	}
	w.Header().Set("x-eresult", "1")
	fmt.Fprint(w, body)
}

func (f *fakeSteamServer) serveUserAchievements(t *testing.T, w http.ResponseWriter, r *http.Request) {
	appID := int64Query(t, r, "appid")
	done := f.enterPerGameRequest()
	defer done()
	f.increment("user", appID)
	if msg := f.playerError[appID]; msg != "" {
		http.Error(w, msg, http.StatusForbidden)
		return
	}
	body := f.userBody[appID]
	if body == "" {
		body = userJSON(unlockedKeysFromNames(f.schema[appID], f.player[appID]), []int64{0})
	}
	w.Header().Set("x-eresult", "1")
	fmt.Fprint(w, body)
}

func (f *fakeSteamServer) serveStoreItems(t *testing.T, w http.ResponseWriter, r *http.Request) {
	var input struct {
		IDs []struct {
			AppID int64 `json:"appid"`
		} `json:"ids"`
	}
	if err := json.Unmarshal([]byte(r.URL.Query().Get("input_json")), &input); err != nil {
		t.Fatalf("bad store input_json: %v", err)
	}
	items := make([]string, 0, len(input.IDs))
	for _, id := range input.IDs {
		f.increment("store", id.AppID)
		fixture, ok := f.store[id.AppID]
		if !ok {
			items = append(items, fmt.Sprintf(`{"id":%d,"appid":%d,"success":2}`, id.AppID, id.AppID))
			continue
		}
		items = append(items, fmt.Sprintf(`{"id":%d,"appid":%d,"success":1,"type":4,"assets":{"library_capsule":%q,"hero_capsule":%q},"related":{"parent_appid":0}}`,
			id.AppID, id.AppID, fixture.library, fixture.hero))
	}
	fmt.Fprintf(w, `{"response":{"store_items":[%s]}}`, strings.Join(items, ","))
}

func (f *fakeSteamServer) increment(kind string, appID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[fmt.Sprintf("%s:%d", kind, appID)]++
}

func (f *fakeSteamServer) count(kind string, appID int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[fmt.Sprintf("%s:%d", kind, appID)]
}

func (f *fakeSteamServer) enterPerGameRequest() func() {
	f.mu.Lock()
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}
}

func (f *fakeSteamServer) maxConcurrent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

// redirectTransport sends every request to the fake server, keeping the path (store assets, store items).
type redirectTransport struct {
	target *url.URL
}

func (rt redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = rt.target.Scheme
	clone.URL.Host = rt.target.Host
	clone.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func serviceWithImages(t *testing.T, repo *repository.Repository, fake *fakeSteamServer, paths appdata.Paths) *Service {
	t.Helper()
	target, err := url.Parse(fake.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cache := images.New(paths)
	cache.HTTPClient = &http.Client{Transport: redirectTransport{target: target}}
	return &Service{Repo: repo, Steam: fake.client, Images: cache}
}

// groupFixture describes one achievement set of a game response.
type groupFixture struct {
	id       int64
	name     string
	dlc      int64
	archived bool
	devOnly  bool
	hidden   bool
}

// achFixture describes one achievement of a game response.
type achFixture struct {
	name     string
	key      int64
	group    int64
	archived bool
	percent  float64
}

// gameJSON renders an IPlayerService/GetGameAchievements body.
func gameJSON(groups []groupFixture, achievements []achFixture) string {
	gs := make([]string, 0, len(groups))
	for i, g := range groups {
		gs = append(gs, fmt.Sprintf(`{"groupid":%d,"localized_name":%q,"dlcappid":%d,"archived":%t,"developeronly":%t,"order":%d,"ispublic":%t,"total_achievements":0,"completion_achievements":0}`,
			g.id, g.name, g.dlc, g.archived, g.devOnly, i, !g.hidden))
	}
	as := make([]string, 0, len(achievements))
	for _, a := range achievements {
		as = append(as, fmt.Sprintf(`{"internal_name":%q,"internal_key":%d,"groupid":%d,"archived":%t,"player_percent_unlocked":"%.1f"}`,
			a.name, a.key, a.group, a.archived, a.percent))
	}
	return fmt.Sprintf(`{"response":{"schema_version":1,"achievements":[%s],"groups":[%s]}}`, strings.Join(as, ","), strings.Join(gs, ","))
}

// userJSON renders an IPlayerService/GetUserAchievements body with unlocked keys and present groups.
func userJSON(unlocked []int64, groups []int64) string {
	as := make([]string, 0, len(unlocked))
	for _, key := range unlocked {
		as = append(as, fmt.Sprintf(`{"internal_key":%d,"unlocked":true,"unlock_time":1700000000}`, key))
	}
	gs := make([]string, 0, len(groups))
	for _, g := range groups {
		gs = append(gs, fmt.Sprintf(`{"groupid":%d}`, g))
	}
	return fmt.Sprintf(`{"response":{"achievements":[%s],"groups":[%s]}}`, strings.Join(as, ","), strings.Join(gs, ","))
}

func defaultAchievements(names []string, global map[string]float64) []achFixture {
	out := make([]achFixture, 0, len(names))
	for i, name := range names {
		out = append(out, achFixture{name: name, key: int64(i + 1), group: 0, percent: global[name]})
	}
	return out
}

func unlockedKeysFromNames(names []string, player map[string]bool) []int64 {
	var keys []int64
	for i, name := range names {
		if player[name] {
			keys = append(keys, int64(i+1))
		}
	}
	return keys
}

func ownedGamesJSON(games []steam.OwnedGame) string {
	parts := make([]string, 0, len(games))
	for _, g := range games {
		parts = append(parts, fmt.Sprintf(`{"appid":%d,"name":%q,"playtime_forever":%d}`, g.AppID, g.Name, g.PlaytimeForever))
	}
	return fmt.Sprintf(`{"response":{"game_count":%d,"games":[%s]}}`, len(games), strings.Join(parts, ","))
}

func schemaJSON(names []string) string {
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf(`{"name":%q,"displayName":%q}`, name, name))
	}
	return fmt.Sprintf(`{"game":{"availableGameStats":{"achievements":[%s]}}}`, strings.Join(parts, ","))
}

func playerJSON(values map[string]bool) string {
	parts := make([]string, 0, len(values))
	for name, achieved := range values {
		achievedInt := 0
		if achieved {
			achievedInt = 1
		}
		parts = append(parts, fmt.Sprintf(`{"apiname":%q,"achieved":%d,"unlocktime":0}`, name, achievedInt))
	}
	return fmt.Sprintf(`{"playerstats":{"success":true,"achievements":[%s]}}`, strings.Join(parts, ","))
}

func globalJSON(values map[string]float64) string {
	parts := make([]string, 0, len(values))
	for name, percent := range values {
		parts = append(parts, fmt.Sprintf(`{"name":%q,"percent":%.2f}`, name, percent))
	}
	return fmt.Sprintf(`{"achievementpercentages":{"achievements":[%s]}}`, strings.Join(parts, ","))
}

func int64Query(t *testing.T, r *http.Request, key string) int64 {
	t.Helper()
	value, err := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	if err != nil {
		t.Fatalf("bad %s query %q: %v", key, r.URL.Query().Get(key), err)
	}
	return value
}

func testProfile(t *testing.T, repo *repository.Repository) *repository.Profile {
	t.Helper()
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func testRepository(t *testing.T) *repository.Repository {
	t.Helper()
	return openTestRepository(t, testPaths(t))
}

func testPaths(t *testing.T) appdata.Paths {
	t.Helper()
	root := t.TempDir()
	return appdata.Paths{
		Root:       root,
		DB:         root + "/test.db",
		Backups:    root + "/backups",
		Cache:      root + "/cache",
		GameImages: root + "/cache/images/games",
		Logs:       root + "/logs",
		LogFile:    root + "/logs/app.log",
	}
}

func openTestRepository(t *testing.T, paths appdata.Paths) *repository.Repository {
	t.Helper()
	db, err := dbinit.OpenAndMigrate(paths)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return repository.New(db)
}

func seedGameSync(t *testing.T, repo *repository.Repository, profileID, appID int64) {
	t.Helper()
	global := 42.0
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.SaveGameSync(repository.GameSyncUpdate{
		ProfileID:         profileID,
		AppID:             appID,
		Name:              fmt.Sprintf("Game %d", appID),
		PlaytimeForever:   60,
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets: []repository.SetSyncUpdate{{
			GroupID:              0,
			TotalAchievements:    1,
			UnlockedAchievements: 1,
			CompletionPercent:    100,
			IsCompleted:          true,
			WasCompleted:         true,
			Achievements:         []repository.AchievementRecord{{APIName: "ACH", GlobalPercent: &global, Unlocked: true}},
		}},
		SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

// pws2Game mirrors PowerWash Simulator 2: base set (40) plus two DLC sets (10 each).
func pws2Game() steam.GameAchievements {
	return steam.GameAchievements{
		Sets: []steam.AchievementSet{
			{GroupID: 0, Public: true, Order: 0},
			{GroupID: 1, Name: "PowerWash Simulator 2 - DLC 1", DLCAppID: 4173830, Public: true, Order: 1},
			{GroupID: 2, Name: "PowerWash Simulator 2 - DLC 2", DLCAppID: 4229450, Public: true, Order: 2},
		},
		Achievements: append(append(achievementsOf(0, 1, 40), achievementsOf(1, 41, 10)...), achievementsOf(2, 51, 10)...),
	}
}

// achievementsOf builds count achievements of one group with consecutive keys starting at firstKey.
func achievementsOf(group, firstKey int64, count int) []steam.SetAchievement {
	out := make([]steam.SetAchievement, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, steam.SetAchievement{
			InternalName: fmt.Sprintf("G%d_A%d", group, i),
			Key:          firstKey + int64(i),
			GroupID:      group,
		})
	}
	return out
}

func archivedCopy(list []steam.SetAchievement) []steam.SetAchievement {
	out := make([]steam.SetAchievement, len(list))
	copy(out, list)
	for i := range out {
		out[i].Archived = true
	}
	return out
}

func keysBetween(from, to int64) []int64 {
	keys := make([]int64, 0, to-from+1)
	for k := from; k <= to; k++ {
		keys = append(keys, k)
	}
	return keys
}

func unlockedKeys(keys ...int64) map[int64]*int64 {
	out := make(map[int64]*int64, len(keys))
	for _, k := range keys {
		out[k] = nil
	}
	return out
}

func groupSet(ids ...int64) map[int64]bool {
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func pct(v float64) *float64 { return &v }
