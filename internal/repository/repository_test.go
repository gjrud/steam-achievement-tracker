package repository

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	dbinit "github.com/gjrud/steam-achievement-tracker/internal/db"
)

func TestClearProfileDataScopesDeletion(t *testing.T) {
	repo := testRepository(t)
	p1, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`update settings set value=? where key='active_profile_id'`, fmt.Sprintf("%d", p1.ID)); err != nil {
		t.Fatal(err)
	}
	seedGameSync(t, repo, p1.ID, 10)
	seedGameSync(t, repo, p1.ID, 20)
	seedGameSync(t, repo, p2.ID, 20)
	seedGameSync(t, repo, p2.ID, 30)
	if err := repo.MarkGamePreviouslyCompleted(p1.ID, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StartSyncRun(p1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.StartSyncRun(p2.ID); err != nil {
		t.Fatal(err)
	}

	appIDs, err := repo.ClearProfileData(p1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := appIDs, []int64{10, 20}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("appIDs = %v, want %v", got, want)
	}
	assertCount(t, repo, `select count(*) from profiles where id=?`, 0, p1.ID)
	assertCount(t, repo, `select count(*) from profiles where id=?`, 1, p2.ID)
	assertCount(t, repo, `select count(*) from profile_games where profile_id=?`, 0, p1.ID)
	assertCount(t, repo, `select count(*) from profile_achievements where profile_id=?`, 0, p1.ID)
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=?`, 0, p1.ID)
	assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=?`, 0, p1.ID)
	assertCount(t, repo, `select count(*) from game_snapshots where profile_id=?`, 0, p1.ID)
	assertCount(t, repo, `select count(*) from sync_runs where profile_id=?`, 0, p1.ID)
	assertCount(t, repo, `select count(*) from profile_games where profile_id=?`, 2, p2.ID)
	assertCount(t, repo, `select count(*) from games where appid=10`, 0)
	assertCount(t, repo, `select count(*) from games where appid=20`, 1)
	assertCount(t, repo, `select count(*) from games where appid=30`, 1)
	assertCount(t, repo, `select count(*) from achievements where appid=10`, 0)
	assertCount(t, repo, `select count(*) from achievements where appid=20`, 1)
	assertCount(t, repo, `select count(*) from settings where key='active_profile_id'`, 0)
}

func TestClearProfileDataKeepsOtherActiveProfileSetting(t *testing.T) {
	repo := testRepository(t)
	p1, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	seedGameSync(t, repo, p1.ID, 10)
	seedGameSync(t, repo, p2.ID, 20)

	if _, err := repo.ClearProfileData(p1.ID); err != nil {
		t.Fatal(err)
	}
	active, err := repo.ActiveProfile()
	if err != nil {
		t.Fatal(err)
	}
	if active == nil || active.ID != p2.ID {
		t.Fatalf("active profile = %#v, want profile %d", active, p2.ID)
	}
	assertCount(t, repo, `select count(*) from settings where key='active_profile_id' and value=?`, 1, fmt.Sprintf("%d", p2.ID))
	assertCount(t, repo, `select count(*) from profiles where id=?`, 0, p1.ID)
	assertCount(t, repo, `select count(*) from profile_games where profile_id=?`, 1, p2.ID)
}

func TestSaveGameWarningPreservesStatsAndSnapshots(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	missingAvg := 35.0
	score := 55.0
	syncedAt := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:            profile.ID,
		AppID:                42,
		Name:                 "Warning Game",
		PlaytimeForever:      30,
		HasAchievements:      true,
		AchievementStatus:    "ok",
		TotalAchievements:    2,
		UnlockedAchievements: 1,
		CompletionPercent:    50,
		MissingAvgUnlock:     &missingAvg,
		SuggestionScore:      &score,
		IsCompleted:          false,
		WasCompleted:         false,
		Achievements: []AchievementRecord{
			{APIName: "ACH_ONE", GlobalPercent: &missingAvg, Unlocked: true},
			{APIName: "ACH_TWO", GlobalPercent: &missingAvg, Unlocked: false},
		},
		SyncedAt: syncedAt,
	}); err != nil {
		t.Fatal(err)
	}

	if err := repo.SaveGameWarning(profile.ID, GameRecord{AppID: 42, Name: "Warning Game Updated", PlaytimeForever: 999}, "temporary Steam failure"); err != nil {
		t.Fatal(err)
	}

	state, err := repo.ProfileGame(profile.ID, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Exists || state.TotalAchievements != 2 || state.UnlockedAchievements != 1 || state.CompletionPercent != 50 || state.IsCompleted {
		t.Fatalf("state after warning = %#v, want previous achievement stats preserved", state)
	}
	if !state.SyncWarning {
		t.Fatalf("sync warning = false, want true")
	}
	if state.LastSyncedAt == nil || *state.LastSyncedAt != syncedAt {
		t.Fatalf("last_synced_at = %v, want %q", state.LastSyncedAt, syncedAt)
	}
	assertCount(t, repo, `select count(*) from game_snapshots where profile_id=? and appid=?`, 1, profile.ID, 42)
	assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=? and appid=? and tag='sync_warning'`, 1, profile.ID, 42)
	assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=? and appid=? and tag='almost_there'`, 1, profile.ID, 42)
	assertCount(t, repo, `select count(*) from games where appid=? and name=? and playtime_forever=?`, 1, 42, "Warning Game Updated", 999)
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=? and last_error='temporary Steam failure' and last_error_at is not null`, 1, profile.ID, 42)
}

func TestSaveGameWarningFreshOnlyMarksSyncWarning(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := repo.SaveGameWarning(profile.ID, GameRecord{AppID: 99, Name: "Fresh Failure", PlaytimeForever: 15}, "Steam unavailable"); err != nil {
		t.Fatal(err)
	}

	state, err := repo.ProfileGame(profile.ID, 99)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Exists || !state.SyncWarning || state.LastSyncedAt != nil {
		t.Fatalf("state = %#v, want warning with no previous sync data", state)
	}
	assertTag(t, repo, profile.ID, 99, "sync_warning", true)
	assertTag(t, repo, profile.ID, 99, "no_achievements", false)
	assertTag(t, repo, profile.ID, 99, "untouched", false)

	cards, err := repo.GameCards(profile.ID, "warnings")
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].AppID != 99 || !cards[0].SyncWarning {
		t.Fatalf("warning cards = %#v, want sync warning card", cards)
	}
}

func TestDashboardLatestSyncRunScopedToProfile(t *testing.T) {
	repo := testRepository(t)
	p1, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	run1, err := repo.StartSyncRun(p1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishSyncRun(run1, "success", 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}
	run2, err := repo.StartSyncRun(p2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishSyncRun(run2, "failed", 1, 0, 1, StringPtr("broken")); err != nil {
		t.Fatal(err)
	}

	dashboard, err := repo.Dashboard(p1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.LatestSyncRun == nil || dashboard.LatestSyncRun.ID != run1 || dashboard.LatestSyncRun.ProfileID == nil || *dashboard.LatestSyncRun.ProfileID != p1.ID {
		t.Fatalf("latest sync run = %#v, want profile %d run %d", dashboard.LatestSyncRun, p1.ID, run1)
	}
}

func TestGameTagsComputedCachedAndClearedWhenDisabled(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	missingAvg := 80.0
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:            profile.ID,
		AppID:                10,
		Name:                 "Nearly Done",
		PlaytimeForever:      120,
		HasAchievements:      true,
		AchievementStatus:    "ok",
		TotalAchievements:    10,
		UnlockedAchievements: 9,
		CompletionPercent:    90,
		MissingAvgUnlock:     &missingAvg,
		IsCompleted:          false,
		Achievements:         []AchievementRecord{{APIName: "ACH", GlobalPercent: &missingAvg, Unlocked: false}},
		SyncedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 10, "almost_there", true)
	assertTag(t, repo, profile.ID, 10, "missing_cover_art", true)

	if err := repo.ToggleMissingAchievementsInDLC(profile.ID, 10); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 10, "missing_achievements_in_dlc", true)

	if err := repo.DisableProfileGame(profile.ID, 10); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=? and appid=?`, 0, profile.ID, 10)

	if err := repo.EnableProfileGame(profile.ID, 10); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 10, "almost_there", true)
	assertTag(t, repo, profile.ID, 10, "missing_achievements_in_dlc", true)
}

func TestMarkGamePreviouslyCompletedTogglesManualFlag(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:            profile.ID,
		AppID:                15,
		Name:                 "Incomplete Game",
		PlaytimeForever:      60,
		HasAchievements:      true,
		AchievementStatus:    "ok",
		TotalAchievements:    10,
		UnlockedAchievements: 9,
		CompletionPercent:    90,
		IsCompleted:          false,
		WasCompleted:         false,
		Achievements:         []AchievementRecord{{APIName: "ACH", Unlocked: false}},
		SyncedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}

	if err := repo.MarkGamePreviouslyCompleted(profile.ID, 15); err != nil {
		t.Fatal(err)
	}
	state, err := repo.ProfileGame(profile.ID, 15)
	if err != nil {
		t.Fatal(err)
	}
	if !state.ManualWasCompleted || !state.WasCompleted || !state.NewlyIncomplete {
		t.Fatalf("state after mark = %#v, want manual, was completed, newly incomplete", state)
	}
	assertTag(t, repo, profile.ID, 15, "new_achievements_added", true)

	if err := repo.MarkGamePreviouslyCompleted(profile.ID, 15); err != nil {
		t.Fatal(err)
	}
	state, err = repo.ProfileGame(profile.ID, 15)
	if err != nil {
		t.Fatal(err)
	}
	if state.ManualWasCompleted || state.WasCompleted || state.NewlyIncomplete {
		t.Fatalf("state after clear = %#v, want manual toggle cleared", state)
	}
	assertTag(t, repo, profile.ID, 15, "new_achievements_added", false)
}

func TestSuggestionOrderingHandlesDLCFlags(t *testing.T) {
	cards := []GameCard{
		{AppID: 1, Name: "Previously Completed", TotalAchievements: 10, UnlockedAchievements: 9, CompletionPercent: 90, NewlyIncomplete: true},
		{AppID: 2, Name: "Normal Incomplete", TotalAchievements: 10, UnlockedAchievements: 1, CompletionPercent: 10},
		{AppID: 3, Name: "Both Flags", TotalAchievements: 10, UnlockedAchievements: 9, CompletionPercent: 90, NewlyIncomplete: true, MissingDLC: true},
		{AppID: 4, Name: "No Achievements", TotalAchievements: 0},
	}

	sortGameCards(cards, "suggestions")

	got := []int64{cards[0].AppID, cards[1].AppID, cards[2].AppID, cards[3].AppID}
	want := []int64{1, 2, 3, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("suggestion order = %v, want %v", got, want)
		}
	}
}

func TestDashboardOrdersBucketsAndExcludesDisabled(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}

	seedDashboardGame(t, repo, profile.ID, dashboardSeed{AppID: 10, Name: "New Easy", Total: 10, Unlocked: 9, Percent: 90, Score: 10, WasCompleted: true, NewlyIncomplete: true, Playtime: 120})
	seedDashboardGame(t, repo, profile.ID, dashboardSeed{AppID: 11, Name: "New Hard", Total: 10, Unlocked: 8, Percent: 80, Score: 95, WasCompleted: true, NewlyIncomplete: true, Playtime: 120})
	seedDashboardGame(t, repo, profile.ID, dashboardSeed{AppID: 20, Name: "High Score", Total: 10, Unlocked: 3, Percent: 30, Score: 80, Playtime: 60})
	seedDashboardGame(t, repo, profile.ID, dashboardSeed{AppID: 21, Name: "Low Score", Total: 10, Unlocked: 2, Percent: 20, Score: 30, Playtime: 60})
	seedDashboardGame(t, repo, profile.ID, dashboardSeed{AppID: 30, Name: "DLC Missing", Total: 10, Unlocked: 1, Percent: 10, Score: 99, MissingDLC: true, Playtime: 60})
	seedDashboardGame(t, repo, profile.ID, dashboardSeed{AppID: 40, Name: "Completed", Total: 10, Unlocked: 10, Percent: 100, Score: 0, Completed: true, WasCompleted: true, Playtime: 180})
	seedDashboardGame(t, repo, profile.ID, dashboardSeed{AppID: 50, Name: "Disabled", Total: 10, Unlocked: 10, Percent: 100, Score: 100, Completed: true, WasCompleted: true, Disabled: true, Playtime: 180})
	seedDashboardGame(t, repo, profile.ID, dashboardSeed{AppID: 60, Name: "Warning", Total: 5, Unlocked: 1, Percent: 20, Score: 15, SyncWarning: true, Playtime: 30})
	seedDashboardGame(t, repo, profile.ID, dashboardSeed{AppID: 70, Name: "Alpha No Achievements", Total: 0, Unlocked: 0, Percent: 0, Playtime: 0})
	seedDashboardGame(t, repo, profile.ID, dashboardSeed{AppID: 71, Name: "Zeta No Achievements", Total: 0, Unlocked: 0, Percent: 0, Playtime: 0})

	dashboard, err := repo.Dashboard(profile.ID)
	if err != nil {
		t.Fatal(err)
	}

	assertGameCardIDs(t, "suggestions", dashboard.Suggestions, []int64{10, 11, 20, 21, 60, 30, 70, 71})
	assertGameCardIDs(t, "completed", dashboard.Completed, []int64{40})
	assertGameCardIDs(t, "warnings", dashboard.Warnings, []int64{60})
	assertGameCardIDs(t, "disabled", dashboard.Disabled, []int64{50})

	if got, want := dashboard.Summary.OwnedGamesCount, 9; got != want {
		t.Fatalf("owned games count = %d, want %d", got, want)
	}
	if got, want := dashboard.Summary.CompletedGamesCount, 1; got != want {
		t.Fatalf("completed games count = %d, want %d", got, want)
	}
	if got, want := dashboard.Summary.GamesWithAchievementsCount, 7; got != want {
		t.Fatalf("games with achievements count = %d, want %d", got, want)
	}
	if got, want := dashboard.Summary.TotalAchievementsUnlocked, 34; got != want {
		t.Fatalf("total achievements unlocked = %d, want %d", got, want)
	}
	if got, want := dashboard.Summary.TotalAchievementsAvailable, 65; got != want {
		t.Fatalf("total achievements available = %d, want %d", got, want)
	}
	if got, want := dashboard.Summary.NewlyIncompleteGamesCount, 2; got != want {
		t.Fatalf("newly incomplete games count = %d, want %d", got, want)
	}
}

func TestNotOwnedDisableIsScopedPerProfile(t *testing.T) {
	repo := testRepository(t)
	p1, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	seedGameSync(t, repo, p1.ID, 50)
	seedGameSync(t, repo, p1.ID, 60)
	seedGameSync(t, repo, p2.ID, 50)

	if err := repo.DisableMissingOwnedGames(p1.ID, []int64{60}); err != nil {
		t.Fatal(err)
	}

	p1Disabled, err := repo.GameCards(p1.ID, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	assertGameCardIDs(t, "p1 disabled", p1Disabled, []int64{50})
	p2Completed, err := repo.GameCards(p2.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	assertGameCardIDs(t, "p2 completed", p2Completed, []int64{50})

	if err := repo.DisableMissingOwnedGames(p2.ID, []int64{50}); err != nil {
		t.Fatal(err)
	}
	p1Disabled, err = repo.GameCards(p1.ID, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	assertGameCardIDs(t, "p1 disabled after p2 sync", p1Disabled, []int64{50})
}

func TestGameCardCoverURLCacheBusts(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	seedGameSync(t, repo, profile.ID, 42)
	if err := repo.SaveCover(42, "/tmp/cache/images/games/42/library_600x900.jpg", "https://example.com/cover.jpg", true); err != nil {
		t.Fatal(err)
	}

	cards, err := repo.GameCards(profile.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want 1", len(cards))
	}
	if !strings.HasPrefix(cards[0].CoverURL, "/game-covers/42/library_600x900.jpg?v=") {
		t.Fatalf("cover URL = %q, want cache-busted game cover URL", cards[0].CoverURL)
	}
}

func TestGameTagsUsePlaytimeForUntouchedAndProgress(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:            profile.ID,
		AppID:                11,
		Name:                 "Untouched Game",
		PlaytimeForever:      0,
		HasAchievements:      true,
		AchievementStatus:    "ok",
		TotalAchievements:    10,
		UnlockedAchievements: 0,
		CompletionPercent:    0,
		IsCompleted:          false,
		Achievements:         []AchievementRecord{{APIName: "ACH", Unlocked: false}},
		SyncedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 11, "untouched", true)
	assertTag(t, repo, profile.ID, 11, "in_progress", false)
	assertTag(t, repo, profile.ID, 11, "almost_there", false)

	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:            profile.ID,
		AppID:                12,
		Name:                 "Started Game",
		PlaytimeForever:      120,
		HasAchievements:      true,
		AchievementStatus:    "ok",
		TotalAchievements:    10,
		UnlockedAchievements: 0,
		CompletionPercent:    0,
		IsCompleted:          false,
		Achievements:         []AchievementRecord{{APIName: "ACH", Unlocked: false}},
		SyncedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 12, "untouched", false)
	assertTag(t, repo, profile.ID, 12, "in_progress", true)
	assertTag(t, repo, profile.ID, 12, "almost_there", false)

	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:         profile.ID,
		AppID:             13,
		Name:              "No Achievements Untouched Game",
		PlaytimeForever:   0,
		HasAchievements:   false,
		AchievementStatus: "no_achievements",
		SyncedAt:          now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 13, "no_achievements", true)
	assertTag(t, repo, profile.ID, 13, "untouched", true)
	assertTag(t, repo, profile.ID, 13, "in_progress", false)
	assertTag(t, repo, profile.ID, 13, "almost_there", false)

	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:         profile.ID,
		AppID:             14,
		Name:              "No Achievements Played Game",
		PlaytimeForever:   120,
		HasAchievements:   false,
		AchievementStatus: "no_achievements",
		SyncedAt:          now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 14, "no_achievements", true)
	assertTag(t, repo, profile.ID, 14, "untouched", false)
	assertTag(t, repo, profile.ID, 14, "in_progress", false)
	assertTag(t, repo, profile.ID, 14, "almost_there", false)
}

func TestGameTagsAlmostThereThreshold(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	missingAvg := 27.0
	suggestionScore := 40.0
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:            profile.ID,
		AppID:                13,
		Name:                 "Suggestion Driven Game",
		PlaytimeForever:      60,
		HasAchievements:      true,
		AchievementStatus:    "ok",
		TotalAchievements:    100,
		UnlockedAchievements: 1,
		CompletionPercent:    1,
		MissingAvgUnlock:     &missingAvg,
		SuggestionScore:      &suggestionScore,
		IsCompleted:          false,
		Achievements:         []AchievementRecord{{APIName: "ACH", Unlocked: false}},
		SyncedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 13, "almost_there", true)
	assertTag(t, repo, profile.ID, 13, "in_progress", false)
}

func TestGameTagsBelowAlmostThereThreshold(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	suggestionScore := 39.9
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:            profile.ID,
		AppID:                14,
		Name:                 "Almost But Not Quite",
		PlaytimeForever:      60,
		HasAchievements:      true,
		AchievementStatus:    "ok",
		TotalAchievements:    100,
		UnlockedAchievements: 1,
		CompletionPercent:    1,
		SuggestionScore:      &suggestionScore,
		IsCompleted:          false,
		Achievements:         []AchievementRecord{{APIName: "ACH", Unlocked: false}},
		SyncedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 14, "almost_there", false)
	assertTag(t, repo, profile.ID, 14, "in_progress", true)
}

func testRepository(t *testing.T) *Repository {
	t.Helper()
	root := t.TempDir()
	paths := appdata.Paths{
		Root:       root,
		DB:         root + "/test.db",
		Backups:    root + "/backups",
		Cache:      root + "/cache",
		GameImages: root + "/cache/images/games",
		Logs:       root + "/logs",
		LogFile:    root + "/logs/app.log",
	}
	db, err := dbinit.OpenAndMigrate(paths)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func seedGameSync(t *testing.T, repo *Repository, profileID, appID int64) {
	t.Helper()
	global := 42.0
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:            profileID,
		AppID:                appID,
		Name:                 "Game",
		HasAchievements:      true,
		AchievementStatus:    "ok",
		TotalAchievements:    1,
		UnlockedAchievements: 1,
		CompletionPercent:    100,
		IsCompleted:          true,
		WasCompleted:         true,
		Achievements:         []AchievementRecord{{APIName: "ACH", GlobalPercent: &global, Unlocked: true}},
		SyncedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
}

type dashboardSeed struct {
	AppID           int64
	Name            string
	Total           int
	Unlocked        int
	Percent         float64
	Score           float64
	Completed       bool
	WasCompleted    bool
	NewlyIncomplete bool
	MissingDLC      bool
	SyncWarning     bool
	Disabled        bool
	Playtime        int
}

func seedDashboardGame(t *testing.T, repo *Repository, profileID int64, seed dashboardSeed) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	missingAvg := 42.0
	score := seed.Score
	var achievements []AchievementRecord
	for i := 0; i < seed.Total; i++ {
		achievements = append(achievements, AchievementRecord{
			APIName:       fmt.Sprintf("ACH_%d_%d", seed.AppID, i),
			GlobalPercent: &missingAvg,
			Unlocked:      i < seed.Unlocked,
		})
	}
	lastError := (*string)(nil)
	if seed.SyncWarning {
		lastError = StringPtr("temporary Steam failure")
	}
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:            profileID,
		AppID:                seed.AppID,
		Name:                 seed.Name,
		PlaytimeForever:      seed.Playtime,
		HasAchievements:      seed.Total > 0,
		AchievementStatus:    "ok",
		TotalAchievements:    seed.Total,
		UnlockedAchievements: seed.Unlocked,
		CompletionPercent:    seed.Percent,
		MissingAvgUnlock:     &missingAvg,
		SuggestionScore:      &score,
		IsCompleted:          seed.Completed,
		WasCompleted:         seed.WasCompleted,
		NewlyIncomplete:      seed.NewlyIncomplete,
		SyncWarning:          seed.SyncWarning,
		LastError:            lastError,
		Achievements:         achievements,
		SyncedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
	if seed.MissingDLC {
		if err := repo.ToggleMissingAchievementsInDLC(profileID, seed.AppID); err != nil {
			t.Fatal(err)
		}
	}
	if seed.Disabled {
		if err := repo.DisableProfileGame(profileID, seed.AppID); err != nil {
			t.Fatal(err)
		}
	}
}

func assertGameCardIDs(t *testing.T, label string, cards []GameCard, want []int64) {
	t.Helper()
	got := make([]int64, len(cards))
	for i, card := range cards {
		got[i] = card.AppID
	}
	if len(got) != len(want) {
		t.Fatalf("%s game ids = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s game ids = %v, want %v", label, got, want)
		}
	}
}

func assertCount(t *testing.T, repo *Repository, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := repo.db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", query, got, want)
	}
}

func assertTag(t *testing.T, repo *Repository, profileID, appID int64, tag string, want bool) {
	t.Helper()
	var count int
	if err := repo.db.QueryRow(`select count(*) from profile_game_tags where profile_id=? and appid=? and tag=?`, profileID, appID, tag).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if got := count > 0; got != want {
		t.Fatalf("tag %q exists = %v, want %v", tag, got, want)
	}
}
