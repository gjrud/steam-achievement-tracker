package db

import (
	"database/sql"
	"path/filepath"
	"sort"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
)

// currentTables is the exact table list of a fresh database (sqlite_ internal tables excluded).
var currentTables = []string{
	"achievements", "cover_images", "game_sets", "game_snapshots", "games", "profile_achievements",
	"profile_game_flags", "profile_game_tags", "profile_games", "profile_library_settings", "profile_owned_games",
	"profiles", "schema_migrations", "settings", "sync_runs",
}

var gamesColumns = []string{"appid", "name", "has_achievements", "achievement_status", "created_at", "updated_at"}

func TestOpenAndMigrateFreshDB(t *testing.T) {
	paths := testPaths(t)
	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, latestVersion())
	assertAllMigrationsRecorded(t, conn)
	assertCount(t, conn, `select count(*) from schema_migrations`, latestVersion())
	assertCount(t, conn, `select count(*) from schema_migrations where version=1 and name='initial_schema'`, 1)
	assertTableList(t, conn, currentTables)
	assertColumns(t, conn, "games", gamesColumns)
	assertColumns(t, conn, "cover_images", coverImagesColumns)
}

func TestFreshSchemaDefaultsAndKeys(t *testing.T) {
	paths := testPaths(t)
	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	if _, err := conn.Exec(`insert into profiles(id, steam_id64, display_name, created_at, updated_at) values(1, '11111111111111111', 'User One', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		insert into games(appid, name, created_at, updated_at) values(42, 'Game', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		insert into profile_games(profile_id, appid, groupid) values(1, 42, 0);
		insert into profile_owned_games(profile_id, appid, updated_at) values(1, 42, '2026-01-01T00:00:00Z');
		insert into profile_library_settings(profile_id, updated_at) values(1, '2026-01-01T00:00:00Z');`); err != nil {
		t.Fatalf("insert baseline rows: %v", err)
	}
	assertCount(t, conn, `select count(*) from profile_games where profile_id=1 and appid=42 and groupid=0 and dlc_not_owned=0`, 1)
	assertCount(t, conn, `select count(*) from profile_owned_games where profile_id=1 and appid=42 and vetted=1 and purchased=0 and playtime_forever=0`, 1)
	assertCount(t, conn, `select count(*) from profile_library_settings where profile_id=1 and include_unvetted_apps=1 and include_played_free_games=1`, 1)
	assertCount(t, conn, `select count(*) from games where appid=42 and has_achievements=0 and achievement_status='unknown'`, 1)

	// Composite key: a sibling entry with another groupid is accepted, a duplicate of an existing entry is not.
	if _, err := conn.Exec(`insert into profile_games(profile_id, appid, groupid) values(1, 42, 1)`); err != nil {
		t.Fatalf("insert sibling entry: %v", err)
	}
	if _, err := conn.Exec(`insert into profile_games(profile_id, appid, groupid) values(1, 42, 0)`); err == nil {
		t.Fatal("duplicate profile entry accepted, want primary key violation")
	}
	// Foreign keys are enforced: library settings for a missing profile are rejected.
	if _, err := conn.Exec(`insert into profile_library_settings(profile_id, updated_at) values(999, '2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("library settings for missing profile accepted, want foreign key failure")
	}
}

func TestOpenAndMigrateSkipsNoopBackup(t *testing.T) {
	paths := testPaths(t)
	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if entries := backupFiles(t, paths); len(entries) != 0 {
		t.Fatalf("backup count after fresh migration = %d, want 0", len(entries))
	}

	conn, err = OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("second OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()
	if entries := backupFiles(t, paths); len(entries) != 0 {
		t.Fatalf("backup count after noop migration = %d, want 0", len(entries))
	}
}

// A database with tables but no schema_migrations table is version 0: the baseline is applied and the existing
// file is backed up first, because it already holds user tables.
func TestOpenAndMigrateUnversionedDBWithTablesIsBackedUp(t *testing.T) {
	paths := testPaths(t)
	raw := openRawDB(t, paths.DB)
	if _, err := raw.Exec(`CREATE TABLE other(id integer primary key); INSERT INTO other(id) VALUES(7);`); err != nil {
		t.Fatalf("seed unversioned db: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, latestVersion())
	assertAllMigrationsRecorded(t, conn)
	assertTable(t, conn, "profile_games")
	assertCount(t, conn, `select count(*) from other where id=7`, 1)
	entries := backupFiles(t, paths)
	if len(entries) != 1 {
		t.Fatalf("backup count = %d, want 1", len(entries))
	}
	backup := openRawDB(t, entries[0])
	assertUserVersion(t, backup, 0)
	if mustTableExists(t, backup, "profile_games") {
		t.Fatal("backup has the baseline tables, want the pre-migration snapshot")
	}
}

// An existing version 1 database is backed up before a later migration runs, and the backup holds the original data.
func TestOpenAndMigrateExistingDBBackupContainsOriginalData(t *testing.T) {
	paths := testPaths(t)
	seedV1Database(t, paths.DB)
	withExtraMigration(t, `CREATE TABLE migration_probe(id integer primary key);`)

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 2)
	assertMigration(t, conn, 2)
	assertTable(t, conn, "migration_probe")
	assertCount(t, conn, `select count(*) from profiles where steam_id64='11111111111111111' and display_name='User One'`, 1)
	assertCount(t, conn, `select count(*) from profile_games where profile_id=1 and appid=42 and total_achievements=10 and unlocked_achievements=7`, 1)

	entries := backupFiles(t, paths)
	if len(entries) != 1 {
		t.Fatalf("backup count = %d, want 1", len(entries))
	}
	backup, err := sql.Open("sqlite3", entries[0])
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backup.Close()
	assertUserVersion(t, backup, 1)
	if mustTableExists(t, backup, "migration_probe") {
		t.Fatal("backup has migration_probe, want pre-migration snapshot")
	}
	assertCount(t, backup, `select count(*) from profiles where steam_id64='11111111111111111' and display_name='User One'`, 1)
	assertCount(t, backup, `select count(*) from profile_games where profile_id=1 and appid=42 and total_achievements=10 and unlocked_achievements=7`, 1)
}

// seedV1Database writes a populated database at version 1 with the baseline schema, as an installed app leaves it.
// The file is closed before OpenAndMigrate runs.
func seedV1Database(t *testing.T, path string) {
	t.Helper()
	raw := openRawDB(t, path)
	if _, err := raw.Exec(initialSchemaSQL + schemaMigrationsSQL + `
		INSERT INTO schema_migrations(version, name, applied_at) VALUES(1, 'initial_schema', '2026-01-01T00:00:00Z');
		PRAGMA user_version = 1;
		INSERT INTO profiles(id, steam_id64, display_name, created_at, updated_at) VALUES(1, '11111111111111111', 'User One', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO games(appid, name, has_achievements, achievement_status, created_at, updated_at) VALUES
			(42, 'Shared Game', 1, 'ok', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
			(43, 'Solo Game', 1, 'ok', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO profile_games(profile_id, appid, groupid, total_achievements, unlocked_achievements, completion_percent, is_completed, last_synced_at) VALUES
			(1, 42, 0, 10, 7, 70, 0, '2026-01-02T00:00:00Z'),
			(1, 43, 0, 4, 4, 100, 1, '2026-01-02T00:00:00Z');
	`); err != nil {
		t.Fatalf("seed v1 db: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close v1 db: %v", err)
	}
}

// withExtraMigration replaces the migration list with the real version 1 plus one test migration at version 2.
func withExtraMigration(t *testing.T, script string) {
	t.Helper()
	replaceMigrations(t, []migration{
		migrations[0],
		{version: 2, name: "test_extra", sql: script},
	})
}

func testPaths(t *testing.T) appdata.Paths {
	t.Helper()
	root := t.TempDir()
	return appdata.Paths{
		DataDir: root,
		DB:      filepath.Join(root, "steam-achievement-tracker.db"),
		Backups: filepath.Join(root, "backups"),
	}
}

func assertUserVersion(t *testing.T, conn *sql.DB, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRow("PRAGMA user_version").Scan(&got); err != nil {
		t.Fatalf("query user_version: %v", err)
	}
	if got != want {
		t.Fatalf("user_version = %d, want %d", got, want)
	}
}

func assertTable(t *testing.T, conn *sql.DB, name string) {
	t.Helper()
	if !mustTableExists(t, conn, name) {
		t.Fatalf("table %s does not exist", name)
	}
}

// mustTableExists is tableExists for tests: a query error fails the test instead of reading as "absent".
func mustTableExists(t *testing.T, conn *sql.DB, name string) bool {
	t.Helper()
	ok, err := tableExists(conn, name)
	if err != nil {
		t.Fatalf("check table %s: %v", name, err)
	}
	return ok
}

// assertAllMigrationsRecorded checks that every migration in the list has a schema_migrations row.
func assertAllMigrationsRecorded(t *testing.T, conn *sql.DB) {
	t.Helper()
	for _, m := range migrations {
		assertMigration(t, conn, m.version)
	}
}

// assertTableList checks the user table names (sqlite_ internal tables excluded) match want exactly.
func assertTableList(t *testing.T, conn *sql.DB, want []string) {
	t.Helper()
	rows, err := conn.Query(`select name from sqlite_master where type='table' and name not like 'sqlite_%'`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if len(got) != len(wantSorted) {
		t.Fatalf("tables = %v, want %v", got, wantSorted)
	}
	for i := range got {
		if got[i] != wantSorted[i] {
			t.Fatalf("tables = %v, want %v", got, wantSorted)
		}
	}
}

// assertColumns checks the column names of table, in declaration order, match want exactly.
func assertColumns(t *testing.T, conn *sql.DB, table string, want []string) {
	t.Helper()
	rows, err := conn.Query(`select name from pragma_table_info('` + table + `') order by cid`)
	if err != nil {
		t.Fatalf("query columns of %s: %v", table, err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("%s columns = %v, want %v", table, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s columns = %v, want %v", table, got, want)
		}
	}
}

func assertMigration(t *testing.T, conn *sql.DB, version int) {
	t.Helper()
	var count int
	if err := conn.QueryRow(`select count(*) from schema_migrations where version=?`, version).Scan(&count); err != nil {
		t.Fatalf("query migration %d: %v", version, err)
	}
	if count != 1 {
		t.Fatalf("migration %d count = %d, want 1", version, count)
	}
}

func assertCount(t *testing.T, conn *sql.DB, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := conn.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if got != want {
		t.Fatalf("count = %d, want %d for %s", got, want, query)
	}
}

func backupFiles(t *testing.T, paths appdata.Paths) []string {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(paths.Backups, "*.db"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	return entries
}
