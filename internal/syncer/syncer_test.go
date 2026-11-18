package syncer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
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
	for _, game := range owned {
		fake.schema[game.AppID] = []string{}
	}
	// The four workers take games 0-3 first, so game 0 (synced before, with data) is in flight when the sync is canceled.
	seedGameSync(t, repo, profile.ID, owned[0].AppID)
	hold := fake.holdGameRequests(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	type outcome struct {
		result Result
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(ctx, *profile)
		finished <- outcome{result: result, err: err}
	}()
	hold.awaitStarted(t, 4) // four GetGameAchievements requests are at the server
	cancel()
	// The client aborts its requests on cancel without a server answer, so the server stays held until the sync returns.
	out := <-finished
	hold.open()

	if out.err == nil || out.err != context.Canceled {
		t.Fatalf("err = %v, want context canceled", out.err)
	}
	result := out.result
	if result.Status != "failed" || result.Error == nil || result.GamesTotal != len(owned) {
		t.Fatalf("result = %#v, want failed canceled sync with total games", result)
	}
	if result.GamesSynced != 0 || result.GamesFailed != 0 {
		t.Fatalf("result = %#v, want no synced and no failed games for an interrupted sync", result)
	}
	run, err := repo.LatestSyncRun(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Status != "failed" || run.Error == nil || !strings.Contains(*run.Error, "context canceled") {
		t.Fatalf("sync run = %#v, want persisted failed cancellation", run)
	}
	// Games interrupted in flight are not failures: no warning rows, and the stored data is kept.
	requireNoWarningCards(t, repo, profile.ID)
	state := requireProfileGame(t, repo, profile.ID, owned[0].AppID, 0)
	if !state.Exists || state.SyncWarning || state.TotalAchievements != 1 || !state.IsCompleted {
		t.Fatalf("in-flight game state = %#v, want previous completed data kept without warning", state)
	}
}

// Cancel after every game is already handed to a worker: dispatch has no error to return, so the interrupted
// in-flight games must still end the run as failed with the cancellation, and the profile must not be touched.
func TestFullSyncCancelAfterAllGamesDispatchedFailsRun(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	owned := make([]steam.OwnedGame, 4) // at most the worker count, so every game is in flight when canceled
	for i := range owned {
		appID := int64(3000 + i)
		owned[i] = steam.OwnedGame{AppID: appID, Name: fmt.Sprintf("Game %d", appID), PlaytimeForever: 1}
	}
	fake := newFakeSteamServer(t, owned)
	for _, game := range owned {
		fake.schema[game.AppID] = []string{}
	}
	seedGameSync(t, repo, profile.ID, owned[0].AppID)
	lastSyncedBefore := currentProfile(t, repo).LastSyncedAt
	hold := fake.holdGameRequests(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	type outcome struct {
		result Result
		err    error
	}
	finished := make(chan outcome, 1)
	go func() {
		result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(ctx, *profile)
		finished <- outcome{result: result, err: err}
	}()
	hold.awaitStarted(t, 4) // all four games are at the server, so dispatch has handed off every game
	cancel()
	out := <-finished
	hold.open()

	if !errors.Is(out.err, context.Canceled) {
		t.Fatalf("err = %v, want context canceled", out.err)
	}
	result := out.result
	if result.Status != "failed" || result.Error == nil || result.GamesTotal != len(owned) {
		t.Fatalf("result = %#v, want failed canceled sync with total games", result)
	}
	if result.GamesSynced != 0 || result.GamesFailed != 0 {
		t.Fatalf("result = %#v, want no synced and no failed games for an interrupted sync", result)
	}
	run := requireLatestRun(t, repo, profile.ID)
	if run.Status != "failed" || run.Error == nil || !strings.Contains(*run.Error, "context canceled") {
		t.Fatalf("sync run = %#v, want persisted failed cancellation", run)
	}
	if run.GamesSynced != 0 || run.GamesFailed != 0 {
		t.Fatalf("sync run counters = %#v, want no synced and no failed games", run)
	}
	requireNoWarningCards(t, repo, profile.ID)
	state := requireProfileGame(t, repo, profile.ID, owned[0].AppID, 0)
	if !state.Exists || state.SyncWarning || state.TotalAchievements != 1 || !state.IsCompleted {
		t.Fatalf("in-flight game state = %#v, want previous completed data kept without warning", state)
	}
	if lastSyncedAfter := currentProfile(t, repo).LastSyncedAt; optionalString(lastSyncedAfter) != optionalString(lastSyncedBefore) {
		t.Fatalf("profile last synced = %s, want %s unchanged after canceled run", optionalString(lastSyncedAfter), optionalString(lastSyncedBefore))
	}
}

// optionalString renders a nullable text value for failure messages.
func optionalString(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestFullSyncGameTransportErrorWarningRedactsAPIKey(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 20, Name: "Dropped Game", PlaytimeForever: 5}})
	fake.schema[20] = []string{"ACH_ONE"}
	fake.hangupGame[20] = true

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})
	requireWarningCard(t, repo, profile.ID, 20, "achievement schema: Get")
	warnings := requireGameCards(t, repo, profile.ID, "warnings")
	if len(warnings) != 1 || warnings[0].LastError == nil {
		t.Fatalf("warnings = %#v, want one warning with text", warnings)
	}
	requireRedactedAPIKeyText(t, *warnings[0].LastError)
	// The run error is only the per-game count summary here; it must not carry the key either.
	if run := requireLatestRun(t, repo, profile.ID); run.Error != nil {
		requireNoAPIKeyText(t, *run.Error)
	}
}

func TestFullSyncOwnedGamesTransportErrorRedactsAPIKeyInSyncRun(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, nil)
	fake.hangupPath["/IPlayerService/GetOwnedGames/v0001/"] = true

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(context.Background(), *profile)
	if err == nil {
		t.Fatal("err = nil, want transport error")
	}
	requireRedactedAPIKeyText(t, err.Error())
	if result.Error == nil {
		t.Fatalf("result = %#v, want error text", result)
	}
	requireRedactedAPIKeyText(t, *result.Error)
	run := requireLatestRun(t, repo, profile.ID)
	if run.Status != "failed" || run.Error == nil {
		t.Fatalf("sync run = %#v, want failed run with error", run)
	}
	requireRedactedAPIKeyText(t, *run.Error)
}

// syncTestSteamID has the 76561 prefix the redaction matches, so a leaked copy would be caught.
const syncTestSteamID = "76561198000000001"

// Transport errors are stored in sync_runs.error and profile_games.last_error. They come from the real steam.Client
// against a server that hangs up, so the request address (with the key and the SteamID64) goes through redaction on
// the way. Neither stored text may keep the SteamID64 or the key.
func TestTransportFailureStoresNoSteamID64OrKeyInSyncText(t *testing.T) {
	t.Run("run error from owned games", func(t *testing.T) {
		repo := testRepository(t)
		profile, err := repo.SaveProfile(syncTestSteamID, "User One", "")
		if err != nil {
			t.Fatal(err)
		}
		fake := newFakeSteamServer(t, nil)
		fake.hangupPath["/IPlayerService/GetOwnedGames/v0001/"] = true

		_, err = (&Service{Repo: repo, Steam: fake.client}).FullSync(context.Background(), *profile)
		if err == nil {
			t.Fatal("err = nil, want transport error")
		}
		requireNoSyncSecrets(t, err.Error())
		run := requireLatestRun(t, repo, profile.ID)
		if run.Error == nil {
			t.Fatalf("sync run = %#v, want error text", run)
		}
		requireNoSyncSecrets(t, *run.Error)
		if !strings.Contains(*run.Error, "REDACTED") {
			t.Fatalf("sync run error = %q, want the redacted request address", *run.Error)
		}
	})

	t.Run("game error in profile_games", func(t *testing.T) {
		repo := testRepository(t)
		profile, err := repo.SaveProfile(syncTestSteamID, "User One", "")
		if err != nil {
			t.Fatal(err)
		}
		fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 20, Name: "Dropped Game", PlaytimeForever: 5}})
		fake.schema[20] = []string{"ACH_ONE"}
		// GetUserAchievements is the per-player request, so its address carries the SteamID64 and the key.
		fake.hangupPath["/IPlayerService/GetUserAchievements/v1/"] = true

		result := runFullSyncOK(t, repo, fake, profile)
		requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})
		warnings := requireGameCards(t, repo, profile.ID, "warnings")
		if len(warnings) != 1 || warnings[0].LastError == nil {
			t.Fatalf("warnings = %#v, want one warning with text", warnings)
		}
		requireNoSyncSecrets(t, *warnings[0].LastError)
		if !strings.Contains(*warnings[0].LastError, "REDACTED") {
			t.Fatalf("profile_games.last_error = %q, want the redacted request address", *warnings[0].LastError)
		}
	})
}

// requireNoSyncSecrets fails when stored sync text carries the test SteamID64 or the test API key.
func requireNoSyncSecrets(t *testing.T, text string) {
	t.Helper()
	requireNoAPIKeyText(t, text)
	if strings.Contains(text, syncTestSteamID) {
		t.Fatalf("text leaks SteamID64: %q", text)
	}
}

// requireNoAPIKeyText fails when text carries the test API key.
func requireNoAPIKeyText(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, "test-key") {
		t.Fatalf("text leaks API key: %q", text)
	}
}

// requireRedactedAPIKeyText fails when text carries the test API key or lacks the redaction marker.
func requireRedactedAPIKeyText(t *testing.T, text string) {
	t.Helper()
	requireNoAPIKeyText(t, text)
	if !strings.Contains(text, "key=REDACTED") {
		t.Fatalf("text = %q, want key=REDACTED", text)
	}
}

func requireLatestRun(t *testing.T, repo *repository.Repository, profileID int64) *repository.SyncRun {
	t.Helper()
	run, err := repo.LatestSyncRun(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil {
		t.Fatal("no sync run persisted")
	}
	return run
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
	return slices.Contains(tags, want)
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

func TestFullSyncSkipsCoverWhenStoreLookupFails(t *testing.T) {
	const appID = 720
	repo, fake, svc, profile := setupBaseCoverSync(t, appID, "library_600x900.jpg")
	// The owned-games file is not served (404), so the store lookup decides the cover and it fails first.
	fake.store[appID] = storeFixture{library: "hashed/library_capsule.jpg"}
	fake.images["720/hashed/library_capsule.jpg"] = true
	fake.storeFailure = http.StatusBadGateway

	first := runServiceOK(t, svc, profile)
	requireSyncResult(t, first, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	if got := fake.count("store", appID); got != 1 {
		t.Fatalf("store lookups during failing first sync = %d, want 1 attempt", got)
	}
	if _, ok, err := repo.GetCover(appID); err != nil || ok {
		t.Fatalf("cover row after failed store lookup: ok=%v err=%v, want no row (not checked)", ok, err)
	}

	fake.storeFailure = 0
	runServiceOK(t, svc, profile)
	cover, ok, err := repo.GetCover(appID)
	if err != nil || !ok {
		t.Fatalf("cover after recovered store lookup = %#v ok=%v err=%v, want stored library cover", cover, ok, err)
	}
	if cover.Kind != images.KindLibrary || cover.FileName != images.LibraryFileName || cover.DownloadedAt == nil {
		t.Fatalf("cover after recovered store lookup = %#v, want downloaded library cover", cover)
	}
}

func TestFullSyncKeepsCachedBaseCoverWhenCapsuleGoneAndStoreEmpty(t *testing.T) {
	const appID = 780
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: appID, Name: "Vanished Capsule Game", PlaytimeForever: 10, CapsuleFilename: "library_600x900.jpg"}})
	svc := serviceWithImages(t, repo, fake, paths)
	fake.images["780/library_600x900.jpg"] = true

	requireSyncResult(t, runServiceOK(t, svc, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	before, ok, err := repo.GetCover(appID)
	if err != nil || !ok || before.Kind != images.KindLibrary || before.DownloadedAt == nil {
		t.Fatalf("cover after first sync = %#v ok=%v err=%v, want downloaded library cover", before, ok, err)
	}

	// Age the stored check past CoverRefreshInterval, then remove the capsule from the CDN; the store lists nothing.
	setCoverCheckedAt(t, paths, appID, "2000-01-01T00:00:00Z")
	delete(fake.images, "780/library_600x900.jpg")

	requireSyncResult(t, runServiceOK(t, svc, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	if got := fake.count("store", appID); got != 1 {
		t.Fatalf("store lookups on stale sync = %d, want 1 (owned-games file gone)", got)
	}
	after, ok, err := repo.GetCover(appID)
	if err != nil || !ok {
		t.Fatalf("cover after stale sync = %#v ok=%v err=%v, want stored cover kept", after, ok, err)
	}
	if after.Kind != images.KindLibrary || after.FileName != images.LibraryFileName {
		t.Fatalf("cover after stale sync = %#v, want cached library cover kept", after)
	}
	if !after.Cached {
		t.Fatalf("cover after stale sync = %#v, want cached flag kept", after)
	}
	coverFile := filepath.Join(paths.GameImages, fmt.Sprintf("%d", appID), after.FileName)
	if _, err := os.Stat(coverFile); err != nil {
		t.Fatalf("cached cover file %q: %v, want file kept on disk", coverFile, err)
	}
}

// setCoverCheckedAt rewrites the stored checked_at of one cover so the next sync treats it as stale.
func setCoverCheckedAt(t *testing.T, paths appdata.Paths, imageAppID int64, checkedAt string) {
	t.Helper()
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", paths.DB))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`update cover_images set checked_at=? where image_appid=?`, checkedAt, imageAppID); err != nil {
		t.Fatal(err)
	}
}

// setupBaseCoverSync returns a service for one owned base game (no achievements) whose GetOwnedGames entry
// lists capsuleFilename. Callers add CDN files and store fixtures before syncing.
func setupBaseCoverSync(t *testing.T, appID int64, capsuleFilename string) (*repository.Repository, *fakeSteamServer, *Service, *repository.Profile) {
	t.Helper()
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: appID, Name: "Base Cover Game", PlaytimeForever: 10, CapsuleFilename: capsuleFilename}})
	return repo, fake, serviceWithImages(t, repo, fake, paths), profile
}

func TestFullSyncBaseCoverFromCapsuleFilenameWithoutStoreLookup(t *testing.T) {
	const appID = 730
	repo, fake, svc, profile := setupBaseCoverSync(t, appID, "library_600x900.jpg")
	fake.images["730/library_600x900.jpg"] = true

	requireSyncResult(t, runServiceOK(t, svc, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	cover, ok, err := repo.GetCover(appID)
	if err != nil || !ok {
		t.Fatalf("cover = %#v ok=%v err=%v, want stored library cover", cover, ok, err)
	}
	if cover.Kind != images.KindLibrary || cover.FileName != images.LibraryFileName || cover.DownloadedAt == nil {
		t.Fatalf("cover = %#v, want downloaded library cover from capsule_filename", cover)
	}
	if got := fake.count("store", appID); got != 0 {
		t.Fatalf("store lookups for base game with capsule_filename = %d, want 0", got)
	}
	if got := fake.count("asset", appID); got < 1 {
		t.Fatalf("cover CDN requests for base game = %d, want at least 1 download request", got)
	}
}

func TestFullSyncBaseCoverFromHashedCapsuleFilename(t *testing.T) {
	const appID = 731
	repo, fake, svc, profile := setupBaseCoverSync(t, appID, "abc/library_capsule.jpg")
	fake.images["731/abc/library_capsule.jpg"] = true

	requireSyncResult(t, runServiceOK(t, svc, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	cover, ok, err := repo.GetCover(appID)
	if err != nil || !ok {
		t.Fatalf("cover = %#v ok=%v err=%v, want stored library cover", cover, ok, err)
	}
	if cover.Kind != images.KindLibrary || cover.FileName != images.LibraryFileName || cover.DownloadedAt == nil {
		t.Fatalf("cover = %#v, want library cover saved as %s", cover, images.LibraryFileName)
	}
	if got := fake.count("store", appID); got != 0 {
		t.Fatalf("store lookups = %d, want 0", got)
	}
}

func TestFullSyncBaseCoverFallsBackToStoreWhenCapsuleFileMissing(t *testing.T) {
	const appID = 740
	repo, fake, svc, profile := setupBaseCoverSync(t, appID, "library_600x900.jpg")
	// The owned-games file is not served (404); the store lists a hero instead.
	fake.store[appID] = storeFixture{hero: "hashed/hero_capsule.jpg"}
	fake.images["740/hashed/hero_capsule.jpg"] = true

	requireSyncResult(t, runServiceOK(t, svc, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	cover, ok, err := repo.GetCover(appID)
	if err != nil || !ok {
		t.Fatalf("cover = %#v ok=%v err=%v, want stored hero cover", cover, ok, err)
	}
	if cover.Kind != images.KindHero || cover.FileName != images.HeroFileName || cover.DownloadedAt == nil {
		t.Fatalf("cover = %#v, want downloaded hero cover from store", cover)
	}
	if got := fake.count("store", appID); got != 1 {
		t.Fatalf("store lookups = %d, want 1", got)
	}
}

func TestFullSyncBaseCoverMissingWhenCapsuleFileAndStoreHaveNothing(t *testing.T) {
	const appID = 750
	repo, fake, svc, profile := setupBaseCoverSync(t, appID, "portrait.png")
	// Neither the owned-games file nor the store has an image (store answers success=2).

	requireSyncResult(t, runServiceOK(t, svc, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	cover, ok, err := repo.GetCover(appID)
	if err != nil || !ok {
		t.Fatalf("cover = %#v ok=%v err=%v, want stored missing cover", cover, ok, err)
	}
	if cover.Kind != "" || cover.CheckedAt == nil {
		t.Fatalf("cover = %#v, want Kind \"\" with CheckedAt set", cover)
	}
	if got := fake.count("store", appID); got != 1 {
		t.Fatalf("store lookups = %d, want 1", got)
	}
	// The owned-games name is tried on the primary and the fallback CDN before giving up.
	if got := fake.count("asset", appID); got < 2 {
		t.Fatalf("cover CDN requests = %d, want at least 2 (primary and mirror)", got)
	}
}

func TestFullSyncBaseCoverUsesStoreWhenCapsuleFilenameEmpty(t *testing.T) {
	const appID = 760
	repo, fake, svc, profile := setupBaseCoverSync(t, appID, "")
	fake.store[appID] = storeFixture{library: "library_600x900.jpg"}
	fake.images["760/library_600x900.jpg"] = true

	requireSyncResult(t, runServiceOK(t, svc, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	cover, ok, err := repo.GetCover(appID)
	if err != nil || !ok {
		t.Fatalf("cover = %#v ok=%v err=%v, want stored library cover", cover, ok, err)
	}
	if cover.Kind != images.KindLibrary || cover.DownloadedAt == nil {
		t.Fatalf("cover = %#v, want downloaded library cover from store", cover)
	}
	if got := fake.count("store", appID); got != 1 {
		t.Fatalf("store lookups = %d, want 1 when capsule_filename is empty", got)
	}
}

func TestFullSyncBaseCoverFreshSecondSyncSkipsCDNAndStore(t *testing.T) {
	const appID = 770
	repo, fake, svc, profile := setupBaseCoverSync(t, appID, "library_600x900.jpg")
	fake.images["770/library_600x900.jpg"] = true

	runServiceOK(t, svc, profile)
	if _, ok, err := repo.GetCover(appID); err != nil || !ok {
		t.Fatalf("cover after first sync: ok=%v err=%v, want stored cover", ok, err)
	}
	assetsBefore := fake.count("asset", appID)

	requireSyncResult(t, runServiceOK(t, svc, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	if got := fake.count("asset", appID); got != assetsBefore {
		t.Fatalf("cover CDN requests on fresh second sync = %d, want %d (no new request)", got, assetsBefore)
	}
	if got := fake.count("store", appID); got != 0 {
		t.Fatalf("store lookups on fresh second sync = %d, want 0", got)
	}
}

func TestFullSyncSavesMissingCoverWhenStoreLookupSucceedsWithoutApp(t *testing.T) {
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 721, Name: "Unlisted Game", PlaytimeForever: 10}})
	fake.gameBody[721] = gameJSON([]groupFixture{{id: 0}}, []achFixture{{name: "BASE", key: 1, group: 0}})
	fake.userBody[721] = userJSON(nil, []int64{0})
	// No store fixture: Steam answers success=2 for app 721, so the lookup succeeds without listing it.

	result := runServiceOK(t, serviceWithImages(t, repo, fake, paths), profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	cover, ok, err := repo.GetCover(721)
	if err != nil || !ok {
		t.Fatalf("cover = %#v ok=%v err=%v, want stored missing cover", cover, ok, err)
	}
	if cover.Kind != "" || cover.CheckedAt == nil {
		t.Fatalf("cover = %#v, want Kind \"\" with CheckedAt set", cover)
	}
	if got := fake.count("asset", 721); got != 0 {
		t.Fatalf("cover asset requests = %d, want 0 for unlisted app", got)
	}
}

// A synced cover is recorded as a cached flag, a file name and a Steam URL. After a real sync writes its file under
// the game images directory, no text column of any user table may hold a filesystem path, so the database never
// carries the OS user name from the cache.
func TestFullSyncCoverWritesNoFilesystemPath(t *testing.T) {
	const appID = 732
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: appID, Name: "Cover Path Game", PlaytimeForever: 10, CapsuleFilename: "library_600x900.jpg"}})
	svc := serviceWithImages(t, repo, fake, paths)
	fake.images[fmt.Sprintf("%d/library_600x900.jpg", appID)] = true

	requireSyncResult(t, runServiceOK(t, svc, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	coverFile := filepath.Join(paths.GameImages, strconv.FormatInt(appID, 10), images.LibraryFileName)
	if _, err := os.Stat(coverFile); err != nil {
		t.Fatalf("sync did not write the cover file under the game images directory: %v", err)
	}

	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", paths.DB))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var cached int
	if err := db.QueryRow(`select cached from cover_images where image_appid=?`, appID).Scan(&cached); err != nil || cached != 1 {
		t.Fatalf("cover_images.cached = %d, err=%v, want 1 for the file on disk", cached, err)
	}
	// The temp root is paths.DataDir in testPaths; the cache root and the game images directory sit under it. A
	// path in any of them, if stored, would also match DataDir.
	for _, forbidden := range []string{paths.DataDir, paths.CacheDir, paths.GameImages} {
		requireNoTextContains(t, db, forbidden)
	}
}

// requireNoTextContains fails when a text value of any user table in db contains text.
func requireNoTextContains(t *testing.T, db *sql.DB, text string) {
	t.Helper()
	tables, err := db.Query(`select name from sqlite_master where type='table' and name not like 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := tables.Err(); err != nil {
		t.Fatal(err)
	}
	if err := tables.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		rows, err := db.Query(`select * from "` + name + `"`)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for i, v := range vals {
				var value string
				switch x := v.(type) {
				case string:
					value = x
				case []byte:
					value = string(x)
				}
				if strings.Contains(value, text) {
					t.Fatalf("%s.%s holds %q", name, cols[i], value)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
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
			{InternalName: "A", Key: 1, GlobalPercent: new(float64(50))},
			{InternalName: "B", Key: 2, GlobalPercent: new(float64(10))},
			{InternalName: "C", Key: 3},
			{InternalName: "D", Key: 4, GlobalPercent: new(float64(90)), Archived: true},
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
	// Completion is 1 of 3 countable achievements. C has no percent and D is archived, so the average is B's 10 alone.
	// float64(1)/3*100 is one ulp away from the constant 100.0/3, so compare with a tolerance.
	if math.Abs(stats.completion-100.0/3) > 1e-9 {
		t.Fatalf("completion = %v, want 100/3", stats.completion)
	}
	if stats.missingAvg == nil || *stats.missingAvg != 10 {
		t.Fatalf("missing average = %v, want 10", stats.missingAvg)
	}
	// Weights are 0.5 completion and 0.5 missing average: 100/6 + 5 = 65/3.
	if math.Abs(stats.score-65.0/3) > 1e-9 {
		t.Fatalf("score = %v, want 65/3", stats.score)
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

// The purchased flag from GetOwnedGames's second list is stored per game; played-free games stay unpurchased.
func TestFullSyncStoresPurchasedFlagFromOwnedGames(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{
		{AppID: 60, Name: "Bought", PlaytimeForever: 3, Purchased: true},
		{AppID: 70, Name: "Played Free", PlaytimeForever: 4},
	})

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(context.Background(), *profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.GamesTotal != 2 {
		t.Fatalf("result = %#v, want success for both games", result)
	}
	if got := fake.count("purchased", 0); got != 1 {
		t.Fatalf("purchased list calls = %d, want 1", got)
	}
	summary, err := repo.Summary(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.OwnedGamesCount != 2 || summary.PurchasedGamesCount != 1 {
		t.Fatalf("owned = %d purchased = %d, want 2 and 1", summary.OwnedGamesCount, summary.PurchasedGamesCount)
	}
}

// The unvetted flag from GetOwnedGames's vetted list is stored per profile game: an unvetted app is stored vetted=0.
func TestFullSyncStoresVettedFlagFromOwnedGames(t *testing.T) {
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{
		{AppID: 60, Name: "Vetted", PlaytimeForever: 3},
		{AppID: 70, Name: "Unvetted", PlaytimeForever: 4, Unvetted: true},
	})

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(context.Background(), *profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.GamesTotal != 2 {
		t.Fatalf("result = %#v, want success for both games", result)
	}
	if got := fake.count("vetted", 0); got != 1 {
		t.Fatalf("vetted list calls = %d, want 1", got)
	}
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", paths.DB))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for appID, want := range map[int64]int{60: 1, 70: 0} {
		var got int
		if err := db.QueryRow(`select vetted from profile_owned_games where profile_id=? and appid=?`, profile.ID, appID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("app %d vetted = %d, want %d", appID, got, want)
		}
	}
}

// A resync reads the vetted list again: an app that was unvetted and is now vetted gets vetted=1, and an app that
// became unvetted gets vetted=0.
func TestFullSyncResyncUpdatesVettedFlag(t *testing.T) {
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	profile := testProfile(t, repo)
	owned := []steam.OwnedGame{
		{AppID: 80, Name: "Becomes Vetted", PlaytimeForever: 3, Unvetted: true},
		{AppID: 81, Name: "Becomes Unvetted", PlaytimeForever: 4},
	}
	fake := newFakeSteamServer(t, owned)
	svc := &Service{Repo: repo, Steam: fake.client}

	if _, err := svc.FullSync(context.Background(), *profile); err != nil {
		t.Fatal(err)
	}
	assertVettedFlags(t, paths, profile.ID, map[int64]int{80: 0, 81: 1})

	// The fake reads owned at request time, so changing its elements changes the next library and vetted lists.
	owned[0].Unvetted = false
	owned[1].Unvetted = true
	result, err := svc.FullSync(context.Background(), *profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.GamesTotal != 2 {
		t.Fatalf("resync result = %#v, want success for both games", result)
	}
	assertVettedFlags(t, paths, profile.ID, map[int64]int{80: 1, 81: 0})
}

func assertVettedFlags(t *testing.T, paths appdata.Paths, profileID int64, want map[int64]int) {
	t.Helper()
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", paths.DB))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for appID, wantVetted := range want {
		var got int
		if err := db.QueryRow(`select vetted from profile_owned_games where profile_id=? and appid=?`, profileID, appID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != wantVetted {
			t.Fatalf("app %d vetted = %d, want %d", appID, got, wantVetted)
		}
	}
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
	server       *httptest.Server
	client       *steam.Client
	schema       map[int64][]string
	player       map[int64]map[string]bool
	global       map[int64]map[string]float64
	playerError  map[int64]string // GetUserAchievements (and legacy player) answers 403/error with this text
	gameFailure  map[int64]gameFailure
	gameBody     map[int64]string // raw GetGameAchievements body override
	userBody     map[int64]string // raw GetUserAchievements body override
	hangupPath   map[string]bool  // closes the TCP connection without a response for this URL path
	hangupGame   map[int64]bool   // closes the connection for GetGameAchievements of this app
	gameStatuses map[int64][]int  // statuses answered to the next GetGameAchievements requests of a game, before gameFailure
	ownedStatus  int              // GetOwnedGames answers this HTTP status when non-zero
	ownedBody    string           // raw GetOwnedGames body override
	store        map[int64]storeFixture
	storeFailure int             // GetItems answers this HTTP status when non-zero (use a non-retried status)
	images       map[string]bool // served store_item_assets paths relative to steam/apps/
	delay        time.Duration
	hold         *handshake // when set, GetGameAchievements requests block at the server until released
	mu           sync.Mutex
	counts       map[string]int
	active       int
	maxActive    int
}

// handshake lets a test see requests reach the fake server and hold them there until open is called.
type handshake struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *handshake) open() {
	h.once.Do(func() { close(h.release) })
}

// awaitStarted waits until n requests have reached the server. It fails the test instead of hanging.
func (h *handshake) awaitStarted(t *testing.T, n int) {
	t.Helper()
	for i := range n {
		select {
		case <-h.started:
		case <-time.After(10 * time.Second):
			t.Fatalf("%d of %d requests reached the server", i, n)
		}
	}
}

// holdGameRequests makes every GetGameAchievements request block at the server until the handshake is opened.
// The release happens at test cleanup too, so the server can close.
func (f *fakeSteamServer) holdGameRequests(t *testing.T) *handshake {
	t.Helper()
	h := &handshake{started: make(chan struct{}, 64), release: make(chan struct{})}
	f.hold = h
	t.Cleanup(h.open)
	return h
}

// nextGameStatus pops the next queued status for a game, if any.
func (f *fakeSteamServer) nextGameStatus(appID int64) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	queue := f.gameStatuses[appID]
	if len(queue) == 0 {
		return 0, false
	}
	f.gameStatuses[appID] = queue[1:]
	return queue[0], true
}

func newFakeSteamServer(t *testing.T, owned []steam.OwnedGame) *fakeSteamServer {
	t.Helper()
	f := &fakeSteamServer{
		schema:       make(map[int64][]string),
		player:       make(map[int64]map[string]bool),
		global:       make(map[int64]map[string]float64),
		playerError:  make(map[int64]string),
		gameFailure:  make(map[int64]gameFailure),
		gameStatuses: make(map[int64][]int),
		gameBody:     make(map[int64]string),
		userBody:     make(map[int64]string),
		hangupPath:   make(map[string]bool),
		hangupGame:   make(map[int64]bool),
		store:        make(map[int64]storeFixture),
		images:       make(map[string]bool),
		counts:       make(map[string]int),
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.hangupPath[r.URL.Path] {
			hangUp(t, w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/IPlayerService/GetOwnedGames/v0001/":
			// GetOwnedGames makes up to three requests: the full list ("owned" count), the purchased list
			// (include_played_free_games=false, counted as "purchased"; Purchased decides it) and the vetted list
			// (skip_unvetted_apps=true, counted as "vetted"; Unvetted decides it).
			if r.URL.Query().Get("skip_unvetted_apps") == "true" {
				f.increment("vetted", 0)
				if f.ownedStatus != 0 {
					http.Error(w, "owned games unavailable", f.ownedStatus)
					return
				}
				fmt.Fprint(w, vettedGamesJSON(owned, r.URL.Query().Get("include_played_free_games") != "false"))
				return
			}
			if r.URL.Query().Get("include_played_free_games") == "false" {
				f.increment("purchased", 0)
				if f.ownedStatus != 0 {
					http.Error(w, "owned games unavailable", f.ownedStatus)
					return
				}
				fmt.Fprint(w, purchasedGamesJSON(owned))
				return
			}
			f.increment("owned", 0)
			if f.ownedStatus != 0 {
				http.Error(w, "owned games unavailable", f.ownedStatus)
				return
			}
			if f.ownedBody != "" {
				fmt.Fprint(w, f.ownedBody)
				return
			}
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
			if strings.HasPrefix(r.URL.Path, "/store_item_assets/") {
				f.increment("asset", assetAppID(r.URL.Path))
			}
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
	// Retries run at once, so retryable statuses and transport errors do not sleep.
	f.client.SetRetryBackoff(func(int) time.Duration { return 0 })
	return f
}

// hangUp closes the TCP connection without writing a response, so the client sees a transport error.
func hangUp(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		t.Error("response writer cannot hijack connection")
		return
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	_ = conn.Close()
}

func (f *fakeSteamServer) serveGameAchievements(t *testing.T, w http.ResponseWriter, r *http.Request) {
	appID := int64Query(t, r, "appid")
	if f.hangupGame[appID] {
		hangUp(t, w)
		return
	}
	done := f.enterPerGameRequest()
	defer done()
	f.increment("game", appID)
	if f.hold != nil {
		f.hold.started <- struct{}{}
		<-f.hold.release
	}
	if status, ok := f.nextGameStatus(appID); ok {
		http.Error(w, "transient", status)
		return
	}
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
	for _, id := range input.IDs {
		f.increment("store", id.AppID) // counted even when the request fails
	}
	if f.storeFailure != 0 {
		http.Error(w, "store unavailable", f.storeFailure)
		return
	}
	items := make([]string, 0, len(input.IDs))
	for _, id := range input.IDs {
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

// assetAppID returns the app id of a store_item_assets path (steam/apps/<id>/...), or 0 when it has none.
func assetAppID(path string) int64 {
	rel, ok := strings.CutPrefix(path, "/store_item_assets/steam/apps/")
	if !ok {
		return 0
	}
	id, _, _ := strings.Cut(rel, "/")
	n, _ := strconv.ParseInt(id, 10, 64)
	return n
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
		entry := fmt.Sprintf(`{"appid":%d,"name":%q,"playtime_forever":%d`, g.AppID, g.Name, g.PlaytimeForever)
		if g.CapsuleFilename != "" {
			entry += fmt.Sprintf(`,"capsule_filename":%q`, g.CapsuleFilename)
		}
		parts = append(parts, entry+"}")
	}
	return fmt.Sprintf(`{"response":{"game_count":%d,"games":[%s]}}`, len(games), strings.Join(parts, ","))
}

// purchasedGamesJSON is the purchased list: only games flagged Purchased, without the played-free ones.
// It is always well formed: game_count and games are both present, even when no game is purchased, because
// the client rejects a bare `{"response":{}}` purchased answer.
func purchasedGamesJSON(games []steam.OwnedGame) string {
	var purchased []steam.OwnedGame
	for _, g := range games {
		if g.Purchased {
			purchased = append(purchased, g)
		}
	}
	return ownedGamesJSON(purchased)
}

// vettedGamesJSON is the vetted list: every game not flagged Unvetted. With includePlayedFree false it also drops
// the played-free games, like the purchased list does.
func vettedGamesJSON(games []steam.OwnedGame, includePlayedFree bool) string {
	var vetted []steam.OwnedGame
	for _, g := range games {
		if !g.Unvetted && (includePlayedFree || g.Purchased) {
			vetted = append(vetted, g)
		}
	}
	return ownedGamesJSON(vetted)
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
		DataDir:    root,
		CacheDir:   filepath.Join(root, "cache"),
		StateDir:   root,
		DB:         filepath.Join(root, "test.db"),
		Backups:    filepath.Join(root, "backups"),
		GameImages: filepath.Join(root, "cache", "images", "games"),
		Logs:       filepath.Join(root, "logs"),
		LogFile:    filepath.Join(root, "logs", "app.log"),
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
	for i := range count {
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
