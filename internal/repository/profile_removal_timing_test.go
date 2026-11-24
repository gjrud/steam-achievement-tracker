package repository

import (
	"database/sql"
	"testing"
	"time"
)

// seedRemovalData fills a migrated database with two profiles, each owning 100 games of 1000 achievements: 100k
// achievement rows and 100k profile_achievements rows per profile. Profile 1 owns appids 1000-1099 and profile 2 owns
// 2000-2099. Achievements are not shared, so removing profile 1 orphans its 100k achievements and its 100 games.
func seedRemovalData(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO profiles(id, steam_id64, display_name, created_at, updated_at) VALUES
			(1, '11111111111111111', 'User One', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
			(2, '22222222222222222', 'User Two', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO profile_library_settings(profile_id, updated_at) VALUES (1, '2026-01-01T00:00:00Z'), (2, '2026-01-01T00:00:00Z');
		INSERT INTO sync_runs(profile_id, started_at, finished_at, status) VALUES
			(1, '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z', 'success'),
			(2, '2026-01-01T00:00:00Z', '2026-01-01T00:01:00Z', 'success');
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i < 199)
		INSERT INTO games(appid, name, created_at, updated_at)
			SELECT CASE WHEN i < 100 THEN 1000 + i ELSE 2000 + (i - 100) END, 'Game', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z' FROM n;
		INSERT INTO profile_games(profile_id, appid, groupid, total_achievements, unlocked_achievements, is_completed, last_synced_at)
			SELECT CASE WHEN appid < 2000 THEN 1 ELSE 2 END, appid, 0, 1000, 500, 0, '2026-01-01T00:00:00Z' FROM games;
		INSERT INTO profile_owned_games(profile_id, appid, updated_at)
			SELECT profile_id, appid, '2026-01-01T00:00:00Z' FROM profile_games;
		INSERT INTO game_snapshots(profile_id, appid, groupid, total_achievements, unlocked_achievements, completion_percent, is_completed, synced_at)
			SELECT profile_id, appid, 0, 1000, 500, 50, 0, '2026-01-01T00:00:00Z' FROM profile_games;
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i < 199999)
		INSERT INTO achievements(appid, apiname, groupid, updated_at)
			SELECT CASE WHEN i < 100000 THEN 1000 + i / 1000 ELSE 2000 + (i - 100000) / 1000 END, printf('ACH_%03d', i % 1000), 0, '2026-01-01T00:00:00Z' FROM n;
		WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i+1 FROM n WHERE i < 199999)
		INSERT INTO profile_achievements(profile_id, appid, apiname, unlocked, unlock_time, updated_at)
			SELECT CASE WHEN i < 100000 THEN 1 ELSE 2 END,
				CASE WHEN i < 100000 THEN 1000 + i / 1000 ELSE 2000 + (i - 100000) / 1000 END,
				printf('ACH_%03d', i % 1000), i % 2, NULL, '2026-01-01T00:00:00Z' FROM n;
	`); err != nil {
		t.Fatalf("seed removal data: %v", err)
	}
}

// Removing one of two profiles with 100k achievement rows each, through ClearProfileData. The achievements delete
// triggers foreign key checks on profile_achievements(appid, apiname) for every parent row; without
// idx_profile_achievements_appid_apiname each check scans the whole table. The bound is generous; the measured time is
// logged (run with -v).
func TestProfileRemovalTimingWithTwoProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("two-profile removal timing is skipped under -short")
	}
	repo := testRepository(t)
	seedRemovalData(t, repo.db)
	assertCount(t, repo, `select count(*) from achievements`, 200000)
	assertCount(t, repo, `select count(*) from profile_achievements`, 200000)

	start := time.Now()
	if _, err := repo.ClearProfileData(1); err != nil {
		t.Fatalf("ClearProfileData(1): %v", err)
	}
	elapsed := time.Since(start)
	t.Logf("removing profile 1 (100 games, 100k achievements) with 2 profiles seeded: %s", elapsed)
	const bound = 60 * time.Second
	if elapsed > bound {
		t.Fatalf("removal took %s, want under %s", elapsed, bound)
	}

	assertCount(t, repo, `select count(*) from profiles`, 1)
	assertCount(t, repo, `select count(*) from achievements`, 100000)
	assertCount(t, repo, `select count(*) from profile_achievements where profile_id=2`, 100000)
	assertCount(t, repo, `select count(*) from games`, 100)
	assertCount(t, repo, `select count(*) from profile_games where profile_id=2`, 100)
}
