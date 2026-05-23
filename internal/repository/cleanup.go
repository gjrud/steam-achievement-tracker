package repository

import (
	"database/sql"
	"strings"
)

func pruneProfileAchievements(tx *sql.Tx, profileID, appID int64, achievements []AchievementRecord) error {
	if len(achievements) == 0 {
		if _, err := tx.Exec(`delete from profile_achievements where profile_id=? and appid=?`, profileID, appID); err != nil {
			return err
		}
		_, err := tx.Exec(`delete from achievements where appid=? and not exists (select 1 from profile_achievements pa where pa.appid=achievements.appid and pa.apiname=achievements.apiname)`, appID)
		return err
	}
	pl := make([]string, len(achievements))
	args := make([]any, 0, len(achievements)+2)
	args = append(args, profileID, appID)
	for i, ach := range achievements {
		pl[i] = "?"
		args = append(args, ach.APIName)
	}
	if _, err := tx.Exec(`delete from profile_achievements where profile_id=? and appid=? and apiname not in (`+strings.Join(pl, ",")+")", args...); err != nil {
		return err
	}
	args = make([]any, 0, len(achievements)+1)
	args = append(args, appID)
	for _, ach := range achievements {
		args = append(args, ach.APIName)
	}
	_, err := tx.Exec(`delete from achievements where appid=? and apiname not in (`+strings.Join(pl, ",")+`) and not exists (select 1 from profile_achievements pa where pa.appid=achievements.appid and pa.apiname=achievements.apiname)`, args...)
	return err
}

func deleteOrphanRowsForAppIDs(tx *sql.Tx, appIDs []int64) error {
	if len(appIDs) == 0 {
		return nil
	}
	pl := make([]string, len(appIDs))
	args := make([]any, 0, len(appIDs))
	for i, appID := range appIDs {
		pl[i] = "?"
		args = append(args, appID)
	}
	inClause := strings.Join(pl, ",")
	if _, err := tx.Exec(`delete from achievements where appid in (`+inClause+`) and not exists (
		select 1 from profile_achievements pa where pa.appid=achievements.appid and pa.apiname=achievements.apiname
	)`, args...); err != nil {
		return err
	}
	_, err := tx.Exec(`delete from games where appid in (`+inClause+`) and not exists (select 1 from profile_games pg where pg.appid=games.appid)
		and not exists (select 1 from profile_achievements pa where pa.appid=games.appid)
		and not exists (select 1 from profile_game_flags f where f.appid=games.appid)
		and not exists (select 1 from profile_game_tags t where t.appid=games.appid)
		and not exists (select 1 from game_snapshots s where s.appid=games.appid)`, args...)
	return err
}
