package db

import (
	"database/sql"
	"os"
	"runtime"
	"strings"
	"testing"
)

func openRawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// replaceMigrations swaps the migration list for the duration of one test.
func replaceMigrations(t *testing.T, replacement []migration) {
	t.Helper()
	original := migrations
	migrations = replacement
	t.Cleanup(func() { migrations = original })
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}

func TestMigrationFailureRollsBackToPreviousSchema(t *testing.T) {
	paths := testPaths(t)
	seedV9Database(t, paths.DB)

	// Migration 10 runs its ALTER and tag cleanup, then fails on a missing table. Everything must roll back.
	broken := append([]migration(nil), migrations...)
	for i := range broken {
		if broken[i].version == 10 {
			broken[i].sql = dlcNotOwnedSetsSQL + "\nINSERT INTO no_such_table VALUES(1);"
		}
	}
	replaceMigrations(t, broken)

	conn, err := OpenAndMigrate(paths)
	if err == nil {
		conn.Close()
		t.Fatal("OpenAndMigrate() error = nil, want migration failure")
	}
	if !strings.Contains(err.Error(), "migration 010 dlc_not_owned_sets") {
		t.Fatalf("error = %v, want migration 010 context", err)
	}
	// The pre-migration backup is taken before the transaction and is kept.
	if n := len(backupFiles(t, paths)); n != 1 {
		t.Fatalf("backup count after failed migration = %d, want 1", n)
	}

	assertRolledBackToV9(t, paths.DB)
}

// assertRolledBackToV9 reopens the file directly and checks that schema and rows match the seeded v9 database.
func assertRolledBackToV9(t *testing.T, path string) {
	t.Helper()
	raw := openRawDB(t, path)
	assertUserVersion(t, raw, 9)
	assertMigration(t, raw, 9)
	assertCount(t, raw, `select count(*) from schema_migrations where version=10`, 0)
	assertCount(t, raw, `select count(*) from pragma_table_info('profile_games') where name='dlc_not_owned'`, 0)
	assertCount(t, raw, `select count(*) from profile_game_tags where profile_id=1 and appid=43 and tag='new_achievements_added'`, 1)
	assertCount(t, raw, `select count(*) from profile_game_tags where profile_id=1 and appid=43 and tag='missing_achievements_in_dlc'`, 1)
	assertCount(t, raw, `select count(*) from profile_games`, 3)
}

// The version record insert (applyMigration) runs after migration 10's ALTER and tag DELETE. A failure there
// must roll the whole migration back, not just the record.
func TestMigrationFailureOnVersionRecordRollsBack(t *testing.T) {
	paths := testPaths(t)
	seedV9Database(t, paths.DB)
	setup := openRawDB(t, paths.DB)
	if _, err := setup.Exec(`CREATE TRIGGER block10 BEFORE INSERT ON schema_migrations WHEN NEW.version=10 BEGIN SELECT RAISE(ABORT,'x'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if err := setup.Close(); err != nil {
		t.Fatal(err)
	}

	conn, err := OpenAndMigrate(paths)
	if err == nil {
		conn.Close()
		t.Fatal("OpenAndMigrate() error = nil, want schema_migrations insert failure")
	}
	if err.Error() != "x" {
		t.Fatalf("error = %q, want the trigger's RAISE message %q", err.Error(), "x")
	}
	if n := len(backupFiles(t, paths)); n != 1 {
		t.Fatalf("backup count after failed migration = %d, want 1", n)
	}
	assertRolledBackToV9(t, paths.DB)
}

// A failed migration must release its transaction. OpenAndMigrate closes the handle on failure, which would hide a
// leaked transaction, so this test calls migrate directly on the same single-connection pool.
func TestMigrationFailureReleasesConnection(t *testing.T) {
	paths := testPaths(t)
	seedV9Database(t, paths.DB)
	broken := append([]migration(nil), migrations...)
	for i := range broken {
		if broken[i].version == 10 {
			broken[i].sql = "INSERT INTO no_such_table VALUES(1);"
		}
	}
	replaceMigrations(t, broken)

	conn, err := sql.Open("sqlite3", sqliteDSN(paths.DB))
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}

	if err := migrate(conn, paths); err == nil {
		t.Fatal("migrate() error = nil, want migration failure")
	}
	if inUse := conn.Stats().InUse; inUse != 0 {
		t.Fatalf("connections in use after failed migration = %d, want 0 (transaction left open)", inUse)
	}
	if _, err := conn.Exec(`insert into settings(key, value) values('after_failure', '1')`); err != nil {
		t.Fatalf("write after failed migration: %v", err)
	}
}

func TestOpenAndMigrateDetectsLegacySchemaFromProfileGames(t *testing.T) {
	paths := testPaths(t)
	legacy := openRawDB(t, paths.DB)
	// user_version stays 0: the legacy baseline must come from the profile_games table alone.
	if _, err := legacy.Exec(initialSchemaSQL + `
		INSERT INTO profiles(id, steam_id64, display_name, created_at, updated_at) VALUES(1, '11111111111111111', 'Unversioned', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');`); err != nil {
		t.Fatalf("create unversioned legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 14)
	// Recorded as the legacy baseline, so the initial schema is not recorded as a fresh migration.
	assertCount(t, conn, `select count(*) from schema_migrations where version=1 and name='initial_schema_legacy'`, 1)
	assertCount(t, conn, `select count(*) from schema_migrations where version=1 and name='initial_schema'`, 0)
	assertCount(t, conn, `select count(*) from profiles where steam_id64='11111111111111111'`, 1)
	if n := len(backupFiles(t, paths)); n != 1 {
		t.Fatalf("backup count = %d, want 1 for unversioned legacy data", n)
	}
}

func TestMigrationV8FallsBackToActiveProfileWithoutSyncRuns(t *testing.T) {
	paths := testPaths(t)
	seedV7Database(t, paths.DB, `INSERT INTO settings(key, value) VALUES('active_profile_id', '2');`)

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 14)
	assertCount(t, conn, `select count(*) from profile_game_flags where profile_id=2 and appid=50 and disabled=1 and disabled_reason='not_owned'`, 1)
	assertCount(t, conn, `select count(*) from profile_game_flags where profile_id=1 and appid=50`, 0)
	assertCount(t, conn, `select count(*) from games where appid=50 and disabled=0 and disabled_reason is null`, 1)
}

func TestMigrationV8WithoutSyncRunsOrActiveProfileScopesToNobody(t *testing.T) {
	paths := testPaths(t)
	seedV7Database(t, paths.DB, "")

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 14)
	// No scope profile at all: the global flag is cleared and no profile receives the flag.
	assertCount(t, conn, `select count(*) from profile_game_flags where appid=50`, 0)
	assertCount(t, conn, `select count(*) from games where appid=50 and disabled=0 and disabled_reason is null`, 1)
}

func TestOpenAndMigrateSecuresDatabaseAndBackupFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	paths := testPaths(t)
	seedV9Database(t, paths.DB)
	if err := os.Chmod(paths.DB, 0o644); err != nil {
		t.Fatal(err)
	}

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertFileMode(t, paths.DB, 0o600)
	if _, err := os.Stat(paths.DB + "-wal"); err != nil {
		t.Fatalf("wal file missing while WAL connection is open: %v", err)
	}
	assertFileMode(t, paths.DB+"-wal", 0o600)
	backups := backupFiles(t, paths)
	if len(backups) != 1 {
		t.Fatalf("backup count = %d, want 1", len(backups))
	}
	assertFileMode(t, backups[0], 0o600)
	assertFileMode(t, paths.Backups, 0o700)
}

func TestSecureDBFilesChmodsOnlyExistingFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	paths := testPaths(t)
	for _, p := range []string{paths.DB, paths.DB + "-wal"} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		// WriteFile is subject to the umask; set the mode explicitly so the test does not depend on it.
		if err := os.Chmod(p, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	secureDBFiles(paths)

	assertFileMode(t, paths.DB, 0o600)
	assertFileMode(t, paths.DB+"-wal", 0o600)
	if _, err := os.Stat(paths.DB + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("missing -shm file: stat err = %v, want not-exist and not created", err)
	}
}
