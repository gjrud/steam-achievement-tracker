package repository

import (
	"database/sql"
	"fmt"
	"time"
)

func (r *Repository) ActiveProfile() (*Profile, error) {
	var activeID string
	err := r.db.QueryRow(`select value from settings where key='active_profile_id'`).Scan(&activeID)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if activeID != "" {
		profile, err := scanProfile(r.db.QueryRow(`select id, steam_id64, display_name, avatar_url, created_at, updated_at, last_synced_at from profiles where id=?`, activeID))
		if err != nil {
			return nil, err
		}
		if profile != nil {
			return profile, nil
		}
	}
	row := r.db.QueryRow(`select id, steam_id64, display_name, avatar_url, created_at, updated_at, last_synced_at from profiles order by id limit 1`)
	return scanProfile(row)
}

func (r *Repository) SaveProfile(steamID64, displayName, avatarURL string) (*Profile, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`insert into profiles(steam_id64, display_name, avatar_url, created_at, updated_at)
		values(?,?,?,?,?)
		on conflict(steam_id64) do update set display_name=excluded.display_name, avatar_url=excluded.avatar_url, updated_at=excluded.updated_at`, steamID64, displayName, nullString(avatarURL), now, now)
	if err != nil {
		return nil, err
	}
	var id int64
	if err := tx.QueryRow(`select id from profiles where steam_id64=?`, steamID64).Scan(&id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`insert into settings(key, value) values('active_profile_id', ?) on conflict(key) do update set value=excluded.value`, fmt.Sprintf("%d", id)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.ActiveProfile()
}

func (r *Repository) ClearProfileData(profileID int64) ([]int64, error) {
	appIDs, err := r.profileAppIDs(profileID)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`delete from profile_achievements where profile_id=?`,
		`delete from profile_game_flags where profile_id=?`,
		`delete from profile_game_tags where profile_id=?`,
		`delete from game_snapshots where profile_id=?`,
		`delete from profile_games where profile_id=?`,
		`delete from sync_runs where profile_id=?`,
	} {
		if _, err := tx.Exec(stmt, profileID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`delete from settings where key='active_profile_id' and value=?`, fmt.Sprintf("%d", profileID)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`delete from profiles where id=?`, profileID); err != nil {
		return nil, err
	}
	if err := deleteOrphanRowsForAppIDs(tx, appIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return appIDs, nil
}

func (r *Repository) profileAppIDs(profileID int64) ([]int64, error) {
	rows, err := r.db.Query(`select distinct appid from (
		select appid from profile_games where profile_id=?
		union select appid from profile_achievements where profile_id=?
		union select appid from profile_game_flags where profile_id=?
		union select appid from profile_game_tags where profile_id=?
		union select appid from game_snapshots where profile_id=?
	) order by appid`, profileID, profileID, profileID, profileID, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var appIDs []int64
	for rows.Next() {
		var appID int64
		if err := rows.Scan(&appID); err != nil {
			return nil, err
		}
		appIDs = append(appIDs, appID)
	}
	return appIDs, rows.Err()
}

func (r *Repository) TouchProfileSynced(profileID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(`update profiles set last_synced_at=?, updated_at=? where id=?`, now, now, profileID)
	return err
}
