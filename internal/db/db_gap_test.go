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

// brokenMigrationSQL creates a table, then fails on a missing table. Everything in it must roll back.
const brokenMigrationSQL = `CREATE TABLE migration_probe(id integer primary key);
INSERT INTO no_such_table VALUES(1);`

func TestMigrationFailureRollsBackToPreviousSchema(t *testing.T) {
	paths := testPaths(t)
	seedV1Database(t, paths.DB)
	withExtraMigration(t, brokenMigrationSQL)

	conn, err := OpenAndMigrate(paths)
	if err == nil {
		conn.Close()
		t.Fatal("OpenAndMigrate() error = nil, want migration failure")
	}
	if !strings.Contains(err.Error(), "migration 002 test_extra") {
		t.Fatalf("error = %v, want migration 002 context", err)
	}
	// The pre-migration backup is taken before the transaction and is kept.
	if n := len(backupFiles(t, paths)); n != 1 {
		t.Fatalf("backup count after failed migration = %d, want 1", n)
	}

	assertRolledBackToV1(t, paths.DB)
}

// assertRolledBackToV1 reopens the file directly and checks that schema and rows match the seeded v1 database.
func assertRolledBackToV1(t *testing.T, path string) {
	t.Helper()
	raw := openRawDB(t, path)
	assertUserVersion(t, raw, 1)
	assertMigration(t, raw, 1)
	assertCount(t, raw, `select count(*) from schema_migrations where version=2`, 0)
	assertCount(t, raw, `select count(*) from sqlite_master where type='table' and name='migration_probe'`, 0)
	assertCount(t, raw, `select count(*) from profile_games`, 2)
	assertCount(t, raw, `select count(*) from profile_games where profile_id=1 and appid=42 and total_achievements=10 and unlocked_achievements=7`, 1)
}

// A database stamped above the latest known version must be refused before any backup or write, so future migrations
// are not skipped silently and an older build cannot downgrade it. Covers a newer schema_migrations row and a
// newer user_version alone.
func TestOpenAndMigrateRejectsNewerSchema(t *testing.T) {
	cases := []struct {
		name  string
		stamp string
	}{
		{name: "schema_migrations row above latest", stamp: `INSERT INTO schema_migrations(version, name, applied_at) VALUES(4, 'future', '2026-01-01T00:00:00Z'); PRAGMA user_version = 4;`},
		{name: "user_version only above latest", stamp: `PRAGMA user_version = 4;`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			paths := testPaths(t)
			seedV1Database(t, paths.DB)
			raw := openRawDB(t, paths.DB)
			if _, err := raw.Exec(tc.stamp); err != nil {
				t.Fatalf("stamp newer version: %v", err)
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
			before := rawRowCounts(t, paths.DB)
			if before.userVersion != 4 {
				t.Fatalf("seed user_version = %d, want 4", before.userVersion)
			}

			conn, err := OpenAndMigrate(paths)
			if err == nil {
				conn.Close()
				t.Fatal("OpenAndMigrate() error = nil, want newer-schema rejection")
			}
			if !strings.Contains(err.Error(), "newer") {
				t.Fatalf("error = %v, want message containing %q", err, "newer")
			}
			if n := len(backupFiles(t, paths)); n != 0 {
				t.Fatalf("backup count after rejected open = %d, want 0", n)
			}
			if after := rawRowCounts(t, paths.DB); after != before {
				t.Fatalf("database changed after rejected open: before %+v, after %+v", before, after)
			}
		})
	}
}

// rowCounts summarizes what the newer-schema test must leave untouched.
type rowCounts struct {
	userVersion int
	maxVersion  int
	migrations  int
	profiles    int
	profileGame int
}

func rawRowCounts(t *testing.T, path string) rowCounts {
	t.Helper()
	raw := openRawDB(t, path)
	return rowCounts{
		userVersion: countOf(t, raw, `PRAGMA user_version`),
		maxVersion:  countOf(t, raw, `select coalesce(max(version), 0) from schema_migrations`),
		migrations:  countOf(t, raw, `select count(*) from schema_migrations`),
		profiles:    countOf(t, raw, `select count(*) from profiles`),
		profileGame: countOf(t, raw, `select count(*) from profile_games`),
	}
}

func countOf(t *testing.T, conn *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("query %s: %v", query, err)
	}
	return n
}

// The version record insert (applyMigration) runs after the migration SQL. A failure there must roll the whole
// migration back, not just the record.
func TestMigrationFailureOnVersionRecordRollsBack(t *testing.T) {
	paths := testPaths(t)
	seedV1Database(t, paths.DB)
	withExtraMigration(t, `CREATE TABLE migration_probe(id integer primary key);`)
	setup := openRawDB(t, paths.DB)
	if _, err := setup.Exec(`CREATE TRIGGER block2 BEFORE INSERT ON schema_migrations WHEN NEW.version=2 BEGIN SELECT RAISE(ABORT,'x'); END;`); err != nil {
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
	assertRolledBackToV1(t, paths.DB)
}

// A failed migration must release its transaction. OpenAndMigrate closes the handle on failure, which would hide a
// leaked transaction, so this test calls migrate directly on the same single-connection pool.
func TestMigrationFailureReleasesConnection(t *testing.T) {
	paths := testPaths(t)
	seedV1Database(t, paths.DB)
	withExtraMigration(t, "INSERT INTO no_such_table VALUES(1);")

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

func TestOpenAndMigrateSecuresDatabaseAndBackupFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	paths := testPaths(t)
	seedV1Database(t, paths.DB)
	if err := os.Chmod(paths.DB, 0o644); err != nil {
		t.Fatal(err)
	}
	withExtraMigration(t, `CREATE TABLE migration_probe(id integer primary key);`)

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
