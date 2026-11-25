package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

// queryText renders every row of a query as text, one line per row, for comparing two databases.
func queryText(t *testing.T, repo *Repository, query string, args ...any) string {
	t.Helper()
	rows, err := repo.db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(&b, vals...)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// ownedSnapshot is the stored owned-game state of the database: shared games, the profile's owned rows, entries,
// flags and tags. Timestamps are left out, so two runs of the same write compare equal.
func ownedSnapshot(t *testing.T, repo *Repository, profileID int64) string {
	t.Helper()
	return queryText(t, repo, `select appid, name from games order by appid`) +
		queryText(t, repo, `select appid, playtime_forever, purchased, vetted from profile_owned_games where profile_id=? order by appid`, profileID) +
		queryText(t, repo, `select appid, groupid from profile_games where profile_id=? order by appid, groupid`, profileID) +
		queryText(t, repo, `select appid, groupid, disabled, coalesce(disabled_reason,'') from profile_game_flags where profile_id=? order by appid, groupid`, profileID) +
		queryText(t, repo, `select appid, groupid, tag from profile_game_tags where profile_id=? order by appid, groupid, tag`, profileID)
}

// The batch write leaves the same stored state as the per-game path: UpsertOwnedGame for each game, then
// DisableMissingOwnedGames over the same ids.
func TestSaveOwnedGamesMatchesPerGamePath(t *testing.T) {
	setup := func(repo *Repository) *Profile {
		profile := saveTestProfile(t, repo, "11111111111111111", "User One")
		seedGameSync(t, repo, profile.ID, 1)
		seedGameSync(t, repo, profile.ID, 2)
		seedGameSync(t, repo, profile.ID, 3)
		if err := repo.DisableProfileGame(profile.ID, 3, 0); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpsertOwnedGame(profile.ID, GameRecord{AppID: 1, Name: "Old Name", PlaytimeForever: 5, Purchased: true}); err != nil {
			t.Fatal(err)
		}
		return profile
	}
	// Game 2 is no longer owned, game 3 is owned but manually disabled, game 4 is new.
	games := []GameRecord{
		{AppID: 1, Name: "New Name", PlaytimeForever: 9, Unvetted: true},
		{AppID: 3, Name: "Game Three", PlaytimeForever: 2, Purchased: true},
		{AppID: 4, Name: "Fresh", PlaytimeForever: 7, Purchased: true},
	}

	batchRepo := testRepository(t)
	batchProfile := setup(batchRepo)
	if err := batchRepo.SaveOwnedGames(batchProfile.ID, games); err != nil {
		t.Fatal(err)
	}

	perGameRepo := testRepository(t)
	perGameProfile := setup(perGameRepo)
	ownedIDs := make([]int64, 0, len(games))
	for _, g := range games {
		if err := perGameRepo.UpsertOwnedGame(perGameProfile.ID, g); err != nil {
			t.Fatal(err)
		}
		ownedIDs = append(ownedIDs, g.AppID)
	}
	if err := perGameRepo.DisableMissingOwnedGames(perGameProfile.ID, ownedIDs); err != nil {
		t.Fatal(err)
	}

	if got, want := ownedSnapshot(t, batchRepo, batchProfile.ID), ownedSnapshot(t, perGameRepo, perGameProfile.ID); got != want {
		t.Fatalf("batch state differs from per-game state\nbatch:\n%s\nper game:\n%s", got, want)
	}
	assertCount(t, batchRepo, `select count(*) from profile_game_flags where profile_id=? and appid=2 and disabled=1 and disabled_reason='not_owned'`, 1, batchProfile.ID)
	assertCount(t, batchRepo, `select count(*) from profile_game_flags where profile_id=? and appid=3 and disabled=1 and disabled_reason='manual'`, 1, batchProfile.ID)
	assertCount(t, batchRepo, `select count(*) from profile_owned_games where profile_id=? and appid=4 and purchased=1 and vetted=1`, 1, batchProfile.ID)
	assertCount(t, batchRepo, `select count(*) from profile_owned_games where profile_id=? and appid=1 and playtime_forever=9 and purchased=0 and vetted=0`, 1, batchProfile.ID)
}

// A failing write in the middle of the batch rolls back every game of the batch: nothing is inserted, and the
// not-owned sweep does not run.
func TestSaveOwnedGamesRollsBackWholeBatch(t *testing.T) {
	repo, path := testRepositoryPath(t)
	profile := saveTestProfile(t, repo, "11111111111111111", "User One")
	seedGameSync(t, repo, profile.ID, 1)
	before := ownedSnapshot(t, repo, profile.ID)

	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`create trigger block_owned_insert before insert on profile_owned_games when new.appid = 30 begin select raise(abort, 'owned blocked'); end`); err != nil {
		t.Fatal(err)
	}

	games := []GameRecord{
		{AppID: 10, Name: "Ten", PlaytimeForever: 1},
		{AppID: 20, Name: "Twenty"},
		{AppID: 30, Name: "Thirty"},
		{AppID: 40, Name: "Forty"},
	}
	if err := repo.SaveOwnedGames(profile.ID, games); err == nil {
		t.Fatal("SaveOwnedGames err = nil, want the blocked write to fail the batch")
	}
	if after := ownedSnapshot(t, repo, profile.ID); after != before {
		t.Fatalf("state after failed batch differs from before\nbefore:\n%s\nafter:\n%s", before, after)
	}
	assertCount(t, repo, `select count(*) from games where appid in (10,20,30,40)`, 0)
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and disabled=1`, 0, profile.ID)

	if _, err := db.Exec(`drop trigger block_owned_insert`); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveOwnedGames(profile.ID, games); err != nil {
		t.Fatalf("SaveOwnedGames after removing the trigger: %v", err)
	}
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid in (10,20,30,40)`, 4, profile.ID)
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid=1 and disabled=1 and disabled_reason='not_owned'`, 1, profile.ID)
}

// An empty list is a list with no games: every entry of the profile is flagged not_owned and its tags are dropped.
func TestSaveOwnedGamesEmptyListFlagsEveryEntryNotOwned(t *testing.T) {
	repo := testRepository(t)
	profile := saveTestProfile(t, repo, "11111111111111111", "User One")
	seedGameSync(t, repo, profile.ID, 1)
	seedGameSync(t, repo, profile.ID, 2)

	if err := repo.SaveOwnedGames(profile.ID, nil); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and disabled=1 and disabled_reason='not_owned'`, 2, profile.ID)
	assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=?`, 0, profile.ID)
}

// A 40k-game list is written in one transaction without hitting SQLite's bound-variable limit. The time is logged,
// not asserted.
func TestSaveOwnedGamesWithLargeLibrary(t *testing.T) {
	repo := testRepository(t)
	profile := saveTestProfile(t, repo, "11111111111111111", "User One")
	// Apps 1 and 2 are stored but not in the owned list; the 40k others are.
	seedGameSync(t, repo, profile.ID, 1)
	seedGameSync(t, repo, profile.ID, 2)
	games := make([]GameRecord, 0, largeLibrarySize)
	for _, id := range bulkAppIDs(100000, largeLibrarySize) {
		games = append(games, GameRecord{AppID: id, Name: "Bulk", PlaytimeForever: int(id % 97), Purchased: id%2 == 0, Unvetted: id%3 == 0})
	}
	start := time.Now()
	if err := repo.SaveOwnedGames(profile.ID, games); err != nil {
		t.Fatalf("SaveOwnedGames(40k owned): %v", err)
	}
	t.Logf("SaveOwnedGames(40k owned) took %s", time.Since(start))
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid >= 100000`, largeLibrarySize, profile.ID)
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=100000 and purchased=1 and vetted=1`, 1, profile.ID)
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid in (1,2) and disabled=1 and disabled_reason='not_owned'`, 2, profile.ID)

	// Re-own app 1 in the same large list: only its not_owned flag clears; app 2 stays disabled.
	games = append(games, GameRecord{AppID: 1, Name: "Game", PlaytimeForever: 3})
	if err := repo.SaveOwnedGames(profile.ID, games); err != nil {
		t.Fatalf("SaveOwnedGames(40k owned, re-own 1): %v", err)
	}
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid=1 and disabled=1`, 0, profile.ID)
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid=2 and disabled=1 and disabled_reason='not_owned'`, 1, profile.ID)
}

// A batch for one profile leaves another profile's rows alone, including the flags of a shared game. The shared
// games row gets the name only.
func TestSaveOwnedGamesLeavesOtherProfilesUntouched(t *testing.T) {
	repo := testRepository(t)
	p1 := saveTestProfile(t, repo, "11111111111111111", "User One")
	p2 := saveTestProfile(t, repo, "22222222222222222", "User Two")
	// Entries first: a game sync writes playtime, so the owned-list values are set after it.
	seedGameSync(t, repo, p1.ID, 901)
	seedGameSync(t, repo, p2.ID, 901)
	if err := repo.SaveOwnedGames(p2.ID, []GameRecord{
		{AppID: 900, Name: "Shared", PlaytimeForever: 100, Purchased: true},
		{AppID: 901, Name: "Only Two", PlaytimeForever: 5, Unvetted: true},
	}); err != nil {
		t.Fatal(err)
	}

	// Profile one owns the shared game with other values, and does not own 901.
	if err := repo.SaveOwnedGames(p1.ID, []GameRecord{{AppID: 900, Name: "Shared Renamed", PlaytimeForever: 1}}); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=900 and playtime_forever=100 and purchased=1 and vetted=1`, 1, p2.ID)
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=901 and playtime_forever=5 and vetted=0`, 1, p2.ID)
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and disabled=1`, 0, p2.ID)
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid=901 and disabled=1 and disabled_reason='not_owned'`, 1, p1.ID)
	assertCount(t, repo, `select count(*) from games where appid=900 and name='Shared Renamed'`, 1)
}

// A manual disable survives the not-owned sweep, and a re-own clears only the not_owned flag.
func TestSaveOwnedGamesReownClearsNotOwnedOnly(t *testing.T) {
	repo := testRepository(t)
	profile := saveTestProfile(t, repo, "11111111111111111", "User One")
	saveSeedGame(t, repo, profile.ID, 50, "Re-own Game", 60,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, SetName: "Pack", Total: 2, Unlocked: 1, Percent: 50})
	if err := repo.DisableProfileGame(profile.ID, 50, 1); err != nil {
		t.Fatal(err)
	}

	// Game 50 is no longer owned: group 0 becomes not_owned, the manual disable on group 1 keeps its reason.
	if err := repo.SaveOwnedGames(profile.ID, []GameRecord{{AppID: 999, Name: "Other"}}); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=? and appid=50`, 0, profile.ID)
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid=50 and groupid=0 and disabled=1 and disabled_reason='not_owned'`, 1, profile.ID)
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid=50 and groupid=1 and disabled=1 and disabled_reason='manual'`, 1, profile.ID)

	// Game 50 is owned again: only the not_owned flag of group 0 clears.
	if err := repo.SaveOwnedGames(profile.ID, []GameRecord{{AppID: 50, Name: "Re-own Game", PlaytimeForever: 61}, {AppID: 999, Name: "Other"}}); err != nil {
		t.Fatal(err)
	}
	completed, err := repo.GameCards(profile.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "completed after re-own", completed, []string{"50:0"})
	disabled, err := repo.GameCards(profile.ID, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "disabled after re-own", disabled, []string{"50:1"})
	if !disabled[0].ManualDisabled || disabled[0].DisabledReason == nil || *disabled[0].DisabledReason != "manual" {
		t.Fatalf("manual disable after re-own = %#v, want manual reason kept", disabled[0])
	}
}
