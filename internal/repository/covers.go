package repository

import (
	"database/sql"
	"fmt"
	"time"
)

type rowScanner interface {
	Scan(dest ...any) error
}

// profileEntry is one profile entry (appid + groupid) for a profile.
type profileEntry struct {
	ProfileID int64
	AppID     int64
	GroupID   int64
}

// SaveCover upserts the cover cached for an image app id and recomputes tags of every profile entry shown with it.
// A non-downloaded update keeps the previous downloaded_at.
func (r *Repository) SaveCover(c CoverUpdate) error {
	if c.ImageAppID <= 0 {
		return fmt.Errorf("save cover: invalid image appid %d", c.ImageAppID)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var downloadedAt any
	if c.Downloaded {
		downloadedAt = now
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`insert into cover_images(image_appid, kind, file_name, path, source_url, checked_at, downloaded_at) values(?,?,?,?,?,?,?)
		on conflict(image_appid) do update set kind=excluded.kind, file_name=excluded.file_name, path=excluded.path, source_url=excluded.source_url, checked_at=excluded.checked_at, downloaded_at=coalesce(excluded.downloaded_at, cover_images.downloaded_at)`,
		c.ImageAppID, c.Kind, c.FileName, nullString(c.Path), nullString(c.SourceURL), now, downloadedAt); err != nil {
		return err
	}
	entries, err := entriesForImageApp(tx, c.ImageAppID)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := r.recomputeGameTagsTx(tx, e.ProfileID, e.AppID, e.GroupID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetCover returns the cached cover row for an image app id. ok is false when none is stored.
func (r *Repository) GetCover(imageAppID int64) (CoverRecord, bool, error) {
	row := r.db.QueryRow(`select image_appid, kind, file_name, coalesce(path,''), coalesce(source_url,''), checked_at, downloaded_at from cover_images where image_appid=?`, imageAppID)
	c, err := scanCover(row)
	if err == sql.ErrNoRows {
		return CoverRecord{}, false, nil
	}
	if err != nil {
		return CoverRecord{}, false, err
	}
	return c, true, nil
}

// ListCovers returns every cached cover keyed by image app id (for staleness checks).
func (r *Repository) ListCovers() (map[int64]CoverRecord, error) {
	rows, err := r.db.Query(`select image_appid, kind, file_name, coalesce(path,''), coalesce(source_url,''), checked_at, downloaded_at from cover_images`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	covers := make(map[int64]CoverRecord)
	for rows.Next() {
		c, err := scanCover(rows)
		if err != nil {
			return nil, err
		}
		covers[c.ImageAppID] = c
	}
	return covers, rows.Err()
}

func scanCover(scanner rowScanner) (CoverRecord, error) {
	var c CoverRecord
	var checked, downloaded sql.NullString
	if err := scanner.Scan(&c.ImageAppID, &c.Kind, &c.FileName, &c.Path, &c.SourceURL, &checked, &downloaded); err != nil {
		return CoverRecord{}, err
	}
	c.CheckedAt = stringPtr(checked)
	c.DownloadedAt = stringPtr(downloaded)
	return c, nil
}

// entriesForImageApp lists profile entries whose cover may change when imageAppID's cover changes: entries that
// own imageAppID's store art, and every entry of game imageAppID, since its DLC entries fall back to the game cover.
func entriesForImageApp(tx *sql.Tx, imageAppID int64) ([]profileEntry, error) {
	rows, err := tx.Query(`select pg.profile_id, pg.appid, pg.groupid from profile_games pg
		left join game_sets gs on gs.appid=pg.appid and gs.groupid=pg.groupid
		where `+imageAppIDSQL+` = ? or pg.appid = ?`, imageAppID, imageAppID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []profileEntry
	for rows.Next() {
		var e profileEntry
		if err := rows.Scan(&e.ProfileID, &e.AppID, &e.GroupID); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
