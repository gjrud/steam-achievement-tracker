package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

// ToggleMissingAchievementsInDLC writes the flag and its tags in one transaction. When the tag write fails, the flag
// must not stay toggled.
func TestToggleMissingAchievementsInDLCIsAtomic(t *testing.T) {
	repo, path := testRepositoryPath(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	seedGameSync(t, repo, profile.ID, 10)

	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`create trigger block_tag_insert before insert on profile_game_tags begin select raise(abort, 'tags blocked'); end`); err != nil {
		t.Fatal(err)
	}

	if err := repo.ToggleMissingAchievementsInDLC(profile.ID, 10, 0); err == nil {
		t.Fatal("err = nil, want the blocked tag write to fail the toggle")
	}
	var flag int
	err = db.QueryRow(`select missing_achievements_in_dlc from profile_game_flags where profile_id=? and appid=? and groupid=?`, profile.ID, 10, 0).Scan(&flag)
	if !errors.Is(err, sql.ErrNoRows) && (err != nil || flag != 0) {
		t.Fatalf("flag = %d (err %v), want no toggled flag after the failed toggle", flag, err)
	}

	if _, err := db.Exec(`drop trigger block_tag_insert`); err != nil {
		t.Fatal(err)
	}
	if err := repo.ToggleMissingAchievementsInDLC(profile.ID, 10, 0); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`select missing_achievements_in_dlc from profile_game_flags where profile_id=? and appid=? and groupid=?`, profile.ID, 10, 0).Scan(&flag); err != nil || flag != 1 {
		t.Fatalf("flag = %d (err %v), want 1 after a working toggle", flag, err)
	}
}

// HasStoredAchievementData is true only for achievement data of this profile's app: a group other than 0, an entry
// with achievements, or the shared game row marked as having achievements. A placeholder entry does not count.
func TestHasStoredAchievementData(t *testing.T) {
	repo, path := testRepositoryPath(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	requireStored := func(name string, want bool) {
		t.Helper()
		got, err := repo.HasStoredAchievementData(profile.ID, 10)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s: HasStoredAchievementData = %v, want %v", name, got, want)
		}
	}

	requireStored("no rows", false)

	// A warning on an app with no entry writes a group-0 placeholder with no achievements: no data.
	if err := repo.SaveGameWarning(profile.ID, GameRecord{AppID: 10, Name: "Game"}, "warning"); err != nil {
		t.Fatal(err)
	}
	requireStored("placeholder only", false)

	if _, err := db.Exec(`update profile_games set total_achievements=0 where profile_id=? and appid=?`, profile.ID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update games set has_achievements=1 where appid=10`); err != nil {
		t.Fatal(err)
	}
	requireStored("game row marked with achievements", true)

	if _, err := db.Exec(`update games set has_achievements=0 where appid=10`); err != nil {
		t.Fatal(err)
	}
	requireStored("cleared game marker, no entry achievements", false)

	// An extra set (groupid<>0) is data even when nothing else is.
	if _, err := db.Exec(`insert into profile_games(profile_id, appid, groupid, total_achievements, unlocked_achievements, last_synced_at) values(?,?,1,1,0,'2026-01-01T00:00:00Z')`, profile.ID, 10); err != nil {
		t.Fatal(err)
	}
	requireStored("extra set only", true)

	// Another profile's data does not count for this profile, unless the shared game row says the app has achievements.
	if _, err := db.Exec(`delete from profile_games where profile_id=?`, profile.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into profile_games(profile_id, appid, groupid, total_achievements, unlocked_achievements, last_synced_at) values(?,?,1,1,0,'2026-01-01T00:00:00Z')`, other.ID, 10); err != nil {
		t.Fatal(err)
	}
	requireStored("other profile's extra set only", false)
}
