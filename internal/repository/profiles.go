package repository

import (
	"database/sql"
	"fmt"
	"slices"
	"time"
)

// ActiveProfile returns the active profile. A settings row with an empty value means no profile is active (set by
// DeactivateProfile and ClearProfileData) and returns nil even when profiles exist. A row holding a profile id returns
// that profile, or nil when the profile no longer exists; it never falls back to another profile. A missing row also
// means no active profile: SaveProfile always writes the row and nothing deletes it, so there is no other way to reach
// that state.
func (r *Repository) ActiveProfile() (*Profile, error) {
	return activeProfile(r.db)
}

// querier is the read side shared by *sql.DB and *sql.Tx. Reads that run inside a transaction take the tx: the
// database has one connection (SetMaxOpenConns(1)), so a read on the *sql.DB while a tx is open waits forever.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// activeProfile is ActiveProfile on q. ClearProfileData calls it with its transaction.
func activeProfile(q querier) (*Profile, error) {
	var activeID string
	err := q.QueryRow(`select value from settings where key='active_profile_id'`).Scan(&activeID)
	switch {
	case err == sql.ErrNoRows:
		return nil, nil
	case err != nil:
		return nil, err
	}
	if activeID == "" {
		return nil, nil
	}
	return scanProfile(q.QueryRow(`select id, steam_id64, display_name, avatar_url, created_at, updated_at, last_synced_at from profiles where id=?`, activeID))
}

// DeactivateProfile sets no profile as active without touching any profile data. The setting row is kept with an
// empty value, meaning no active profile. Saving the same SteamID64 again re-activates it.
func (r *Repository) DeactivateProfile() error {
	_, err := r.db.Exec(`insert into settings(key, value) values('active_profile_id', '') on conflict(key) do update set value=excluded.value`)
	return err
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

// ClearProfileData deletes a profile and returns the image app ids whose cover_images rows were removed,
// i.e. no remaining profile still displays them. Their cached image directories are safe to delete.
// Image app ids still used by other profiles are not returned, so their covers stay on disk.
// The profile's library settings row is deleted with the profile (it references profiles(id)); a profile
// that is re-added starts from DefaultLibrarySettings.
// If this profile is the active one, the active setting is set to an empty value (no active profile) instead of
// being deleted.
//
// Every read runs inside the transaction, before any delete. A sync that writes shared games, achievements, game_sets
// or cover_images rows cannot insert them between the reads that pick the candidates and the delete that cleans them.
func (r *Repository) ClearProfileData(profileID int64) ([]int64, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	active, err := activeProfile(tx)
	if err != nil {
		return nil, err
	}
	wasActive := active != nil && active.ID == profileID
	appIDs, err := profileAppIDs(tx, profileID)
	if err != nil {
		return nil, err
	}
	imageAppIDs, err := profileDLCImageAppIDs(tx, profileID)
	if err != nil {
		return nil, err
	}
	for _, stmt := range []string{
		`delete from profile_achievements where profile_id=?`,
		`delete from profile_game_flags where profile_id=?`,
		`delete from profile_owned_games where profile_id=?`,
		`delete from profile_game_tags where profile_id=?`,
		`delete from game_snapshots where profile_id=?`,
		`delete from profile_games where profile_id=?`,
		`delete from sync_runs where profile_id=?`,
		`delete from profile_library_settings where profile_id=?`,
	} {
		if _, err := tx.Exec(stmt, profileID); err != nil {
			return nil, err
		}
	}
	if wasActive {
		// wasActive implies the row exists (ActiveProfile returned a profile from it), so a plain update is enough.
		if _, err := tx.Exec(`update settings set value='' where key='active_profile_id'`); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`delete from profiles where id=?`, profileID); err != nil {
		return nil, err
	}
	if err := deleteOrphanRowsForAppIDs(tx, appIDs); err != nil {
		return nil, err
	}
	removed, err := deleteOrphanCoverRows(tx, mergeAppIDs(appIDs, imageAppIDs))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return removed, nil
}

func profileDLCImageAppIDs(q querier, profileID int64) ([]int64, error) {
	rows, err := q.Query(`select distinct gs.dlcappid from profile_games pg
		join game_sets gs on gs.appid=pg.appid and gs.groupid=pg.groupid
		where pg.profile_id=? and gs.dlcappid<>0 order by gs.dlcappid`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func mergeAppIDs(a, b []int64) []int64 {
	seen := make(map[int64]bool, len(a)+len(b))
	merged := make([]int64, 0, len(a)+len(b))
	for _, id := range append(append([]int64{}, a...), b...) {
		if !seen[id] {
			seen[id] = true
			merged = append(merged, id)
		}
	}
	slices.Sort(merged)
	return merged
}

func profileAppIDs(q querier, profileID int64) ([]int64, error) {
	rows, err := q.Query(`select distinct appid from (
		select appid from profile_games where profile_id=?
		union select appid from profile_achievements where profile_id=?
		union select appid from profile_game_flags where profile_id=?
		union select appid from profile_game_tags where profile_id=?
		union select appid from game_snapshots where profile_id=?
		union select appid from profile_owned_games where profile_id=?
	) order by appid`, profileID, profileID, profileID, profileID, profileID, profileID)
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
