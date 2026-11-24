package db

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// seedV3Database writes a database at version 3 by applying the real migrations up to version 3 through applyMigration,
// the same code path the app uses. It has the schema and bookkeeping but no rows; callers add rows. The file is closed
// before OpenAndMigrate runs.
func seedV3Database(t *testing.T, path string) {
	t.Helper()
	raw := openRawDB(t, path)
	tx, err := raw.Begin()
	if err != nil {
		t.Fatalf("begin v3 seed: %v", err)
	}
	defer tx.Rollback()
	if err := ensureMigrationsTable(tx); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, m := range migrations {
		if m.version > 3 {
			continue
		}
		if err := applyMigration(tx, m); err != nil {
			t.Fatalf("seed v3 at migration %d: %v", m.version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v3 seed: %v", err)
	}
	assertUserVersion(t, raw, 3)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
}

// seedProfilesAndGames adds two profiles and the games the snapshot seeds refer to. The raw seed connection does not
// enforce foreign keys, but the rows are valid anyway.
func seedProfilesAndGames(t *testing.T, raw *sql.DB) {
	t.Helper()
	if _, err := raw.Exec(`
		INSERT INTO profiles(id, steam_id64, display_name, created_at, updated_at) VALUES
			(1, '11111111111111111', 'User One', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
			(2, '22222222222222222', 'User Two', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO games(appid, name, created_at, updated_at) VALUES
			(42, 'Game 42', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
			(43, 'Game 43', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
			(44, 'Game 44', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
			(45, 'Game 45', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
	`); err != nil {
		t.Fatalf("seed profiles and games: %v", err)
	}
}

// snapshotSeed is one game_snapshots row in insertion order (id order).
type snapshotSeed struct {
	profile, appid, groupid int64
	completed               bool
}

// seedDuplicateSnapshots writes snapshot history with heavy duplicates: a group completed and then incomplete, a group
// never completed, a DLC group completed many times, a group that goes complete, incomplete, complete, incomplete, and
// a second profile sharing an appid. Each row has a distinct total so the latest row is identifiable.
func seedDuplicateSnapshots(t *testing.T, path string) {
	t.Helper()
	var seq []snapshotSeed
	add := func(n int, s snapshotSeed) {
		for i := 0; i < n; i++ {
			seq = append(seq, s)
		}
	}
	add(3, snapshotSeed{1, 42, 0, true})
	add(5, snapshotSeed{1, 42, 0, false})  // completed, then incomplete: keep rows 3 and 8
	add(10, snapshotSeed{1, 43, 0, false}) // never completed: keep the last row
	add(4, snapshotSeed{1, 44, 1, true})   // DLC group, completed repeatedly
	seq = append(seq,
		snapshotSeed{1, 44, 0, true},
		snapshotSeed{1, 44, 0, false},
		snapshotSeed{1, 44, 0, true},
		snapshotSeed{1, 44, 0, false}) // complete, incomplete, complete, incomplete: keep the 3rd and 4th
	add(5, snapshotSeed{2, 42, 0, true})
	add(3, snapshotSeed{2, 43, 0, false})
	seq = append(seq, snapshotSeed{2, 43, 0, true}) // latest row is also the latest completed row

	raw := openRawDB(t, path)
	seedProfilesAndGames(t, raw)
	tx, err := raw.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO game_snapshots(profile_id, appid, groupid, total_achievements, unlocked_achievements, completion_percent, missing_avg_unlock, is_completed, synced_at)
		VALUES(?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	for i, s := range seq {
		completed := 0
		if s.completed {
			completed = 1
		}
		if _, err := stmt.Exec(s.profile, s.appid, s.groupid, 100+i, i%11, 50.0, nil, completed, "2026-01-01T00:00:00Z"); err != nil {
			t.Fatalf("insert snapshot %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
}

// readerSnapshotKeys are the (profile, appid, groupid) entries the readers ask about. The last one has no rows.
var readerSnapshotKeys = [][3]int64{{1, 42, 0}, {1, 43, 0}, {1, 44, 0}, {1, 44, 1}, {2, 42, 0}, {2, 43, 0}, {2, 45, 0}}

// snapshotReaderAnswers collects what the snapshot readers can observe: whether a completed row exists, whether any row
// exists, the latest row's values (what a dedupe check compares against), the appids per profile, and whether an appid
// has any row. Nothing else in the app reads game_snapshots, so equal answers mean the readers see the same behavior.
func snapshotReaderAnswers(t *testing.T, conn *sql.DB) map[string]string {
	t.Helper()
	answers := map[string]string{}
	for _, k := range readerSnapshotKeys {
		var completed, any int
		if err := conn.QueryRow(`select exists(select 1 from game_snapshots where profile_id=? and appid=? and groupid=? and is_completed=1)`, k[0], k[1], k[2]).Scan(&completed); err != nil {
			t.Fatalf("completed reader %v: %v", k, err)
		}
		if err := conn.QueryRow(`select exists(select 1 from game_snapshots where profile_id=? and appid=? and groupid=?)`, k[0], k[1], k[2]).Scan(&any); err != nil {
			t.Fatalf("any-row reader %v: %v", k, err)
		}
		var latest string
		var total, unlocked, done int
		err := conn.QueryRow(`select total_achievements, unlocked_achievements, is_completed from game_snapshots where profile_id=? and appid=? and groupid=? order by id desc limit 1`, k[0], k[1], k[2]).Scan(&total, &unlocked, &done)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			latest = "none"
		case err != nil:
			t.Fatalf("latest reader %v: %v", k, err)
		default:
			latest = fmt.Sprintf("%d/%d/%d", total, unlocked, done)
		}
		prefix := fmt.Sprintf("%d/%d/%d", k[0], k[1], k[2])
		answers["completed "+prefix] = fmt.Sprint(completed)
		answers["any "+prefix] = fmt.Sprint(any)
		answers["latest "+prefix] = latest
	}
	for _, profile := range []int64{1, 2} {
		rows, err := conn.Query(`select distinct appid from game_snapshots where profile_id=? order by appid`, profile)
		if err != nil {
			t.Fatalf("appid reader: %v", err)
		}
		var ids []string
		for rows.Next() {
			var appid int64
			if err := rows.Scan(&appid); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, fmt.Sprint(appid))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		answers[fmt.Sprintf("appids profile %d", profile)] = strings.Join(ids, ",")
	}
	for _, appid := range []int64{42, 43, 44, 45} {
		var any int
		if err := conn.QueryRow(`select exists(select 1 from game_snapshots where appid=?)`, appid).Scan(&any); err != nil {
			t.Fatalf("app reader %d: %v", appid, err)
		}
		answers[fmt.Sprintf("app %d has rows", appid)] = fmt.Sprint(any)
	}
	return answers
}

// snapshotIDs returns the game_snapshots ids in order.
func snapshotIDs(t *testing.T, conn *sql.DB) []int64 {
	t.Helper()
	rows, err := conn.Query(`select id from game_snapshots order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// Migration 4 on a version 3 database with heavy duplicates: it keeps exactly the latest row and the latest completed
// row of each group, and every reader answer is the same before and after.
func TestMigration4PrunesSnapshotsAndPreservesReaderAnswers(t *testing.T) {
	paths := testPaths(t)
	seedV3Database(t, paths.DB)
	seedDuplicateSnapshots(t, paths.DB)

	raw := openRawDB(t, paths.DB)
	before := snapshotReaderAnswers(t, raw)
	assertCount(t, raw, `select count(*) from game_snapshots`, 35)
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	// Migration 4 is version 4 by definition; the literal is the point of this test.
	assertUserVersion(t, conn, 4)
	assertMigration(t, conn, 4)
	assertCount(t, conn, `select count(*) from schema_migrations where version=4 and name='add_lookup_indexes_and_prune_snapshots'`, 1)

	// Kept: (1,42,0) rows 3 and 8; (1,43,0) row 18; (1,44,1) row 22; (1,44,0) rows 25 and 26; (2,42,0) row 31; (2,43,0) row 35.
	wantIDs := []int64{3, 8, 18, 22, 25, 26, 31, 35}
	if got := snapshotIDs(t, conn); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("surviving snapshot ids = %v, want %v", got, wantIDs)
	}

	after := snapshotReaderAnswers(t, conn)
	if !reflect.DeepEqual(before, after) {
		for k, want := range before {
			if got := after[k]; got != want {
				t.Errorf("reader answer %q changed: before %q, after %q", k, want, got)
			}
		}
		t.Fatal("reader answers differ after migration 4")
	}
}

// Migration 4's SQL is idempotent: running it again on the migrated database deletes nothing, and reopening the
// database runs no migration (no second backup).
func TestMigration4IsIdempotent(t *testing.T) {
	paths := testPaths(t)
	seedV3Database(t, paths.DB)
	seedDuplicateSnapshots(t, paths.DB)

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	ids := snapshotIDs(t, conn)

	res, err := conn.Exec(pruneSnapshotsSQL)
	if err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 0 {
		t.Fatalf("second prune deleted %d rows, want 0", n)
	}
	if _, err := conn.Exec(lookupIndexesAndPruneSnapshotsSQL); err != nil {
		t.Fatalf("re-running the whole migration SQL: %v", err)
	}
	if got := snapshotIDs(t, conn); !reflect.DeepEqual(got, ids) {
		t.Fatalf("snapshot ids changed on re-run: %v, want %v", got, ids)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	conn, err = OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("second OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()
	assertUserVersion(t, conn, latestVersion())
	if n := len(backupFiles(t, paths)); n != 1 {
		t.Fatalf("backup count after reopening a v4 database = %d, want 1 (the pre-migration backup only)", n)
	}
	if got := snapshotIDs(t, conn); !reflect.DeepEqual(got, ids) {
		t.Fatalf("snapshot ids changed on reopen: %v, want %v", got, ids)
	}
}

// migration4Indexes are the indexes migration 4 creates, by name.
var migration4Indexes = []string{
	"idx_profile_achievements_appid_apiname",
	"idx_profile_games_appid",
	"idx_profile_owned_games_appid",
	"idx_profile_game_flags_appid",
	"idx_profile_game_tags_appid",
	"idx_game_snapshots_appid",
	"idx_game_snapshots_profile_id_appid_groupid_is_completed",
	"idx_game_sets_dlcappid",
	"idx_sync_runs_profile_id",
}

func TestMigration4CreatesLookupIndexes(t *testing.T) {
	paths := testPaths(t)
	seedV3Database(t, paths.DB)
	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()
	for _, name := range migration4Indexes {
		assertCount(t, conn, `select count(*) from sqlite_master where type='index' and name=?`, 1, name)
	}
}

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

// assertSearchesIndex fails unless the plan of query searches an index named index (SEARCH ... USING [COVERING] INDEX).
func assertSearchesIndex(t *testing.T, conn *sql.DB, name, query, index string, args ...any) {
	t.Helper()
	details := queryPlan(t, conn, query, args...)
	for _, d := range details {
		if strings.HasPrefix(d, "SEARCH") && strings.Contains(d, "USING") && strings.Contains(d, index) {
			return
		}
	}
	t.Fatalf("%s: plan %q does not SEARCH via %s", name, details, index)
}

// The queries below are copied from internal/repository (the SQL text, not the call), so a change there that drops
// the index use shows up here. The index names are the migration 4 ones.
func TestLookupQueriesUseIndexes(t *testing.T) {
	paths := testPaths(t)
	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	// The cleanup.go orphan deletes (achievements, games) are checked in internal/repository, against the shipped SQL.

	// games.go MarkGamePreviouslyCompleted: the UPDATE's snapshot subqueries (15 arguments).
	markQuery := `update profile_games set
		was_completed=case when is_completed=1 or exists(select 1 from game_snapshots where profile_id=? and appid=? and groupid=? and is_completed=1) or exists(select 1 from profile_game_flags where profile_id=? and appid=? and groupid=? and manual_was_completed=1) then 1 else 0 end,
		newly_incomplete=case when is_completed=0 and (exists(select 1 from game_snapshots where profile_id=? and appid=? and groupid=? and is_completed=1) or exists(select 1 from profile_game_flags where profile_id=? and appid=? and groupid=? and manual_was_completed=1)) then 1 else 0 end
		where profile_id=? and appid=? and groupid=?`
	markArgs := []any{1, 42, 0, 1, 42, 0, 1, 42, 0, 1, 42, 0, 1, 42, 0}
	assertSearchesIndex(t, conn, "MarkGamePreviouslyCompleted", markQuery, "idx_game_snapshots_profile_id_appid_groupid_is_completed", markArgs...)

	// dashboard.go profileGameHistory.
	assertSearchesIndex(t, conn, "profileGameHistory",
		`select coalesce(f.manual_was_completed,0), coalesce(f.missing_achievements_in_dlc,0), coalesce(f.disabled,0),
		exists(select 1 from game_snapshots s where s.profile_id=? and s.appid=? and s.groupid=? and s.is_completed=1)
		from (select 1) x
		left join profile_game_flags f on f.profile_id=? and f.appid=? and f.groupid=?`,
		"idx_game_snapshots_profile_id_appid_groupid_is_completed", 1, 42, 0, 1, 42, 0)

	// sets.go HasSetData: the profile_games half uses the appid index.
	assertSearchesIndex(t, conn, "HasSetData",
		`select exists(select 1 from game_sets where appid=? and groupid<>0) or exists(select 1 from profile_games where appid=? and groupid<>0)`,
		"idx_profile_games_appid", 42, 42)

	// profiles.go ClearProfileData: per-profile snapshot delete.
	assertSearchesIndex(t, conn, "ClearProfileData snapshots",
		`delete from game_snapshots where profile_id=?`,
		"idx_game_snapshots_profile_id_appid_groupid_is_completed", 1)

	// sync_runs.go LatestSyncRun.
	assertSearchesIndex(t, conn, "LatestSyncRun",
		`select id, profile_id, started_at, finished_at, status, games_total, games_synced, games_failed, error from sync_runs where profile_id=? order by id desc limit 1`,
		"idx_sync_runs_profile_id", 1)
}

// seedMillionSnapshots fills a version 3 database with 1,000,000 game_snapshots rows over 2 profiles and 1000 apps
// (2000 groups of 500 rows each, about one row in seven completed).
func seedMillionSnapshots(t *testing.T, path string) {
	t.Helper()
	raw := openRawDB(t, path)
	seedProfilesAndGames(t, raw)
	if _, err := raw.Exec(`
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i < 999)
		INSERT INTO games(appid, name, created_at, updated_at)
			SELECT 1000 + i, 'Game', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z' FROM n;
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i < 999999)
		INSERT INTO game_snapshots(profile_id, appid, groupid, total_achievements, unlocked_achievements, completion_percent, missing_avg_unlock, is_completed, synced_at)
			SELECT 1 + i % 2, 1000 + (i / 2) % 1000, 0, 10 + i, i % 11, 50.0, NULL, CASE WHEN i % 7 = 0 THEN 1 ELSE 0 END, '2026-01-01T00:00:00Z' FROM n;
	`); err != nil {
		t.Fatalf("seed 1M snapshots: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
}

// Startup cost of migration 4 with 1M snapshot rows. It runs before the window opens (app.go). The migration SQL alone
// is timed in a transaction that is rolled back, then OpenAndMigrate is timed (pre-migration backup plus migration). Run
// with -v for the numbers.
func TestMigration4TimingAtOneMillionSnapshots(t *testing.T) {
	if testing.Short() {
		t.Skip("1M-row migration timing is skipped under -short")
	}
	paths := testPaths(t)
	seedV3Database(t, paths.DB)
	seedMillionSnapshots(t, paths.DB)

	raw := openRawDB(t, paths.DB)
	assertCount(t, raw, `select count(*) from game_snapshots`, 1000000)
	tx, err := raw.Begin()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := tx.Exec(lookupIndexesAndPruneSnapshotsSQL); err != nil {
		tx.Rollback()
		t.Fatalf("migration 4 SQL: %v", err)
	}
	sqlOnly := time.Since(start)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("migration 4 SQL (prune + indexes) at 1M snapshot rows: %s", sqlOnly)

	start = time.Now()
	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()
	t.Logf("OpenAndMigrate at 1M snapshot rows (backup + migration 4): %s", time.Since(start))

	assertUserVersion(t, conn, latestVersion())
	// 2000 groups, each keeps its latest row plus at most one completed row: at least 2000 and at most 4000 rows.
	var kept int
	if err := conn.QueryRow(`select count(*) from game_snapshots`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	t.Logf("snapshot rows kept after migration 4: %d", kept)
	if kept < 2000 || kept > 4000 {
		t.Fatalf("snapshot rows after migration = %d, want 2000..4000", kept)
	}
	if n := len(backupFiles(t, paths)); n != 1 {
		t.Fatalf("backup count = %d, want 1", n)
	}
}
