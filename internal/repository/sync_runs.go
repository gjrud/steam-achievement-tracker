package repository

import "time"

func (r *Repository) StartSyncRun(profileID int64) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.db.Exec(`insert into sync_runs(profile_id, started_at, status) values(?,?,?)`, profileID, now, "running")
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *Repository) FinishSyncRun(id int64, status string, total, synced, failed int, errText *string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(`update sync_runs set finished_at=?, status=?, games_total=?, games_synced=?, games_failed=?, error=? where id=?`, now, status, total, synced, failed, errText, id)
	return err
}

func (r *Repository) UpdateSyncRunProgress(id int64, total, synced, failed int) error {
	_, err := r.db.Exec(`update sync_runs set games_total=?, games_synced=?, games_failed=? where id=?`, total, synced, failed, id)
	return err
}

func (r *Repository) LatestSyncRun(profileID int64) (*SyncRun, error) {
	row := r.db.QueryRow(`select id, profile_id, started_at, finished_at, status, games_total, games_synced, games_failed, error from sync_runs where profile_id=? order by id desc limit 1`, profileID)
	return scanSyncRun(row)
}

func (r *Repository) CancelRunningSyncRuns(reason string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.db.Exec(`update sync_runs set finished_at=?, status='canceled', error=? where status='running'`, now, reason)
	return err
}
