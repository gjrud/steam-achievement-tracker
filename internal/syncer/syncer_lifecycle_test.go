package syncer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
)

// emptySchemaText is the sentence an empty schema for a game with stored data gets (steam.ErrNoAchievementsReturned).
const emptySchemaText = "Steam did not return achievements for this game. Try again later."

// syncedAchievementsFixture is a two-achievement schema with one unlock, served through the fake for appID.
func syncedAchievementsFixture(fake *fakeSteamServer, appID int64) {
	fake.schema[appID] = []string{"ACH_ONE", "ACH_TWO"}
	fake.player[appID] = map[string]bool{"ACH_ONE": true, "ACH_TWO": false}
	fake.global[appID] = map[string]float64{"ACH_ONE": 80, "ACH_TWO": 30}
}

// A populated game whose next schema is empty keeps its stored entries. The game gets the warning, and the run ends
// partial, not success: the empty answer is not taken as "no achievements".
func TestFullSyncEmptySchemaAfterPopulatedGameKeepsDataAndWarns(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 10, Name: "Populated Game", PlaytimeForever: 120}})
	syncedAchievementsFixture(fake, 10)
	requireSyncResult(t, runFullSyncOK(t, repo, fake, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})

	fake.schema[10] = []string{}
	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})
	requireWarningCard(t, repo, profile.ID, 10, emptySchemaText)
	state := requireProfileGame(t, repo, profile.ID, 10, 0)
	if !state.Exists || !state.SyncWarning || state.TotalAchievements != 2 || state.UnlockedAchievements != 1 {
		t.Fatalf("state = %#v, want stored 1/2 kept with a warning", state)
	}
	if cards := cardsForApp(allCards(t, repo, profile.ID), 10); len(cards) != 1 || cards[0].AchievementStatus != "ok" {
		t.Fatalf("cards = %#v, want the stored entry with its status unchanged", cards)
	}
}

// The next successful sync clears the warning, so a transient empty answer costs one partial run and nothing more.
func TestFullSyncEmptySchemaWarningClearsWhenSchemaReturns(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 10, Name: "Populated Game", PlaytimeForever: 120}})
	syncedAchievementsFixture(fake, 10)
	runFullSyncOK(t, repo, fake, profile)

	fake.schema[10] = []string{}
	requireSyncResult(t, runFullSyncOK(t, repo, fake, profile), expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})

	syncedAchievementsFixture(fake, 10)
	requireSyncResult(t, runFullSyncOK(t, repo, fake, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	requireNoWarningCards(t, repo, profile.ID)
}

// Stored data can be DLC or extra sets only (groupid<>0), with the game row not marked as having achievements. That
// counts as stored data too: the empty schema must not prune the extra set.
func TestFullSyncEmptySchemaKeepsDLCSetsWhenOnlyExtraSetsAreStored(t *testing.T) {
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 30, Name: "DLC Only Game", PlaytimeForever: 9}})
	fake.gameBody[30] = gameJSON(
		[]groupFixture{{id: 0, hidden: true}, {id: 1, name: "Expansion", dlc: 31}},
		[]achFixture{{name: "EXP_ONE", key: 1, group: 1}, {name: "EXP_TWO", key: 2, group: 1}},
	)
	fake.userBody[30] = userJSON([]int64{1}, []int64{1})
	requireSyncResult(t, runFullSyncOK(t, repo, fake, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	if state := requireProfileGame(t, repo, profile.ID, 30, 1); !state.Exists {
		t.Fatalf("expansion state = %#v, want stored after the first sync", state)
	}
	// Clear the game-level marker, so only the extra set holds the stored data.
	setGameHasAchievements(t, paths, 30, 0)

	fake.gameBody[30] = gameJSON(nil, nil)
	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})
	requireWarningCard(t, repo, profile.ID, 30, emptySchemaText)
	if state := requireProfileGame(t, repo, profile.ID, 30, 1); !state.Exists || state.TotalAchievements != 2 {
		t.Fatalf("expansion state = %#v, want kept with 2 achievements", state)
	}
}

// On the legacy path an empty schema is guarded the same way: the game keeps its stored base entry and gets the
// warning.
func TestFullSyncEmptyLegacySchemaAfterPopulatedGameKeepsDataAndWarns(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 40, Name: "Legacy Game", PlaytimeForever: 3}})
	syncedAchievementsFixture(fake, 40)
	requireSyncResult(t, runFullSyncOK(t, repo, fake, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})

	// A non-transient failure of the new API takes the legacy path (no set data, so no refusal), whose schema is empty.
	fake.gameFailure[40] = gameFailure{status: http.StatusNotFound}
	fake.schema[40] = []string{}
	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})
	if got := fake.count("schema", 40); got != 1 {
		t.Fatalf("legacy schema calls = %d, want 1", got)
	}
	requireWarningCard(t, repo, profile.ID, 40, emptySchemaText)
	if state := requireProfileGame(t, repo, profile.ID, 40, 0); !state.Exists || state.TotalAchievements != 2 || state.UnlockedAchievements != 1 {
		t.Fatalf("state = %#v, want stored 1/2 kept", state)
	}
}

// A first sync of a game with an empty schema still stores it as no_achievements: there is no data to protect.
func TestFullSyncEmptySchemaWithoutStoredDataStillStoresNoAchievements(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 50, Name: "Fresh Game", PlaytimeForever: 0}})
	fake.schema[50] = []string{}
	requireSyncResult(t, runFullSyncOK(t, repo, fake, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	requireNoWarningCards(t, repo, profile.ID)
	if state := requireProfileGame(t, repo, profile.ID, 50, 0); !state.Exists || state.TotalAchievements != 0 {
		t.Fatalf("state = %#v, want a no-achievements entry", state)
	}
}

// Regression: a 403 for a game is that game's warning, and the run ends partial, never failed. With stored set data the
// refusal does not reach the legacy API.
func TestFullSyncForbiddenGameIsPerGameWarningNeverFailedRun(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{
		{AppID: 50, Name: "Refused Game", PlaytimeForever: 3},
		{AppID: 60, Name: "Synced Game", PlaytimeForever: 4},
	})
	fake.gameBody[50] = gameJSON(
		[]groupFixture{{id: 0}, {id: 1, name: "Extra"}},
		[]achFixture{{name: "A", key: 1, group: 0}, {name: "B", key: 2, group: 1}},
	)
	fake.userBody[50] = userJSON([]int64{1, 2}, []int64{0, 1})
	fake.schema[60] = []string{"ACH_ONE"}
	fake.player[60] = map[string]bool{"ACH_ONE": true}
	fake.global[60] = map[string]float64{"ACH_ONE": 50}
	requireSyncResult(t, runFullSyncOK(t, repo, fake, profile), expectedSyncResult{status: "success", total: 2, synced: 2, failed: 0})

	fake.gameFailure[50] = gameFailure{status: http.StatusForbidden}
	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 2, synced: 1, failed: 1, hasError: true})
	requireLatestSyncRun(t, repo, profile.ID, expectedSyncResult{status: "partial", total: 2, synced: 1, failed: 1, hasError: true})
	// The game has two entries (base and extra set), each carries the warning. Nothing shows raw Steam text.
	warnings := requireGameCards(t, repo, profile.ID, "warnings")
	if len(warnings) != 2 {
		t.Fatalf("warnings = %#v, want one per entry of game 50", warnings)
	}
	for _, card := range warnings {
		if card.AppID != 50 || card.LastError == nil || *card.LastError != "Steam refused this request." {
			t.Fatalf("warning card = %#v, want game 50 with the refusal sentence", card)
		}
	}
	if got := fake.count("schema", 50); got != 0 {
		t.Fatalf("legacy schema calls = %d, want 0 for app with set data", got)
	}
}

// A 403 on a game with no stored data takes the legacy path, which the profile refuses too. That is still a per-game
// warning with the legacy failure's sentence, and the run stays partial.
func TestFullSyncForbiddenWithoutStoredDataFallsBackAndWarnsPerGame(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 70, Name: "Private Game", PlaytimeForever: 3}})
	fake.schema[70] = []string{"ACH_ONE"}
	fake.playerError[70] = "profile is private"
	fake.gameFailure[70] = gameFailure{status: http.StatusForbidden}

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})
	requireWarningCard(t, repo, profile.ID, 70, "Steam did not provide achievements for this game.")
}

// ---- Canceled and deadline runs (A5) ----

// gateTransport holds the GetGameAchievements requests of the listed apps until their context ends. It reports each
// held request on arrived, so a test can cancel while those games are in flight. The client retries transport errors,
// so a held request can reach here again after cancel: the report is non-blocking for that reason. Other requests go to next.
type gateTransport struct {
	next    http.RoundTripper
	hold    map[string]bool
	arrived chan struct{}
}

func (g gateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path == "/IPlayerService/GetGameAchievements/v1/" && g.hold[req.URL.Query().Get("appid")] {
		select {
		case g.arrived <- struct{}{}:
		default:
		}
		<-req.Context().Done()
		return nil, req.Context().Err()
	}
	return g.next.RoundTrip(req)
}

// A user cancel with one game synced and three in flight ends the run canceled, keeps games_synced, and touches the
// profile because a game synced. The in-flight games are not failures.
func TestFullSyncCancelAfterOneGameSyncedEndsCanceledAndTouchesProfile(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{
		{AppID: 1, Name: "Quick Game", PlaytimeForever: 1},
		{AppID: 2, Name: "Held Game 2", PlaytimeForever: 1},
		{AppID: 3, Name: "Held Game 3", PlaytimeForever: 1},
		{AppID: 4, Name: "Held Game 4", PlaytimeForever: 1},
	})
	syncedAchievementsFixture(fake, 1)
	gate := gateTransport{next: http.DefaultTransport, hold: map[string]bool{"2": true, "3": true, "4": true}, arrived: make(chan struct{}, 3)}
	fake.client.HTTPClient = &http.Client{Transport: gate}
	if got := currentProfile(t, repo).LastSyncedAt; got != nil {
		t.Fatalf("profile last synced = %q before the sync, want unset", *got)
	}

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
	for range 3 {
		select {
		case <-gate.arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("held games did not reach the transport")
		}
	}
	waitFor(t, "game 1 counted as synced", func() bool {
		run, err := repo.LatestSyncRun(profile.ID)
		return err == nil && run != nil && run.GamesSynced == 1
	})
	cancel()
	var out outcome
	select {
	case out = <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("canceled sync did not return")
	}

	if !errors.Is(out.err, context.Canceled) {
		t.Fatalf("err = %v, want context canceled", out.err)
	}
	if out.result.Status != "canceled" || out.result.GamesTotal != 4 || out.result.GamesSynced != 1 || out.result.GamesFailed != 0 {
		t.Fatalf("result = %#v, want canceled with 1 synced of 4 and no failures", out.result)
	}
	run := requireLatestRun(t, repo, profile.ID)
	if run.Status != "canceled" || run.GamesSynced != 1 || run.GamesFailed != 0 || run.FinishedAt == nil {
		t.Fatalf("sync run = %#v, want finished canceled run with games_synced 1", run)
	}
	if run.Error == nil || *run.Error != "The request was canceled." {
		t.Fatalf("sync run error = %v, want the cancel sentence", run.Error)
	}
	if got := currentProfile(t, repo).LastSyncedAt; got == nil {
		t.Fatal("profile last synced = nil, want set: a game synced in the canceled run")
	}
	requireNoWarningCards(t, repo, profile.ID)
}

// A deadline is not a user cancel: the run ends failed with the network sentence, not canceled.
func TestFullSyncDeadlineEndsFailedNotCanceled(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 1, Name: "Never Reached", PlaytimeForever: 1}})
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(ctx, *profile)
	if err == nil {
		t.Fatal("err = nil, want the deadline error")
	}
	if result.Status != "failed" || result.Error == nil || *result.Error != unreachableSteamText {
		t.Fatalf("result = %#v, want failed with %q", result, unreachableSteamText)
	}
	run := requireLatestRun(t, repo, profile.ID)
	if run.Status != "failed" || run.Error == nil || *run.Error != unreachableSteamText {
		t.Fatalf("sync run = %#v, want failed, not canceled", run)
	}
}

// ---- helpers ----

// waitFor polls cond until it holds, failing the test after 10 seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// setGameHasAchievements sets the shared game row's has_achievements flag, through a second connection like addTriggers.
func setGameHasAchievements(t *testing.T, paths appdata.Paths, appID int64, value int) {
	t.Helper()
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", paths.DB))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`update games set has_achievements=? where appid=?`, value, appID); err != nil {
		t.Fatal(err)
	}
}
