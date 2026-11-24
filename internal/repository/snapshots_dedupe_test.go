package repository

import (
	"testing"
	"time"
)

// snapshotCount returns the number of game_snapshots rows for one (profile, app, group).
func snapshotCount(t *testing.T, repo *Repository, profileID, appID, groupID int64) int {
	t.Helper()
	var n int
	if err := repo.db.QueryRow(`select count(*) from game_snapshots where profile_id=? and appid=? and groupid=?`, profileID, appID, groupID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSaveGameSyncUnchangedSyncsAddNoSnapshots(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	seed := dashboardSeed{GroupID: 0, Total: 3, Unlocked: 1, Percent: 33.3}
	saveSeedGame(t, repo, profile.ID, 500, "Steady Game", 0, seed)
	if got := snapshotCount(t, repo, profile.ID, 500, 0); got != 1 {
		t.Fatalf("snapshots after first sync = %d, want 1", got)
	}
	for i := 0; i < 5; i++ {
		saveSeedGame(t, repo, profile.ID, 500, "Steady Game", 0, seed)
	}
	if got := snapshotCount(t, repo, profile.ID, 500, 0); got != 1 {
		t.Fatalf("snapshots after 5 unchanged syncs = %d, want 1", got)
	}
}

func TestSaveGameSyncChangedStateAddsExactlyOneSnapshot(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 501, "Changing Game", 0, dashboardSeed{GroupID: 0, Total: 3, Unlocked: 1, Percent: 33.3})
	saveSeedGame(t, repo, profile.ID, 501, "Changing Game", 0, dashboardSeed{GroupID: 0, Total: 3, Unlocked: 2, Percent: 66.6})
	if got := snapshotCount(t, repo, profile.ID, 501, 0); got != 2 {
		t.Fatalf("snapshots after unlock change = %d, want 2", got)
	}
	saveSeedGame(t, repo, profile.ID, 501, "Changing Game", 0, dashboardSeed{GroupID: 0, Total: 3, Unlocked: 2, Percent: 66.6})
	if got := snapshotCount(t, repo, profile.ID, 501, 0); got != 2 {
		t.Fatalf("snapshots after repeat of new state = %d, want 2", got)
	}
}

func TestSaveGameSyncKeepsCompletedHistoryAcrossFlips(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	completed := dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true}
	incomplete := dashboardSeed{GroupID: 0, Total: 3, Unlocked: 2, Percent: 66.6}

	saveSeedGame(t, repo, profile.ID, 502, "Flip Game", 0, completed)
	saveSeedGame(t, repo, profile.ID, 502, "Flip Game", 0, incomplete)
	saveSeedGame(t, repo, profile.ID, 502, "Flip Game", 0, completed)
	if got := snapshotCount(t, repo, profile.ID, 502, 0); got != 3 {
		t.Fatalf("snapshots after completed->incomplete->completed = %d, want 3", got)
	}
	assertCount(t, repo, `select count(*) from game_snapshots where profile_id=? and appid=? and groupid=? and is_completed=1`, 2, profile.ID, 502, 0)
	// Repeating the completed state is a no-op.
	saveSeedGame(t, repo, profile.ID, 502, "Flip Game", 0, completed)
	if got := snapshotCount(t, repo, profile.ID, 502, 0); got != 3 {
		t.Fatalf("snapshots after repeated completed sync = %d, want 3", got)
	}
	// The completed history still answers the "ever completed" question.
	state, err := repo.ProfileGame(profile.ID, 502, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !state.IsCompleted || !state.WasCompleted {
		t.Fatalf("state = %#v, want completed and was completed", state)
	}
}

func TestSaveGameSyncGroupPrunedThenRestoredDoesNotDuplicate(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	base := dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50}
	dlc := dashboardSeed{GroupID: 1, DLCAppID: 4173900, SetName: "DLC", Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true}

	saveSeedGame(t, repo, profile.ID, 503, "Pruned Game", 0, base, dlc)
	if got := snapshotCount(t, repo, profile.ID, 503, 1); got != 1 {
		t.Fatalf("DLC snapshots before prune = %d, want 1", got)
	}
	// The DLC drops out of the eligible set: its profile entry is pruned, its snapshot history stays.
	saveSeedGame(t, repo, profile.ID, 503, "Pruned Game", 0, base)
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=503 and groupid=1`, 0, profile.ID)
	if got := snapshotCount(t, repo, profile.ID, 503, 1); got != 1 {
		t.Fatalf("DLC snapshots after prune = %d, want 1", got)
	}
	// The DLC comes back with the same numbers: the latest snapshot still matches, so nothing is added.
	saveSeedGame(t, repo, profile.ID, 503, "Pruned Game", 0, base, dlc)
	if got := snapshotCount(t, repo, profile.ID, 503, 1); got != 1 {
		t.Fatalf("DLC snapshots after restore = %d, want 1", got)
	}
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=503 and groupid=1`, 1, profile.ID)
}

func TestSaveGameSyncFirstSyncAfterWarningPlaceholderWritesSnapshot(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	// A warning on a game never synced creates a 0/0/0 placeholder entry and no snapshot.
	if err := repo.SaveGameWarning(profile.ID, GameRecord{AppID: 504, Name: "Placeholder Game"}, "Steam 503"); err != nil {
		t.Fatal(err)
	}
	if got := snapshotCount(t, repo, profile.ID, 504, 0); got != 0 {
		t.Fatalf("snapshots after warning placeholder = %d, want 0", got)
	}
	saveSeedGame(t, repo, profile.ID, 504, "Placeholder Game", 0, dashboardSeed{GroupID: 0, Total: 4, Unlocked: 0, Percent: 0})
	if got := snapshotCount(t, repo, profile.ID, 504, 0); got != 1 {
		t.Fatalf("snapshots after first real sync = %d, want 1", got)
	}
}

func TestSaveGameSyncProfilesSharingAppGetOwnSnapshots(t *testing.T) {
	repo := testRepository(t)
	p1, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	seed := dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50}
	saveSeedGame(t, repo, p1.ID, 505, "Shared Game", 0, seed)
	saveSeedGame(t, repo, p2.ID, 505, "Shared Game", 0, seed)
	// Same numbers for both profiles: each profile's own latest snapshot is compared, so each keeps one row.
	if got := snapshotCount(t, repo, p1.ID, 505, 0); got != 1 {
		t.Fatalf("profile 1 snapshots = %d, want 1", got)
	}
	if got := snapshotCount(t, repo, p2.ID, 505, 0); got != 1 {
		t.Fatalf("profile 2 snapshots = %d, want 1", got)
	}
	// Changing one profile does not add rows for the other.
	saveSeedGame(t, repo, p1.ID, 505, "Shared Game", 0, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})
	if got := snapshotCount(t, repo, p1.ID, 505, 0); got != 2 {
		t.Fatalf("profile 1 snapshots after its change = %d, want 2", got)
	}
	if got := snapshotCount(t, repo, p2.ID, 505, 0); got != 1 {
		t.Fatalf("profile 2 snapshots after profile 1 change = %d, want 1", got)
	}
}

func TestSaveGameSyncDedupeComparesLatestSnapshotOnly(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	// Seed history directly: an older row matches the state being synced, but the latest row does not.
	// Only the latest row may be compared, so the sync must still add one row.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := repo.db.Exec(`insert into games(appid, name, created_at, updated_at) values(?,?,?,?)`, 506, "Latest Game", now, now); err != nil {
		t.Fatal(err)
	}
	// Columns: total, unlocked, is_completed. Inserted oldest first.
	for _, row := range [][3]int{{2, 1, 0}, {2, 2, 1}} {
		if _, err := repo.db.Exec(`insert into game_snapshots(profile_id, appid, groupid, total_achievements, unlocked_achievements, completion_percent, is_completed, synced_at) values(?,?,0,?,?,0,?,?)`, profile.ID, 506, row[0], row[1], row[2], now); err != nil {
			t.Fatal(err)
		}
	}
	saveSeedGame(t, repo, profile.ID, 506, "Latest Game", 0, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50})
	if got := snapshotCount(t, repo, profile.ID, 506, 0); got != 3 {
		t.Fatalf("snapshots after state matching only an older row = %d, want 3", got)
	}
	// Now the latest row matches: no further row.
	saveSeedGame(t, repo, profile.ID, 506, "Latest Game", 0, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50})
	if got := snapshotCount(t, repo, profile.ID, 506, 0); got != 3 {
		t.Fatalf("snapshots after repeat = %d, want 3", got)
	}
}
