package syncer

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	"github.com/gjrud/steam-achievement-tracker/internal/repository"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
)

// captureLog sends the standard logger to a buffer until the test ends. Read the buffer after FullSync returns.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// addTriggers runs SQL statements (triggers that raise) on the test database, so the sync's writes fail
// the way a broken database would. They run on a separate connection, after the fixtures are seeded.
func addTriggers(t *testing.T, paths appdata.Paths, statements ...string) {
	t.Helper()
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", paths.DB))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
}

// faultyRepository returns a repository on a fresh database, with its paths and one profile.
func faultyRepository(t *testing.T) (*repository.Repository, appdata.Paths, *repository.Profile) {
	t.Helper()
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	return repo, paths, testProfile(t, repo)
}

// A DB error while the owned list is saved fails the run and writes nothing. The list is one transaction: game 60 is
// written first, then game 50's write fails, so game 60's rows must roll back too. No game is synced.
func TestFullSyncOwnedGameUpsertFailureFailsRunAndRollsBack(t *testing.T) {
	repo, paths, profile := faultyRepository(t)
	seedGameSync(t, repo, profile.ID, 50)
	if err := repo.DisableProfileGame(profile.ID, 50, 0); err != nil {
		t.Fatal(err)
	}
	addTriggers(t, paths,
		`create trigger block_game_insert before insert on games when new.appid = 50 begin select raise(abort, 'games blocked'); end`,
		`create trigger block_game_update before update on games when new.appid = 50 begin select raise(abort, 'games blocked'); end`,
	)
	fake := newFakeSteamServer(t, []steam.OwnedGame{
		{AppID: 60, Name: "Synced Game", PlaytimeForever: 3},
		{AppID: 50, Name: "Disabled Game", PlaytimeForever: 1},
	})
	fake.schema[60] = []string{}
	logs := captureLog(t)

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(context.Background(), *profile)
	if err == nil {
		t.Fatal("FullSync err = nil, want the owned-list write error")
	}
	requireSyncResult(t, result, expectedSyncResult{status: "failed", total: 0, synced: 0, failed: 0, hasError: true})
	if result.Error == nil || *result.Error != genericFailureText {
		t.Fatalf("result error = %v, want the generic sentence", result.Error)
	}
	if !strings.Contains(logs.String(), "save owned games:") {
		t.Fatalf("log = %q, want the owned-list save failure", logs.String())
	}
	if got := fake.count("game", 60); got != 0 {
		t.Fatalf("achievement calls for game 60 = %d, want 0 after the run failed", got)
	}
	if n := faultInt(t, paths, `select count(*) from games where appid=60`); n != 0 {
		t.Fatalf("games row for 60 = %d, want rolled back", n)
	}
	if n := faultInt(t, paths, `select count(*) from profile_owned_games where profile_id=? and appid=60`, profile.ID); n != 0 {
		t.Fatalf("profile_owned_games row for 60 = %d, want rolled back", n)
	}
	if disabled := requireGameCards(t, repo, profile.ID, "disabled"); len(disabled) != 1 || disabled[0].AppID != 50 {
		t.Fatalf("disabled cards = %#v, want manually disabled game 50 unchanged", disabled)
	}
	run := requireLatestRun(t, repo, profile.ID)
	if run.Status != "failed" || run.Error == nil || *run.Error != genericFailureText {
		t.Fatalf("sync run = %#v, want failed with the generic sentence", run)
	}
}

// A DB error on the not-owned sweep also fails the run and rolls back the owned-list writes of the same batch.
func TestFullSyncDisableMissingOwnedGamesFailureFailsRunAndRollsBack(t *testing.T) {
	repo, paths, profile := faultyRepository(t)
	seedGameSync(t, repo, profile.ID, 50)
	addTriggers(t, paths,
		`create trigger block_flag_insert before insert on profile_game_flags begin select raise(abort, 'flags blocked'); end`,
	)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 60, Name: "Still Owned", PlaytimeForever: 3}})
	fake.schema[60] = []string{}
	logs := captureLog(t)

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(context.Background(), *profile)
	if err == nil {
		t.Fatal("FullSync err = nil, want the not-owned sweep error")
	}
	requireSyncResult(t, result, expectedSyncResult{status: "failed", total: 0, synced: 0, failed: 0, hasError: true})
	if result.Error == nil || *result.Error != genericFailureText {
		t.Fatalf("result error = %v, want the generic sentence", result.Error)
	}
	if !strings.Contains(logs.String(), "save owned games:") {
		t.Fatalf("log = %q, want the owned-list save failure", logs.String())
	}
	if got := fake.count("game", 60); got != 0 {
		t.Fatalf("achievement calls for game 60 = %d, want 0 after the run failed", got)
	}
	if n := faultInt(t, paths, `select count(*) from games where appid=60`); n != 0 {
		t.Fatalf("games row for 60 = %d, want rolled back", n)
	}
	if n := faultInt(t, paths, `select count(*) from profile_owned_games where profile_id=? and appid=60`, profile.ID); n != 0 {
		t.Fatalf("profile_owned_games row for 60 = %d, want rolled back", n)
	}
	if disabled := requireGameCards(t, repo, profile.ID, "disabled"); len(disabled) != 0 {
		t.Fatalf("disabled cards = %#v, want none: the failed sweep flagged nothing", disabled)
	}
	run := requireLatestRun(t, repo, profile.ID)
	if run.Status != "failed" || run.Error == nil || *run.Error != genericFailureText {
		t.Fatalf("sync run = %#v, want failed with the generic sentence", run)
	}
}

// A game whose achievement calls fail is a per-game warning, not a run failure: the owned list is saved in full, the
// other game syncs, and the run ends partial.
func TestFullSyncGameFailureWarnsAndSavesOwnedListInFull(t *testing.T) {
	repo, paths, profile := faultyRepository(t)
	fake := newFakeSteamServer(t, []steam.OwnedGame{
		{AppID: 70, Name: "Private Game", PlaytimeForever: 3, Purchased: true},
		{AppID: 60, Name: "Synced Game", PlaytimeForever: 4},
	})
	fake.schema[70] = []string{"ACH_ONE"}
	fake.playerError[70] = "profile is private"
	fake.gameFailure[70] = gameFailure{status: http.StatusForbidden}
	fake.schema[60] = []string{}

	requireSyncResult(t, runFullSyncOK(t, repo, fake, profile), expectedSyncResult{status: "partial", total: 2, synced: 1, failed: 1, hasError: true})
	requireWarningCard(t, repo, profile.ID, 70, "Steam did not provide achievements for this game.")
	if n := faultInt(t, paths, `select count(*) from profile_owned_games where profile_id=? and appid in (60,70) and playtime_forever in (3,4)`, profile.ID); n != 2 {
		t.Fatalf("profile_owned_games rows for both games = %d, want 2", n)
	}
	if n := faultInt(t, paths, `select purchased from profile_owned_games where profile_id=? and appid=70`, profile.ID); n != 1 {
		t.Fatalf("purchased flag for game 70 = %d, want 1 from the owned list", n)
	}
}

// cancelAfterOwnedListTransport cancels the sync context once the last GetOwnedGames request (the vetted list, sent
// after the full and purchased lists) has been read in full. The body is buffered before the cancel, so the owned
// list is complete and GetOwnedGames returns normally. The cancel lands before SaveOwnedGames, which takes no context.
type cancelAfterOwnedListTransport struct {
	next   http.RoundTripper
	cancel context.CancelFunc
}

func (t cancelAfterOwnedListTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil || req.URL.Path != "/IPlayerService/GetOwnedGames/v0001/" || req.URL.Query().Get("skip_unvetted_apps") != "true" {
		return resp, err
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	t.cancel()
	return resp, nil
}

// A cancel that lands after the owned list is fetched but before it is saved. SaveOwnedGames takes no context, so the
// whole list still commits in full. No game is synced, so the run ends canceled with no games counted, no game
// rows, and the profile untouched. Every game is cut off before its first request, so the outcome does not depend on
// dispatch timing. (A cancel inside the save transaction itself has no hook here.)
func TestFullSyncCancelBeforeOwnedListSaveStoresFullListAndEndsCanceled(t *testing.T) {
	repo, paths, profile := faultyRepository(t)
	owned := []steam.OwnedGame{
		{AppID: 80, Name: "Game 80", PlaytimeForever: 11},
		{AppID: 81, Name: "Game 81", PlaytimeForever: 12},
		{AppID: 82, Name: "Game 82", PlaytimeForever: 13},
	}
	fake := newFakeSteamServer(t, owned)
	for _, game := range owned {
		fake.schema[game.AppID] = []string{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fake.client.HTTPClient = &http.Client{Transport: cancelAfterOwnedListTransport{next: http.DefaultTransport, cancel: cancel}}

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(ctx, *profile)
	if err != context.Canceled {
		t.Fatalf("err = %v, want context canceled", err)
	}
	if result.Status != "canceled" || result.Error == nil || *result.Error != "The request was canceled." {
		t.Fatalf("result = %#v, want canceled run with the cancel sentence", result)
	}
	if result.GamesTotal != len(owned) || result.GamesSynced != 0 || result.GamesFailed != 0 {
		t.Fatalf("result = %#v, want total %d and no synced or failed games", result, len(owned))
	}
	run := requireLatestRun(t, repo, profile.ID)
	if run.Status != "canceled" || run.GamesSynced != 0 || run.GamesFailed != 0 || run.FinishedAt == nil {
		t.Fatalf("sync run = %#v, want finished canceled run with no synced or failed games", run)
	}
	// The owned list is committed in full, with each game's own playtime, even though the run was canceled.
	playtimes := faultPlaytimes(t, paths, profile.ID)
	if len(playtimes) != len(owned) {
		t.Fatalf("profile_owned_games rows = %d, want the full list of %d", len(playtimes), len(owned))
	}
	for _, game := range owned {
		if got, ok := playtimes[game.AppID]; !ok || got != game.PlaytimeForever {
			t.Fatalf("profile_owned_games playtime for app %d = %d (present %v), want %d", game.AppID, got, ok, game.PlaytimeForever)
		}
	}
	// No per-game sync happened: no achievement calls reached the server, no game rows, no synced timestamp.
	for _, game := range owned {
		if got := fake.count("game", game.AppID); got != 0 {
			t.Fatalf("achievement calls for game %d = %d, want 0 after the cancel", game.AppID, got)
		}
	}
	if n := faultInt(t, paths, `select count(*) from profile_games where profile_id=?`, profile.ID); n != 0 {
		t.Fatalf("profile_games rows = %d, want 0: no game was synced", n)
	}
	if last := currentProfile(t, repo).LastSyncedAt; last != nil {
		t.Fatalf("profile last synced = %s, want unset: no game synced", *last)
	}
}

// faultInt runs a single-int query on a separate connection to the test database, after the sync has finished.
func faultInt(t *testing.T, paths appdata.Paths, query string, args ...any) int {
	t.Helper()
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", paths.DB))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// faultPlaytimes maps each appid of the profile's owned list to its playtime, read on a separate connection after the
// sync has finished.
func faultPlaytimes(t *testing.T, paths appdata.Paths, profileID int64) map[int64]int {
	t.Helper()
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", paths.DB))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`select appid, playtime_forever from profile_owned_games where profile_id=?`, profileID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var appID int64
		var playtime int
		if err := rows.Scan(&appID, &playtime); err != nil {
			t.Fatal(err)
		}
		out[appID] = playtime
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// A game failure whose warning cannot be saved is logged. The run is still partial and no warning row exists.
func TestFullSyncSaveGameWarningFailureIsLoggedAndRunStaysPartial(t *testing.T) {
	repo, paths, profile := faultyRepository(t)
	addTriggers(t, paths,
		`create trigger block_warning_insert before insert on profile_games when new.sync_warning = 1 begin select raise(abort, 'warnings blocked'); end`,
		`create trigger block_warning_update before update on profile_games when new.sync_warning = 1 begin select raise(abort, 'warnings blocked'); end`,
	)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 20, Name: "Bad Gateway Game", PlaytimeForever: 3}})
	fake.gameFailure[20] = gameFailure{status: http.StatusBadGateway}
	logs := captureLog(t)

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})
	if !strings.Contains(logs.String(), "save game warning 20:") {
		t.Fatalf("log = %q, want warning save failure for game 20", logs.String())
	}
	requireNoWarningCards(t, repo, profile.ID)
}

// A failing FinishSyncRun is logged. FullSync still returns the result, and sync_runs keeps the last progress
// write: the run stays running with its counters, and no finish time. The syncer leaves the row 'running'.
// GetAppState reports it as failed while no sync is live, and the next startup's CancelRunningSyncRuns repairs the row.
func TestFullSyncFinishSyncRunFailureIsLoggedAndRunStaysRunning(t *testing.T) {
	repo, paths, profile := faultyRepository(t)
	addTriggers(t, paths,
		`create trigger block_run_finish before update of status on sync_runs begin select raise(abort, 'finish blocked'); end`,
	)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 60, Name: "Synced Game", PlaytimeForever: 3}})
	fake.schema[60] = []string{}
	logs := captureLog(t)

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	if !strings.Contains(logs.String(), "finish sync run:") {
		t.Fatalf("log = %q, want finish failure", logs.String())
	}
	run := requireLatestRun(t, repo, profile.ID)
	if run.Status != "running" || run.FinishedAt != nil {
		t.Fatalf("sync run = %#v, want still running with no finish time", run)
	}
	if run.GamesTotal != 1 || run.GamesSynced != 1 || run.GamesFailed != 0 {
		t.Fatalf("sync run counters = %#v, want last progress 1/1/0", run)
	}
}

// Failing progress writes are logged. The final counters still come from FinishSyncRun.
func TestFullSyncProgressUpdateFailureIsLoggedAndFinalCountersPersist(t *testing.T) {
	repo, paths, profile := faultyRepository(t)
	addTriggers(t, paths,
		`create trigger block_run_progress before update on sync_runs when new.status = 'running' begin select raise(abort, 'progress blocked'); end`,
	)
	fake := newFakeSteamServer(t, []steam.OwnedGame{
		{AppID: 60, Name: "Game 60", PlaytimeForever: 3},
		{AppID: 61, Name: "Game 61", PlaytimeForever: 4},
	})
	fake.schema[60] = []string{}
	fake.schema[61] = []string{}
	logs := captureLog(t)

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: 2, synced: 2, failed: 0})
	if !strings.Contains(logs.String(), "update sync progress:") {
		t.Fatalf("log = %q, want progress failure", logs.String())
	}
	requireLatestSyncRun(t, repo, profile.ID, expectedSyncResult{status: "success", total: 2, synced: 2, failed: 0})
}

// The profile's last sync time is set only when at least one game synced.
func TestFullSyncTouchesProfileOnlyWhenAGameSynced(t *testing.T) {
	t.Run("game synced", func(t *testing.T) {
		repo := testRepository(t)
		profile := testProfile(t, repo)
		fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 60, Name: "Synced Game", PlaytimeForever: 3}})
		fake.schema[60] = []string{}

		requireSyncResult(t, runFullSyncOK(t, repo, fake, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
		if got := currentProfile(t, repo).LastSyncedAt; got == nil {
			t.Fatal("profile last synced = nil, want set after a synced game")
		}
	})
	t.Run("no game synced", func(t *testing.T) {
		repo := testRepository(t)
		profile := testProfile(t, repo)
		fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 20, Name: "Bad Gateway Game", PlaytimeForever: 3}})
		fake.gameFailure[20] = gameFailure{status: http.StatusBadGateway}

		requireSyncResult(t, runFullSyncOK(t, repo, fake, profile), expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})
		if got := currentProfile(t, repo).LastSyncedAt; got != nil {
			t.Fatalf("profile last synced = %q, want unset when no game synced", *got)
		}
	})
}

// A failing TouchProfileSynced is logged; the sync result is unaffected.
func TestFullSyncTouchProfileFailureIsLoggedAndSyncContinues(t *testing.T) {
	repo, paths, profile := faultyRepository(t)
	addTriggers(t, paths,
		`create trigger block_profile_touch before update on profiles begin select raise(abort, 'profile blocked'); end`,
	)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 60, Name: "Synced Game", PlaytimeForever: 3}})
	fake.schema[60] = []string{}
	logs := captureLog(t)

	requireSyncResult(t, runFullSyncOK(t, repo, fake, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	if !strings.Contains(logs.String(), "touch profile synced:") {
		t.Fatalf("log = %q, want touch failure", logs.String())
	}
	if got := currentProfile(t, repo).LastSyncedAt; got != nil {
		t.Fatalf("profile last synced = %q, want unset after failed touch", *got)
	}
}

// A failing cover write is logged for the image app id; the game still syncs and the run succeeds.
func TestFullSyncCoverRefreshFailureIsLoggedAndSyncStillSucceeds(t *testing.T) {
	const appID = 730
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: appID, Name: "Cover Game", PlaytimeForever: 10, CapsuleFilename: "library_600x900.jpg"}})
	fake.images["730/library_600x900.jpg"] = true
	svc := serviceWithImages(t, repo, fake, paths)
	addTriggers(t, paths,
		`create trigger block_cover_insert before insert on cover_images begin select raise(abort, 'cover blocked'); end`,
		`create trigger block_cover_update before update on cover_images begin select raise(abort, 'cover blocked'); end`,
	)
	logs := captureLog(t)

	requireSyncResult(t, runServiceOK(t, svc, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	if !strings.Contains(logs.String(), fmt.Sprintf("refresh cover %d:", appID)) {
		t.Fatalf("log = %q, want cover refresh failure", logs.String())
	}
	if _, ok, err := repo.GetCover(appID); err != nil || ok {
		t.Fatalf("cover row: ok=%v err=%v, want none after failed write", ok, err)
	}
}

func currentProfile(t *testing.T, repo *repository.Repository) *repository.Profile {
	t.Helper()
	profile, err := repo.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	if profile == nil {
		t.Fatal("no active profile")
	}
	return profile
}
