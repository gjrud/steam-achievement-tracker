package repository

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// clearProfileDataWithin runs ClearProfileData and fails the test if it does not return in time. ClearProfileData runs
// all its reads inside one transaction on a single connection, so a read that went through the *sql.DB instead of the
// transaction would block forever. The timeout turns that hang into a failure.
func clearProfileDataWithin(t *testing.T, repo *Repository, profileID int64) []int64 {
	t.Helper()
	type result struct {
		ids []int64
		err error
	}
	done := make(chan result, 1)
	go func() {
		ids, err := repo.ClearProfileData(profileID)
		done <- result{ids, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("ClearProfileData(%d): %v", profileID, res.err)
		}
		return res.ids
	case <-time.After(10 * time.Second):
		t.Fatalf("ClearProfileData(%d) did not return: a read probably used the database instead of the transaction", profileID)
	}
	return nil
}

// seedSharedCoverGame saves app appID for profileID with a base set and a DLC set bound to dlcAppID, and caches its
// library cover and its DLC cover, the rows a sync leaves behind.
func seedSharedCoverGame(t *testing.T, repo *Repository, profileID, appID, dlcAppID int64) {
	t.Helper()
	saveSeedGame(t, repo, profileID, appID, "Shared Game", 0,
		dashboardSeed{GroupID: 0, Total: 3, Unlocked: 1, Percent: 33},
		dashboardSeed{GroupID: 1, DLCAppID: dlcAppID, SetName: "Pack", Total: 2, Unlocked: 0, Percent: 0})
	mustSaveLibraryCover(t, repo, appID)
	if err := repo.SaveCover(CoverUpdate{ImageAppID: dlcAppID, Kind: "hero", FileName: "hero_capsule.jpg", Cached: true, SourceURL: "https://example.com/hero.jpg", Downloaded: true}); err != nil {
		t.Fatal(err)
	}
}

// TestClearProfileDataDeletesSharedRowsNoOneElseShows removes games, achievements, game_sets and cover_images rows
// that only the cleared profile used, and returns the cover app ids it removed. The rows are written the way a sync
// writes them. The old race (rows inserted between the candidate reads and the transaction) cannot be forced
// deterministically with one connection, so this test checks the outcome: no orphan rows remain.
func TestClearProfileDataDeletesSharedRowsNoOneElseShows(t *testing.T) {
	repo := testRepository(t)
	p1 := saveTestProfile(t, repo, "11111111111111111", "User One")
	saveTestProfile(t, repo, "22222222222222222", "User Two")
	if _, err := repo.db.Exec(`update settings set value=? where key='active_profile_id'`, fmt.Sprintf("%d", p1.ID)); err != nil {
		t.Fatal(err)
	}
	seedSharedCoverGame(t, repo, p1.ID, 2968420, 4173830)
	// Profile two owns a different game with its own cover; neither may be touched by clearing profile one.
	saveSeedGame(t, repo, p1.ID, 555, "Other Game", 0, dashboardSeed{GroupID: 0, Total: 1, Unlocked: 1, Percent: 100})
	mustSaveLibraryCover(t, repo, 555)

	ids := clearProfileDataWithin(t, repo, p1.ID)

	// Every cover row these games used goes: the two library covers and the DLC cover of 2968420.
	if want := []int64{555, 2968420, 4173830}; !slices.Equal(ids, want) {
		t.Fatalf("removed cover app ids = %v, want %v", ids, want)
	}
	for _, appID := range []int64{2968420, 555} {
		assertCount(t, repo, `select count(*) from games where appid=?`, 0, appID)
		assertCount(t, repo, `select count(*) from achievements where appid=?`, 0, appID)
		assertCount(t, repo, `select count(*) from game_sets where appid=?`, 0, appID)
		assertCount(t, repo, `select count(*) from cover_images where image_appid=?`, 0, appID)
	}
	assertCount(t, repo, `select count(*) from cover_images where image_appid=?`, 0, 4173830)
	assertCount(t, repo, `select count(*) from profile_games where profile_id=?`, 0, p1.ID)
	assertCount(t, repo, `select count(*) from profiles`, 1)
}

// TestClearProfileDataKeepsRowsAnotherProfileStillShows is the counterpart: a game and its DLC cover that a second
// profile also shows survive the clear of the first profile.
func TestClearProfileDataKeepsRowsAnotherProfileStillShows(t *testing.T) {
	repo := testRepository(t)
	p1 := saveTestProfile(t, repo, "11111111111111111", "User One")
	p2 := saveTestProfile(t, repo, "22222222222222222", "User Two")
	seedSharedCoverGame(t, repo, p1.ID, 2968420, 4173830)
	seedSharedCoverGame(t, repo, p2.ID, 2968420, 4173830)

	ids := clearProfileDataWithin(t, repo, p1.ID)

	if len(ids) != 0 {
		t.Fatalf("removed cover app ids = %v, want none: profile two still shows them", ids)
	}
	assertCount(t, repo, `select count(*) from games where appid=?`, 1, 2968420)
	// Five achievements: three in the base set and two in the DLC set.
	assertCount(t, repo, `select count(*) from achievements where appid=?`, 5, 2968420)
	assertCount(t, repo, `select count(*) from game_sets where appid=?`, 2, 2968420)
	assertCount(t, repo, `select count(*) from cover_images where image_appid in (?, ?)`, 2, 2968420, 4173830)
	// Two entries: the base set and the DLC set of 2968420.
	assertCount(t, repo, `select count(*) from profile_games where profile_id=?`, 2, p2.ID)
}

// TestClearProfileDataReadsActiveProfileInTransaction checks the active-profile result the tx reads: clearing the
// active profile leaves no active profile, and clearing an inactive one leaves the active profile alone.
func TestClearProfileDataReadsActiveProfileInTransaction(t *testing.T) {
	repo := testRepository(t)
	active := saveTestProfile(t, repo, "11111111111111111", "User One")
	other := saveTestProfile(t, repo, "22222222222222222", "User Two")
	if _, err := repo.db.Exec(`update settings set value=? where key='active_profile_id'`, fmt.Sprintf("%d", active.ID)); err != nil {
		t.Fatal(err)
	}

	clearProfileDataWithin(t, repo, other.ID)
	if got, err := repo.ActiveProfile(); err != nil || got == nil || got.ID != active.ID {
		t.Fatalf("active after clearing the inactive profile = %+v, %v; want profile %d", got, err, active.ID)
	}

	clearProfileDataWithin(t, repo, active.ID)
	if got, err := repo.ActiveProfile(); err != nil || got != nil {
		t.Fatalf("active after clearing the active profile = %+v, %v; want none", got, err)
	}
	assertCount(t, repo, `select count(*) from settings where key='active_profile_id' and value=''`, 1)
}
