package repository

import (
	"fmt"
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

// syncSetGroups syncs the app for one profile with one set per group id (no achievements). No groups syncs an empty list.
func syncSetGroups(t *testing.T, repo *Repository, profileID, appID int64, groups ...int64) {
	t.Helper()
	seeds := make([]dashboardSeed, 0, len(groups))
	for _, g := range groups {
		seeds = append(seeds, dashboardSeed{GroupID: g, SetName: fmt.Sprintf("Set %d", g)})
	}
	saveSeedGame(t, repo, profileID, appID, "Set Game", 0, seeds...)
}

func gameSetGroups(t *testing.T, repo *Repository, appID int64) []int64 {
	t.Helper()
	rows, err := repo.db.Query(`select groupid from game_sets where appid=? order by groupid`, appID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func requireGameSetGroups(t *testing.T, repo *Repository, appID int64, want ...int64) {
	t.Helper()
	got := gameSetGroups(t, repo, appID)
	if len(got) != len(want) {
		t.Fatalf("game_sets groups for %d = %v, want %v", appID, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("game_sets groups for %d = %v, want %v", appID, got, want)
		}
	}
}

func TestSyncPrunesDroppedGameSets(t *testing.T) {
	t.Run("dropped set is removed and the remaining set keeps HasSetData", func(t *testing.T) {
		repo := testRepository(t)
		profile, err := repo.SaveProfile("11111111111111111", "Prune One", "")
		if err != nil {
			t.Fatal(err)
		}
		syncSetGroups(t, repo, profile.ID, 9101, 0, 5, 7)
		requireGameSetGroups(t, repo, 9101, 0, 5, 7)

		syncSetGroups(t, repo, profile.ID, 9101, 0, 5)
		requireGameSetGroups(t, repo, 9101, 0, 5)
		if got, err := repo.HasSetData(9101); err != nil || !got {
			t.Fatalf("HasSetData(9101) = %v, %v; want true (group 5 remains)", got, err)
		}
	})

	t.Run("HasSetData is false once every non-base set is dropped", func(t *testing.T) {
		repo := testRepository(t)
		profile, err := repo.SaveProfile("11111111111111111", "Prune Two", "")
		if err != nil {
			t.Fatal(err)
		}
		syncSetGroups(t, repo, profile.ID, 9102, 0, 5)
		syncSetGroups(t, repo, profile.ID, 9102, 0)
		requireGameSetGroups(t, repo, 9102, 0)
		if got, err := repo.HasSetData(9102); err != nil || got {
			t.Fatalf("HasSetData(9102) = %v, %v; want false", got, err)
		}
	})

	t.Run("shared set stays while another profile references it", func(t *testing.T) {
		repo := testRepository(t)
		a, err := repo.SaveProfile("11111111111111111", "Shared A", "")
		if err != nil {
			t.Fatal(err)
		}
		b, err := repo.SaveProfile("22222222222222222", "Shared B", "")
		if err != nil {
			t.Fatal(err)
		}
		// Group 7 is a DLC set. Both profiles sync it with the same dlcappid, so the row keeps it across syncs.
		const dlc7 = int64(4007)
		syncShared := func(profileID int64, groups ...int64) {
			t.Helper()
			seeds := make([]dashboardSeed, 0, len(groups))
			for _, g := range groups {
				seed := dashboardSeed{GroupID: g, SetName: fmt.Sprintf("Set %d", g)}
				if g == 7 {
					seed.DLCAppID = dlc7
				}
				seeds = append(seeds, seed)
			}
			saveSeedGame(t, repo, profileID, 9103, "Set Game", 0, seeds...)
		}

		syncShared(a.ID, 0, 5, 7)
		syncShared(b.ID, 0, 5, 7)

		syncShared(a.ID, 0, 5)
		requireGameSetGroups(t, repo, 9103, 0, 5, 7)
		var dlc int64
		if err := repo.db.QueryRow(`select dlcappid from game_sets where appid=? and groupid=?`, 9103, 7).Scan(&dlc); err != nil {
			t.Fatal(err)
		}
		if dlc != dlc7 {
			t.Fatalf("game_sets dlcappid for group 7 = %d, want %d", dlc, dlc7)
		}
		// B's entry keeps its set name through the dashboard join.
		var name string
		if err := repo.db.QueryRow(`select gs.name from profile_games pg left join game_sets gs on gs.appid=pg.appid and gs.groupid=pg.groupid where pg.profile_id=? and pg.appid=? and pg.groupid=?`, b.ID, 9103, 7).Scan(&name); err != nil {
			t.Fatal(err)
		}
		if name != "Set 7" {
			t.Fatalf("profile B joined set name = %q, want %q", name, "Set 7")
		}
		assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=?`, 3, b.ID, 9103)

		syncShared(b.ID, 0, 5)
		requireGameSetGroups(t, repo, 9103, 0, 5)
	})

	t.Run("empty set list prunes unreferenced rows only", func(t *testing.T) {
		repo := testRepository(t)
		a, err := repo.SaveProfile("11111111111111111", "Empty A", "")
		if err != nil {
			t.Fatal(err)
		}
		b, err := repo.SaveProfile("22222222222222222", "Empty B", "")
		if err != nil {
			t.Fatal(err)
		}
		syncSetGroups(t, repo, a.ID, 9104, 0, 5, 7)
		syncSetGroups(t, repo, b.ID, 9104, 0, 5)

		syncSetGroups(t, repo, a.ID, 9104)
		requireGameSetGroups(t, repo, 9104, 0, 5)

		syncSetGroups(t, repo, b.ID, 9104)
		requireGameSetGroups(t, repo, 9104)
	})

	t.Run("other apps game_sets rows are untouched", func(t *testing.T) {
		repo := testRepository(t)
		profile, err := repo.SaveProfile("11111111111111111", "Other App", "")
		if err != nil {
			t.Fatal(err)
		}
		syncSetGroups(t, repo, profile.ID, 9105, 0, 3, 4)
		syncSetGroups(t, repo, profile.ID, 9106, 0, 8)

		syncSetGroups(t, repo, profile.ID, 9105, 0)
		requireGameSetGroups(t, repo, 9105, 0)
		requireGameSetGroups(t, repo, 9106, 0, 8)
	})
}
