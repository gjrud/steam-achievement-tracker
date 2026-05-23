package repository

import (
	"database/sql"
	"strings"
	"time"
)

func (r *Repository) UpsertOwnedGame(g GameRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(`insert into games(appid, name, playtime_forever, created_at, updated_at)
		values(?,?,?,?,?)
		on conflict(appid) do update set name=excluded.name, playtime_forever=excluded.playtime_forever, updated_at=excluded.updated_at`, g.AppID, g.Name, g.PlaytimeForever, now, now)
	return err
}

func (r *Repository) SaveGameSync(update GameSyncUpdate) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := update.SyncedAt
	_, err = tx.Exec(`insert into games(appid, name, playtime_forever, has_achievements, achievement_status, created_at, updated_at)
		values(?,?,?,?,?,?,?)
		on conflict(appid) do update set name=excluded.name, playtime_forever=excluded.playtime_forever, has_achievements=excluded.has_achievements, achievement_status=excluded.achievement_status, updated_at=excluded.updated_at`, update.AppID, update.Name, update.PlaytimeForever, boolInt(update.HasAchievements), update.AchievementStatus, now, now)
	if err != nil {
		return err
	}
	if err := pruneProfileAchievements(tx, update.ProfileID, update.AppID, update.Achievements); err != nil {
		return err
	}
	for _, ach := range update.Achievements {
		_, err = tx.Exec(`insert into achievements(appid, apiname, global_percent, updated_at) values(?,?,?,?)
			on conflict(appid, apiname) do update set global_percent=excluded.global_percent, updated_at=excluded.updated_at`, update.AppID, ach.APIName, ach.GlobalPercent, now)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`insert into profile_achievements(profile_id, appid, apiname, unlocked, unlock_time, updated_at) values(?,?,?,?,?,?)
			on conflict(profile_id, appid, apiname) do update set unlocked=excluded.unlocked, unlock_time=excluded.unlock_time, updated_at=excluded.updated_at`, update.ProfileID, update.AppID, ach.APIName, boolInt(ach.Unlocked), ach.UnlockTime, now)
		if err != nil {
			return err
		}
	}
	lastErrAt := any(nil)
	if update.LastError != nil {
		lastErrAt = now
	}
	_, err = tx.Exec(`insert into profile_games(profile_id, appid, total_achievements, unlocked_achievements, completion_percent, missing_avg_unlock, suggestion_score, is_completed, was_completed, newly_incomplete, sync_warning, last_error, last_error_at, last_synced_at)
		values(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		on conflict(profile_id, appid) do update set total_achievements=excluded.total_achievements, unlocked_achievements=excluded.unlocked_achievements, completion_percent=excluded.completion_percent, missing_avg_unlock=excluded.missing_avg_unlock, suggestion_score=excluded.suggestion_score, is_completed=excluded.is_completed, was_completed=excluded.was_completed, newly_incomplete=excluded.newly_incomplete, sync_warning=excluded.sync_warning, last_error=excluded.last_error, last_error_at=excluded.last_error_at, last_synced_at=excluded.last_synced_at`, update.ProfileID, update.AppID, update.TotalAchievements, update.UnlockedAchievements, update.CompletionPercent, update.MissingAvgUnlock, update.SuggestionScore, boolInt(update.IsCompleted), boolInt(update.WasCompleted), boolInt(update.NewlyIncomplete), boolInt(update.SyncWarning), update.LastError, lastErrAt, now)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`insert into game_snapshots(profile_id, appid, total_achievements, unlocked_achievements, completion_percent, missing_avg_unlock, is_completed, synced_at) values(?,?,?,?,?,?,?,?)`, update.ProfileID, update.AppID, update.TotalAchievements, update.UnlockedAchievements, update.CompletionPercent, update.MissingAvgUnlock, boolInt(update.IsCompleted), now)
	if err != nil {
		return err
	}
	if err := r.recomputeGameTagsTx(tx, update.ProfileID, update.AppID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) SaveGameWarning(profileID int64, game GameRecord, warning string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if err := r.UpsertOwnedGame(game); err != nil {
		return err
	}
	prev, err := r.ProfileGame(profileID, game.AppID)
	if err != nil {
		return err
	}
	if !prev.Exists {
		_, err = r.db.Exec(`insert into profile_games(profile_id, appid, sync_warning, last_error, last_error_at, last_synced_at) values(?,?,?,?,?,?)
			on conflict(profile_id, appid) do update set sync_warning=1, last_error=excluded.last_error, last_error_at=excluded.last_error_at`, profileID, game.AppID, 1, warning, now, nil)
		if err != nil {
			return err
		}
		return r.RecomputeGameTags(profileID, game.AppID)
	}
	_, err = r.db.Exec(`update profile_games set sync_warning=1, last_error=?, last_error_at=? where profile_id=? and appid=?`, warning, now, profileID, game.AppID)
	if err != nil {
		return err
	}
	return r.RecomputeGameTags(profileID, game.AppID)
}

func (r *Repository) SaveCover(appID int64, path, sourceURL string, downloaded bool) error {
	now := time.Now().UTC().Format(time.RFC3339)
	var downloadedAt any
	if downloaded {
		downloadedAt = now
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`update games set cover_path=?, cover_source_url=?, cover_checked_at=?, cover_downloaded_at=coalesce(?, cover_downloaded_at), updated_at=? where appid=?`, path, sourceURL, now, downloadedAt, now, appID); err != nil {
		return err
	}
	rows, err := tx.Query(`select profile_id from profile_games where appid=?`, appID)
	if err != nil {
		return err
	}
	var profileIDs []int64
	for rows.Next() {
		var profileID int64
		if err := rows.Scan(&profileID); err != nil {
			rows.Close()
			return err
		}
		profileIDs = append(profileIDs, profileID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, profileID := range profileIDs {
		if err := r.recomputeGameTagsTx(tx, profileID, appID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) MarkGamePreviouslyCompleted(profileID, appID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`insert into profile_game_flags(profile_id, appid, manual_was_completed, created_at, updated_at)
		values(?,?,?,?,?)
		on conflict(profile_id, appid) do update set manual_was_completed=case when manual_was_completed=1 then 0 else 1 end, updated_at=excluded.updated_at`, profileID, appID, 1, now, now); err != nil {
		return err
	}
	res, err := tx.Exec(`update profile_games set
		was_completed=case when is_completed=1 or exists(select 1 from game_snapshots where profile_id=? and appid=? and is_completed=1) or exists(select 1 from profile_game_flags where profile_id=? and appid=? and manual_was_completed=1) then 1 else 0 end,
		newly_incomplete=case when is_completed=0 and (exists(select 1 from game_snapshots where profile_id=? and appid=? and is_completed=1) or exists(select 1 from profile_game_flags where profile_id=? and appid=? and manual_was_completed=1)) then 1 else 0 end
		where profile_id=? and appid=?`, profileID, appID, profileID, appID, profileID, appID, profileID, appID, profileID, appID)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err != nil {
		return err
	} else if rows == 0 {
		return sql.ErrNoRows
	}
	if err := r.recomputeGameTagsTx(tx, profileID, appID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ToggleMissingAchievementsInDLC(profileID, appID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(`insert into profile_game_flags(profile_id, appid, missing_achievements_in_dlc, created_at, updated_at)
		values(?,?,?,?,?)
		on conflict(profile_id, appid) do update set missing_achievements_in_dlc=case when missing_achievements_in_dlc=1 then 0 else 1 end, updated_at=excluded.updated_at`, profileID, appID, 1, now, now)
	if err != nil {
		return err
	}
	return r.RecomputeGameTags(profileID, appID)
}

func (r *Repository) DisableMissingOwnedGames(profileID int64, owned []int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(owned) == 0 {
		if _, err := tx.Exec(`insert into profile_game_flags(profile_id, appid, disabled, disabled_reason, created_at, updated_at)
			select profile_id, appid, 1, 'not_owned', ?, ? from profile_games where profile_id=?
			on conflict(profile_id, appid) do update set disabled=1, disabled_reason=excluded.disabled_reason, updated_at=excluded.updated_at`, now, now, profileID); err != nil {
			return err
		}
		if _, err := tx.Exec(`delete from profile_game_tags where profile_id=?`, profileID); err != nil {
			return err
		}
		return tx.Commit()
	}
	pl := make([]string, len(owned))
	ownedArgs := make([]any, 0, len(owned)+1)
	ownedArgs = append(ownedArgs, profileID)
	for i, id := range owned {
		pl[i] = "?"
		ownedArgs = append(ownedArgs, id)
	}
	inClause := strings.Join(pl, ",")
	if _, err := tx.Exec(`update profile_game_flags set disabled=0, disabled_reason=null, updated_at=? where profile_id=? and disabled=1 and disabled_reason='not_owned' and appid in (`+inClause+")", append([]any{now}, ownedArgs...)...); err != nil {
		return err
	}
	missingArgs := make([]any, 0, len(owned)+3)
	missingArgs = append(missingArgs, now, now, profileID)
	for _, id := range owned {
		missingArgs = append(missingArgs, id)
	}
	if _, err := tx.Exec(`insert into profile_game_flags(profile_id, appid, disabled, disabled_reason, created_at, updated_at)
		select profile_id, appid, 1, 'not_owned', ?, ? from profile_games where profile_id=? and appid not in (`+inClause+`)
		on conflict(profile_id, appid) do update set disabled=1, disabled_reason=excluded.disabled_reason, updated_at=excluded.updated_at`, missingArgs...); err != nil {
		return err
	}
	deleteArgs := make([]any, 0, len(owned)+1)
	deleteArgs = append(deleteArgs, profileID)
	for _, id := range owned {
		deleteArgs = append(deleteArgs, id)
	}
	if _, err := tx.Exec(`delete from profile_game_tags where profile_id=? and appid not in (`+inClause+")", deleteArgs...); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) DisableProfileGame(profileID, appID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`insert into profile_game_flags(profile_id, appid, disabled, disabled_reason, created_at, updated_at)
		values(?,?,?,?,?,?)
		on conflict(profile_id, appid) do update set disabled=1, disabled_reason=excluded.disabled_reason, updated_at=excluded.updated_at`, profileID, appID, 1, "manual", now, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`delete from profile_game_tags where profile_id=? and appid=?`, profileID, appID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) EnableProfileGame(profileID, appID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`update profile_game_flags set disabled=0, disabled_reason=null, updated_at=? where profile_id=? and appid=?`, now, profileID, appID); err != nil {
		return err
	}
	if err := r.recomputeGameTagsTx(tx, profileID, appID); err != nil {
		return err
	}
	return tx.Commit()
}
