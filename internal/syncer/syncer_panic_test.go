package syncer

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/repository"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
)

// genericFailureText is the sentence stored for a recovered panic (steam.UserMessage's generic sentence).
const genericFailureText = "Something went wrong. See the log file for details."

// panicKey and panicSteamID are planted in every panic value. Logs must not carry either one.
const (
	panicKey     = "0123456789ABCDEF0123456789ABCDEF"
	panicSteamID = "76561198000000001"
)

// panicTransport panics on the requests that match panicOn. The panic happens in the goroutine that sends the request,
// as a bug in the request path would. Other requests go to next.
type panicTransport struct {
	next    http.RoundTripper
	panicOn func(*http.Request) bool
	value   any
}

func (p panicTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if p.panicOn(req) {
		panic(p.value)
	}
	return p.next.RoundTrip(req)
}

// panicOnRequests makes the fake client's requests that match panic with a value holding a key and a SteamID64.
func panicOnRequests(fake *fakeSteamServer, match func(*http.Request) bool) {
	value := fmt.Sprintf("boom while syncing with key %s for %s", panicKey, panicSteamID)
	fake.client.HTTPClient = &http.Client{Transport: panicTransport{next: http.DefaultTransport, panicOn: match, value: value}}
}

// achievementRequestFor matches GetGameAchievements requests of the listed apps.
func achievementRequestFor(appIDs ...string) func(*http.Request) bool {
	return func(r *http.Request) bool {
		return r.URL.Path == "/IPlayerService/GetGameAchievements/v1/" && slices.Contains(appIDs, r.URL.Query().Get("appid"))
	}
}

// requireNoPanicSecrets fails when the captured log carries the key or the SteamID64 planted in the panic value.
func requireNoPanicSecrets(t *testing.T, logged string) {
	t.Helper()
	if strings.Contains(strings.ToUpper(logged), panicKey) || strings.Contains(logged, panicSteamID) {
		t.Fatalf("log = %q carries a key or SteamID64 from the panic value", logged)
	}
}

// runFullSyncWithin runs FullSync and fails the test if it has not returned within d. A worker that died after a
// panic would leave dispatch blocked, so the bound turns that hang into a failure.
func runFullSyncWithin(t *testing.T, svc *Service, profile *repository.Profile, d time.Duration) (Result, error) {
	t.Helper()
	type outcome struct {
		result Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := svc.FullSync(context.Background(), *profile)
		done <- outcome{result: result, err: err}
	}()
	select {
	case out := <-done:
		return out.result, out.err
	case <-time.After(d):
		t.Fatalf("sync did not return within %s: a worker may have died after a panic", d)
		return Result{}, nil
	}
}

// A panic while syncing one game is that game's failure: it gets a warning with the generic sentence, the other
// games are synced, and the run ends partial.
func TestFullSyncPanicInOneGameWarnsAndSyncsTheRest(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{
		{AppID: 10, Name: "Good Game", PlaytimeForever: 120},
		{AppID: 20, Name: "Panicking Game", PlaytimeForever: 5},
		{AppID: 30, Name: "Other Game", PlaytimeForever: 7},
	})
	for _, appID := range []int64{10, 30} {
		fake.schema[appID] = []string{"ACH_ONE", "ACH_TWO"}
		fake.player[appID] = map[string]bool{"ACH_ONE": true, "ACH_TWO": false}
		fake.global[appID] = map[string]float64{"ACH_ONE": 80, "ACH_TWO": 30}
	}
	panicOnRequests(fake, achievementRequestFor("20"))
	logs := captureLog(t)

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 3, synced: 2, failed: 1, hasError: true})
	requireLatestSyncRun(t, repo, profile.ID, expectedSyncResult{status: "partial", total: 3, synced: 2, failed: 1, hasError: true})
	requireWarningCard(t, repo, profile.ID, 20, genericFailureText)
	requireGoodGameState(t, repo, profile.ID, 10)
	if state := requireProfileGame(t, repo, profile.ID, 30, 0); !state.Exists || state.SyncWarning || state.TotalAchievements != 2 {
		t.Fatalf("game 30 state = %#v, want synced 2 achievements without warning", state)
	}
	if !strings.Contains(logs.String(), "appid 20: sync panic:") {
		t.Fatalf("log = %q, want the panic logged for game 20", logs.String())
	}
	requireNoPanicSecrets(t, logs.String())
}

// A panic in every game of a library larger than the worker count must not stop the workers: each panic is one
// failed game and the rest of the library still gets its turn. A dead worker would hang the sync (see the bound).
func TestFullSyncWorkersSurvivePanicsInMoreGamesThanWorkers(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	var owned []steam.OwnedGame
	var panicking []string
	for i := range 8 {
		appID := int64(101 + i)
		owned = append(owned, steam.OwnedGame{AppID: appID, Name: fmt.Sprintf("Game %d", appID), PlaytimeForever: 1})
		if i < 6 {
			panicking = append(panicking, fmt.Sprint(appID))
		}
	}
	fake := newFakeSteamServer(t, owned)
	for _, game := range owned[6:] {
		fake.schema[game.AppID] = []string{"ACH_ONE"}
		fake.player[game.AppID] = map[string]bool{"ACH_ONE": true}
		fake.global[game.AppID] = map[string]float64{"ACH_ONE": 50}
	}
	panicOnRequests(fake, achievementRequestFor(panicking...))
	logs := captureLog(t)

	result, err := runFullSyncWithin(t, &Service{Repo: repo, Steam: fake.client}, profile, 20*time.Second)
	if err != nil {
		t.Fatalf("err = %v, want nil: per-game panics do not fail the run", err)
	}
	requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 8, synced: 2, failed: 6, hasError: true})
	if got := strings.Count(logs.String(), "sync panic:"); got != 6 {
		t.Fatalf("panic log lines = %d, want 6", got)
	}
	requireNoPanicSecrets(t, logs.String())
}

// A panic outside any game (here, in GetOwnedGames) ends the run as failed. The run row is written as failed with the
// generic sentence, and the caller gets an error instead of a crash.
func TestFullSyncPanicOutsideGamesFailsRunAndStoresGenericSentence(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 10, Name: "Never Reached", PlaytimeForever: 1}})
	panicOnRequests(fake, func(r *http.Request) bool {
		return r.URL.Path == "/IPlayerService/GetOwnedGames/v0001/"
	})
	logs := captureLog(t)

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(context.Background(), *profile)
	if err == nil || !strings.Contains(err.Error(), "panic:") {
		t.Fatalf("err = %v, want the recovered panic as an error", err)
	}
	if result.Status != "failed" || result.Error == nil || *result.Error != genericFailureText {
		t.Fatalf("result = %#v, want failed with the generic sentence", result)
	}
	if result.GamesTotal != 0 || result.GamesSynced != 0 || result.GamesFailed != 0 {
		t.Fatalf("result = %#v, want no game counted", result)
	}
	run := requireLatestRun(t, repo, profile.ID)
	if run.Status != "failed" || run.Error == nil || *run.Error != genericFailureText || run.FinishedAt == nil {
		t.Fatalf("sync run = %#v, want finished failed run with the generic sentence", run)
	}
	if got := fake.count("game", 10); got != 0 {
		t.Fatalf("achievement calls = %d, want 0 after the run failed", got)
	}
	if !strings.Contains(logs.String(), "sync run panic:") {
		t.Fatalf("log = %q, want the run panic logged", logs.String())
	}
	requireNoPanicSecrets(t, logs.String())
}

// A panic in the bookkeeping that a worker runs after a game (here an OnChange emitter that panics on one late event)
// must not crash the process. The event order is fixed: event 1 is the run start, events 2 to 11 are the ten progress
// writes made by the workers, and event 6 is one of them. The worker survives, the run finishes with every game
// counted, and all workers drain (the time bound catches a hang).
func TestFullSyncPanicInWorkerBookkeepingDoesNotCrashOrHang(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	var owned []steam.OwnedGame
	for i := range 10 {
		owned = append(owned, steam.OwnedGame{AppID: int64(201 + i), Name: fmt.Sprintf("Game %d", 201+i), PlaytimeForever: 1})
	}
	fake := newFakeSteamServer(t, owned)
	for _, game := range owned {
		syncedAchievementsFixture(fake, game.AppID)
	}
	logs := captureLog(t)

	var events atomic.Int64
	svc := &Service{Repo: repo, Steam: fake.client, ProgressInterval: -1, OnChange: func() {
		if events.Add(1) == 6 {
			panic("emitter failed on a late event")
		}
	}}
	result, err := runFullSyncWithin(t, svc, profile, 20*time.Second)
	if err != nil {
		t.Fatalf("err = %v, want nil: a bookkeeping panic is logged, not returned", err)
	}
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: 10, synced: 10, failed: 0})
	requireLatestSyncRun(t, repo, profile.ID, expectedSyncResult{status: "success", total: 10, synced: 10, failed: 0})
	if got := events.Load(); got != 12 {
		t.Fatalf("change events = %d, want 12 (start, 10 progress writes, finish)", got)
	}
	if !strings.Contains(logs.String(), "worker panic:") {
		t.Fatalf("log = %q, want the worker panic logged", logs.String())
	}
	requireNoPanicSecrets(t, logs.String())
}
