package syncer

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/steam"
	"github.com/gjrud/steam-achievement-tracker/internal/usererr"
)

// A GetOwnedGames failure ends the run before any game is touched: the run is saved as failed with the error,
// and the stored games, their flags and the warning state stay as they were.
func TestFullSyncOwnedGamesFailureFailsRunAndKeepsExistingData(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantCalls int
		wantText  string // raw text of the returned and logged error
		wantUser  string // user-safe text stored on the result and the sync run
	}{
		{name: "private profile", body: `{"response":{}}`, wantCalls: 1, wantText: "game details may be private", wantUser: "Steam did not return your game list. Your profile's game details may be private."},
		{name: "http 404 not retried", status: http.StatusNotFound, wantCalls: 1, wantText: "returned 404 Not Found", wantUser: "Steam has no data for this request."},
		{name: "http 429 exhausted", status: http.StatusTooManyRequests, wantCalls: 4, wantText: "returned 429 Too Many Requests", wantUser: "Steam is limiting requests right now. Try again in a few minutes."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepository(t)
			profile := testProfile(t, repo)
			seedGameSync(t, repo, profile.ID, 50)
			fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 60, Name: "Would Sync", PlaytimeForever: 3}})
			fake.ownedStatus = tc.status
			fake.ownedBody = tc.body

			result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(context.Background(), *profile)
			// FullSync returns the raw error; the app boundary shows only the user-safe sentence. The stored result is the sentence.
			if err == nil || !strings.Contains(usererr.Raw(err).Error(), tc.wantText) {
				t.Fatalf("err = %v, want raw cause %q", err, tc.wantText)
			}
			if result.Status != "failed" || result.Error == nil || *result.Error != tc.wantUser {
				t.Fatalf("result = %#v, want failed with user text %q", result, tc.wantUser)
			}
			if result.GamesTotal != 0 || result.GamesSynced != 0 || result.GamesFailed != 0 {
				t.Fatalf("result = %#v, want no game counted", result)
			}
			if got := fake.count("owned", 0); got != tc.wantCalls {
				t.Fatalf("owned games calls = %d, want %d", got, tc.wantCalls)
			}
			run := requireLatestRun(t, repo, profile.ID)
			if run.Status != "failed" || run.Error == nil || *run.Error != tc.wantUser {
				t.Fatalf("sync run = %#v, want persisted failed run with user text %q", run, tc.wantUser)
			}
			if strings.Contains(*run.Error, "returned") || strings.Contains(*run.Error, "/") {
				t.Fatalf("sync run error = %q, want no endpoint or status text", *run.Error)
			}
			if got := fake.count("game", 60); got != 0 {
				t.Fatalf("achievement calls for listed game = %d, want 0", got)
			}
			state := requireProfileGame(t, repo, profile.ID, 50, 0)
			if !state.Exists || !state.IsCompleted || state.TotalAchievements != 1 || state.Disabled || state.SyncWarning {
				t.Fatalf("existing game state = %#v, want untouched completed entry", state)
			}
			requireNoWarningCards(t, repo, profile.ID)
			if disabled := requireGameCards(t, repo, profile.ID, "disabled"); len(disabled) != 0 {
				t.Fatalf("disabled cards = %#v, want none (not-owned flags not written)", disabled)
			}
		})
	}
}

// Retryable statuses (429, 500, 503) that stay after the client's four attempts are a game failure, not a legacy
// fallback: the game gets a warning, its stored data is kept, and the run is partial.
func TestFullSyncRetryableGameStatusExhaustedIsPartialWithoutLegacyFallback(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			repo := testRepository(t)
			profile := testProfile(t, repo)
			fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 20, Name: "Busy Game", PlaytimeForever: 5}})
			fake.schema[20] = []string{"ACH_ONE"}
			fake.player[20] = map[string]bool{"ACH_ONE": true}
			fake.global[20] = map[string]float64{"ACH_ONE": 50}
			fake.gameFailure[20] = gameFailure{status: code}

			result := runFullSyncOK(t, repo, fake, profile)
			requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})
			wantWarning := map[int]string{
				http.StatusTooManyRequests:     "Steam is limiting requests right now. Try again in a few minutes.",
				http.StatusInternalServerError: "Steam is temporarily unavailable. Try again later.",
				http.StatusServiceUnavailable:  "Steam is temporarily unavailable. Try again later.",
			}[code]
			requireWarningCard(t, repo, profile.ID, 20, wantWarning)
			if got := fake.count("game", 20); got != 4 {
				t.Fatalf("achievement schema attempts = %d, want 4 (client retries exhausted)", got)
			}
			for _, kind := range []string{"schema", "player", "global"} {
				if got := fake.count(kind, 20); got != 0 {
					t.Fatalf("legacy %s calls = %d, want 0 for a transient status", kind, got)
				}
			}
		})
	}
}

// A retryable status followed by a success is a clean sync: the client's retry delivers the schema, no warning is left.
func TestFullSyncRetryableGameStatusSucceedsAfterRetries(t *testing.T) {
	cases := []struct {
		name      string
		statuses  []int
		wantCalls int
	}{
		{name: "two transient answers then success", statuses: []int{http.StatusTooManyRequests, http.StatusServiceUnavailable}, wantCalls: 3},
		{name: "three transient answers then success", statuses: []int{http.StatusInternalServerError, http.StatusInternalServerError, http.StatusInternalServerError}, wantCalls: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := testRepository(t)
			profile := testProfile(t, repo)
			fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 20, Name: "Flaky Game", PlaytimeForever: 5}})
			fake.schema[20] = []string{"ACH_ONE", "ACH_TWO"}
			fake.player[20] = map[string]bool{"ACH_ONE": true, "ACH_TWO": false}
			fake.global[20] = map[string]float64{"ACH_ONE": 50, "ACH_TWO": 20}
			fake.gameStatuses[20] = tc.statuses

			result := runFullSyncOK(t, repo, fake, profile)
			requireSyncResult(t, result, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
			if got := fake.count("game", 20); got != tc.wantCalls {
				t.Fatalf("achievement schema attempts = %d, want %d", got, tc.wantCalls)
			}
			if got := fake.count("schema", 20); got != 0 {
				t.Fatalf("legacy schema calls = %d, want 0", got)
			}
			state := requireProfileGame(t, repo, profile.ID, 20, 0)
			if !state.Exists || state.SyncWarning || state.TotalAchievements != 2 || state.UnlockedAchievements != 1 {
				t.Fatalf("state = %#v, want 1/2 with no warning", state)
			}
			requireNoWarningCards(t, repo, profile.ID)
		})
	}
}
