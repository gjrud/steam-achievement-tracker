package repository

import (
	"database/sql"
	"strings"
	"testing"
)

// queryPlan returns the EXPLAIN QUERY PLAN detail lines of query.
func queryPlan(t *testing.T, conn *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := conn.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain %s: %v", query, err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return details
}

// assertSubqueryUsesIndex fails unless the plan has a line "SEARCH <alias> USING [COVERING] INDEX <index>", so each
// NOT EXISTS subquery of a statement is checked by its own table alias rather than by any line naming the index.
func assertSubqueryUsesIndex(t *testing.T, conn *sql.DB, name, query, alias, index string, args ...any) {
	t.Helper()
	details := queryPlan(t, conn, query, args...)
	prefix := "SEARCH " + alias + " "
	for _, d := range details {
		if strings.HasPrefix(d, prefix) && strings.Contains(d, "USING") && strings.Contains(d, index) {
			return
		}
	}
	t.Fatalf("%s: plan %q has no %q line using %s", name, details, prefix, index)
}

// The orphan deletes that ClearProfileData runs (cleanup.go) must use the migration 4 indexes. The SQL is the shipped
// constant, bound the way deleteOrphanRowsForAppIDs binds it (one JSON array of app ids).
func TestCleanupQueriesUseIndexes(t *testing.T) {
	repo := testRepository(t)
	idsJSON, err := int64sJSON([]int64{42})
	if err != nil {
		t.Fatal(err)
	}

	assertSubqueryUsesIndex(t, repo.db, "achievements orphan check", deleteOrphanAchievementsSQL,
		"pa", "idx_profile_achievements_appid_apiname", idsJSON)

	assertSubqueryUsesIndex(t, repo.db, "game_sets orphan check", deleteOrphanGameSetsSQL,
		"pg", "idx_profile_games_appid", idsJSON)

	// The SaveGameSync prune binds (appid, keep JSON array of groupids), not an app id list.
	keepJSON, err := int64sJSON([]int64{0, 5})
	if err != nil {
		t.Fatal(err)
	}
	assertSubqueryUsesIndex(t, repo.db, "game_sets prune check", pruneStaleGameSetsSQL,
		"pg", "idx_profile_games_appid", int64(42), keepJSON)

	// One assertion per NOT EXISTS subquery of deleteOrphanGamesSQL, by alias.
	for _, sub := range []struct{ alias, index string }{
		{"pg", "idx_profile_games_appid"},
		{"pa", "idx_profile_achievements_appid_apiname"},
		{"f", "idx_profile_game_flags_appid"},
		{"po", "idx_profile_owned_games_appid"},
		{"t", "idx_profile_game_tags_appid"},
		{"s", "idx_game_snapshots_appid"},
	} {
		assertSubqueryUsesIndex(t, repo.db, "games orphan check "+sub.alias, deleteOrphanGamesSQL, sub.alias, sub.index, idsJSON)
	}
}
