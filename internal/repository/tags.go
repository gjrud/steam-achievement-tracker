package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (r *Repository) profileGameTags(profileID int64) (map[int64][]string, error) {
	rows, err := r.db.Query(`select appid, tag from profile_game_tags where profile_id=? order by appid,
		case tag
			when 'new_achievements_added' then 0
			when 'completed' then 1
			when 'almost_there' then 2
			when 'in_progress' then 3
			when 'untouched' then 4
			when 'no_achievements' then 5
			when 'missing_achievements_in_dlc' then 6
			when 'missing_cover_art' then 7
			when 'sync_warning' then 8
			else 99
		end, tag`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := make(map[int64][]string)
	for rows.Next() {
		var appID int64
		var tag string
		if err := rows.Scan(&appID, &tag); err != nil {
			return nil, err
		}
		tags[appID] = append(tags[appID], tag)
	}
	return tags, rows.Err()
}

func (r *Repository) RecomputeGameTags(profileID, appID int64) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := r.recomputeGameTagsTx(tx, profileID, appID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) recomputeGameTagsTx(tx *sql.Tx, profileID, appID int64) error {
	if _, err := tx.Exec(`delete from profile_game_tags where profile_id=? and appid=?`, profileID, appID); err != nil {
		return err
	}
	row := tx.QueryRow(`select g.has_achievements, g.playtime_forever, coalesce(g.cover_path,''), coalesce(f.disabled,0), coalesce(f.missing_achievements_in_dlc,0),
		pg.total_achievements, pg.unlocked_achievements, pg.completion_percent, pg.missing_avg_unlock, pg.suggestion_score, pg.is_completed, pg.newly_incomplete, pg.sync_warning, pg.last_synced_at
		from profile_games pg join games g on g.appid=pg.appid left join profile_game_flags f on f.profile_id=pg.profile_id and f.appid=pg.appid
		where pg.profile_id=? and pg.appid=?`, profileID, appID)
	var hasAchievements, playtimeForever, flagDisabled, missingDLC, isCompleted, newlyIncomplete, syncWarning int
	var coverPath string
	var total, unlocked int
	var completion float64
	var missingAvg sql.NullFloat64
	var suggestionScore sql.NullFloat64
	var lastSyncedAt sql.NullString
	err := row.Scan(&hasAchievements, &playtimeForever, &coverPath, &flagDisabled, &missingDLC, &total, &unlocked, &completion, &missingAvg, &suggestionScore, &isCompleted, &newlyIncomplete, &syncWarning, &lastSyncedAt)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if flagDisabled == 1 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	insert := func(tag string, score *float64, reason string) error {
		_, err := tx.Exec(`insert into profile_game_tags(profile_id, appid, tag, score, reason, computed_at) values(?,?,?,?,?,?)`, profileID, appID, tag, score, nullString(reason), now)
		return err
	}
	canInferStats := !(syncWarning == 1 && !lastSyncedAt.Valid)
	if canInferStats {
		noAchievements := total == 0 || hasAchievements == 0
		untouched := playtimeForever <= 0
		if isCompleted == 1 {
			if err := insert("completed", nil, "completed"); err != nil {
				return err
			}
		}
		if noAchievements {
			if err := insert("no_achievements", nil, "no achievements"); err != nil {
				return err
			}
		}
		if untouched {
			if err := insert("untouched", nil, "0 playtime"); err != nil {
				return err
			}
		}
		if isCompleted == 0 && !noAchievements && !untouched {
			score := suggestionScoreOrFallback(suggestionScore, completion, missingAvg)
			tag := "in_progress"
			if score >= almostThereThreshold {
				tag = "almost_there"
			}
			reason := fmt.Sprintf("%.1f%% completion, %.1f%% missing avg unlock, suggestion score", completion, nullFloat(missingAvg))
			if err := insert(tag, &score, reason); err != nil {
				return err
			}
		}
	}
	if newlyIncomplete == 1 {
		if err := insert("new_achievements_added", nil, "previously completed, now incomplete"); err != nil {
			return err
		}
	}
	if missingDLC == 1 {
		if err := insert("missing_achievements_in_dlc", nil, "manual DLC-missing flag"); err != nil {
			return err
		}
	}
	if strings.TrimSpace(coverPath) == "" {
		if err := insert("missing_cover_art", nil, "no cached cover image"); err != nil {
			return err
		}
	}
	if syncWarning == 1 {
		if err := insert("sync_warning", nil, "latest sync warning"); err != nil {
			return err
		}
	}
	return nil
}

func suggestionScoreOrFallback(score sql.NullFloat64, completion float64, missingAvg sql.NullFloat64) float64 {
	if score.Valid {
		return score.Float64
	}
	if missingAvg.Valid {
		return completion*0.50 + missingAvg.Float64*0.50
	}
	return completion * 0.50
}
