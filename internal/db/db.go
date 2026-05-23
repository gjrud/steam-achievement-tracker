package db

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	"github.com/gjrud/steam-achievement-tracker/internal/config"
)

type migration struct {
	version int
	name    string
	sql     string
}

type schemaState struct {
	version       int
	userVersion   int
	hasMigrations bool
	hasUserTables bool
	isLegacyV1    bool
}

func OpenAndMigrate(paths appdata.Paths) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_busy_timeout=5000&_foreign_keys=on", paths.DB)
	conn, err := sql.Open("sqlite3", dsn)
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

func migrate(conn *sql.DB, paths appdata.Paths) error {
	state, err := inspectSchema(conn)
	if err != nil {
		return err
	}
	if state.version >= config.SchemaVersion && state.hasMigrations && state.userVersion >= config.SchemaVersion {
		return nil
	}
	if state.hasUserTables {
		if err := backupDB(conn, paths); err != nil {
			return err
		}
	}
	tx, err := conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations(
  version integer primary key,
  name text not null,
  applied_at text not null
);`); err != nil {
		return err
	}

	current := state.version
	if !state.hasMigrations && state.isLegacyV1 {
		if _, err := tx.Exec(`insert or ignore into schema_migrations(version, name, applied_at) values(1, 'initial_schema_legacy', ?)`, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		current = 1
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if _, err := tx.Exec(m.sql); err != nil {
			return fmt.Errorf("migration %03d %s: %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(`insert into schema_migrations(version, name, applied_at) values(?,?,?)`, m.version, m.name, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", config.SchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func inspectSchema(conn *sql.DB) (schemaState, error) {
	var state schemaState
	if err := conn.QueryRow("PRAGMA user_version").Scan(&state.userVersion); err != nil {
		return state, err
	}
	state.hasMigrations = tableExists(conn, "schema_migrations")
	state.hasUserTables = hasUserTables(conn)
	if state.hasMigrations {
		if err := conn.QueryRow(`select coalesce(max(version), 0) from schema_migrations`).Scan(&state.version); err != nil {
			return state, err
		}
		return state, nil
	}
	state.isLegacyV1 = tableExists(conn, "profile_games") || state.userVersion >= 1
	if state.isLegacyV1 {
		state.version = 1
	} else {
		state.version = 0
	}
	return state, nil
}

func tableExists(conn *sql.DB, name string) bool {
	var exists int
	err := conn.QueryRow(`select 1 from sqlite_master where type='table' and name=? limit 1`, name).Scan(&exists)
	return err == nil && exists == 1
}

func hasUserTables(conn *sql.DB) bool {
	var count int
	err := conn.QueryRow(`select count(*) from sqlite_master where type='table' and name not like 'sqlite_%'`).Scan(&count)
	return err == nil && count > 0
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
	out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		os.Remove(outPath)
		return err
	}
	if err := out.Sync(); err != nil {
		os.Remove(outPath)
		return err
	}
	return os.Chmod(outPath, 0o600)
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
	{version: 2, name: "manual_game_flags", sql: manualGameFlagsSQL},
	{version: 3, name: "profile_game_tags", sql: profileGameTagsSQL},
	{version: 4, name: "profile_game_tags_playtime_fix", sql: profileGameTagsV4SQL},
	{version: 5, name: "profile_game_tags_threshold_fix", sql: profileGameTagsV5SQL},
	{version: 6, name: "profile_game_tags_suggestion_score_fix", sql: profileGameTagsV6SQL},
	{version: 7, name: "profile_game_tags_threshold_40", sql: profileGameTagsV7SQL},
	{version: 8, name: "profile_scoped_not_owned_flags", sql: profileScopedNotOwnedFlagsSQL},
}

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

CREATE TABLE IF NOT EXISTS games(
  appid integer primary key,
  name text not null,
  playtime_forever integer not null default 0,
  has_achievements integer not null default 0,
  achievement_status text not null default 'unknown',
  disabled integer not null default 0,
  disabled_reason text,
  cover_path text,
  cover_source_url text,
  cover_checked_at text,
  cover_downloaded_at text,
  created_at text not null,
  updated_at text not null
);

CREATE TABLE IF NOT EXISTS profile_games(
  profile_id integer not null,
  appid integer not null,
  total_achievements integer not null default 0,
  unlocked_achievements integer not null default 0,
  completion_percent real not null default 0,
  missing_avg_unlock real,
  suggestion_score real,
  is_completed integer not null default 0,
  was_completed integer not null default 0,
  newly_incomplete integer not null default 0,
  sync_warning integer not null default 0,
  last_error text,
  last_error_at text,
  last_synced_at text,
  primary key(profile_id, appid),
  foreign key(profile_id) references profiles(id),
  foreign key(appid) references games(appid)
);

CREATE TABLE IF NOT EXISTS achievements(
  appid integer not null,
  apiname text not null,
  global_percent real,
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
`

const manualGameFlagsSQL = `
CREATE TABLE IF NOT EXISTS profile_game_flags(
  profile_id integer not null,
  appid integer not null,
  manual_was_completed integer not null default 0,
  missing_achievements_in_dlc integer not null default 0,
  disabled integer not null default 0,
  disabled_reason text,
  created_at text not null,
  updated_at text not null,
  primary key(profile_id, appid),
  foreign key(profile_id) references profiles(id),
  foreign key(appid) references games(appid)
);

CREATE INDEX IF NOT EXISTS idx_profile_game_flags_profile_disabled on profile_game_flags(profile_id, disabled);
`

const profileGameTagsSQL = `
CREATE TABLE IF NOT EXISTS profile_game_tags(
  profile_id integer not null,
  appid integer not null,
  tag text not null,
  score real,
  reason text,
  computed_at text not null,
  primary key(profile_id, appid, tag),
  foreign key(profile_id) references profiles(id),
  foreign key(appid) references games(appid)
);

CREATE INDEX IF NOT EXISTS idx_profile_game_tags_profile_tag on profile_game_tags(profile_id, tag);

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'completed', null, 'completed', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'no_achievements', null, 'no achievements', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND (pg.total_achievements=0 OR g.has_achievements=0);

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'untouched', null, '0 playtime', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND pg.total_achievements>0 AND g.has_achievements=1 AND g.playtime_forever<=0;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
    SELECT profile_id, appid, CASE WHEN almost_score >= 40 THEN 'almost_there' ELSE 'in_progress' END, almost_score, reason, datetime('now')
FROM (
  SELECT pg.profile_id, pg.appid,
    coalesce(pg.suggestion_score, pg.completion_percent * 0.50 + coalesce(pg.missing_avg_unlock, 0) * 0.50) AS almost_score,
    printf('%.1f%% completion, %.1f%% missing avg unlock, suggestion score', pg.completion_percent, coalesce(pg.missing_avg_unlock, 0)) AS reason
  FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
  WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND pg.total_achievements>0 AND g.has_achievements=1 AND g.playtime_forever>0
);

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'new_achievements_added', null, 'previously completed, now incomplete', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.newly_incomplete=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'missing_achievements_in_dlc', null, 'manual DLC-missing flag', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND f.missing_achievements_in_dlc=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'missing_cover_art', null, 'no cached cover image', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND coalesce(g.cover_path,'')='';

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'sync_warning', null, 'latest sync warning', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.sync_warning=1;
`

const profileGameTagsV4SQL = `
DELETE FROM profile_game_tags
WHERE tag IN ('completed', 'no_achievements', 'untouched', 'in_progress', 'almost_there');

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'completed', null, 'completed', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'no_achievements', null, 'no achievements', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND (pg.total_achievements=0 OR g.has_achievements=0);

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'untouched', null, '0 playtime', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND pg.total_achievements>0 AND g.has_achievements=1 AND g.playtime_forever<=0;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
    SELECT profile_id, appid, CASE WHEN almost_score >= 40 THEN 'almost_there' ELSE 'in_progress' END, almost_score, reason, datetime('now')
FROM (
  SELECT pg.profile_id, pg.appid,
    coalesce(pg.suggestion_score, pg.completion_percent * 0.50 + coalesce(pg.missing_avg_unlock, 0) * 0.50) AS almost_score,
    printf('%.1f%% completion, %.1f%% missing avg unlock, suggestion score', pg.completion_percent, coalesce(pg.missing_avg_unlock, 0)) AS reason
  FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
  WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND pg.total_achievements>0 AND g.has_achievements=1 AND g.playtime_forever>0
);

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'new_achievements_added', null, 'previously completed, now incomplete', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.newly_incomplete=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'missing_achievements_in_dlc', null, 'manual DLC-missing flag', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND f.missing_achievements_in_dlc=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'missing_cover_art', null, 'no cached cover image', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND coalesce(g.cover_path,'')='';

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'sync_warning', null, 'latest sync warning', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.sync_warning=1;
`

const profileGameTagsV6SQL = `
DELETE FROM profile_game_tags
WHERE tag IN ('completed', 'no_achievements', 'untouched', 'in_progress', 'almost_there');

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'completed', null, 'completed', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'no_achievements', null, 'no achievements', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND (pg.total_achievements=0 OR g.has_achievements=0);

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'untouched', null, '0 playtime', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND pg.total_achievements>0 AND g.has_achievements=1 AND g.playtime_forever<=0;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
    SELECT profile_id, appid, CASE WHEN almost_score >= 40 THEN 'almost_there' ELSE 'in_progress' END, almost_score, reason, datetime('now')
FROM (
  SELECT pg.profile_id, pg.appid,
    coalesce(pg.suggestion_score, pg.completion_percent * 0.50 + coalesce(pg.missing_avg_unlock, 0) * 0.50) AS almost_score,
    printf('%.1f%% completion, %.1f%% missing avg unlock, suggestion score', pg.completion_percent, coalesce(pg.missing_avg_unlock, 0)) AS reason
  FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
  WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND pg.total_achievements>0 AND g.has_achievements=1 AND g.playtime_forever>0
);

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'new_achievements_added', null, 'previously completed, now incomplete', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.newly_incomplete=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'missing_achievements_in_dlc', null, 'manual DLC-missing flag', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND f.missing_achievements_in_dlc=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'missing_cover_art', null, 'no cached cover image', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND coalesce(g.cover_path,'')='';

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'sync_warning', null, 'latest sync warning', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.sync_warning=1;
`

const profileGameTagsV7SQL = `
DELETE FROM profile_game_tags
WHERE tag IN ('completed', 'no_achievements', 'untouched', 'in_progress', 'almost_there');

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'completed', null, 'completed', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'no_achievements', null, 'no achievements', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND (pg.total_achievements=0 OR g.has_achievements=0);

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'untouched', null, '0 playtime', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND pg.total_achievements>0 AND g.has_achievements=1 AND g.playtime_forever<=0;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT profile_id, appid, CASE WHEN almost_score >= 40 THEN 'almost_there' ELSE 'in_progress' END, almost_score, reason, datetime('now')
FROM (
  SELECT pg.profile_id, pg.appid,
    coalesce(pg.suggestion_score, pg.completion_percent * 0.50 + coalesce(pg.missing_avg_unlock, 0) * 0.50) AS almost_score,
    printf('%.1f%% completion, %.1f%% missing avg unlock, suggestion score', pg.completion_percent, coalesce(pg.missing_avg_unlock, 0)) AS reason
  FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
  WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND pg.total_achievements>0 AND g.has_achievements=1 AND g.playtime_forever>0
);

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'new_achievements_added', null, 'previously completed, now incomplete', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.newly_incomplete=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'missing_achievements_in_dlc', null, 'manual DLC-missing flag', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND f.missing_achievements_in_dlc=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'missing_cover_art', null, 'no cached cover image', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND coalesce(g.cover_path,'')='';

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'sync_warning', null, 'latest sync warning', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.sync_warning=1;
`

const profileScopedNotOwnedFlagsSQL = `
INSERT INTO profile_game_flags(profile_id, appid, disabled, disabled_reason, created_at, updated_at)
SELECT pg.profile_id, pg.appid, 1, coalesce(g.disabled_reason, 'not_owned'), datetime('now'), datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid
WHERE g.disabled=1
  AND pg.profile_id=coalesce(
    (select profile_id from sync_runs where profile_id is not null order by id desc limit 1),
    (select cast(value as integer) from settings where key='active_profile_id' limit 1),
    -1
  )
ON CONFLICT(profile_id, appid) DO UPDATE SET
  disabled=1,
  disabled_reason=excluded.disabled_reason,
  updated_at=excluded.updated_at;

UPDATE games SET disabled=0, disabled_reason=null, updated_at=datetime('now') WHERE disabled=1;
`

const profileGameTagsV5SQL = `
DELETE FROM profile_game_tags
WHERE tag IN ('completed', 'no_achievements', 'untouched', 'in_progress', 'almost_there');

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'completed', null, 'completed', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'no_achievements', null, 'no achievements', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND (pg.total_achievements=0 OR g.has_achievements=0);

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'untouched', null, '0 playtime', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND pg.total_achievements>0 AND g.has_achievements=1 AND g.playtime_forever<=0;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
    SELECT profile_id, appid, CASE WHEN almost_score >= 40 THEN 'almost_there' ELSE 'in_progress' END, almost_score, reason, datetime('now')
FROM (
  SELECT pg.profile_id, pg.appid,
    (pg.completion_percent * 0.50 + coalesce(pg.missing_avg_unlock, 0) * 0.35 + (100.0 / (1.0 + ((pg.total_achievements - pg.unlocked_achievements) / 5.0))) * 0.15) AS almost_score,
    printf('%.1f%% complete, %.1f%% missing avg unlock, %d missing', pg.completion_percent, coalesce(pg.missing_avg_unlock, 0), pg.total_achievements - pg.unlocked_achievements) AS reason
  FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
  WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.is_completed=0 AND pg.total_achievements>0 AND g.has_achievements=1 AND g.playtime_forever>0
);

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'new_achievements_added', null, 'previously completed, now incomplete', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.newly_incomplete=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'missing_achievements_in_dlc', null, 'manual DLC-missing flag', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND f.missing_achievements_in_dlc=1;

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'missing_cover_art', null, 'no cached cover image', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND coalesce(g.cover_path,'')='';

INSERT OR IGNORE INTO profile_game_tags(profile_id, appid, tag, score, reason, computed_at)
SELECT pg.profile_id, pg.appid, 'sync_warning', null, 'latest sync warning', datetime('now')
FROM profile_games pg JOIN games g ON g.appid=pg.appid LEFT JOIN profile_game_flags f ON f.profile_id=pg.profile_id AND f.appid=pg.appid
WHERE g.disabled=0 AND coalesce(f.disabled,0)=0 AND pg.sync_warning=1;
`
