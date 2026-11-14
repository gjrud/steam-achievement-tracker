package repository

// HasSetData reports whether the app already has achievement-set data stored: any non-base game_sets row (groupid<>0) for the app,
// or any profile entry of the app with groupid<>0. The syncer uses it to keep the legacy single-entry
// fallback (which prunes every other entry of the game) away from apps that already have new-API data.
func (r *Repository) HasSetData(appID int64) (bool, error) {
	var found int
	err := r.db.QueryRow(`select exists(select 1 from game_sets where appid=? and groupid<>0) or exists(select 1 from profile_games where appid=? and groupid<>0)`, appID, appID).Scan(&found)
	if err != nil {
		return false, err
	}
	return found == 1, nil
}
