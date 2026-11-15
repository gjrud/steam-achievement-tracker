package syncer

import (
	"bytes"
	"database/sql"
	"fmt"
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

// An owned game whose upsert fails is logged; the sync goes on with the other games and the run succeeds.
func TestFullSyncOwnedGameUpsertFailureIsLoggedAndSyncContinues(t *testing.T) {
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
		{AppID: 50, Name: "Disabled Game", PlaytimeForever: 1},
		{AppID: 60, Name: "Synced Game", PlaytimeForever: 3},
	})
	fake.schema[60] = []string{}
	logs := captureLog(t)

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	if !strings.Contains(logs.String(), "upsert owned game 50:") {
		t.Fatalf("log = %q, want upsert failure for game 50", logs.String())
	}
	if disabled := requireGameCards(t, repo, profile.ID, "disabled"); len(disabled) != 1 || disabled[0].AppID != 50 {
		t.Fatalf("disabled cards = %#v, want manually disabled game 50 still disabled", disabled)
	}
}

// A failing DisableMissingOwnedGames is logged; the run goes on and the game no longer owned is not flagged.
func TestFullSyncDisableMissingOwnedGamesFailureIsLoggedAndSyncContinues(t *testing.T) {
	repo, paths, profile := faultyRepository(t)
	seedGameSync(t, repo, profile.ID, 50)
	addTriggers(t, paths,
		`create trigger block_flag_insert before insert on profile_game_flags begin select raise(abort, 'flags blocked'); end`,
	)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 60, Name: "Still Owned", PlaytimeForever: 3}})
	fake.schema[60] = []string{}
	logs := captureLog(t)

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	if !strings.Contains(logs.String(), "disable missing games:") {
		t.Fatalf("log = %q, want disable failure", logs.String())
	}
	if disabled := requireGameCards(t, repo, profile.ID, "disabled"); len(disabled) != 0 {
		t.Fatalf("disabled cards = %#v, want none after failed not-owned flagging", disabled)
	}
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
