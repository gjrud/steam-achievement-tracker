package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
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

	assertUserVersion(t, conn, 14)
	assertTable(t, conn, "schema_migrations")
	assertTable(t, conn, "profile_game_flags")
	assertTable(t, conn, "profile_game_tags")
	assertTable(t, conn, "game_sets")
	assertTable(t, conn, "cover_images")
	assertCount(t, conn, `select count(*) from pragma_table_info('games') where name='purchased' and dflt_value='0' and "notnull"=1`, 1)
	assertTable(t, conn, "profile_owned_games")
	assertTable(t, conn, "profile_library_settings")
	assertMigration(t, conn, 1)
	assertMigration(t, conn, 2)
	assertMigration(t, conn, 3)
	assertMigration(t, conn, 4)
	assertMigration(t, conn, 5)
	assertMigration(t, conn, 6)
	assertMigration(t, conn, 7)
	assertMigration(t, conn, 8)
	assertMigration(t, conn, 9)
	assertMigration(t, conn, 10)
	assertMigration(t, conn, 11)
	assertMigration(t, conn, 12)
	assertMigration(t, conn, 13)
}

func TestOpenAndMigrateLegacyV1DB(t *testing.T) {
	paths := testPaths(t)
	legacy := openRawDB(t, paths.DB)
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

	assertUserVersion(t, conn, 14)
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
	assertMigration(t, conn, 9)
	assertMigration(t, conn, 10)
	assertMigration(t, conn, 11)
	assertMigration(t, conn, 12)
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
	legacy := openRawDB(t, paths.DB)
	_, err := legacy.Exec(initialSchemaSQL + `PRAGMA user_version = 1;
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

	assertUserVersion(t, conn, 14)
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
	seedV7Database(t, paths.DB, `INSERT INTO settings(key, value) VALUES('active_profile_id', '2');
		INSERT INTO sync_runs(id, profile_id, started_at, status) VALUES(1, 1, '2026-01-01T00:00:00Z', 'success');`)

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 14)
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

func TestMigrationV9RebuildsPopulatedV8DB(t *testing.T) {
	paths := testPaths(t)
	legacy := openRawDB(t, paths.DB)
	// Build the exact v8 schema by running the real migrations 1..8.
	applyMigrationsUpTo(t, legacy, 8)
	_, err := legacy.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations(version integer primary key, name text not null, applied_at text not null);
		INSERT INTO schema_migrations(version, name, applied_at) VALUES
			(1,'m1','2026-01-01T00:00:00Z'),(2,'m2','2026-01-01T00:00:00Z'),(3,'m3','2026-01-01T00:00:00Z'),(4,'m4','2026-01-01T00:00:00Z'),
			(5,'m5','2026-01-01T00:00:00Z'),(6,'m6','2026-01-01T00:00:00Z'),(7,'m7','2026-01-01T00:00:00Z'),(8,'m8','2026-01-01T00:00:00Z');
		PRAGMA user_version = 8;
		INSERT INTO profiles(id, steam_id64, display_name, created_at, updated_at) VALUES(1, '11111111111111111', 'Populated User', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO settings(key, value) VALUES('active_profile_id', '1');
		INSERT INTO games(appid, name, playtime_forever, has_achievements, achievement_status, cover_path, cover_source_url, cover_checked_at, cover_downloaded_at, created_at, updated_at) VALUES
			(42, 'Populated Game', 90, 1, 'ok', '/cache/images/games/42/library_600x900.jpg', 'https://example.com/42.jpg', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
			(43, 'Uncovered Game', 0, 1, 'ok', NULL, NULL, NULL, NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO profile_games(profile_id, appid, total_achievements, unlocked_achievements, completion_percent, missing_avg_unlock, suggestion_score, is_completed, was_completed, newly_incomplete, sync_warning, last_synced_at) VALUES
			(1, 42, 10, 7, 70, 40.5, 61.2, 0, 1, 0, 0, '2026-01-02T00:00:00Z'),
			(1, 43, 4, 4, 100, NULL, NULL, 1, 1, 0, 0, '2026-01-02T00:00:00Z');
		INSERT INTO profile_game_flags(profile_id, appid, manual_was_completed, missing_achievements_in_dlc, disabled, disabled_reason, created_at, updated_at) VALUES
			(1, 42, 1, 1, 0, NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at) VALUES
			(1, 42, 'almost_there', 61.2, 'reason', '2026-01-02T00:00:00Z'),
			(1, 42, 'missing_achievements_in_dlc', NULL, 'manual DLC-missing flag', '2026-01-02T00:00:00Z');
		INSERT INTO game_snapshots(profile_id, appid, total_achievements, unlocked_achievements, completion_percent, missing_avg_unlock, is_completed, synced_at) VALUES
			(1, 42, 10, 7, 70, 40.5, 0, '2026-01-02T00:00:00Z');
		INSERT INTO achievements(appid, apiname, global_percent, updated_at) VALUES(42, 'ACH_A', 12.5, '2026-01-02T00:00:00Z');
		INSERT INTO profile_achievements(profile_id, appid, apiname, unlocked, unlock_time, updated_at) VALUES(1, 42, 'ACH_A', 1, 1700000000, '2026-01-02T00:00:00Z');
	`)
	if err != nil {
		t.Fatalf("seed v8 db: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close v8 db: %v", err)
	}

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 14)
	assertMigration(t, conn, 9)
	assertMigration(t, conn, 10)
	assertMigration(t, conn, 11)
	assertMigration(t, conn, 12)
	assertCount(t, conn, `select count(*) from profile_games where profile_id=1 and appid=42 and groupid=0 and total_achievements=10 and unlocked_achievements=7 and suggestion_score=61.2`, 1)
	assertCount(t, conn, `select count(*) from profile_games where profile_id=1 and appid=43 and groupid=0 and is_completed=1`, 1)
	assertCount(t, conn, `select count(*) from profile_game_flags where profile_id=1 and appid=42 and groupid=0 and manual_was_completed=1 and missing_achievements_in_dlc=1`, 1)
	assertCount(t, conn, `select count(*) from profile_game_tags where profile_id=1 and appid=42 and groupid=0 and tag='almost_there'`, 1)
	assertCount(t, conn, `select count(*) from profile_game_tags where profile_id=1 and appid=42 and groupid=0 and tag='missing_achievements_in_dlc'`, 1)
	assertCount(t, conn, `select count(*) from game_snapshots where profile_id=1 and appid=42 and groupid=0 and total_achievements=10`, 1)
	assertCount(t, conn, `select count(*) from achievements where appid=42 and apiname='ACH_A' and groupid=0 and archived=0`, 1)
	assertCount(t, conn, `select count(*) from profile_achievements where profile_id=1 and appid=42 and apiname='ACH_A' and unlocked=1`, 1)
	assertCount(t, conn, `select count(*) from cover_images where image_appid=42 and kind='library' and file_name='library_600x900.jpg' and path='/cache/images/games/42/library_600x900.jpg' and source_url='https://example.com/42.jpg' and checked_at='2026-01-02T00:00:00Z' and downloaded_at='2026-01-02T00:00:00Z'`, 1)
	assertCount(t, conn, `select count(*) from cover_images`, 1)
	assertCount(t, conn, `select count(*) from game_sets`, 0)
	assertCount(t, conn, `select count(*) from sqlite_master where type='index' and name='idx_profile_game_flags_profile_disabled'`, 1)
	assertCount(t, conn, `select count(*) from sqlite_master where type='index' and name='idx_profile_game_tags_profile_tag'`, 1)

	// Composite PK: a second entry for the same app with another groupid must be accepted.
	if _, err := conn.Exec(`insert into game_sets(appid, groupid, name, dlcappid, updated_at) values(42, 1, 'Populated Game DLC', 4173830, '2026-01-03T00:00:00Z')`); err != nil {
		t.Fatalf("insert game_sets: %v", err)
	}
	if _, err := conn.Exec(`insert into profile_games(profile_id, appid, groupid, total_achievements) values(1, 42, 1, 3)`); err != nil {
		t.Fatalf("insert sibling entry: %v", err)
	}
	assertCount(t, conn, `select count(*) from profile_games where profile_id=1 and appid=42`, 2)
	if _, err := conn.Exec(`insert into profile_games(profile_id, appid, groupid) values(1, 42, 0)`); err == nil {
		t.Fatal("duplicate profile entry accepted, want primary key violation")
	}

	entries := backupFiles(t, paths)
	if len(entries) != 1 {
		t.Fatalf("backup count = %d, want 1", len(entries))
	}
	backup, err := sql.Open("sqlite3", entries[0])
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backup.Close()
	assertUserVersion(t, backup, 8)
	assertCount(t, backup, `select count(*) from profile_games where profile_id=1 and appid=42 and total_achievements=10`, 1)
	if tableExists(backup, "cover_images") {
		t.Fatal("backup has cover_images, want pre-migration snapshot")
	}
}

func TestMigrationV10AddsDLCNotOwnedAndPrunesCompletedIssueTags(t *testing.T) {
	paths := testPaths(t)
	seedV9Database(t, paths.DB)

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 14)
	assertMigration(t, conn, 10)
	assertMigration(t, conn, 11)
	assertMigration(t, conn, 12)
	assertCount(t, conn, `select count(*) from profile_games where profile_id=1 and appid=42 and groupid=0 and total_achievements=10 and unlocked_achievements=7 and is_completed=0 and was_completed=1`, 1)
	assertCount(t, conn, `select count(*) from profile_games where profile_id=1 and appid=42 and groupid=1 and total_achievements=4`, 1)
	assertCount(t, conn, `select count(*) from profile_games where profile_id=1 and appid=43 and groupid=0 and is_completed=1 and newly_incomplete=1`, 1)
	assertCount(t, conn, `select count(*) from profile_games where dlc_not_owned=0`, 3)
	assertCount(t, conn, `select count(*) from profile_game_tags where profile_id=1 and appid=42 and groupid=0 and tag='new_achievements_added'`, 1)
	assertCount(t, conn, `select count(*) from profile_game_tags where profile_id=1 and appid=42 and groupid=1 and tag='in_progress'`, 1)
	assertCount(t, conn, `select count(*) from profile_game_tags where profile_id=1 and appid=43 and groupid=0 and tag='completed'`, 1)
	assertCount(t, conn, `select count(*) from profile_game_tags where profile_id=1 and appid=43 and groupid=0 and tag='missing_cover_art'`, 1)
	assertCount(t, conn, `select count(*) from profile_game_tags where profile_id=1 and appid=43 and tag='new_achievements_added'`, 0)
	assertCount(t, conn, `select count(*) from profile_game_tags where profile_id=1 and appid=43 and tag='missing_achievements_in_dlc'`, 0)
}

func TestMigrationV11AddsPurchasedColumnDefaultingToZero(t *testing.T) {
	paths := testPaths(t)
	seedUpgradeDatabase(t, paths.DB, 10, "")

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 14)
	assertMigration(t, conn, 11)
	assertCount(t, conn, `select count(*) from pragma_table_info('games') where name='purchased' and dflt_value='0' and "notnull"=1`, 1)
	// Games seeded before the migration (appid 42 and 43) keep the default; v12 copies it per profile.
	assertCount(t, conn, `select count(*) from games where purchased=0`, 2)
}

// A v10 database (no games.purchased) upgraded through v11 and v12: each profile entry gets its own
// profile_owned_games row with the legacy playtime and purchased=0 until the next sync.
func TestMigrationV12BackfillsPlaytimePerProfileFromV10(t *testing.T) {
	paths := testPaths(t)
	seedUpgradeDatabase(t, paths.DB, 10, "")

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 14)
	assertMigration(t, conn, 12)
	assertCount(t, conn, `select count(*) from profile_owned_games`, 3)
	assertCount(t, conn, `select count(*) from profile_owned_games where profile_id=1 and appid=42 and playtime_forever=90 and purchased=0`, 1)
	assertCount(t, conn, `select count(*) from profile_owned_games where profile_id=2 and appid=42 and playtime_forever=90 and purchased=0`, 1)
	assertCount(t, conn, `select count(*) from profile_owned_games where profile_id=1 and appid=43 and playtime_forever=30 and purchased=0`, 1)
}

// A v11 database whose games.purchased flags were set by the old global sync: v12 copies the legacy global
// purchased flag to every profile that has the game, even a profile that did not buy it, until that profile's
// next sync corrects it.
func TestMigrationV12BackfillsPurchasedFromV11Database(t *testing.T) {
	paths := testPaths(t)
	seedUpgradeDatabase(t, paths.DB, 11, `UPDATE games SET purchased=1 WHERE appid=42;`)

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 14)
	assertMigration(t, conn, 12)
	assertCount(t, conn, `select count(*) from profile_owned_games`, 3)
	assertCount(t, conn, `select count(*) from profile_owned_games where profile_id=1 and appid=42 and playtime_forever=90 and purchased=1`, 1)
	assertCount(t, conn, `select count(*) from profile_owned_games where profile_id=2 and appid=42 and playtime_forever=90 and purchased=1`, 1)
	assertCount(t, conn, `select count(*) from profile_owned_games where profile_id=1 and appid=43 and playtime_forever=30 and purchased=0`, 1)
	// The legacy columns stay in place (not dropped), but the app no longer reads them.
	assertCount(t, conn, `select count(*) from pragma_table_info('games') where name in ('playtime_forever','purchased')`, 2)
}

// A v12 database upgraded to v13: profile_library_settings exists, starts empty (no backfill), defaults both
// flags on for a new row, and rejects rows for profiles that do not exist.
func TestMigrationV13AddsProfileLibrarySettingsFromV12(t *testing.T) {
	paths := testPaths(t)
	seedUpgradeDatabase(t, paths.DB, 12, "")

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 14)
	assertMigration(t, conn, 13)
	assertCount(t, conn, `select count(*) from profile_library_settings`, 0)
	if _, err := conn.Exec(`insert into profile_library_settings(profile_id, updated_at) values(1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert default library settings: %v", err)
	}
	assertCount(t, conn, `select count(*) from profile_library_settings where profile_id=1 and include_unvetted_apps=1 and include_played_free_games=1`, 1)
	if _, err := conn.Exec(`insert into profile_library_settings(profile_id, updated_at) values(999, '2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("insert library settings for missing profile succeeded, want foreign key failure")
	}
}

// A v13 database upgraded to v14: existing profile_owned_games rows default to vetted=1 (until the next sync),
// and a new row without an explicit vetted value also defaults to 1.
func TestMigrationV14AddsVettedDefaultingToOneFromV13(t *testing.T) {
	paths := testPaths(t)
	seedUpgradeDatabase(t, paths.DB, 13, `
		INSERT INTO profile_owned_games(profile_id, appid, playtime_forever, purchased, updated_at) VALUES
			(1, 42, 90, 0, '2026-01-02T00:00:00Z'),
			(2, 42, 90, 1, '2026-01-02T00:00:00Z'),
			(1, 43, 30, 0, '2026-01-02T00:00:00Z');
	`)

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 14)
	assertMigration(t, conn, 14)
	assertCount(t, conn, `select count(*) from pragma_table_info('profile_owned_games') where name='vetted' and dflt_value='1' and "notnull"=1`, 1)
	assertCount(t, conn, `select count(*) from profile_owned_games`, 3)
	assertCount(t, conn, `select count(*) from profile_owned_games where vetted=1`, 3)
	if _, err := conn.Exec(`insert into profile_owned_games(profile_id, appid, updated_at) values(2, 43, '2026-01-03T00:00:00Z')`); err != nil {
		t.Fatalf("insert owned game without vetted: %v", err)
	}
}

// seedUpgradeDatabase writes a populated schema at the given version (real migrations 1..version) with two
// profiles: profile 1 owns games 42 and 43, profile 2 owns game 42 only. extraSQL runs last. OpenAndMigrate
// then upgrades the file to the current schema.
func seedUpgradeDatabase(t *testing.T, path string, version int, extraSQL string) {
	t.Helper()
	legacy := openRawDB(t, path)
	applyMigrationsUpTo(t, legacy, version)
	rows := make([]string, 0, version)
	for v := 1; v <= version; v++ {
		rows = append(rows, fmt.Sprintf("(%d,'m%d','2026-01-01T00:00:00Z')", v, v))
	}
	script := `
		CREATE TABLE IF NOT EXISTS schema_migrations(version integer primary key, name text not null, applied_at text not null);
		INSERT INTO schema_migrations(version, name, applied_at) VALUES ` + strings.Join(rows, ",") + `;
		PRAGMA user_version = ` + fmt.Sprint(version) + `;
		INSERT INTO profiles(id, steam_id64, display_name, created_at, updated_at) VALUES
			(1, '11111111111111111', 'User One', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
			(2, '22222222222222222', 'User Two', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO settings(key, value) VALUES('active_profile_id', '1');
		INSERT INTO games(appid, name, playtime_forever, has_achievements, achievement_status, created_at, updated_at) VALUES
			(42, 'Shared Game', 90, 1, 'ok', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
			(43, 'Solo Game', 30, 1, 'ok', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO profile_games(profile_id, appid, groupid, total_achievements, unlocked_achievements, completion_percent, is_completed, was_completed, newly_incomplete, last_synced_at) VALUES
			(1, 42, 0, 10, 7, 70, 0, 1, 0, '2026-01-02T00:00:00Z'),
			(1, 43, 0, 4, 4, 100, 1, 1, 1, '2026-01-02T00:00:00Z'),
			(2, 42, 0, 10, 2, 20, 0, 0, 0, '2026-01-02T00:00:00Z');
	` + extraSQL
	if _, err := legacy.Exec(script); err != nil {
		t.Fatalf("seed v%d db: %v", version, err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close v%d db: %v", version, err)
	}
}

// seedV9Database writes a populated schema at version 9 (real migrations 1..9) with an
// unmigrated profile, games, entries, flags and tags, so later migrations have data to rewrite.
func seedV9Database(t *testing.T, path string) {
	t.Helper()
	legacy := openRawDB(t, path)
	applyMigrationsUpTo(t, legacy, 9)
	_, err := legacy.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations(version integer primary key, name text not null, applied_at text not null);
		INSERT INTO schema_migrations(version, name, applied_at) VALUES
			(1,'m1','2026-01-01T00:00:00Z'),(2,'m2','2026-01-01T00:00:00Z'),(3,'m3','2026-01-01T00:00:00Z'),(4,'m4','2026-01-01T00:00:00Z'),
			(5,'m5','2026-01-01T00:00:00Z'),(6,'m6','2026-01-01T00:00:00Z'),(7,'m7','2026-01-01T00:00:00Z'),(8,'m8','2026-01-01T00:00:00Z'),
			(9,'m9','2026-01-01T00:00:00Z');
		PRAGMA user_version = 9;
		INSERT INTO profiles(id, steam_id64, display_name, created_at, updated_at) VALUES(1, '11111111111111111', 'V9 User', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO settings(key, value) VALUES('active_profile_id', '1');
		INSERT INTO games(appid, name, playtime_forever, has_achievements, achievement_status, created_at, updated_at) VALUES
			(42, 'Open Game', 90, 1, 'ok', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
			(43, 'Done Game', 30, 1, 'ok', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO profile_games(profile_id, appid, groupid, total_achievements, unlocked_achievements, completion_percent, is_completed, was_completed, newly_incomplete, last_synced_at) VALUES
			(1, 42, 0, 10, 7, 70, 0, 1, 0, '2026-01-02T00:00:00Z'),
			(1, 42, 1, 4, 0, 0, 0, 0, 0, '2026-01-02T00:00:00Z'),
			(1, 43, 0, 4, 4, 100, 1, 1, 1, '2026-01-02T00:00:00Z');
		INSERT INTO profile_game_tags(profile_id, appid, groupid, tag, score, reason, computed_at) VALUES
			(1, 42, 0, 'new_achievements_added', NULL, 'previously completed, now incomplete', '2026-01-02T00:00:00Z'),
			(1, 42, 1, 'in_progress', 12.0, 'reason', '2026-01-02T00:00:00Z'),
			(1, 43, 0, 'completed', NULL, 'completed', '2026-01-02T00:00:00Z'),
			(1, 43, 0, 'new_achievements_added', NULL, 'previously completed, now incomplete', '2026-01-02T00:00:00Z'),
			(1, 43, 0, 'missing_achievements_in_dlc', NULL, 'manual DLC-missing flag', '2026-01-02T00:00:00Z'),
			(1, 43, 0, 'missing_cover_art', NULL, 'no cached cover image', '2026-01-02T00:00:00Z');
	`)
	if err != nil {
		t.Fatalf("seed v9 db: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close v9 db: %v", err)
	}
}

// seedV7Database writes a v7 schema with profiles 1 and 2 sharing game 50, which is globally disabled as
// not_owned. extraSQL adds the settings and sync_runs rows that decide which profile the v8 migration scopes to.
func seedV7Database(t *testing.T, path, extraSQL string) {
	t.Helper()
	legacy := openRawDB(t, path)
	_, err := legacy.Exec(initialSchemaSQL + manualGameFlagsSQL + profileGameTagsSQL + `
		CREATE TABLE IF NOT EXISTS schema_migrations(version integer primary key, name text not null, applied_at text not null);
		INSERT INTO schema_migrations(version, name, applied_at) VALUES
			(1,'m1','2026-01-01T00:00:00Z'),(2,'m2','2026-01-01T00:00:00Z'),(3,'m3','2026-01-01T00:00:00Z'),(4,'m4','2026-01-01T00:00:00Z'),
			(5,'m5','2026-01-01T00:00:00Z'),(6,'m6','2026-01-01T00:00:00Z'),(7,'m7','2026-01-01T00:00:00Z');
		PRAGMA user_version = 7;
		INSERT INTO profiles(id, steam_id64, display_name, created_at, updated_at) VALUES
			(1, '11111111111111111', 'User One', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
			(2, '22222222222222222', 'User Two', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO games(appid, name, playtime_forever, disabled, disabled_reason, created_at, updated_at) VALUES(50, 'Shared Game', 10, 1, 'not_owned', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO profile_games(profile_id, appid) VALUES(1, 50), (2, 50);
	` + extraSQL)
	if err != nil {
		t.Fatalf("seed v7 db: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close v7 db: %v", err)
	}
}

// applyMigrationsUpTo runs the real migration SQL for every version up to and including version.
func applyMigrationsUpTo(t *testing.T, conn *sql.DB, version int) {
	t.Helper()
	for _, m := range migrations {
		if m.version > version {
			continue
		}
		if _, err := conn.Exec(m.sql); err != nil {
			t.Fatalf("apply migration %d: %v", m.version, err)
		}
	}
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
