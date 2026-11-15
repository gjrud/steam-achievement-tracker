package repository

import "testing"

func TestLibrarySettingsDefaultToTrueWhenProfileHasNoRow(t *testing.T) {
	repo := testRepository(t)
	p, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.LibrarySettings(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := (LibrarySettings{IncludeUnvettedApps: true, IncludePlayedFreeGames: true}); got != want {
		t.Fatalf("LibrarySettings() = %+v, want defaults %+v", got, want)
	}
	assertCount(t, repo, `select count(*) from profile_library_settings`, 0)
}

func TestLibrarySettingsRoundTripAndUpsertOneRowPerProfile(t *testing.T) {
	repo := testRepository(t)
	p, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	tests := []LibrarySettings{
		{IncludeUnvettedApps: false, IncludePlayedFreeGames: true},
		{IncludeUnvettedApps: true, IncludePlayedFreeGames: false},
		{IncludeUnvettedApps: false, IncludePlayedFreeGames: false},
		{IncludeUnvettedApps: true, IncludePlayedFreeGames: true},
	}
	for _, want := range tests {
		if err := repo.SaveLibrarySettings(p.ID, want); err != nil {
			t.Fatalf("SaveLibrarySettings(%+v): %v", want, err)
		}
		got, err := repo.LibrarySettings(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("LibrarySettings() = %+v, want %+v", got, want)
		}
		assertCount(t, repo, `select count(*) from profile_library_settings where profile_id=? and include_unvetted_apps=? and include_played_free_games=?`, 1,
			p.ID, boolInt(want.IncludeUnvettedApps), boolInt(want.IncludePlayedFreeGames))
	}
	// Upsert: one row per profile, never duplicated.
	assertCount(t, repo, `select count(*) from profile_library_settings where profile_id=?`, 1, p.ID)
}

func TestLibrarySettingsAreIsolatedPerProfile(t *testing.T) {
	repo := testRepository(t)
	a, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	off := LibrarySettings{IncludeUnvettedApps: false, IncludePlayedFreeGames: false}
	if err := repo.SaveLibrarySettings(a.ID, off); err != nil {
		t.Fatal(err)
	}

	gotA, err := repo.LibrarySettings(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotA != off {
		t.Fatalf("profile A LibrarySettings() = %+v, want %+v", gotA, off)
	}
	gotB, err := repo.LibrarySettings(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := DefaultLibrarySettings(); gotB != want {
		t.Fatalf("profile B LibrarySettings() = %+v, want defaults %+v after saving profile A", gotB, want)
	}

	// Saving for B leaves A's saved values alone.
	onB := LibrarySettings{IncludeUnvettedApps: true, IncludePlayedFreeGames: false}
	if err := repo.SaveLibrarySettings(b.ID, onB); err != nil {
		t.Fatal(err)
	}
	if gotA, err = repo.LibrarySettings(a.ID); err != nil || gotA != off {
		t.Fatalf("profile A LibrarySettings() = %+v, %v, want %+v after saving profile B", gotA, err, off)
	}
	if gotB, err = repo.LibrarySettings(b.ID); err != nil || gotB != onB {
		t.Fatalf("profile B LibrarySettings() = %+v, %v, want %+v", gotB, err, onB)
	}
	assertCount(t, repo, `select count(*) from profile_library_settings`, 2)
}

func TestClearProfileDataRemovesProfileLibrarySettings(t *testing.T) {
	repo := testRepository(t)
	p, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	off := LibrarySettings{IncludeUnvettedApps: false, IncludePlayedFreeGames: false}
	if err := repo.SaveLibrarySettings(p.ID, off); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveLibrarySettings(other.ID, off); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ClearProfileData(p.ID); err != nil {
		t.Fatalf("ClearProfileData() with saved library settings: %v", err)
	}
	assertCount(t, repo, `select count(*) from profile_library_settings where profile_id=?`, 0, p.ID)
	assertCount(t, repo, `select count(*) from profile_library_settings where profile_id=?`, 1, other.ID)
	// Re-adding the same Steam profile starts from defaults.
	readded, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := repo.LibrarySettings(readded.ID); err != nil || got != DefaultLibrarySettings() {
		t.Fatalf("re-added LibrarySettings() = %+v, %v, want defaults", got, err)
	}
}
