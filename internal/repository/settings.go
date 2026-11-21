package repository

import (
	"database/sql"
	"fmt"
	"time"
)

// updateChecksEnabledKey is the app-wide settings key for the startup update check. Stored as "1" (on) or "0" (off).
const updateChecksEnabledKey = "update_checks_enabled"

// UpdateChecksEnabled reports whether the startup update check is enabled. A missing row means enabled (the default).
// A stored value other than "1" or "0", or a read failure, returns an error with a false result: callers must treat
// the setting as unknown and skip the startup check rather than guess.
func (r *Repository) UpdateChecksEnabled() (bool, error) {
	var value string
	err := r.db.QueryRow(`select value from settings where key=?`, updateChecksEnabledKey).Scan(&value)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	switch value {
	case "1":
		return true, nil
	case "0":
		return false, nil
	default:
		return false, fmt.Errorf("settings %s: invalid value %q", updateChecksEnabledKey, value)
	}
}

// SetUpdateChecksEnabled stores the startup update check setting. Other settings rows, including active_profile_id,
// are not touched.
func (r *Repository) SetUpdateChecksEnabled(enabled bool) error {
	value := "0"
	if enabled {
		value = "1"
	}
	_, err := r.db.Exec(`insert into settings(key, value) values(?, ?) on conflict(key) do update set value=excluded.value`, updateChecksEnabledKey, value)
	return err
}

// LibrarySettings controls which games GetOwnedGames returns during sync. Changes apply on the next sync.
// Each profile has its own values (profile_library_settings).
type LibrarySettings struct {
	IncludeUnvettedApps    bool `json:"includeUnvettedApps"`
	IncludePlayedFreeGames bool `json:"includePlayedFreeGames"`
}

// DefaultLibrarySettings is the library sync behavior for a profile with no saved settings: all games are synced.
// It is the single source for the default used when no profile_library_settings row exists.
func DefaultLibrarySettings() LibrarySettings {
	return LibrarySettings{IncludeUnvettedApps: true, IncludePlayedFreeGames: true}
}

// LibrarySettings reads the library sync settings of profileID. A profile without a row gets DefaultLibrarySettings.
func (r *Repository) LibrarySettings(profileID int64) (LibrarySettings, error) {
	var includeUnvetted, includePlayedFree int
	err := r.db.QueryRow(`select include_unvetted_apps, include_played_free_games from profile_library_settings where profile_id=?`, profileID).Scan(&includeUnvetted, &includePlayedFree)
	if err == sql.ErrNoRows {
		return DefaultLibrarySettings(), nil
	}
	if err != nil {
		return LibrarySettings{}, err
	}
	return LibrarySettings{IncludeUnvettedApps: includeUnvetted != 0, IncludePlayedFreeGames: includePlayedFree != 0}, nil
}

// SaveLibrarySettings upserts the library sync settings of profileID. Other profiles are not touched.
func (r *Repository) SaveLibrarySettings(profileID int64, s LibrarySettings) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(`insert into profile_library_settings(profile_id, include_unvetted_apps, include_played_free_games, updated_at)
		values(?,?,?,?)
		on conflict(profile_id) do update set include_unvetted_apps=excluded.include_unvetted_apps, include_played_free_games=excluded.include_played_free_games, updated_at=excluded.updated_at`,
		profileID, boolInt(s.IncludeUnvettedApps), boolInt(s.IncludePlayedFreeGames), now)
	return err
}
