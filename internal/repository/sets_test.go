package repository

import (
	"testing"
	"time"
)

func TestHasSetData(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("22222222222222222", "Set Tester", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)

	requireHasSetData := func(t *testing.T, appID int64, want bool) {
		t.Helper()
		got, err := repo.HasSetData(appID)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("HasSetData(%d) = %v, want %v", appID, got, want)
		}
	}

	t.Run("unknown app has no set data", func(t *testing.T) {
		requireHasSetData(t, 9001, false)
	})

	t.Run("profile entry with groupid other than 0 counts", func(t *testing.T) {
		if _, err := repo.db.Exec(`insert into games(appid, name, created_at, updated_at) values(?,?,?,?)`, 9002, "Entry Game", now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.db.Exec(`insert into profile_games(profile_id, appid, groupid) values(?,?,?)`, profile.ID, 9002, 3); err != nil {
			t.Fatal(err)
		}
		requireHasSetData(t, 9002, true)
	})

	t.Run("game_sets row counts", func(t *testing.T) {
		if _, err := repo.db.Exec(`insert into games(appid, name, created_at, updated_at) values(?,?,?,?)`, 9003, "Set Game", now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.db.Exec(`insert into game_sets(appid, groupid, name, dlcappid, updated_at) values(?,?,?,?,?)`, 9003, 7, "Extra", 0, now); err != nil {
			t.Fatal(err)
		}
		requireHasSetData(t, 9003, true)
	})

	t.Run("group 0 only from a synced game has no set data", func(t *testing.T) {
		seedGameSync(t, repo, profile.ID, 9004)
		requireHasSetData(t, 9004, false)
	})

	t.Run("game without any row is not affected by other apps", func(t *testing.T) {
		if _, err := repo.db.Exec(`insert into games(appid, name, created_at, updated_at) values(?,?,?,?)`, 9005, "Plain Game", now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.db.Exec(`insert into profile_games(profile_id, appid, groupid) values(?,?,?)`, profile.ID, 9005, 0); err != nil {
			t.Fatal(err)
		}
		requireHasSetData(t, 9005, false)
	})
}
