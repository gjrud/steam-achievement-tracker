package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
)

func TestOpenAndMigrateFreshDB(t *testing.T) {
	paths := testPaths(t)
	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 8)
	assertTable(t, conn, "schema_migrations")
	assertTable(t, conn, "profile_game_flags")
	assertTable(t, conn, "profile_game_tags")
	assertMigration(t, conn, 1)
	assertMigration(t, conn, 2)
	assertMigration(t, conn, 3)
	assertMigration(t, conn, 4)
	assertMigration(t, conn, 5)
	assertMigration(t, conn, 6)
	assertMigration(t, conn, 7)
	assertMigration(t, conn, 8)
}

func TestOpenAndMigrateLegacyV1DB(t *testing.T) {
	paths := testPaths(t)
	legacy, err := sql.Open("sqlite3", paths.DB)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(initialSchemaSQL + "PRAGMA user_version = 1;"); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 8)
	assertTable(t, conn, "schema_migrations")
	assertTable(t, conn, "profile_game_flags")
	assertTable(t, conn, "profile_game_tags")
	assertMigration(t, conn, 1)
	assertMigration(t, conn, 2)
	assertMigration(t, conn, 3)
	assertMigration(t, conn, 4)
	assertMigration(t, conn, 5)
	assertMigration(t, conn, 6)
	assertMigration(t, conn, 7)
	assertMigration(t, conn, 8)
	entries, err := filepath.Glob(filepath.Join(paths.Backups, "*.db"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected pre-migration backup")
	}
}

func TestOpenAndMigrateLegacyV1BackupContainsOriginalData(t *testing.T) {
	paths := testPaths(t)
	legacy, err := sql.Open("sqlite3", paths.DB)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	_, err = legacy.Exec(initialSchemaSQL + `PRAGMA user_version = 1;
		insert into profiles(id, steam_id64, display_name, created_at, updated_at) values(1, '11111111111111111', 'Legacy User', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		insert into games(appid, name, playtime_forever, has_achievements, achievement_status, created_at, updated_at) values(42, 'Legacy Game', 90, 1, 'ok', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		insert into profile_games(profile_id, appid, total_achievements, unlocked_achievements, completion_percent, is_completed, was_completed) values(1, 42, 10, 7, 70, 0, 1);`)
	if err != nil {
		t.Fatalf("seed legacy db: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 8)
	assertCount(t, conn, `select count(*) from profiles where steam_id64='11111111111111111' and display_name='Legacy User'`, 1)
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
	if tableExists(backup, "schema_migrations") {
		t.Fatal("backup has schema_migrations, want pre-migration snapshot")
	}
	assertCount(t, backup, `select count(*) from profiles where steam_id64='11111111111111111' and display_name='Legacy User'`, 1)
	assertCount(t, backup, `select count(*) from profile_games where profile_id=1 and appid=42 and total_achievements=10 and unlocked_achievements=7`, 1)
}

func TestMigrationV8ScopesGlobalDisabledToLatestSyncProfile(t *testing.T) {
	paths := testPaths(t)
	legacy, err := sql.Open("sqlite3", paths.DB)
	if err != nil {
		t.Fatalf("open v7 db: %v", err)
	}
	_, err = legacy.Exec(initialSchemaSQL + manualGameFlagsSQL + profileGameTagsSQL + `
		CREATE TABLE IF NOT EXISTS schema_migrations(version integer primary key, name text not null, applied_at text not null);
		INSERT INTO schema_migrations(version, name, applied_at) VALUES
			(1,'m1','2026-01-01T00:00:00Z'),(2,'m2','2026-01-01T00:00:00Z'),(3,'m3','2026-01-01T00:00:00Z'),(4,'m4','2026-01-01T00:00:00Z'),
			(5,'m5','2026-01-01T00:00:00Z'),(6,'m6','2026-01-01T00:00:00Z'),(7,'m7','2026-01-01T00:00:00Z');
		PRAGMA user_version = 7;
		INSERT INTO profiles(id, steam_id64, display_name, created_at, updated_at) VALUES
			(1, '11111111111111111', 'User One', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
			(2, '22222222222222222', 'User Two', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO settings(key, value) VALUES('active_profile_id', '2');
		INSERT INTO sync_runs(id, profile_id, started_at, status) VALUES(1, 1, '2026-01-01T00:00:00Z', 'success');
		INSERT INTO games(appid, name, playtime_forever, disabled, disabled_reason, created_at, updated_at) VALUES(50, 'Shared Game', 10, 1, 'not_owned', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO profile_games(profile_id, appid) VALUES(1, 50), (2, 50);
	`)
	if err != nil {
		t.Fatalf("seed v7 db: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close v7 db: %v", err)
	}

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 8)
	assertCount(t, conn, `select count(*) from profile_game_flags where profile_id=1 and appid=50 and disabled=1 and disabled_reason='not_owned'`, 1)
	assertCount(t, conn, `select count(*) from profile_game_flags where profile_id=2 and appid=50 and disabled=1`, 0)
	assertCount(t, conn, `select count(*) from games where appid=50 and disabled=0 and disabled_reason is null`, 1)
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

func testPaths(t *testing.T) appdata.Paths {
	t.Helper()
	root := t.TempDir()
	return appdata.Paths{
		Root:    root,
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
	if !tableExists(conn, name) {
		t.Fatalf("table %s does not exist", name)
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

func assertCount(t *testing.T, conn *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := conn.QueryRow(query).Scan(&got); err != nil {
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
