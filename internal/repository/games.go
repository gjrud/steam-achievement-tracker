package repository

import (
	"database/sql"
	"strings"
	"time"
)

// UpsertOwnedGame records one owned game for a profile. The shared games row gets the name only; the
// per-profile playtime, purchased and vetted flags go to profile_owned_games, so another profile's values are untouched.
func (r *Repository) UpsertOwnedGame(profileID int64, g GameRecord) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`insert into games(appid, name, created_at, updated_at) values(?,?,?,?)
		on conflict(appid) do update set name=excluded.name, updated_at=excluded.updated_at`, g.AppID, g.Name, now, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`insert into profile_owned_games(profile_id, appid, playtime_forever, purchased, vetted, updated_at) values(?,?,?,?,?,?)
		on conflict(profile_id, appid) do update set playtime_forever=excluded.playtime_forever, purchased=excluded.purchased, vetted=excluded.vetted, updated_at=excluded.updated_at`, profileID, g.AppID, g.PlaytimeForever, boolInt(g.Purchased), boolInt(!g.Unvetted), now); err != nil {
		return err
	}
	return tx.Commit()
}

// SaveGameSync writes one game and all its eligible entries in a single transaction. The playtime goes to
// the profile's profile_owned_games row. Its purchased and vetted flags are preserved: UpsertOwnedGame sets them
// from the library lists, and this write does not reset them (a brand-new row defaults to purchased=0, vetted=1).
func (r *Repository) SaveGameSync(update GameSyncUpdate) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := update.SyncedAt
	_, err = tx.Exec(`insert into games(appid, name, has_achievements, achievement_status, created_at, updated_at)
		values(?,?,?,?,?,?)
		on conflict(appid) do update set name=excluded.name, has_achievements=excluded.has_achievements, achievement_status=excluded.achievement_status, updated_at=excluded.updated_at`, update.AppID, update.Name, boolInt(update.HasAchievements), update.AchievementStatus, now, now)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`insert into profile_owned_games(profile_id, appid, playtime_forever, purchased, updated_at) values(?,?,?,0,?)
		on conflict(profile_id, appid) do update set playtime_forever=excluded.playtime_forever, updated_at=excluded.updated_at`, update.ProfileID, update.AppID, update.PlaytimeForever, now); err != nil {
		return err
	}
	if err := pruneProfileAchievements(tx, update.ProfileID, update.AppID, allSetAchievements(update.Sets)); err != nil {
		return err
	}
	if err := upsertGameSets(tx, update.AppID, update.Sets, now); err != nil {
		return err
	}
	for _, set := range update.Sets {
		if err := upsertSetAchievements(tx, update.ProfileID, update.AppID, set, now); err != nil {
			return err
		}
	}
	if err := pruneStaleEntries(tx, update.ProfileID, update.AppID, groupIDs(update.Sets)); err != nil {
		return err
	}
	for _, set := range update.Sets {
		if err := upsertEntry(tx, update.ProfileID, update.AppID, set, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`insert into game_snapshots(profile_id, appid, groupid, total_achievements, unlocked_achievements, completion_percent, missing_avg_unlock, is_completed, synced_at) values(?,?,?,?,?,?,?,?,?)`, update.ProfileID, update.AppID, set.GroupID, set.TotalAchievements, set.UnlockedAchievements, set.CompletionPercent, set.MissingAvgUnlock, boolInt(set.IsCompleted), now); err != nil {
			return err
		}
		if err := r.recomputeGameTagsTx(tx, update.ProfileID, update.AppID, set.GroupID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func allSetAchievements(sets []SetSyncUpdate) []AchievementRecord {
	var all []AchievementRecord
	for _, set := range sets {
		all = append(all, set.Achievements...)
	}
	return all
}

func groupIDs(sets []SetSyncUpdate) []int64 {
	ids := make([]int64, 0, len(sets))
	for _, set := range sets {
		ids = append(ids, set.GroupID)
	}
	return ids
}

func upsertGameSets(tx *sql.Tx, appID int64, sets []SetSyncUpdate, now string) error {
	for _, set := range sets {
		if _, err := tx.Exec(`insert into game_sets(appid, groupid, name, dlcappid, sort_order, updated_at) values(?,?,?,?,?,?)
			on conflict(appid, groupid) do update set name=excluded.name, dlcappid=excluded.dlcappid, sort_order=excluded.sort_order, updated_at=excluded.updated_at`, appID, set.GroupID, set.Name, set.DLCAppID, set.Order, now); err != nil {
			return err
		}
	}
	return nil
}

func upsertSetAchievements(tx *sql.Tx, profileID, appID int64, set SetSyncUpdate, now string) error {
	for _, ach := range set.Achievements {
		if _, err := tx.Exec(`insert into achievements(appid, apiname, groupid, archived, global_percent, updated_at) values(?,?,?,?,?,?)
			on conflict(appid, apiname) do update set groupid=excluded.groupid, archived=excluded.archived, global_percent=excluded.global_percent, updated_at=excluded.updated_at`, appID, ach.APIName, set.GroupID, boolInt(ach.Archived), ach.GlobalPercent, now); err != nil {
			return err
		}
		if _, err := tx.Exec(`insert into profile_achievements(profile_id, appid, apiname, unlocked, unlock_time, updated_at) values(?,?,?,?,?,?)
			on conflict(profile_id, appid, apiname) do update set unlocked=excluded.unlocked, unlock_time=excluded.unlock_time, updated_at=excluded.updated_at`, profileID, appID, ach.APIName, boolInt(ach.Unlocked), ach.UnlockTime, now); err != nil {
			return err
		}
	}
	return nil
}

func upsertEntry(tx *sql.Tx, profileID, appID int64, set SetSyncUpdate, now string) error {
	lastErrAt := any(nil)
	if set.LastError != nil {
		lastErrAt = now
	}
	_, err := tx.Exec(`insert into profile_games(profile_id, appid, groupid, total_achievements, unlocked_achievements, completion_percent, missing_avg_unlock, suggestion_score, is_completed, was_completed, newly_incomplete, sync_warning, last_error, last_error_at, last_synced_at, dlc_not_owned)
		values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		on conflict(profile_id, appid, groupid) do update set total_achievements=excluded.total_achievements, unlocked_achievements=excluded.unlocked_achievements, completion_percent=excluded.completion_percent, missing_avg_unlock=excluded.missing_avg_unlock, suggestion_score=excluded.suggestion_score, is_completed=excluded.is_completed, was_completed=excluded.was_completed, newly_incomplete=excluded.newly_incomplete, sync_warning=excluded.sync_warning, last_error=excluded.last_error, last_error_at=excluded.last_error_at, last_synced_at=excluded.last_synced_at, dlc_not_owned=excluded.dlc_not_owned`,
		profileID, appID, set.GroupID, set.TotalAchievements, set.UnlockedAchievements, set.CompletionPercent, set.MissingAvgUnlock, set.SuggestionScore, boolInt(set.IsCompleted), boolInt(set.WasCompleted), boolInt(set.NewlyIncomplete), boolInt(set.SyncWarning), set.LastError, lastErrAt, now, boolInt(set.DLCNotOwned))
	return err
}

// pruneStaleEntries removes profile entries (and their cached tags) whose groupid is no longer eligible.
// Flags and snapshots are kept.
func pruneStaleEntries(tx *sql.Tx, profileID, appID int64, keep []int64) error {
	where := `profile_id=? and appid=?`
	args := []any{profileID, appID}
	if len(keep) > 0 {
		pl := make([]string, len(keep))
		for i, id := range keep {
			pl[i] = "?"
			args = append(args, id)
		}
		where += ` and groupid not in (` + strings.Join(pl, ",") + `)`
	}
	if _, err := tx.Exec(`delete from profile_game_tags where `+where, args...); err != nil {
		return err
	}
	_, err := tx.Exec(`delete from profile_games where `+where, args...)
	return err
}

func profileGroupIDs(tx *sql.Tx, profileID, appID int64) ([]int64, error) {
	rows, err := tx.Query(`select groupid from profile_games where profile_id=? and appid=? order by groupid`, profileID, appID)
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

// SaveGameWarning marks a sync failure on every existing entry of the game, or creates a group-0 placeholder.
func (r *Repository) SaveGameWarning(profileID int64, game GameRecord, warning string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if err := r.UpsertOwnedGame(profileID, game); err != nil {
		return err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	groups, err := profileGroupIDs(tx, profileID, game.AppID)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		if _, err := tx.Exec(`insert into profile_games(profile_id, appid, groupid, sync_warning, last_error, last_error_at, last_synced_at) values(?,?,0,?,?,?,NULL)
			on conflict(profile_id, appid, groupid) do update set sync_warning=1, last_error=excluded.last_error, last_error_at=excluded.last_error_at`, profileID, game.AppID, 1, warning, now); err != nil {
			return err
		}
		groups = []int64{0}
	} else if _, err := tx.Exec(`update profile_games set sync_warning=1, last_error=?, last_error_at=? where profile_id=? and appid=?`, warning, now, profileID, game.AppID); err != nil {
		return err
	}
	for _, groupID := range groups {
		if err := r.recomputeGameTagsTx(tx, profileID, game.AppID, groupID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) MarkGamePreviouslyCompleted(profileID, appID, groupID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`insert into profile_game_flags(profile_id, appid, groupid, manual_was_completed, created_at, updated_at)
		values(?,?,?,?,?,?)
		on conflict(profile_id, appid, groupid) do update set manual_was_completed=case when manual_was_completed=1 then 0 else 1 end, updated_at=excluded.updated_at`, profileID, appID, groupID, 1, now, now); err != nil {
		return err
	}
	res, err := tx.Exec(`update profile_games set
		was_completed=case when is_completed=1 or exists(select 1 from game_snapshots where profile_id=? and appid=? and groupid=? and is_completed=1) or exists(select 1 from profile_game_flags where profile_id=? and appid=? and groupid=? and manual_was_completed=1) then 1 else 0 end,
		newly_incomplete=case when is_completed=0 and (exists(select 1 from game_snapshots where profile_id=? and appid=? and groupid=? and is_completed=1) or exists(select 1 from profile_game_flags where profile_id=? and appid=? and groupid=? and manual_was_completed=1)) then 1 else 0 end
		where profile_id=? and appid=? and groupid=?`,
		profileID, appID, groupID, profileID, appID, groupID, profileID, appID, groupID, profileID, appID, groupID, profileID, appID, groupID)
	if err != nil {
		return err
	}
	if rows, err := res.RowsAffected(); err != nil {
		return err
	} else if rows == 0 {
		return sql.ErrNoRows
	}
	if err := r.recomputeGameTagsTx(tx, profileID, appID, groupID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ToggleMissingAchievementsInDLC(profileID, appID, groupID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(`insert into profile_game_flags(profile_id, appid, groupid, missing_achievements_in_dlc, created_at, updated_at)
		values(?,?,?,?,?,?)
		on conflict(profile_id, appid, groupid) do update set missing_achievements_in_dlc=case when missing_achievements_in_dlc=1 then 0 else 1 end, updated_at=excluded.updated_at`, profileID, appID, groupID, 1, now, now)
	if err != nil {
		return err
	}
	return r.RecomputeGameTags(profileID, appID, groupID)
}

// notOwnedReasonSQL is the disabled_reason written by the not-owned sweep on conflict. An entry already
// disabled manually keeps its manual reason, so a later re-own does not re-enable it.
const notOwnedReasonSQL = `case when profile_game_flags.disabled=1 and profile_game_flags.disabled_reason='manual' then 'manual' else excluded.disabled_reason end`

// DisableMissingOwnedGames flags every entry of each game the profile no longer owns as not_owned,
// and re-enables not_owned flags for games that are owned again.
func (r *Repository) DisableMissingOwnedGames(profileID int64, owned []int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(owned) == 0 {
		if _, err := tx.Exec(`insert into profile_game_flags(profile_id, appid, groupid, disabled, disabled_reason, created_at, updated_at)
			select profile_id, appid, groupid, 1, 'not_owned', ?, ? from profile_games where profile_id=?
			on conflict(profile_id, appid, groupid) do update set disabled=1, disabled_reason=`+notOwnedReasonSQL+`, updated_at=excluded.updated_at`, now, now, profileID); err != nil {
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
	if _, err := tx.Exec(`insert into profile_game_flags(profile_id, appid, groupid, disabled, disabled_reason, created_at, updated_at)
		select profile_id, appid, groupid, 1, 'not_owned', ?, ? from profile_games where profile_id=? and appid not in (`+inClause+`)
		on conflict(profile_id, appid, groupid) do update set disabled=1, disabled_reason=`+notOwnedReasonSQL+`, updated_at=excluded.updated_at`, missingArgs...); err != nil {
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

// DisableProfileGame disables one entry; sibling sets of the same game are unaffected.
func (r *Repository) DisableProfileGame(profileID, appID, groupID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`insert into profile_game_flags(profile_id, appid, groupid, disabled, disabled_reason, created_at, updated_at)
		values(?,?,?,?,?,?,?)
		on conflict(profile_id, appid, groupid) do update set disabled=1, disabled_reason=excluded.disabled_reason, updated_at=excluded.updated_at`, profileID, appID, groupID, 1, "manual", now, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`delete from profile_game_tags where profile_id=? and appid=? and groupid=?`, profileID, appID, groupID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) EnableProfileGame(profileID, appID, groupID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`update profile_game_flags set disabled=0, disabled_reason=null, updated_at=? where profile_id=? and appid=? and groupid=?`, now, profileID, appID, groupID); err != nil {
		return err
	}
	if err := r.recomputeGameTagsTx(tx, profileID, appID, groupID); err != nil {
		return err
	}
	return tx.Commit()
}
