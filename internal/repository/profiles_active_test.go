package repository

import (
	"fmt"
	"testing"
)

// tableCount returns the number of rows in table. Table names are fixed in the tests below.
func tableCount(t *testing.T, repo *Repository, table string) int {
	t.Helper()
	var got int
	if err := repo.db.QueryRow(fmt.Sprintf("select count(*) from %s", table)).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestDeactivateProfileLeavesNoActiveProfile(t *testing.T) {
	repo := testRepository(t)
	saveTestProfile(t, repo, "11111111111111111", "User One")
	saveTestProfile(t, repo, "22222222222222222", "User Two")

	if err := repo.DeactivateProfile(); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.ActiveProfile(); err != nil || active != nil {
		t.Fatalf("active after deactivate = %#v, %v; want nil even though profiles exist", active, err)
	}
	assertCount(t, repo, `select count(*) from settings where key='active_profile_id' and value=''`, 1)
	assertCount(t, repo, `select count(*) from profiles`, 2)
	// Deactivating again is harmless.
	if err := repo.DeactivateProfile(); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.ActiveProfile(); err != nil || active != nil {
		t.Fatalf("active after second deactivate = %#v, %v; want nil", active, err)
	}
}

func TestDeactivateProfileKeepsProfileData(t *testing.T) {
	repo := testRepository(t)
	profile := saveTestProfile(t, repo, "11111111111111111", "User One")
	other := saveTestProfile(t, repo, "22222222222222222", "User Two")
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 300,
		dashboardSeed{GroupID: 0, Total: 4, Unlocked: 2, Percent: 50},
		dashboardSeed{GroupID: 1, DLCAppID: 4173830, SetName: "Pack", Total: 2, Unlocked: 1, Percent: 50})
	seedGameSync(t, repo, other.ID, 20)
	mustSaveLibraryCover(t, repo, 2968420)
	if err := repo.UpsertOwnedGame(profile.ID, GameRecord{AppID: 2968420, Name: "PowerWash Simulator 2", PlaytimeForever: 300, Purchased: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StartSyncRun(profile.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveLibrarySettings(profile.ID, LibrarySettings{IncludeUnvettedApps: true, IncludePlayedFreeGames: true}); err != nil {
		t.Fatal(err)
	}

	tables := []string{"games", "profile_games", "profile_achievements", "profile_game_flags", "profile_game_tags",
		"game_snapshots", "profile_owned_games", "sync_runs", "profile_library_settings", "cover_images"}
	before := make(map[string]int, len(tables))
	for _, table := range tables {
		before[table] = tableCount(t, repo, table)
	}

	if err := repo.DeactivateProfile(); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if got := tableCount(t, repo, table); got != before[table] {
			t.Fatalf("%s count after deactivate = %d, want %d", table, got, before[table])
		}
	}
	assertCount(t, repo, `select count(*) from profiles where id=?`, 1, profile.ID)
	if game, err := repo.ProfileGame(profile.ID, 2968420, 0); err != nil || !game.Exists {
		t.Fatalf("profile game after deactivate = %+v, %v; want kept", game, err)
	}
	settings, err := repo.LibrarySettings(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.IncludeUnvettedApps || !settings.IncludePlayedFreeGames {
		t.Fatalf("library settings after deactivate = %+v, want kept", settings)
	}
}

func TestClearProfileDataOfActiveProfileLeavesNoFallback(t *testing.T) {
	repo := testRepository(t)
	p1 := saveTestProfile(t, repo, "11111111111111111", "User One")
	p2 := saveTestProfile(t, repo, "22222222222222222", "User Two")
	// Make p1 the active profile explicitly; p2 is kept in the database.
	if _, err := repo.db.Exec(`update settings set value=? where key='active_profile_id'`, fmt.Sprintf("%d", p1.ID)); err != nil {
		t.Fatal(err)
	}
	seedGameSync(t, repo, p1.ID, 10)
	seedGameSync(t, repo, p2.ID, 20)

	if _, err := repo.ClearProfileData(p1.ID); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.ActiveProfile(); err != nil || active != nil {
		t.Fatalf("active after clearing active profile = %#v, %v; want nil, not kept profile %d", active, err, p2.ID)
	}
	assertCount(t, repo, `select count(*) from settings where key='active_profile_id' and value=''`, 1)
	assertCount(t, repo, `select count(*) from profiles where id=?`, 1, p2.ID)
	assertCount(t, repo, `select count(*) from profile_games where profile_id=?`, 1, p2.ID)
}

// A legacy database has a profile but no active_profile_id row, so the lowest id is the active profile.
func TestClearProfileDataOfFallbackActiveProfileLeavesNoFallback(t *testing.T) {
	repo := testRepository(t)
	p1 := saveTestProfile(t, repo, "11111111111111111", "User One")
	p2 := saveTestProfile(t, repo, "22222222222222222", "User Two")
	if _, err := repo.db.Exec(`delete from settings where key='active_profile_id'`); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.ActiveProfile(); err != nil || active == nil || active.ID != p1.ID {
		t.Fatalf("legacy active = %#v, %v; want fallback profile %d", active, err, p1.ID)
	}

	if _, err := repo.ClearProfileData(p1.ID); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.ActiveProfile(); err != nil || active != nil {
		t.Fatalf("active after clearing fallback profile = %#v, %v; want nil, not kept profile %d", active, err, p2.ID)
	}
	assertCount(t, repo, `select count(*) from settings where key='active_profile_id' and value=''`, 1)
}

// A row that holds the id of a profile that no longer exists means no active profile. It must not fall back to the
// lowest id, even though another profile exists and the row is present.
func TestActiveProfileStaleIDReturnsNilWithoutFallback(t *testing.T) {
	repo := testRepository(t)
	saveTestProfile(t, repo, "11111111111111111", "User One")
	p2 := saveTestProfile(t, repo, "22222222222222222", "User Two")
	missingID := p2.ID + 100
	if _, err := repo.db.Exec(`update settings set value=? where key='active_profile_id'`, fmt.Sprintf("%d", missingID)); err != nil {
		t.Fatal(err)
	}

	if active, err := repo.ActiveProfile(); err != nil || active != nil {
		t.Fatalf("active with stale id %d = %#v, %v; want nil, not a fallback profile", missingID, active, err)
	}
	assertCount(t, repo, `select count(*) from settings where key='active_profile_id' and value=?`, 1, fmt.Sprintf("%d", missingID))
	assertCount(t, repo, `select count(*) from profiles`, 2)
}

// Clearing a kept, non-active profile of a legacy database leaves the fallback profile active.
func TestClearProfileDataOfNonActiveProfileKeepsFallback(t *testing.T) {
	repo := testRepository(t)
	p1 := saveTestProfile(t, repo, "11111111111111111", "User One")
	p2 := saveTestProfile(t, repo, "22222222222222222", "User Two")
	if _, err := repo.db.Exec(`delete from settings where key='active_profile_id'`); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ClearProfileData(p2.ID); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.ActiveProfile(); err != nil || active == nil || active.ID != p1.ID {
		t.Fatalf("active after clearing other profile = %#v, %v; want profile %d", active, err, p1.ID)
	}
}

func TestSaveProfileReactivatesDeactivatedProfileWithDataIntact(t *testing.T) {
	repo := testRepository(t)
	profile := saveTestProfile(t, repo, "11111111111111111", "User One")
	saveSeedGame(t, repo, profile.ID, 30, "Kept Game", 60,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50})
	if err := repo.DeactivateProfile(); err != nil {
		t.Fatal(err)
	}

	again, err := repo.SaveProfile("11111111111111111", "User One Again", "")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != profile.ID {
		t.Fatalf("re-saved profile id = %d, want %d", again.ID, profile.ID)
	}
	if active, err := repo.ActiveProfile(); err != nil || active == nil || active.ID != profile.ID {
		t.Fatalf("active after re-save = %#v, %v; want profile %d", active, err, profile.ID)
	}
	if game, err := repo.ProfileGame(profile.ID, 30, 0); err != nil || !game.Exists {
		t.Fatalf("profile game after re-save = %+v, %v; want kept", game, err)
	}
	assertCount(t, repo, `select count(*) from profiles`, 1)
}
