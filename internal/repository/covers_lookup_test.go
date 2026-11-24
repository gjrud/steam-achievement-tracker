package repository

import (
	"cmp"
	"slices"
	"strings"
	"testing"
)

// seedCoverLookupFixture writes the rows the cover lookups must classify, the way a sync writes them.
//
// Shared appids: app 100 is a base entry of both profiles.
// DLC bindings: 200 is bound by a set of app 100 (p1) and a set of app 400 (p2); 100 is bound by a set of app 300 (p1);
// 600 is bound to itself (p2); 850 is bound by a set of app 800, whose profile_games rows are then removed.
// Plain set: app 500 has a set with dlcappid 0.
// Cover rows 100, 200, 300, 400, 500, 600 are displayed by some entry; 700, 800, 850 and 999 are not.
func seedCoverLookupFixture(t *testing.T, repo *Repository) (p1, p2 int64) {
	t.Helper()
	p1 = saveTestProfile(t, repo, "11111111111111111", "User One").ID
	p2 = saveTestProfile(t, repo, "22222222222222222", "User Two").ID

	saveSeedGame(t, repo, p1, 100, "Shared Base", 0,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50},
		dashboardSeed{GroupID: 1, DLCAppID: 200, SetName: "Pack", Total: 1})
	saveSeedGame(t, repo, p1, 300, "Bound To Base", 0,
		dashboardSeed{GroupID: 0, Total: 1},
		dashboardSeed{GroupID: 1, DLCAppID: 100, SetName: "Bound", Total: 1})
	saveSeedGame(t, repo, p1, 500, "Plain Sets", 0,
		dashboardSeed{GroupID: 0, Total: 1},
		dashboardSeed{GroupID: 1, DLCAppID: 0, SetName: "Plain", Total: 1})

	saveSeedGame(t, repo, p2, 100, "Shared Base", 0,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})
	saveSeedGame(t, repo, p2, 400, "DLC Owner", 0,
		dashboardSeed{GroupID: 0, Total: 1},
		dashboardSeed{GroupID: 1, DLCAppID: 200, SetName: "Pack", Total: 1})
	saveSeedGame(t, repo, p2, 600, "Self Bound", 0,
		dashboardSeed{GroupID: 0, Total: 1},
		dashboardSeed{GroupID: 1, DLCAppID: 600, SetName: "Self", Total: 1})
	saveSeedGame(t, repo, p2, 800, "Gone Set", 0,
		dashboardSeed{GroupID: 1, DLCAppID: 850, SetName: "Gone", Total: 1})
	// game_sets(800, 1) keeps dlcappid 850 but no profile_games row references it any more.
	if _, err := repo.db.Exec(`delete from profile_games where appid=800`); err != nil {
		t.Fatal(err)
	}

	for _, id := range []int64{100, 200, 300, 400, 500, 600, 700, 800, 850, 999} {
		mustSaveLibraryCover(t, repo, id)
	}
	return p1, p2
}

func sortProfileEntries(entries []profileEntry) {
	slices.SortFunc(entries, func(a, b profileEntry) int {
		return cmp.Or(
			cmp.Compare(a.ProfileID, b.ProfileID),
			cmp.Compare(a.AppID, b.AppID),
			cmp.Compare(a.GroupID, b.GroupID),
		)
	})
}

// lookupEntriesForImageApp runs entriesForImageApp in a transaction, the way SaveCover does.
func lookupEntriesForImageApp(t *testing.T, repo *Repository, imageAppID int64) []profileEntry {
	t.Helper()
	tx, err := repo.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	entries, err := entriesForImageApp(tx, imageAppID)
	if err != nil {
		t.Fatalf("entriesForImageApp(%d): %v", imageAppID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	sortProfileEntries(entries)
	return entries
}

// TestEntriesForImageAppMatchesDisplayRules pins the entries whose cover may change for each image id. An entry
// matches when its store-art owner (DLC id when bound, else app id) is the image id, or when its app id is the image
// id (DLC entries fall back to the game cover). Each row must appear once.
func TestEntriesForImageAppMatchesDisplayRules(t *testing.T) {
	repo := testRepository(t)
	p1, p2 := seedCoverLookupFixture(t, repo)

	cases := []struct {
		name    string
		imageID int64
		want    []profileEntry
	}{
		{"base game and DLC entries of its appid, and a set bound to it", 100, []profileEntry{
			{p1, 100, 0}, {p1, 100, 1}, {p1, 300, 1}, {p2, 100, 0},
		}},
		{"DLC bound to 200 in two games", 200, []profileEntry{
			{p1, 100, 1}, {p2, 400, 1},
		}},
		{"game with a base and a bound set", 300, []profileEntry{
			{p1, 300, 0}, {p1, 300, 1},
		}},
		{"game 400 entries, one bound to 200", 400, []profileEntry{
			{p2, 400, 0}, {p2, 400, 1},
		}},
		{"self-bound DLC: the row matches by both rules but appears once", 600, []profileEntry{
			{p2, 600, 0}, {p2, 600, 1},
		}},
		{"plain set (dlcappid 0) is not an owner of id 0", 0, nil},
		{"plain set owns its appid only", 500, []profileEntry{
			{p1, 500, 0}, {p1, 500, 1},
		}},
		{"set row without profile_games is not an entry", 850, nil},
		{"no entry at all", 999, nil},
		{"app whose profile_games rows were removed", 800, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := lookupEntriesForImageApp(t, repo, tc.imageID)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("entriesForImageApp(%d) = %v, want %v", tc.imageID, got, tc.want)
			}
		})
	}
}

// TestDeleteOrphanCoverRowsKeepsDisplayedCovers removes only cover rows that no profile_games entry displays.
// A game's cover stays while any entry of that game remains, and a DLC cover stays while an entry is bound to it.
func TestDeleteOrphanCoverRowsKeepsDisplayedCovers(t *testing.T) {
	repo := testRepository(t)
	seedCoverLookupFixture(t, repo)

	ids := []int64{100, 200, 300, 400, 500, 600, 700, 800, 850, 999}
	tx, err := repo.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := deleteOrphanCoverRows(tx, ids)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if want := []int64{700, 800, 850, 999}; !slices.Equal(deleted, want) {
		t.Fatalf("deleted cover ids = %v, want %v", deleted, want)
	}

	rows, err := repo.db.Query(`select image_appid from cover_images order by image_appid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var remaining []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		remaining = append(remaining, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []int64{100, 200, 300, 400, 500, 600}; !slices.Equal(remaining, want) {
		t.Fatalf("remaining cover rows = %v, want %v", remaining, want)
	}
}

func TestDeleteOrphanCoverRowsEmptyInput(t *testing.T) {
	repo := testRepository(t)
	tx, err := repo.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	deleted, err := deleteOrphanCoverRows(tx, nil)
	if err != nil || deleted != nil {
		t.Fatalf("deleteOrphanCoverRows(nil) = %v, %v; want nil, nil", deleted, err)
	}
}

// assertNoFullScan fails when the plan scans any of the given table aliases.
func assertNoFullScan(t *testing.T, name string, details []string, aliases ...string) {
	t.Helper()
	for _, d := range details {
		for _, alias := range aliases {
			if d == "SCAN "+alias || strings.HasPrefix(d, "SCAN "+alias+" ") {
				t.Fatalf("%s: plan %q scans %s", name, details, alias)
			}
		}
	}
}

// The cover lookups must not scan profile_games or game_sets. The SQL is the shipped constant, bound the way the
// production code binds it.
func TestCoverLookupQueriesUseIndexes(t *testing.T) {
	repo := testRepository(t)

	// entriesForImageAppSQL: branch 1 searches pg by appid; branch 2 searches gs by dlcappid, then pg by appid.
	entries := queryPlan(t, repo.db, entriesForImageAppSQL, int64(100), int64(100))
	assertPlanLine(t, "entries branch 1", entries, "SEARCH pg USING INDEX idx_profile_games_appid")
	assertPlanLine(t, "entries branch 2", entries, "SEARCH gs USING INDEX idx_game_sets_dlcappid")
	assertNoFullScan(t, "entries", entries, "pg", "gs")

	// orphanCoverRowsSQL: both NOT EXISTS subqueries are index searches. Each one searches pg by appid (the appid
	// check directly, the DLC entry check through the join), so the plan needs two such lines.
	orphans := queryPlan(t, repo.db, orphanCoverRowsSQL, "[100]")
	assertPlanLine(t, "orphan dlcappid check", orphans, "SEARCH gs USING INDEX idx_game_sets_dlcappid")
	assertPlanSearchCount(t, "orphan pg checks", orphans, "pg", "idx_profile_games_appid", 2)
	assertNoFullScan(t, "orphan", orphans, "pg", "gs")
}

// assertPlanLine fails unless some plan line starts with want.
func assertPlanLine(t *testing.T, name string, details []string, want string) {
	t.Helper()
	for _, d := range details {
		if strings.HasPrefix(d, want) {
			return
		}
	}
	t.Fatalf("%s: plan %q has no line %q", name, details, want)
}

// assertPlanSearchCount fails unless the plan has at least want lines that SEARCH alias through index. Counting
// lines lets each subquery that searches alias be checked on its own: one line can't satisfy two of them. The line
// may or may not say COVERING (SQLite's wording depends on the selected columns and version).
func assertPlanSearchCount(t *testing.T, name string, details []string, alias, index string, want int) {
	t.Helper()
	prefix := "SEARCH " + alias + " "
	n := 0
	for _, d := range details {
		if strings.HasPrefix(d, prefix) && strings.Contains(d, " INDEX "+index) {
			n++
		}
	}
	if n < want {
		t.Fatalf("%s: plan %q has %d SEARCH %s lines using index %s, want at least %d", name, details, n, alias, index, want)
	}
}
