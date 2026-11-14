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
	if _, err := tx.Exec(`delete from game_sets where appid in (`+inClause+`) and not exists (select 1 from profile_games pg where pg.appid=game_sets.appid)`, args...); err != nil {
		return err
	}
	_, err := tx.Exec(`delete from games where appid in (`+inClause+`) and not exists (select 1 from profile_games pg where pg.appid=games.appid)
		and not exists (select 1 from profile_achievements pa where pa.appid=games.appid)
		and not exists (select 1 from profile_game_flags f where f.appid=games.appid)
		and not exists (select 1 from profile_game_tags t where t.appid=games.appid)
		and not exists (select 1 from game_snapshots s where s.appid=games.appid)`, args...)
	return err
}

// deleteOrphanCoverRows removes cached cover rows for the given image app ids that no remaining entry displays.
// A game's cover also stays while any entry of that game remains, since its DLC entries fall back to it.
// It returns the image app ids whose rows were actually deleted; only those have cached files to remove.
func deleteOrphanCoverRows(tx *sql.Tx, imageAppIDs []int64) ([]int64, error) {
	if len(imageAppIDs) == 0 {
		return nil, nil
	}
	pl := make([]string, len(imageAppIDs))
	args := make([]any, 0, len(imageAppIDs))
	for i, id := range imageAppIDs {
		pl[i] = "?"
		args = append(args, id)
	}
	rows, err := tx.Query(`select image_appid from cover_images where image_appid in (`+strings.Join(pl, ",")+`) and not exists (
		select 1 from profile_games pg left join game_sets gs on gs.appid=pg.appid and gs.groupid=pg.groupid
		where `+imageAppIDSQL+` = cover_images.image_appid or pg.appid = cover_images.image_appid
	) order by image_appid`, args...)
	if err != nil {
		return nil, err
	}
	var orphans []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		orphans = append(orphans, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for _, id := range orphans {
		if _, err := tx.Exec(`delete from cover_images where image_appid=?`, id); err != nil {
			return nil, err
		}
	}
	return orphans, nil
}
