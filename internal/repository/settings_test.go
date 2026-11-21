package repository

import (
	"fmt"
	"strings"
	"testing"
)

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

func TestUpdateChecksEnabledDefaultsToTrueWhenAbsent(t *testing.T) {
	repo := testRepository(t)
	got, err := repo.UpdateChecksEnabled()
	if err != nil {
		t.Fatalf("UpdateChecksEnabled() with no row: %v", err)
	}
	if !got {
		t.Fatal("UpdateChecksEnabled() with no row = false, want true")
	}
	assertCount(t, repo, `select count(*) from settings where key='update_checks_enabled'`, 0)
}

func TestUpdateChecksEnabledRoundTrip(t *testing.T) {
	repo := testRepository(t)
	for _, want := range []bool{false, true, false, true} {
		if err := repo.SetUpdateChecksEnabled(want); err != nil {
			t.Fatalf("SetUpdateChecksEnabled(%v): %v", want, err)
		}
		got, err := repo.UpdateChecksEnabled()
		if err != nil {
			t.Fatalf("UpdateChecksEnabled() after set %v: %v", want, err)
		}
		if got != want {
			t.Fatalf("UpdateChecksEnabled() = %v, want %v", got, want)
		}
	}
	// Upsert: one row, stored as "1" or "0".
	assertCount(t, repo, `select count(*) from settings where key='update_checks_enabled' and value='1'`, 1)
	if err := repo.SetUpdateChecksEnabled(false); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from settings where key='update_checks_enabled' and value='0'`, 1)
	assertCount(t, repo, `select count(*) from settings where key='update_checks_enabled'`, 1)
}

func TestUpdateChecksEnabledRejectsInvalidStoredValue(t *testing.T) {
	repo := testRepository(t)
	for _, value := range []string{"", "true", "false", "yes", " 1", "1 ", "2"} {
		if _, err := repo.db.Exec(`insert into settings(key, value) values('update_checks_enabled', ?) on conflict(key) do update set value=excluded.value`, value); err != nil {
			t.Fatal(err)
		}
		got, err := repo.UpdateChecksEnabled()
		if err == nil {
			t.Fatalf("UpdateChecksEnabled() with stored %q = %v, nil error; want error", value, got)
		}
		if got {
			t.Fatalf("UpdateChecksEnabled() with stored %q returned true with error; want false", value)
		}
		if !strings.Contains(err.Error(), "update_checks_enabled") {
			t.Fatalf("UpdateChecksEnabled() error = %v, want key named", err)
		}
	}
	// A valid value written afterwards reads normally again.
	if err := repo.SetUpdateChecksEnabled(true); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.UpdateChecksEnabled(); err != nil || !got {
		t.Fatalf("UpdateChecksEnabled() after repair = %v, %v, want true, nil", got, err)
	}
}

func TestUpdateChecksEnabledReadFailureReturnsFalseAndError(t *testing.T) {
	repo := testRepository(t)
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := repo.UpdateChecksEnabled()
	if err == nil {
		t.Fatalf("UpdateChecksEnabled() on closed repository = %v, nil error; want error", got)
	}
	if got {
		t.Fatal("UpdateChecksEnabled() on closed repository returned true with error; want false")
	}
}

func TestUpdateChecksEnabledSurvivesProfileFlows(t *testing.T) {
	repo := testRepository(t)
	assertDisabled := func(step string) {
		t.Helper()
		got, err := repo.UpdateChecksEnabled()
		if err != nil {
			t.Fatalf("UpdateChecksEnabled() after %s: %v", step, err)
		}
		if got {
			t.Fatalf("UpdateChecksEnabled() after %s = true, want false", step)
		}
	}

	a, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetUpdateChecksEnabled(false); err != nil {
		t.Fatal(err)
	}
	assertDisabled("SetUpdateChecksEnabled(false)")

	b, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	assertDisabled("SaveProfile(second profile)")

	if err := repo.DeactivateProfile(); err != nil {
		t.Fatal(err)
	}
	assertDisabled("DeactivateProfile")

	if _, err := repo.SaveProfile("11111111111111111", "User One", ""); err != nil {
		t.Fatal(err)
	}
	assertDisabled("SaveProfile(re-activate existing)")

	if _, err := repo.ClearProfileData(b.ID); err != nil {
		t.Fatalf("ClearProfileData(inactive): %v", err)
	}
	assertDisabled("ClearProfileData(inactive profile)")

	if _, err := repo.ClearProfileData(a.ID); err != nil {
		t.Fatalf("ClearProfileData(active): %v", err)
	}
	assertDisabled("ClearProfileData(active profile)")
}

func TestUpdateChecksEnabledDoesNotDisturbActiveProfile(t *testing.T) {
	repo := testRepository(t)
	a, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetUpdateChecksEnabled(false); err != nil {
		t.Fatal(err)
	}
	active, err := repo.ActiveProfile()
	if err != nil || active == nil || active.ID != a.ID {
		t.Fatalf("ActiveProfile() after SetUpdateChecksEnabled(false) = %+v, %v, want profile %d", active, err, a.ID)
	}
	assertCount(t, repo, `select count(*) from settings where key='active_profile_id' and value=?`, 1, fmt.Sprintf("%d", a.ID))

	// With no active profile the active_profile_id row keeps its empty value.
	if err := repo.DeactivateProfile(); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetUpdateChecksEnabled(true); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from settings where key='active_profile_id' and value=''`, 1)
	if got, err := repo.UpdateChecksEnabled(); err != nil || !got {
		t.Fatalf("UpdateChecksEnabled() = %v, %v, want true, nil", got, err)
	}
}
