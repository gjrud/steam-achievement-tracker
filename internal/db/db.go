package db

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	"github.com/gjrud/steam-achievement-tracker/internal/fsretry"
	"github.com/gjrud/steam-achievement-tracker/internal/redact"
)

type migration struct {
	version int
	name    string
	sql     string
	// compact marks a migration that drops data; the database is compacted after it commits (see compact).
	compact bool
}

type schemaState struct {
	version       int
	userVersion   int
	hasMigrations bool
	hasUserTables bool
}

func OpenAndMigrate(paths appdata.Paths) (*sql.DB, error) {
	conn, err := sql.Open("sqlite3", sqliteDSN(paths.DB))
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, err
	}
	if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
		conn.Close()
		return nil, err
	}
	secureDBFiles(paths)
	if err := migrate(conn, paths); err != nil {
		conn.Close()
		return nil, err
	}
	secureDBFiles(paths)
	return conn, nil
}

// migrate brings the database to latestVersion(). Each migration and its schema_migrations row run in one
// transaction with the user_version bump, so a failure leaves the previous schema in place.
func migrate(conn *sql.DB, paths appdata.Paths) error {
	target := latestVersion()
	state, err := inspectSchema(conn)
	if err != nil {
		return err
	}
	// Checked before any backup or migration: a database stamped above the latest known version would skip future
	// migrations silently, and a downgrade must not touch it either.
	if newer := max(state.version, state.userVersion); newer > target {
		return fmt.Errorf("database schema version %d is newer than this build supports (%d)", newer, target)
	}
	if schemaCurrent(state, target) {
		return nil
	}
	if err := backupBeforeMigration(conn, paths, state); err != nil {
		return err
	}
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := ensureMigrationsTable(tx); err != nil {
		return err
	}
	if err := applyMigrations(tx, state.version); err != nil {
		return err
	}
	if err := setUserVersion(tx, target); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// A fresh database holds no dropped data, so it is not compacted. The migration is already committed here, so a
	// failed compaction is logged and not returned: failing startup would not undo the migration, and compact is not
	// retried on later opens.
	if shouldCompact(state, target) {
		if err := compact(conn); err != nil {
			// compact's errors already start with "compact database:"; this line only says the migration itself held.
			log.Printf("migration committed; %v", redact.Paths(err))
		}
	}
	return nil
}

// shouldCompact reports whether the open that migrates state to target compacts afterwards: the database held user
// tables before the migration (a fresh one has none) and a migration in (version, target] dropped data.
func shouldCompact(state schemaState, target int) bool {
	return state.hasUserTables && needsCompaction(state.version, target)
}

// needsCompaction reports whether a migration in (current, target] dropped data that must leave the file.
func needsCompaction(current, target int) bool {
	for _, m := range migrations {
		if m.compact && m.version > current && m.version <= target {
			return true
		}
	}
	return false
}

// compact rewrites the database with VACUUM and truncates the WAL, so freed pages and WAL frames holding dropped
// data are no longer in the database files. VACUUM cannot run inside a transaction, so it runs after the commit.
func compact(conn *sql.DB) error {
	if _, err := conn.Exec("VACUUM"); err != nil {
		return fmt.Errorf("compact database: %w", err)
	}
	var busy, logFrames, checkpointedFrames int
	if err := conn.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return fmt.Errorf("compact database: %w", err)
	}
	if busy != 0 || logFrames != checkpointedFrames {
		return fmt.Errorf("compact database: checkpoint incomplete: busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointedFrames)
	}
	return nil
}

// latestVersion is the highest version in migrations, the version a fully migrated database reports.
func latestVersion() int {
	latest := 0
	for _, m := range migrations {
		if m.version > latest {
			latest = m.version
		}
	}
	return latest
}

func schemaCurrent(state schemaState, target int) bool {
	return state.version >= target && state.hasMigrations && state.userVersion >= target
}

func backupBeforeMigration(conn *sql.DB, paths appdata.Paths, state schemaState) error {
	if !state.hasUserTables {
		return nil
	}
	return backupDB(conn, paths)
}

// schemaMigrationsSQL creates the table that records applied migrations. The test seed reuses it.
const schemaMigrationsSQL = `CREATE TABLE IF NOT EXISTS schema_migrations(
  version integer primary key,
  name text not null,
  applied_at text not null
);`

func ensureMigrationsTable(tx *sql.Tx) error {
	_, err := tx.Exec(schemaMigrationsSQL)
	return err
}

func applyMigrations(tx *sql.Tx, current int) error {
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := applyMigration(tx, m); err != nil {
			return err
		}
	}
	return nil
}

func applyMigration(tx *sql.Tx, m migration) error {
	if _, err := tx.Exec(m.sql); err != nil {
		return fmt.Errorf("migration %03d %s: %w", m.version, m.name, err)
	}
	if _, err := tx.Exec(`insert into schema_migrations(version, name, applied_at) values(?,?,?)`, m.version, m.name, timestampUTC()); err != nil {
		return err
	}
	return setUserVersion(tx, m.version)
}

func setUserVersion(tx *sql.Tx, version int) error {
	_, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version))
	return err
}

func timestampUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// inspectSchema reads the applied version from schema_migrations. A database without that table has version 0.
// A failed read is returned, never taken as "no tables": treating it as an empty database would run the baseline
// migration over existing data without a backup.
func inspectSchema(conn *sql.DB) (schemaState, error) {
	var state schemaState
	if err := conn.QueryRow("PRAGMA user_version").Scan(&state.userVersion); err != nil {
		return state, err
	}
	var err error
	if state.hasMigrations, err = tableExists(conn, "schema_migrations"); err != nil {
		return state, err
	}
	if state.hasUserTables, err = hasUserTables(conn); err != nil {
		return state, err
	}
	if state.hasMigrations {
		if err := conn.QueryRow(`select coalesce(max(version), 0) from schema_migrations`).Scan(&state.version); err != nil {
			return state, err
		}
	}
	return state, nil
}

// tableExists reports whether a table named name exists. A missing table is (false, nil); any other error is returned.
func tableExists(conn *sql.DB, name string) (bool, error) {
	var exists int
	err := conn.QueryRow(`select 1 from sqlite_master where type='table' and name=? limit 1`, name).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return exists == 1, nil
}

// hasUserTables reports whether the database holds any table outside SQLite's own. Errors are returned, not read as
// an empty database.
func hasUserTables(conn *sql.DB) (bool, error) {
	var count int
	if err := conn.QueryRow(`select count(*) from sqlite_master where type='table' and name not like 'sqlite_%'`).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

func backupDB(conn *sql.DB, paths appdata.Paths) error {
	if err := os.MkdirAll(paths.Backups, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(paths.Backups, 0o700); err != nil {
		return err
	}
	var busy, logFrames, checkpointedFrames int
	if err := conn.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return err
	}
	if busy != 0 || logFrames != checkpointedFrames {
		return fmt.Errorf("checkpoint before backup incomplete: busy=%d log=%d checkpointed=%d", busy, logFrames, checkpointedFrames)
	}
	in, err := os.Open(paths.DB)
	if err != nil {
		return err
	}
	defer in.Close()
	outPath := filepath.Join(paths.Backups, fmt.Sprintf("steam-achievement-tracker-%d.db", time.Now().UnixNano()))
	if err := writeBackupFile(in, outPath); err != nil {
		return err
	}
	return os.Chmod(outPath, 0o600)
}

// writeBackupFile creates outPath exclusively, copies in to it and syncs it. The file is closed before any
// removal: on Windows an open file cannot be deleted, so the partial copy is removed only after Close.
// A file that could not be created (for example one that already exists) is never removed.
func writeBackupFile(in io.Reader, outPath string) error {
	out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr == nil {
		copyErr = out.Sync()
	}
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = fsretry.Remove(outPath)
		return copyErr
	}
	return nil
}

func secureDBFiles(paths appdata.Paths) {
	for _, path := range []string{paths.DB, paths.DB + "-wal", paths.DB + "-shm"} {
		if _, err := os.Stat(path); err == nil {
			_ = os.Chmod(path, 0o600)
		}
	}
}

var migrations = []migration{
	{version: 1, name: "initial_schema", sql: initialSchemaSQL},
	{version: 2, name: "cover_cached_flag", sql: coverCachedFlagSQL, compact: true},
	{version: 3, name: "rewrite_stored_sync_text", sql: rewriteStoredSyncTextSQL, compact: true},
	{version: 4, name: "add_lookup_indexes_and_prune_snapshots", sql: lookupIndexesAndPruneSnapshotsSQL},
}

// pruneSnapshotsSQL deletes game_snapshots rows that nothing reads. Per (profile_id, appid, groupid) it keeps the
// latest row (max id) and the latest completed row. The readers ask only whether a completed row exists, whether any
// row exists, or which appids have rows (games.go, dashboard.go, cleanup.go, profiles.go). Order is by id
// (autoincrement), not synced_at. Nothing references game_snapshots by foreign key, so the delete fires no FK checks.
// Running it again deletes nothing.
const pruneSnapshotsSQL = `
DELETE FROM game_snapshots WHERE id NOT IN (
  SELECT max(id) FROM game_snapshots GROUP BY profile_id, appid, groupid
  UNION
  SELECT max(id) FROM game_snapshots WHERE is_completed=1 GROUP BY profile_id, appid, groupid
);
`

// lookupIndexesSQL adds the indexes for the lookups that would otherwise scan a whole table: the per-app checks in
// the cleanup and orphan queries, the snapshot checks in MarkGamePreviouslyCompleted and profileGameHistory, the set
// lookups in HasSetData, and the latest sync run. The game_sets dlcappid index serves the DLC branch of the cover
// lookups (entriesForImageAppSQL, orphanCoverRowsSQL). The profile-leading primary keys do not serve the appid lookups, and
// profile_achievements(appid, apiname) also serves the foreign key check when an achievements row is deleted.
const lookupIndexesSQL = `
CREATE INDEX IF NOT EXISTS idx_profile_achievements_appid_apiname on profile_achievements(appid, apiname);
CREATE INDEX IF NOT EXISTS idx_profile_games_appid on profile_games(appid);
CREATE INDEX IF NOT EXISTS idx_profile_owned_games_appid on profile_owned_games(appid);
CREATE INDEX IF NOT EXISTS idx_profile_game_flags_appid on profile_game_flags(appid);
CREATE INDEX IF NOT EXISTS idx_profile_game_tags_appid on profile_game_tags(appid);
CREATE INDEX IF NOT EXISTS idx_game_snapshots_appid on game_snapshots(appid);
CREATE INDEX IF NOT EXISTS idx_game_snapshots_profile_id_appid_groupid_is_completed on game_snapshots(profile_id, appid, groupid, is_completed);
CREATE INDEX IF NOT EXISTS idx_game_sets_dlcappid on game_sets(dlcappid);
CREATE INDEX IF NOT EXISTS idx_sync_runs_profile_id on sync_runs(profile_id);
`

// lookupIndexesAndPruneSnapshotsSQL is migration 4. The prune runs first: it is cheaper to delete duplicate rows than
// to index them. Not compact: no VACUUM at startup, and the pruned rows are not sensitive. A database at version 4 is
// refused by older builds (see docs/database.md).
const lookupIndexesAndPruneSnapshotsSQL = pruneSnapshotsSQL + lookupIndexesSQL

// coverCachedFlagSQL replaces the stored absolute cover path with a cached flag. The path held the OS user name
// (GDPR: no filesystem paths in the database). Nothing reads it: covers are served by image app id and file name.
// Column drops need SQLite 3.35 or newer; the bundled go-sqlite3 ships a newer one.
const coverCachedFlagSQL = `
ALTER TABLE cover_images ADD COLUMN cached integer not null default 0;
UPDATE cover_images SET cached = 1 WHERE path IS NOT NULL AND trim(path) <> '';
ALTER TABLE cover_images DROP COLUMN path;
`

// rewriteStoredSyncTextSQL replaces stored error text that is not a sentence the app wrote for the user. Versions before
// this one stored the raw error (a Steam endpoint, a status line, a URL, a decode error) in profile_games.last_error and
// sync_runs.error, and the UI shows both, so that text must not stay.
//
// The rule is an allowlist, not a pattern match: a value is kept only when it is exactly one of the sentences listed
// below, or, for sync_runs.error only, the run summary "N of M games failed to sync". Every other non-empty value is
// replaced. The one exception is the old cancellation text "context canceled", which becomes "The request was
// canceled.". Empty and NULL values are left alone. The list is frozen here, so later sentence changes do not affect
// this migration; a later release that stores new text needs no change here, because this runs once per database.
//
// Running it again changes nothing: a kept sentence matches the list, and a replaced value is the generic sentence,
// which is on the list too (the second run finds nothing to rewrite). compact is set because an UPDATE leaves the old
// text in free pages until the file is compacted; compaction removes it from the file.
const rewriteStoredSyncTextSQL = `
UPDATE profile_games
SET last_error = CASE WHEN last_error = 'context canceled' THEN 'The request was canceled.' ELSE 'Something went wrong. See the log file for details.' END
WHERE last_error IS NOT NULL AND trim(last_error) <> '' AND last_error NOT IN (` + syncTextSafeSentences + `);
UPDATE sync_runs
SET error = CASE WHEN error = 'context canceled' THEN 'The request was canceled.' ELSE 'Something went wrong. See the log file for details.' END
WHERE error IS NOT NULL AND trim(error) <> '' AND error NOT IN (` + syncTextSafeSentences + `)
  AND NOT ` + partialRunSummarySQL + `;
`

// partialRunSummarySQL matches the run summary "N of M games failed to sync", where N and M are non-empty runs of
// digits and nothing else is in the value. A GLOB cannot require digits-only parts ('*' takes any text), so each part
// is checked with rtrim (which leaves an empty string only when every character is a digit) and the fixed words are
// checked by position: the first " of " ends N, the first " games failed to sync" ends M, and the value ends there.
const partialRunSummarySQL = `(
  instr(error, ' of ') > 1
  AND rtrim(substr(error, 1, instr(error, ' of ') - 1), '0123456789') = ''
  AND instr(error, ' games failed to sync') - instr(error, ' of ') - 4 > 0
  AND rtrim(substr(error, instr(error, ' of ') + 4, instr(error, ' games failed to sync') - instr(error, ' of ') - 4), '0123456789') = ''
  AND substr(error, instr(error, ' games failed to sync')) = ' games failed to sync'
)`

// syncTextSafeSentences is the SQL list of the sentences that may stay in profile_games.last_error and sync_runs.error,
// as exact strings (apostrophes doubled for SQL). It is the sentences the app can store in these columns: the
// steam.UserMessage sentences and the other user-facing sentences that reach a sync, plus the run texts the app writes
// itself (the stale-run and shutdown texts, and the cancellation sentence the migration rewrites to). It belongs to
// migration 3 only: changing it changes what migration 3 keeps. A later change to the sentences the app writes goes in
// a new migration, not here. TestSyncTextSafeSentencesCoverAppSentences fails when a sentence the app can store is
// missing here.
const syncTextSafeSentences = `
  'Steam rejected the Web API key. Check that the key is correct and still valid, then replace it in Settings.',
  'Steam did not share achievements for this game. The profile''s game details may be private.',
  'Steam refused this request.',
  'Steam could not process the request.',
  'Steam has no data for this request.',
  'Steam is limiting requests right now. Try again in a few minutes.',
  'Steam is temporarily unavailable. Try again later.',
  'Steam could not provide this data right now.',
  'Could not reach Steam. Check your internet connection and try again.',
  'The request was canceled.',
  'Steam sent a response the app could not read.',
  'Something went wrong. See the log file for details.',
  'Steam returned an unexpected response.',
  'This looks like your Steam Web API key. Enter your profile URL, SteamID64 or custom name instead.',
  'No Steam profile matches that custom name.',
  'Enter a Steam profile URL, SteamID64 or custom name.',
  'Steam did not return your game list. Your profile''s game details may be private.',
  'Steam returned an incomplete game list. Try again later.',
  'Steam did not return your purchased games. Try again later.',
  'Steam returned an incomplete list of purchased games. Try again later.',
  'Steam did not return the list of reviewed games. Try again later.',
  'Steam returned an incomplete list of reviewed games. Try again later.',
  'Steam did not provide achievements for this game.',
  'Steam did not return achievements for this game. Try again later.',
  'A sync is already running. Try again when it finishes.',
  'The app is shutting down.',
  'Could not access the system keyring. Make sure it is unlocked, then try again.',
  'The app could not open its database.',
  'The app database is not available. Restart the app, or see the log file for details.',
  'No Steam profile is active. Add your Steam profile first.',
  'A Steam profile is required.',
  'No Steam Web API key is saved. Enter your key first.',
  'The Steam Web API key cannot be empty or contain spaces or hidden characters.',
  'app restarted before sync completed',
  'sync ended but its final status could not be saved'
`

// initialSchemaSQL creates the complete current schema in one step. Nothing older than this layout was ever shipped,
// so there is no upgrade path from earlier layouts. Later schema changes go in new entries appended to migrations.
const initialSchemaSQL = `
CREATE TABLE IF NOT EXISTS settings(
  key text primary key,
  value text not null
);

CREATE TABLE IF NOT EXISTS profiles(
  id integer primary key autoincrement,
  steam_id64 text not null unique,
  display_name text not null,
  avatar_url text,
  created_at text not null,
  updated_at text not null,
  last_synced_at text
);

-- Shared Steam app metadata. Per-account facts live in profile_owned_games, per-profile game state in
-- profile_games and profile_game_flags, and cover cache state in cover_images.
CREATE TABLE IF NOT EXISTS games(
  appid integer primary key,
  name text not null,
  has_achievements integer not null default 0,
  achievement_status text not null default 'unknown',
  created_at text not null,
  updated_at text not null
);

CREATE TABLE IF NOT EXISTS game_sets(
  appid integer not null,
  groupid integer not null,
  name text not null default '',
  dlcappid integer not null default 0,
  sort_order integer not null default 0,
  updated_at text not null,
  primary key(appid, groupid),
  foreign key(appid) references games(appid)
);

CREATE TABLE IF NOT EXISTS cover_images(
  image_appid integer primary key,
  kind text not null,
  file_name text not null,
  path text,
  source_url text,
  checked_at text,
  downloaded_at text
);

-- One row per (profile, app, groupid) entry. groupid 0 is the base game; other groupids are DLC or set entries.
CREATE TABLE IF NOT EXISTS profile_games(
  profile_id integer not null,
  appid integer not null,
  groupid integer not null default 0,
  total_achievements integer not null default 0,
  unlocked_achievements integer not null default 0,
  completion_percent real not null default 0,
  missing_avg_unlock real,
  suggestion_score real,
  is_completed integer not null default 0,
  was_completed integer not null default 0,
  newly_incomplete integer not null default 0,
  sync_warning integer not null default 0,
  dlc_not_owned integer not null default 0,
  last_error text,
  last_error_at text,
  last_synced_at text,
  primary key(profile_id, appid, groupid),
  foreign key(profile_id) references profiles(id),
  foreign key(appid) references games(appid)
);

-- Per-profile facts about an owned game. vetted=0 means Steam returns the app only with unvetted apps included.
CREATE TABLE IF NOT EXISTS profile_owned_games(
  profile_id integer not null,
  appid integer not null,
  playtime_forever integer not null default 0,
  purchased integer not null default 0,
  vetted integer not null default 1,
  updated_at text not null,
  primary key(profile_id, appid),
  foreign key(profile_id) references profiles(id),
  foreign key(appid) references games(appid)
);

-- Library sync options per profile. A profile without a row uses the defaults (both on).
CREATE TABLE IF NOT EXISTS profile_library_settings(
  profile_id integer primary key,
  include_unvetted_apps integer not null default 1,
  include_played_free_games integer not null default 1,
  updated_at text not null,
  foreign key(profile_id) references profiles(id)
);

CREATE TABLE IF NOT EXISTS achievements(
  appid integer not null,
  apiname text not null,
  groupid integer not null default 0,
  global_percent real,
  archived integer not null default 0,
  updated_at text not null,
  primary key(appid, apiname),
  foreign key(appid) references games(appid)
);

CREATE TABLE IF NOT EXISTS profile_achievements(
  profile_id integer not null,
  appid integer not null,
  apiname text not null,
  unlocked integer not null default 0,
  unlock_time integer,
  updated_at text not null,
  primary key(profile_id, appid, apiname),
  foreign key(profile_id) references profiles(id),
  foreign key(appid, apiname) references achievements(appid, apiname)
);

CREATE TABLE IF NOT EXISTS game_snapshots(
  id integer primary key autoincrement,
  profile_id integer not null,
  appid integer not null,
  groupid integer not null default 0,
  total_achievements integer not null,
  unlocked_achievements integer not null,
  completion_percent real not null,
  missing_avg_unlock real,
  is_completed integer not null,
  synced_at text not null,
  foreign key(profile_id) references profiles(id),
  foreign key(appid) references games(appid)
);

CREATE TABLE IF NOT EXISTS sync_runs(
  id integer primary key autoincrement,
  profile_id integer,
  started_at text not null,
  finished_at text,
  status text not null,
  games_total integer not null default 0,
  games_synced integer not null default 0,
  games_failed integer not null default 0,
  error text,
  foreign key(profile_id) references profiles(id)
);

-- Manual and not-owned disables, per profile entry.
CREATE TABLE IF NOT EXISTS profile_game_flags(
  profile_id integer not null,
  appid integer not null,
  groupid integer not null default 0,
  manual_was_completed integer not null default 0,
  missing_achievements_in_dlc integer not null default 0,
  disabled integer not null default 0,
  disabled_reason text,
  created_at text not null,
  updated_at text not null,
  primary key(profile_id, appid, groupid),
  foreign key(profile_id) references profiles(id),
  foreign key(appid) references games(appid)
);

CREATE INDEX IF NOT EXISTS idx_profile_game_flags_profile_disabled on profile_game_flags(profile_id, disabled);

-- Dashboard issue and suggestion tags, recomputed by the app per profile entry.
CREATE TABLE IF NOT EXISTS profile_game_tags(
  profile_id integer not null,
  appid integer not null,
  groupid integer not null default 0,
  tag text not null,
  score real,
  reason text,
  computed_at text not null,
  primary key(profile_id, appid, groupid, tag),
  foreign key(profile_id) references profiles(id),
  foreign key(appid) references games(appid)
);

CREATE INDEX IF NOT EXISTS idx_profile_game_tags_profile_tag on profile_game_tags(profile_id, tag);
`
