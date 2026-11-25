package repository

import (
	"testing"
	"time"
)

// largeLibrarySize is above SQLite's default bound-variable limit (32766), so any statement that binds one
// placeholder per id fails with "too many SQL variables".
const largeLibrarySize = 40000

// seedBulkGames inserts one games row per appid (existing rows are kept) and, when profileID is non-zero, one
// group-0 profile_games row for each. It runs in a single transaction and does not call repository methods
// (single connection).
func seedBulkGames(t *testing.T, repo *Repository, profileID int64, appIDs []int64) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := repo.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	gameStmt, err := tx.Prepare(`insert or ignore into games(appid, name, created_at, updated_at) values(?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer gameStmt.Close()
	for _, id := range appIDs {
		if _, err := gameStmt.Exec(id, "Bulk", now, now); err != nil {
			t.Fatal(err)
		}
	}
	if profileID != 0 {
		entryStmt, err := tx.Prepare(`insert into profile_games(profile_id, appid, groupid) values(?,?,0)`)
		if err != nil {
			t.Fatal(err)
		}
		defer entryStmt.Close()
		for _, id := range appIDs {
			if _, err := entryStmt.Exec(profileID, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func bulkAppIDs(from, n int64) []int64 {
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = from + int64(i)
	}
	return ids
}

// Empty and nil lists must encode as "[]", not "null": json_each('null') yields a NULL row, and `not in` against it
// matches nothing.
func TestInt64sJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		ids  []int64
		want string
	}{
		{"nil", nil, "[]"},
		{"empty", []int64{}, "[]"},
		{"one", []int64{42}, "[42]"},
		{"several", []int64{1, 2, 3}, "[1,2,3]"},
	} {
		got, err := int64sJSON(tc.ids)
		if err != nil {
			t.Fatalf("%s: int64sJSON error = %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("%s: int64sJSON = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestDisableMissingOwnedGamesWithLargeLibrary(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	// Apps 1 and 2 are not in the owned list; the 40k others are.
	seedGameSync(t, repo, profile.ID, 1)
	seedGameSync(t, repo, profile.ID, 2)
	owned := bulkAppIDs(100000, largeLibrarySize)
	if err := repo.DisableMissingOwnedGames(profile.ID, owned); err != nil {
		t.Fatalf("DisableMissingOwnedGames(40k owned): %v", err)
	}
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid in (1,2) and disabled=1 and disabled_reason='not_owned'`, 2, profile.ID)

	// Re-own app 1 in the same large list: only its not_owned flag clears; app 2 stays disabled.
	owned = append(owned, 1)
	if err := repo.DisableMissingOwnedGames(profile.ID, owned); err != nil {
		t.Fatalf("DisableMissingOwnedGames(40k owned, re-own 1): %v", err)
	}
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid=1 and disabled=1`, 0, profile.ID)
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid=2 and disabled=1 and disabled_reason='not_owned'`, 1, profile.ID)
}

func TestClearProfileDataWithLargeLibrary(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	appIDs := bulkAppIDs(1, largeLibrarySize)
	seedBulkGames(t, repo, profile.ID, appIDs)
	// The other profile shares the first 10 apps, so those must survive the clear.
	seedBulkGames(t, repo, other.ID, appIDs[:10])

	removed, err := repo.ClearProfileData(profile.ID)
	if err != nil {
		t.Fatalf("ClearProfileData(40k games): %v", err)
	}
	// Covers are only cached for displayed DLC images; none were seeded, so nothing is returned.
	if len(removed) != 0 {
		t.Fatalf("removed cover ids = %d, want 0", len(removed))
	}
	assertCount(t, repo, `select count(*) from profile_games where profile_id=?`, 0, profile.ID)
	assertCount(t, repo, `select count(*) from games`, 10)
	assertCount(t, repo, `select count(*) from profile_games where profile_id=?`, 10, other.ID)
}

func TestPruneStaleEntriesWithLargeKeepList(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	const appID = 3
	seedBulkGames(t, repo, 0, []int64{appID})
	tx, err := repo.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// Groups 1..40000 are kept; group 0 is stale and must be pruned.
	stmt, err := tx.Prepare(`insert into profile_games(profile_id, appid, groupid) values(?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	if _, err := stmt.Exec(profile.ID, appID, 0); err != nil {
		t.Fatal(err)
	}
	keep := bulkAppIDs(1, largeLibrarySize)
	for _, g := range keep {
		if _, err := stmt.Exec(profile.ID, appID, g); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneStaleEntries(tx, profile.ID, appID, keep); err != nil {
		t.Fatalf("pruneStaleEntries(40k keep): %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=?`, largeLibrarySize, profile.ID, appID)
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=? and groupid=0`, 0, profile.ID, appID)
}
