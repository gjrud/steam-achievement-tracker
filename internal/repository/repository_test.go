package repository

import (
	"database/sql"
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
	mustSaveLibraryCover(t, repo, 10)
	mustSaveLibraryCover(t, repo, 20)
	if err := repo.MarkGamePreviouslyCompleted(p1.ID, 10, 0); err != nil {
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
	// Only app 10 is orphaned; app 20 is still shown by profile p2.
	if got, want := appIDs, []int64{10}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("appIDs = %v, want %v", got, want)
	}
	assertCount(t, repo, `select count(*) from cover_images where image_appid=10`, 0)
	assertCount(t, repo, `select count(*) from cover_images where image_appid=20`, 1)
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

func TestClearProfileDataReturnsOnlyOrphanedCoverIDs(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 0, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 4173830, SetName: "Pack", Total: 1, Unlocked: 0, Percent: 0})
	seedGameSync(t, repo, other.ID, 2968420)
	mustSaveHeroCover(t, repo, 4173830)
	mustSaveLibraryCover(t, repo, 2968420)

	ids, err := repo.ClearProfileData(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	// DLC cover had no other user: row removed and its id returned. Base cover is still used by the other profile.
	if got, want := ids, []int64{4173830}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("returned ids = %v, want %v", got, want)
	}
	assertCount(t, repo, `select count(*) from cover_images where image_appid=4173830`, 0)
	assertCount(t, repo, `select count(*) from cover_images where image_appid=2968420`, 1)
}

func TestClearProfileDataKeepsDLCCoverUsedByOtherProfile(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	dlcSet := dashboardSeed{GroupID: 1, DLCAppID: 4173830, SetName: "Pack", Total: 1, Unlocked: 0, Percent: 0}
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 0, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true}, dlcSet)
	saveSeedGame(t, repo, other.ID, 2968420, "PowerWash Simulator 2", 0, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true}, dlcSet)
	mustSaveHeroCover(t, repo, 4173830)

	ids, err := repo.ClearProfileData(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("returned ids = %v, want none while other profile shows the DLC set", ids)
	}
	assertCount(t, repo, `select count(*) from cover_images where image_appid=4173830`, 1)
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
		ProfileID:         profile.ID,
		AppID:             42,
		Name:              "Warning Game",
		PlaytimeForever:   30,
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets: []SetSyncUpdate{{
			GroupID:              0,
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
		}},
		SyncedAt: syncedAt,
	}); err != nil {
		t.Fatal(err)
	}

	if err := repo.SaveGameWarning(profile.ID, GameRecord{AppID: 42, Name: "Warning Game Updated", PlaytimeForever: 999}, "temporary Steam failure"); err != nil {
		t.Fatal(err)
	}

	state, err := repo.ProfileGame(profile.ID, 42, 0)
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
	assertCount(t, repo, `select count(*) from game_snapshots where profile_id=? and appid=? and groupid=?`, 1, profile.ID, 42, 0)
	assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=? and appid=? and groupid=? and tag='sync_warning'`, 1, profile.ID, 42, 0)
	assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=? and appid=? and groupid=? and tag='almost_there'`, 1, profile.ID, 42, 0)
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=? and playtime_forever=?`, 1, profile.ID, 42, 999)
	assertCount(t, repo, `select count(*) from games where appid=? and name=?`, 1, 42, "Warning Game Updated")
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

	state, err := repo.ProfileGame(profile.ID, 99, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Exists || !state.SyncWarning || state.LastSyncedAt != nil {
		t.Fatalf("state = %#v, want warning with no previous sync data", state)
	}
	assertTag(t, repo, profile.ID, 99, 0, "sync_warning", true)
	assertTag(t, repo, profile.ID, 99, 0, "no_achievements", false)
	assertTag(t, repo, profile.ID, 99, 0, "untouched", false)

	cards, err := repo.GameCards(profile.ID, "warnings")
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].AppID != 99 || !cards[0].SyncWarning {
		t.Fatalf("warning cards = %#v, want sync warning card", cards)
	}
}

func TestSaveGameWarningMarksEveryExistingEntry(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 0,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 4173830, SetName: "Pack", Total: 2, Unlocked: 1, Percent: 50})

	if err := repo.SaveGameWarning(profile.ID, GameRecord{AppID: 2968420, Name: "PowerWash Simulator 2", PlaytimeForever: 0}, "Steam 503"); err != nil {
		t.Fatal(err)
	}
	cards, err := repo.GameCards(profile.ID, "warnings")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "warnings", cards, []string{"2968420:0", "2968420:1"})
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
		ProfileID:         profile.ID,
		AppID:             10,
		Name:              "Nearly Done",
		PlaytimeForever:   120,
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets: []SetSyncUpdate{{
			GroupID:              0,
			TotalAchievements:    10,
			UnlockedAchievements: 9,
			CompletionPercent:    90,
			MissingAvgUnlock:     &missingAvg,
			IsCompleted:          false,
			Achievements:         []AchievementRecord{{APIName: "ACH", GlobalPercent: &missingAvg, Unlocked: false}},
		}},
		SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 10, 0, "almost_there", true)
	assertTag(t, repo, profile.ID, 10, 0, "missing_cover_art", true)

	if err := repo.ToggleMissingAchievementsInDLC(profile.ID, 10, 0); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 10, 0, "missing_achievements_in_dlc", true)

	if err := repo.DisableProfileGame(profile.ID, 10, 0); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=? and appid=? and groupid=?`, 0, profile.ID, 10, 0)

	if err := repo.EnableProfileGame(profile.ID, 10, 0); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 10, 0, "almost_there", true)
	assertTag(t, repo, profile.ID, 10, 0, "missing_achievements_in_dlc", true)
}

func TestMarkGamePreviouslyCompletedTogglesManualFlag(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:         profile.ID,
		AppID:             15,
		Name:              "Incomplete Game",
		PlaytimeForever:   60,
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets: []SetSyncUpdate{{
			GroupID:              0,
			TotalAchievements:    10,
			UnlockedAchievements: 9,
			CompletionPercent:    90,
			IsCompleted:          false,
			WasCompleted:         false,
			Achievements:         []AchievementRecord{{APIName: "ACH", Unlocked: false}},
		}},
		SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	if err := repo.MarkGamePreviouslyCompleted(profile.ID, 15, 0); err != nil {
		t.Fatal(err)
	}
	state, err := repo.ProfileGame(profile.ID, 15, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !state.ManualWasCompleted || !state.WasCompleted || !state.NewlyIncomplete {
		t.Fatalf("state after mark = %#v, want manual, was completed, newly incomplete", state)
	}
	assertTag(t, repo, profile.ID, 15, 0, "new_achievements_added", true)

	if err := repo.MarkGamePreviouslyCompleted(profile.ID, 15, 0); err != nil {
		t.Fatal(err)
	}
	state, err = repo.ProfileGame(profile.ID, 15, 0)
	if err != nil {
		t.Fatal(err)
	}
	if state.ManualWasCompleted || state.WasCompleted || state.NewlyIncomplete {
		t.Fatalf("state after clear = %#v, want manual toggle cleared", state)
	}
	assertTag(t, repo, profile.ID, 15, 0, "new_achievements_added", false)
}

func TestSuggestionOrderingHandlesDLCFlags(t *testing.T) {
	// Unowned DLC with a higher score than every candidate still sorts after all candidates.
	unownedScore := 95.0
	cards := []GameCard{
		{AppID: 5, Name: "Unowned DLC", TotalAchievements: 10, CompletionPercent: 0, SuggestionScore: &unownedScore, DLCNotOwned: true},
		{AppID: 1, Name: "Previously Completed", TotalAchievements: 10, UnlockedAchievements: 9, CompletionPercent: 90, NewlyIncomplete: true},
		{AppID: 2, Name: "Normal Incomplete", TotalAchievements: 10, UnlockedAchievements: 1, CompletionPercent: 10},
		{AppID: 3, Name: "DLC Missing", TotalAchievements: 10, UnlockedAchievements: 9, CompletionPercent: 90, MissingDLC: true},
		{AppID: 4, Name: "No Achievements", TotalAchievements: 0},
	}

	sortGameCards(cards, "suggestions")

	got := []int64{cards[0].AppID, cards[1].AppID, cards[2].AppID, cards[3].AppID, cards[4].AppID}
	want := []int64{1, 2, 5, 3, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("suggestion order = %v, want %v", got, want)
		}
	}
}

func TestSuggestionGroupsOrderCandidateNotOwnedDLCFlaggedNoAchievements(t *testing.T) {
	score := func(v float64) *float64 { return &v }
	cards := []GameCard{
		// Names are chosen so name order alone would give a different result than group order.
		{AppID: 1, Name: "Zed Candidate", TotalAchievements: 10, UnlockedAchievements: 1, CompletionPercent: 10, SuggestionScore: score(50)},
		{AppID: 2, Name: "Aaa Unowned", TotalAchievements: 10, SuggestionScore: score(95), DLCNotOwned: true},
		// Both flags: belongs to the not-owned group, ordered by name with other not-owned cards.
		{AppID: 3, Name: "Ccc Both Flags", TotalAchievements: 10, SuggestionScore: score(99), DLCNotOwned: true, MissingDLC: true},
		{AppID: 4, Name: "Bbb Flagged Low", TotalAchievements: 10, UnlockedAchievements: 1, CompletionPercent: 10, SuggestionScore: score(10), MissingDLC: true},
		{AppID: 5, Name: "Zed Flagged High", TotalAchievements: 10, UnlockedAchievements: 8, CompletionPercent: 80, SuggestionScore: score(80), MissingDLC: true},
		{AppID: 6, Name: "Aaa No Achievements", TotalAchievements: 0},
	}

	sortGameCards(cards, "suggestions")

	want := []int64{1, 2, 3, 5, 4, 6}
	for i := range want {
		if cards[i].AppID != want[i] {
			got := make([]int64, len(cards))
			for j := range cards {
				got[j] = cards[j].AppID
			}
			t.Fatalf("suggestion order = %v, want %v (candidate < not-owned < DLC-flagged < no-achievements)", got, want)
		}
	}
}

func TestCompletedEntriesDropNewAndDLCMissingIssueTags(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	// Completed entry carrying stored new-achievements and DLC-missing state.
	seedDashboardGame(t, repo, profile.ID, dashboardSeed{AppID: 30, Name: "Done Late", Total: 10, Unlocked: 10, Percent: 100, Completed: true, WasCompleted: true, NewlyIncomplete: true, MissingDLC: true, Playtime: 100})

	assertTag(t, repo, profile.ID, 30, 0, "completed", true)
	assertTag(t, repo, profile.ID, 30, 0, "missing_cover_art", true)
	assertTag(t, repo, profile.ID, 30, 0, "new_achievements_added", false)
	assertTag(t, repo, profile.ID, 30, 0, "missing_achievements_in_dlc", false)
	// The manual flags themselves are kept; only the derived tags are dropped.
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=30 and newly_incomplete=1`, 1, profile.ID)
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid=30 and missing_achievements_in_dlc=1`, 1, profile.ID)

	dashboard, err := repo.Dashboard(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.Completed) != 1 {
		t.Fatalf("completed cards = %d, want 1", len(dashboard.Completed))
	}
	if tags := dashboard.Completed[0].Tags; hasTagValue(tags, "new_achievements_added") || hasTagValue(tags, "missing_achievements_in_dlc") {
		t.Fatalf("completed card tags = %v, want no new-achievements or DLC-missing tag", tags)
	}
}

func TestDLCNotOwnedSetsStayOutOfTotalsAndRankLast(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	seedDashboardGame(t, repo, profile.ID, dashboardSeed{AppID: 10, Name: "Candidate", Total: 10, Unlocked: 1, Percent: 10, Score: 5, Playtime: 60})
	// The unowned set scores higher than every candidate, yet must still sort last.
	saveSeedGame(t, repo, profile.ID, 20, "Owned Base", 60,
		dashboardSeed{GroupID: 0, Total: 10, Unlocked: 4, Percent: 40, Score: 20},
		dashboardSeed{GroupID: 1, DLCAppID: 555, SetName: "Unowned Pack", Total: 10, Unlocked: 0, Percent: 0, Score: 95, DLCNotOwned: true})

	dashboard, err := repo.Dashboard(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "suggestions", dashboard.Suggestions, []string{"20:0", "10:0", "20:1"})
	assertEntryKeys(t, "completed", dashboard.Completed, nil)

	unowned := dashboard.Suggestions[2]
	if !unowned.DLCNotOwned || unowned.Disabled || unowned.IsCompleted {
		t.Fatalf("unowned card = %#v, want DLCNotOwned in Not completed view", unowned)
	}
	if !hasTagValue(unowned.Tags, "dlc_not_owned") {
		t.Fatalf("unowned card tags = %v, want dlc_not_owned", unowned.Tags)
	}
	for _, tag := range []string{"in_progress", "almost_there", "untouched"} {
		if hasTagValue(unowned.Tags, tag) {
			t.Fatalf("unowned card tags = %v, want no %s", unowned.Tags, tag)
		}
	}
	if dashboard.Summary.TotalAchievementsAvailable != 20 || dashboard.Summary.TotalAchievementsUnlocked != 5 {
		t.Fatalf("summary available/unlocked = %d/%d, want 20/5 without the unowned set", dashboard.Summary.TotalAchievementsAvailable, dashboard.Summary.TotalAchievementsUnlocked)
	}
}

func hasTagValue(tags []string, want string) bool {
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

func TestSortGameCardsTieBreaksByEntryKey(t *testing.T) {
	cards := []GameCard{
		{AppID: 2968420, GroupID: 1, EntryKey: "2968420:1", Name: "Same Name", TotalAchievements: 5, UnlockedAchievements: 5, IsCompleted: true},
		{AppID: 2968420, GroupID: 0, EntryKey: "2968420:0", Name: "Same Name", TotalAchievements: 5, UnlockedAchievements: 5, IsCompleted: true},
	}
	sortGameCards(cards, "completed")
	if cards[0].EntryKey != "2968420:0" || cards[1].EntryKey != "2968420:1" {
		t.Fatalf("order = %q, %q, want entryKey tie-breaker", cards[0].EntryKey, cards[1].EntryKey)
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
	if got, want := dashboard.Summary.CompletedSetsCount, 1; got != want {
		t.Fatalf("completed sets count = %d, want %d", got, want)
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

func TestSetEntriesSplitCompletedFromSuggestionsAndNameImages(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 300,
		dashboardSeed{GroupID: 0, Total: 40, Unlocked: 40, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 4173830, SetName: "PowerWash Simulator 2 DLC Pack", Total: 10, Unlocked: 3, Percent: 30})
	saveSeedGame(t, repo, profile.ID, 440, "Team Fortress 2", 0,
		dashboardSeed{GroupID: 3, SetName: "Scout", Total: 12, Unlocked: 12, Percent: 100, Completed: true, WasCompleted: true})

	completed, err := repo.GameCards(profile.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "completed", completed, []string{"2968420:0", "440:3"})
	if completed[1].Name != "Team Fortress 2 – Scout" || !completed[1].IsSet || completed[1].SetName != "Scout" {
		t.Fatalf("set card = %#v, want display name 'Team Fortress 2 – Scout' and set metadata", completed[1])
	}

	suggestions, err := repo.GameCards(profile.ID, "suggestions")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "suggestions", suggestions, []string{"2968420:1"})
	dlc := suggestions[0]
	if dlc.Name != "PowerWash Simulator 2 DLC Pack" || dlc.DLCAppID != 4173830 || dlc.AppID != 2968420 || dlc.GroupID != 1 {
		t.Fatalf("dlc entry = %#v, want DLC set of base app", dlc)
	}
	if dlc.CoverURL != "" {
		t.Fatalf("dlc cover = %q, want no cover before one is cached", dlc.CoverURL)
	}

	// Cover cached for the DLC image app shows on the DLC entry only.
	mustSaveHeroCover(t, repo, 4173830)
	suggestions, err = repo.GameCards(profile.ID, "suggestions")
	if err != nil {
		t.Fatal(err)
	}
	if got := suggestions[0].CoverURL; !strings.HasPrefix(got, "/game-covers/4173830/hero_capsule.jpg?v=") || suggestions[0].CoverKind != "hero" {
		t.Fatalf("dlc cover = %q kind %q, want hero cover of DLC id", got, suggestions[0].CoverKind)
	}
	completed, err = repo.GameCards(profile.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	if completed[0].CoverURL != "" {
		t.Fatalf("base cover = %q, want base entry unaffected by DLC image", completed[0].CoverURL)
	}
}

// seedDLCSetCoverGame syncs a completed base game (kept off the suggestions list) and one unfinished DLC set bound to 4173830.
func seedDLCSetCoverGame(t *testing.T, repo *Repository, profileID int64) {
	t.Helper()
	saveSeedGame(t, repo, profileID, 2968420, "PowerWash Simulator 2", 300,
		dashboardSeed{GroupID: 0, Total: 40, Unlocked: 40, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 4173830, SetName: "PowerWash Simulator 2 DLC Pack", Total: 10, Unlocked: 3, Percent: 30})
}

func cardByEntryKey(t *testing.T, cards []GameCard, entryKey string) GameCard {
	t.Helper()
	for _, c := range cards {
		if c.EntryKey == entryKey {
			return c
		}
	}
	t.Fatalf("entry %s not in cards", entryKey)
	return GameCard{}
}

func TestDLCEntryFallsBackToParentCover(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	seedDLCSetCoverGame(t, repo, profile.ID)
	mustSaveLibraryCover(t, repo, 2968420)

	suggestions, err := repo.GameCards(profile.ID, "suggestions")
	if err != nil {
		t.Fatal(err)
	}
	dlc := cardByEntryKey(t, suggestions, "2968420:1")
	if got := dlc.CoverURL; !strings.HasPrefix(got, "/game-covers/2968420/library_600x900.jpg?v=") || dlc.CoverKind != "library" {
		t.Fatalf("dlc cover = %q kind %q, want parent library cover", got, dlc.CoverKind)
	}
	assertTag(t, repo, profile.ID, 2968420, 1, "missing_cover_art", false)

	completed, err := repo.GameCards(profile.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	if got := cardByEntryKey(t, completed, "2968420:0").CoverURL; !strings.HasPrefix(got, "/game-covers/2968420/library_600x900.jpg?v=") {
		t.Fatalf("base cover = %q, want parent library cover", got)
	}
}

func TestDLCOwnCoverOverridesParentCover(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	seedDLCSetCoverGame(t, repo, profile.ID)
	mustSaveLibraryCover(t, repo, 2968420)
	mustSaveHeroCover(t, repo, 4173830)

	suggestions, err := repo.GameCards(profile.ID, "suggestions")
	if err != nil {
		t.Fatal(err)
	}
	dlc := cardByEntryKey(t, suggestions, "2968420:1")
	if got := dlc.CoverURL; !strings.HasPrefix(got, "/game-covers/4173830/hero_capsule.jpg?v=") || dlc.CoverKind != "hero" {
		t.Fatalf("dlc cover = %q kind %q, want own hero cover of DLC id", got, dlc.CoverKind)
	}
	assertTag(t, repo, profile.ID, 2968420, 1, "missing_cover_art", false)

	completed, err := repo.GameCards(profile.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	if got := cardByEntryKey(t, completed, "2968420:0").CoverURL; !strings.HasPrefix(got, "/game-covers/2968420/library_600x900.jpg?v=") {
		t.Fatalf("base cover = %q, want parent library cover", got)
	}
}

func TestDLCEmptyCoverRowFallsBackToParentCover(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	seedDLCSetCoverGame(t, repo, profile.ID)
	mustSaveLibraryCover(t, repo, 2968420)
	// DLC cover row exists without a cached file: the parent cover still shows.
	if err := repo.SaveCover(CoverUpdate{ImageAppID: 4173830, Kind: "hero", FileName: "hero_capsule.jpg"}); err != nil {
		t.Fatal(err)
	}

	suggestions, err := repo.GameCards(profile.ID, "suggestions")
	if err != nil {
		t.Fatal(err)
	}
	dlc := cardByEntryKey(t, suggestions, "2968420:1")
	if got := dlc.CoverURL; !strings.HasPrefix(got, "/game-covers/2968420/library_600x900.jpg?v=") || dlc.CoverKind != "library" {
		t.Fatalf("dlc cover = %q kind %q, want parent library cover", got, dlc.CoverKind)
	}
	assertTag(t, repo, profile.ID, 2968420, 1, "missing_cover_art", false)
}

func TestDLCWithoutCoverShowsMissingArtUntilParentCoverSaved(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	seedDLCSetCoverGame(t, repo, profile.ID)

	suggestions, err := repo.GameCards(profile.ID, "suggestions")
	if err != nil {
		t.Fatal(err)
	}
	dlc := cardByEntryKey(t, suggestions, "2968420:1")
	if dlc.CoverURL != "" {
		t.Fatalf("dlc cover = %q, want none when neither DLC nor parent has a cover", dlc.CoverURL)
	}
	assertTag(t, repo, profile.ID, 2968420, 1, "missing_cover_art", true)

	// Saving the parent cover recomputes the DLC entry's tags through its game id.
	mustSaveLibraryCover(t, repo, 2968420)
	suggestions, err = repo.GameCards(profile.ID, "suggestions")
	if err != nil {
		t.Fatal(err)
	}
	dlc = cardByEntryKey(t, suggestions, "2968420:1")
	if !strings.HasPrefix(dlc.CoverURL, "/game-covers/2968420/library_600x900.jpg?v=") {
		t.Fatalf("dlc cover = %q, want parent library cover after parent saved", dlc.CoverURL)
	}
	assertTag(t, repo, profile.ID, 2968420, 1, "missing_cover_art", false)
}

func TestClearProfileDataKeepsGameCoverUsedByDLCEntryOfOtherProfile(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 0, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})
	saveSeedGame(t, repo, other.ID, 2968420, "PowerWash Simulator 2", 0, dashboardSeed{GroupID: 1, DLCAppID: 4173830, SetName: "Pack", Total: 1, Unlocked: 0, Percent: 0})
	mustSaveLibraryCover(t, repo, 2968420)

	ids, err := repo.ClearProfileData(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The other profile's DLC entry falls back to the game cover, so the cover row must survive.
	if len(ids) != 0 {
		t.Fatalf("returned ids = %v, want none while other profile's DLC entry shows the game cover", ids)
	}
	assertCount(t, repo, `select count(*) from cover_images where image_appid=2968420`, 1)
}

func TestCoverURLForLibraryAndHero(t *testing.T) {
	downloaded := sql.NullString{String: "2026-11-14T10:00:00Z", Valid: true}
	if got, want := coverURL(42, "library_600x900.jpg", "/cache/42/library_600x900.jpg", downloaded), "/game-covers/42/library_600x900.jpg?v=2026-11-14T10:00:00Z"; got != want {
		t.Fatalf("library cover url = %q, want %q", got, want)
	}
	if got, want := coverURL(4173830, "hero_capsule.jpg", "/cache/4173830/hero_capsule.jpg", sql.NullString{}), "/game-covers/4173830/hero_capsule.jpg"; got != want {
		t.Fatalf("hero cover url = %q, want %q", got, want)
	}
	if got, want := coverURL(4173830, "main_capsule.jpg", "/cache/4173830/main_capsule.jpg", downloaded), "/game-covers/4173830/main_capsule.jpg?v=2026-11-14T10:00:00Z"; got != want {
		t.Fatalf("main cover url = %q, want %q", got, want)
	}
	if got := coverURL(42, "library_600x900.jpg", "", downloaded); got != "" {
		t.Fatalf("cover url without path = %q, want empty", got)
	}
}

func TestDisplayName(t *testing.T) {
	tests := []struct {
		name, game, set string
		group           int64
		want            string
	}{
		{"base game uses game name", "Elden Ring", "", 0, "Elden Ring"},
		{"base game ignores set name", "Elden Ring", "Ignored", 0, "Elden Ring"},
		{"set containing game name is used as is", "PowerWash Simulator 2", "PowerWash Simulator 2 DLC Pack", 1, "PowerWash Simulator 2 DLC Pack"},
		{"set containing game name case-insensitively", "PowerWash Simulator 2", "powerwash simulator 2 extra", 2, "powerwash simulator 2 extra"},
		{"custom set gets game prefix", "Team Fortress 2", "Scout", 3, "Team Fortress 2 – Scout"},
		{"unnamed set falls back to group id", "Portal 2", "", 5, "Portal 2 – Set 5"},
		{"set name trimmed before empty check", "Portal 2", "   ", 6, "Portal 2 – Set 6"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DisplayName(tt.game, tt.set, tt.group); got != tt.want {
				t.Fatalf("DisplayName(%q, %q, %d) = %q, want %q", tt.game, tt.set, tt.group, got, tt.want)
			}
		})
	}
}

func TestSummaryCountsDistinctGamesAcrossSets(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 300,
		dashboardSeed{GroupID: 0, Total: 40, Unlocked: 40, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 4173830, SetName: "PowerWash Simulator 2 DLC Pack", Total: 10, Unlocked: 3, Percent: 30},
		dashboardSeed{GroupID: 2, DLCAppID: 4229450, SetName: "PowerWash Simulator 2 Other Pack", Total: 10, Unlocked: 0, Percent: 0})

	summary, err := repo.Summary(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.OwnedGamesCount != 1 || summary.GamesWithAchievementsCount != 1 {
		t.Fatalf("games = owned %d / with achievements %d, want 1/1 (distinct appid)", summary.OwnedGamesCount, summary.GamesWithAchievementsCount)
	}
	if summary.CompletedSetsCount != 1 || summary.CompletedGamesCount != 1 {
		t.Fatalf("completed sets/games = %d/%d, want 1/1", summary.CompletedSetsCount, summary.CompletedGamesCount)
	}
	if summary.TotalAchievementsAvailable != 60 || summary.TotalAchievementsUnlocked != 43 {
		t.Fatalf("achievements = %d/%d, want 43/60", summary.TotalAchievementsUnlocked, summary.TotalAchievementsAvailable)
	}
}

// A completed DLC set does not make its game completed while the base set is incomplete, and a completed base
// set counts as a completed game even when its DLC sets are incomplete. Sets count per entry, games per app.
func TestSummaryCompletedGamesDifferFromCompletedSets(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	// Game 100: base incomplete, DLC set 1 complete. One completed set, zero completed games.
	saveSeedGame(t, repo, profile.ID, 100, "Base Open", 10,
		dashboardSeed{GroupID: 0, Total: 5, Unlocked: 2, Percent: 40},
		dashboardSeed{GroupID: 1, DLCAppID: 1001, SetName: "Open DLC", Total: 3, Unlocked: 3, Percent: 100, Completed: true, WasCompleted: true})
	// Game 200: base complete, DLC set 1 incomplete. One completed set and one completed game.
	saveSeedGame(t, repo, profile.ID, 200, "Base Done", 20,
		dashboardSeed{GroupID: 0, Total: 5, Unlocked: 5, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 2001, SetName: "Idle DLC", Total: 3, Unlocked: 0, Percent: 0})
	// Game 300: base and DLC set both complete. Two completed sets, one completed game.
	saveSeedGame(t, repo, profile.ID, 300, "Both Done", 30,
		dashboardSeed{GroupID: 0, Total: 4, Unlocked: 4, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 3001, SetName: "Done DLC", Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})

	summary, err := repo.Summary(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.CompletedSetsCount != 4 {
		t.Fatalf("completed sets = %d, want 4", summary.CompletedSetsCount)
	}
	if summary.CompletedGamesCount != 2 {
		t.Fatalf("completed games = %d, want 2 (games 200 and 300)", summary.CompletedGamesCount)
	}
	if summary.OwnedGamesCount != 3 {
		t.Fatalf("owned games = %d, want 3", summary.OwnedGamesCount)
	}
}

// Purchased games count distinct enabled apps whose profile_owned_games row has purchased=1.
// Played-free games and disabled purchased games do not count.
func TestSummaryPurchasedGamesCount(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertOwnedGame(profile.ID, GameRecord{AppID: 500, Name: "Bought", Purchased: true}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertOwnedGame(profile.ID, GameRecord{AppID: 501, Name: "Played Free"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertOwnedGame(profile.ID, GameRecord{AppID: 502, Name: "Bought Disabled", Purchased: true}); err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 500, "Bought", 0, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50})
	saveSeedGame(t, repo, profile.ID, 501, "Played Free", 0, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50})
	saveSeedGame(t, repo, profile.ID, 502, "Bought Disabled", 0, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50, Disabled: true})

	summary, err := repo.Summary(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.OwnedGamesCount != 2 || summary.PurchasedGamesCount != 1 {
		t.Fatalf("owned = %d purchased = %d, want 2 and 1 (disabled purchased game excluded)", summary.OwnedGamesCount, summary.PurchasedGamesCount)
	}
}

// Two profiles that own the same app keep their own playtime and purchased flag. Syncing one profile
// (SaveGameSync) changes neither its own purchased flag nor the other profile's values.
func TestProfilesKeepOwnPlaytimeAndPurchasedForSharedGame(t *testing.T) {
	repo := testRepository(t)
	p1, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, p1.ID, 700, "Shared", 100, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})
	saveSeedGame(t, repo, p2.ID, 700, "Shared", 5, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})
	if err := repo.UpsertOwnedGame(p1.ID, GameRecord{AppID: 700, Name: "Shared", PlaytimeForever: 100, Purchased: true}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertOwnedGame(p2.ID, GameRecord{AppID: 700, Name: "Shared", PlaytimeForever: 5, Purchased: false}); err != nil {
		t.Fatal(err)
	}
	assertGameCardPlaytime(t, repo, p1.ID, 700, 100)
	assertGameCardPlaytime(t, repo, p2.ID, 700, 5)
	assertSummaryPurchased(t, repo, p1.ID, 1)
	assertSummaryPurchased(t, repo, p2.ID, 0)

	// Profile B syncs with new playtime. Profile A keeps its values; B keeps its own purchased=0.
	saveSeedGame(t, repo, p2.ID, 700, "Shared", 7, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})
	assertGameCardPlaytime(t, repo, p1.ID, 700, 100)
	assertGameCardPlaytime(t, repo, p2.ID, 700, 7)
	assertSummaryPurchased(t, repo, p1.ID, 1)
	assertSummaryPurchased(t, repo, p2.ID, 0)
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=? and playtime_forever=? and purchased=1`, 1, p1.ID, 700, 100)
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=? and playtime_forever=? and purchased=0`, 1, p2.ID, 700, 7)
}

// Clearing a profile removes its profile_owned_games rows. The shared games row stays while another profile
// still owns the game, and is removed once no profile references it.
func TestClearProfileDataRemovesOwnedGameFacts(t *testing.T) {
	repo := testRepository(t)
	p1, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, p1.ID, 800, "Shared Owned", 10, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50})
	saveSeedGame(t, repo, p2.ID, 800, "Shared Owned", 20, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50})
	if err := repo.UpsertOwnedGame(p2.ID, GameRecord{AppID: 800, Name: "Shared Owned", PlaytimeForever: 20, Purchased: true}); err != nil {
		t.Fatal(err)
	}
	// Owned only through UpsertOwnedGame: no profile_games, achievement, or snapshot rows reference app 801.
	if err := repo.UpsertOwnedGame(p2.ID, GameRecord{AppID: 801, Name: "Owned Only", PlaytimeForever: 3, Purchased: true}); err != nil {
		t.Fatal(err)
	}
	// Owned by both profiles through UpsertOwnedGame only: no profile_games, achievement, or snapshot rows reference app 802.
	// Clearing p2 must keep the games row because p1 still owns it.
	if err := repo.UpsertOwnedGame(p1.ID, GameRecord{AppID: 802, Name: "Owned By Both", PlaytimeForever: 4}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertOwnedGame(p2.ID, GameRecord{AppID: 802, Name: "Owned By Both", PlaytimeForever: 6, Purchased: true}); err != nil {
		t.Fatal(err)
	}

	if _, err := repo.ClearProfileData(p2.ID); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=?`, 0, p2.ID)
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=? and playtime_forever=?`, 1, p1.ID, 800, 10)
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=?`, 1, p1.ID, 802)
	assertCount(t, repo, `select count(*) from games where appid=?`, 1, 800)
	assertCount(t, repo, `select count(*) from games where appid=?`, 1, 802)
	assertCount(t, repo, `select count(*) from games where appid=?`, 0, 801)

	if _, err := repo.ClearProfileData(p1.ID); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from profile_owned_games`, 0)
	assertCount(t, repo, `select count(*) from games where appid=?`, 0, 800)
	assertCount(t, repo, `select count(*) from games where appid=?`, 0, 802)
}

func assertGameCardPlaytime(t *testing.T, repo *Repository, profileID, appID int64, want int) {
	t.Helper()
	cards, err := repo.GameCards(profileID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cards {
		if c.AppID == appID {
			if c.PlaytimeForever != want {
				t.Fatalf("profile %d app %d playtime = %d, want %d", profileID, appID, c.PlaytimeForever, want)
			}
			return
		}
	}
	t.Fatalf("profile %d has no card for app %d", profileID, appID)
}

func assertSummaryPurchased(t *testing.T, repo *Repository, profileID int64, want int) {
	t.Helper()
	summary, err := repo.Summary(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PurchasedGamesCount != want {
		t.Fatalf("profile %d purchased games = %d, want %d", profileID, summary.PurchasedGamesCount, want)
	}
}

// Re-syncing a game must not reset the purchased flag written by UpsertOwnedGame.
func TestSaveGameSyncPreservesPurchasedFlag(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertOwnedGame(profile.ID, GameRecord{AppID: 600, Name: "Bought", PlaytimeForever: 1, Purchased: true}); err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 600, "Bought", 5, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})

	summary, err := repo.Summary(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PurchasedGamesCount != 1 {
		t.Fatalf("purchased games after sync = %d, want 1", summary.PurchasedGamesCount)
	}
	// A later owned-games upsert with purchased=false clears it (the purchased list no longer has the game).
	if err := repo.UpsertOwnedGame(profile.ID, GameRecord{AppID: 600, Name: "Bought", PlaytimeForever: 5}); err != nil {
		t.Fatal(err)
	}
	summary, err = repo.Summary(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.PurchasedGamesCount != 0 {
		t.Fatalf("purchased games after unpurchased upsert = %d, want 0", summary.PurchasedGamesCount)
	}
}

// Vetted counters apply the completed-counter filters to entries whose app is vetted (profile_owned_games.vetted=1).
// Unvetted apps (vetted=0) are excluded from them; disabled entries stay excluded from every counter.
func TestSummaryVettedCompletedCounts(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	// Game 100 (unvetted): completed base and completed DLC set. Two sets, one game, none vetted.
	mustUpsertOwnedGame(t, repo, profile.ID, GameRecord{AppID: 100, Name: "Unvetted Base", Unvetted: true})
	saveSeedGame(t, repo, profile.ID, 100, "Unvetted Base", 0,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 1001, SetName: "Unvetted DLC", Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})
	// Game 200 (vetted): completed base and two completed DLC sets. Three sets, one game, all vetted.
	mustUpsertOwnedGame(t, repo, profile.ID, GameRecord{AppID: 200, Name: "Vetted Base"})
	saveSeedGame(t, repo, profile.ID, 200, "Vetted Base", 0,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 2001, SetName: "Vetted DLC", Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 2, DLCAppID: 2002, SetName: "Vetted DLC Two", Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})
	// Game 300 (unvetted): base incomplete, completed DLC set only. One set, no game.
	mustUpsertOwnedGame(t, repo, profile.ID, GameRecord{AppID: 300, Name: "Unvetted DLC Only", Unvetted: true})
	saveSeedGame(t, repo, profile.ID, 300, "Unvetted DLC Only", 0,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50},
		dashboardSeed{GroupID: 1, DLCAppID: 3001, SetName: "Unvetted Done DLC", Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})
	// Game 400 (vetted) with a disabled completed base: excluded from every counter, vetted or not.
	mustUpsertOwnedGame(t, repo, profile.ID, GameRecord{AppID: 400, Name: "Vetted Disabled"})
	saveSeedGame(t, repo, profile.ID, 400, "Vetted Disabled", 0,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true, Disabled: true})

	summary, err := repo.Summary(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Sets: game 100 (2) + game 200 (3) + game 300 DLC (1) = 6; vetted: game 200's three entries.
	if summary.CompletedSetsCount != 6 || summary.CompletedSetsVettedCount != 3 {
		t.Fatalf("completed sets = %d (vetted %d), want 6 (vetted 3)", summary.CompletedSetsCount, summary.CompletedSetsVettedCount)
	}
	if summary.CompletedGamesCount != 2 || summary.CompletedGamesVettedCount != 1 {
		t.Fatalf("completed games = %d (vetted %d), want 2 (vetted 1: game 200 only)", summary.CompletedGamesCount, summary.CompletedGamesVettedCount)
	}
	if summary.CompletedSetsVettedCount > summary.CompletedSetsCount || summary.CompletedGamesVettedCount > summary.CompletedGamesCount {
		t.Fatalf("vetted counts exceed totals: %+v", summary)
	}
}

// Without a profile_owned_games row an entry reads as vetted, so a missing row never hides a completed game.
func TestSummaryVettedCountsTreatMissingOwnedRowAsVetted(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 500, "No Owned Row", 0, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})

	summary, err := repo.Summary(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.CompletedGamesCount != 1 || summary.CompletedGamesVettedCount != 1 || summary.CompletedSetsVettedCount != 1 {
		t.Fatalf("games %d/%d sets vetted %d, want 1/1 and 1", summary.CompletedGamesCount, summary.CompletedGamesVettedCount, summary.CompletedSetsVettedCount)
	}
}

// Vetted state is per profile: the same app can be unvetted for one profile and vetted for another.
func TestSummaryVettedCountsAreProfileScoped(t *testing.T) {
	repo := testRepository(t)
	p1, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := repo.SaveProfile("22222222222222222", "User Two", "")
	if err != nil {
		t.Fatal(err)
	}
	complete := dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true}
	mustUpsertOwnedGame(t, repo, p1.ID, GameRecord{AppID: 900, Name: "Shared Game", Unvetted: true})
	mustUpsertOwnedGame(t, repo, p2.ID, GameRecord{AppID: 900, Name: "Shared Game"})
	saveSeedGame(t, repo, p1.ID, 900, "Shared Game", 0, complete)
	saveSeedGame(t, repo, p2.ID, 900, "Shared Game", 0, complete)

	s1, err := repo.Summary(p1.ID)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := repo.Summary(p2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if s1.CompletedGamesCount != 1 || s1.CompletedGamesVettedCount != 0 || s1.CompletedSetsVettedCount != 0 {
		t.Fatalf("profile 1 games %d vetted %d sets vetted %d, want 1/0/0", s1.CompletedGamesCount, s1.CompletedGamesVettedCount, s1.CompletedSetsVettedCount)
	}
	if s2.CompletedGamesCount != 1 || s2.CompletedGamesVettedCount != 1 || s2.CompletedSetsVettedCount != 1 {
		t.Fatalf("profile 2 games %d vetted %d sets vetted %d, want 1/1/1", s2.CompletedGamesCount, s2.CompletedGamesVettedCount, s2.CompletedSetsVettedCount)
	}
}

// A sync must not reset the vetted flag written by UpsertOwnedGame, and a brand-new row synced without an
// UpsertOwnedGame defaults to vetted.
func TestSaveGameSyncPreservesVettedFlag(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	complete := dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true}
	mustUpsertOwnedGame(t, repo, profile.ID, GameRecord{AppID: 610, Name: "Unvetted", Unvetted: true})
	saveSeedGame(t, repo, profile.ID, 610, "Unvetted", 5, complete)
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=? and vetted=0`, 1, profile.ID, 610)
	assertSummaryVetted(t, repo, profile.ID, 1, 0)

	// A sync for a brand-new app (no UpsertOwnedGame) defaults to vetted.
	saveSeedGame(t, repo, profile.ID, 611, "Fresh", 0, complete)
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=? and vetted=1`, 1, profile.ID, 611)
	assertSummaryVetted(t, repo, profile.ID, 2, 1)
}

// UpsertOwnedGame writes the vetted flag on insert and on conflict, per profile.
func TestUpsertOwnedGameSetsAndUpdatesVetted(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	mustUpsertOwnedGame(t, repo, profile.ID, GameRecord{AppID: 620, Name: "Game", Unvetted: true})
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=? and vetted=0`, 1, profile.ID, 620)

	mustUpsertOwnedGame(t, repo, profile.ID, GameRecord{AppID: 620, Name: "Game"})
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=? and vetted=1`, 1, profile.ID, 620)

	mustUpsertOwnedGame(t, repo, profile.ID, GameRecord{AppID: 620, Name: "Game", Unvetted: true})
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=? and vetted=0`, 1, profile.ID, 620)
}

// A sync failure for an unvetted app (SaveGameWarning upserts the owned row) persists vetted=0 and the card reads Unvetted.
func TestSaveGameWarningUnvettedPersistsVettedZero(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveGameWarning(profile.ID, GameRecord{AppID: 99, Name: "Unvetted Failure", Unvetted: true}, "Steam 503"); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from profile_owned_games where profile_id=? and appid=? and vetted=0`, 1, profile.ID, 99)

	cards, err := repo.GameCards(profile.ID, "warnings")
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || !cards[0].SyncWarning || !cards[0].Unvetted {
		t.Fatalf("warning cards = %#v, want one sync-warning card that is Unvetted", cards)
	}
}

// GameCards sets Unvetted on every entry of an unvetted app: its base game and its DLC set, not on a vetted app.
func TestGameCardsUnvettedFlagFollowsBaseApp(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	// Game 100 is unvetted: completed base (completed list) and an incomplete DLC set (not completed list).
	mustUpsertOwnedGame(t, repo, profile.ID, GameRecord{AppID: 100, Name: "Unvetted Base", Unvetted: true})
	saveSeedGame(t, repo, profile.ID, 100, "Unvetted Base", 0,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 1001, SetName: "Unvetted DLC", Total: 2, Unlocked: 1, Percent: 50})
	// Game 200 is vetted (no unvetted flag): incomplete base.
	mustUpsertOwnedGame(t, repo, profile.ID, GameRecord{AppID: 200, Name: "Vetted Base"})
	saveSeedGame(t, repo, profile.ID, 200, "Vetted Base", 0,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50})

	suggestions, err := repo.GameCards(profile.ID, "suggestions")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := repo.GameCards(profile.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range append(suggestions, completed...) {
		got[c.EntryKey] = c.Unvetted
	}
	want := map[string]bool{"100:0": true, "100:1": true, "200:0": false}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for key, wantUnvetted := range want {
		if got[key] != wantUnvetted {
			t.Fatalf("entry %s Unvetted = %v, want %v (all: %v)", key, got[key], wantUnvetted, got)
		}
	}
	if len(completed) != 1 || !completed[0].Unvetted || completed[0].EntryKey != "100:0" {
		t.Fatalf("completed cards = %#v, want the unvetted base 100:0 only", completed)
	}
}

func mustUpsertOwnedGame(t *testing.T, repo *Repository, profileID int64, g GameRecord) {
	t.Helper()
	if err := repo.UpsertOwnedGame(profileID, g); err != nil {
		t.Fatal(err)
	}
}

func assertSummaryVetted(t *testing.T, repo *Repository, profileID int64, wantGames, wantVettedGames int) {
	t.Helper()
	summary, err := repo.Summary(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.CompletedGamesCount != wantGames || summary.CompletedGamesVettedCount != wantVettedGames {
		t.Fatalf("profile %d completed games = %d (vetted %d), want %d (vetted %d)", profileID, summary.CompletedGamesCount, summary.CompletedGamesVettedCount, wantGames, wantVettedGames)
	}
}

func TestDisableOneSetLeavesSiblingsAlone(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 300,
		dashboardSeed{GroupID: 0, Total: 40, Unlocked: 40, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 4173830, SetName: "PowerWash Simulator 2 DLC Pack", Total: 10, Unlocked: 3, Percent: 30})

	if err := repo.DisableProfileGame(profile.ID, 2968420, 1); err != nil {
		t.Fatal(err)
	}
	disabled, err := repo.GameCards(profile.ID, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "disabled", disabled, []string{"2968420:1"})
	completed, err := repo.GameCards(profile.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "completed after disabling DLC set", completed, []string{"2968420:0"})
	ids, err := repo.DisabledProfileGameIDs(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ids[2968420] {
		t.Fatal("app with an enabled base entry must still sync")
	}

	if err := repo.EnableProfileGame(profile.ID, 2968420, 1); err != nil {
		t.Fatal(err)
	}
	disabled, err = repo.GameCards(profile.ID, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "disabled after enable", disabled, nil)
}

func TestDisabledProfileGameIDsRequireEveryEntryDisabled(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 300,
		dashboardSeed{GroupID: 0, Total: 40, Unlocked: 40, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 4173830, SetName: "PowerWash Simulator 2 DLC Pack", Total: 10, Unlocked: 3, Percent: 30})

	if err := repo.DisableProfileGame(profile.ID, 2968420, 1); err != nil {
		t.Fatal(err)
	}
	ids, err := repo.DisabledProfileGameIDs(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("disabled ids = %v, want none while base entry enabled", ids)
	}
	if err := repo.DisableProfileGame(profile.ID, 2968420, 0); err != nil {
		t.Fatal(err)
	}
	ids, err = repo.DisabledProfileGameIDs(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ids[2968420] || len(ids) != 1 {
		t.Fatalf("disabled ids = %v, want only 2968420 once every entry is disabled", ids)
	}
}

func TestDisabledProfileGameIDsIgnoresOrphanFlagsUntilSynced(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	// Flag written for an owned app without any profile entry must not lock the app out of sync.
	if err := repo.UpsertOwnedGame(profile.ID, GameRecord{AppID: 777, Name: "Owned Without Entries"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DisableProfileGame(profile.ID, 777, 0); err != nil {
		t.Fatal(err)
	}
	ids, err := repo.DisabledProfileGameIDs(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("disabled ids = %v, want none for orphan flag without profile entries", ids)
	}

	// Once the app has entries and every one is disabled, the app is skipped as before.
	seedGameSync(t, repo, profile.ID, 777)
	ids, err = repo.DisabledProfileGameIDs(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ids[777] || len(ids) != 1 {
		t.Fatalf("disabled ids = %v, want only 777 once its entries are disabled", ids)
	}
}

func TestDisabledProfileGameIDsKeepsNotOwnedAppWithoutEntries(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	seedGameSync(t, repo, profile.ID, 555)
	if err := repo.DisableMissingOwnedGames(profile.ID, nil); err != nil {
		t.Fatal(err)
	}
	// Entries pruned by a later sync leave the not_owned flag behind; the app stays excluded.
	if err := repo.SaveGameSync(GameSyncUpdate{ProfileID: profile.ID, AppID: 555, Name: "Gone Game", HasAchievements: false, AchievementStatus: "no_achievements", SyncedAt: time.Now().UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=555`, 0, profile.ID)
	ids, err := repo.DisabledProfileGameIDs(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ids[555] {
		t.Fatalf("disabled ids = %v, want not_owned app 555 still excluded", ids)
	}
}

func TestReturningPrunedSetKeepsHistory(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 300,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50},
		dashboardSeed{GroupID: 1, DLCAppID: 4173830, SetName: "DLC Pack", Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 2, SetName: "Extra", Total: 1, Unlocked: 1, Percent: 100, Completed: true, WasCompleted: true})
	if err := repo.MarkGamePreviouslyCompleted(profile.ID, 2968420, 1); err != nil {
		t.Fatal(err)
	}

	// Both sets are no longer eligible: entries are pruned, flags and snapshots stay.
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 300,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50})
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=2968420 and groupid in (1,2)`, 0, profile.ID)

	pruned, err := repo.ProfileGame(profile.ID, 2968420, 1)
	if err != nil {
		t.Fatal(err)
	}
	if pruned.Exists {
		t.Fatalf("pruned state = %#v, want no profile entry", pruned)
	}
	if !pruned.ManualWasCompleted || !pruned.WasCompleted {
		t.Fatalf("pruned manual set state = %#v, want manual flag and snapshot history", pruned)
	}
	extra, err := repo.ProfileGame(profile.ID, 2968420, 2)
	if err != nil {
		t.Fatal(err)
	}
	if extra.Exists || extra.ManualWasCompleted || !extra.WasCompleted {
		t.Fatalf("pruned snapshot-only set state = %#v, want was completed from snapshot only", extra)
	}
	never, err := repo.ProfileGame(profile.ID, 2968420, 7)
	if err != nil {
		t.Fatal(err)
	}
	if never != (ProfileGameState{}) {
		t.Fatalf("unknown set state = %#v, want zero state", never)
	}
}

func TestNotOwnedDisableCoversEveryGroup(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 300,
		dashboardSeed{GroupID: 0, Total: 40, Unlocked: 40, Percent: 100, Completed: true, WasCompleted: true})
	saveSeedGame(t, repo, profile.ID, 555, "Gone Game", 0,
		dashboardSeed{GroupID: 0, Total: 3, Unlocked: 1, Percent: 33},
		dashboardSeed{GroupID: 3, SetName: "Extra", Total: 2, Unlocked: 0, Percent: 0})

	if err := repo.DisableMissingOwnedGames(profile.ID, []int64{2968420}); err != nil {
		t.Fatal(err)
	}
	disabled, err := repo.GameCards(profile.ID, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "not owned disabled", disabled, []string{"555:0", "555:3"})
	for _, card := range disabled {
		if card.DisabledReason == nil || *card.DisabledReason != "not_owned" {
			t.Fatalf("card %s reason = %v, want not_owned", card.EntryKey, card.DisabledReason)
		}
	}
	ids, err := repo.DisabledProfileGameIDs(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !ids[555] || ids[2968420] {
		t.Fatalf("disabled ids = %v, want only 555", ids)
	}

	if err := repo.DisableMissingOwnedGames(profile.ID, []int64{2968420, 555}); err != nil {
		t.Fatal(err)
	}
	disabled, err = repo.GameCards(profile.ID, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "owned again", disabled, nil)
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

func TestSaveGameSyncPrunesStaleEntriesButKeepsFlagsAndSnapshots(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 300,
		dashboardSeed{GroupID: 0, Total: 40, Unlocked: 40, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 4173830, SetName: "PowerWash Simulator 2 DLC Pack", Total: 10, Unlocked: 3, Percent: 30, MissingDLC: true})

	// DLC no longer eligible (user lost the DLC): only base entry in the update. Two of the update's
	// achievement names exist already (ACH_2968420_0_0 and ACH_2968420_0_1), one is new. The DLC
	// achievements (ACH_2968420_1_*) are not in the update and must go.
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:         profile.ID,
		AppID:             2968420,
		Name:              "PowerWash Simulator 2",
		PlaytimeForever:   300,
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets: []SetSyncUpdate{{
			GroupID:              0,
			TotalAchievements:    3,
			UnlockedAchievements: 2,
			CompletionPercent:    66.7,
			IsCompleted:          false,
			WasCompleted:         true,
			Achievements: []AchievementRecord{
				{APIName: "ACH_2968420_0_0", Unlocked: false},
				{APIName: "ACH_2968420_0_1", Unlocked: true},
				{APIName: "ACH_NEW", Unlocked: true},
			},
		}},
		SyncedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=2968420`, 1, profile.ID)
	assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=? and appid=2968420 and groupid=1`, 0, profile.ID)
	assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid=2968420 and groupid=1 and missing_achievements_in_dlc=1`, 1, profile.ID)
	assertCount(t, repo, `select count(*) from game_snapshots where profile_id=? and appid=2968420 and groupid=1`, 1, profile.ID)
	// Exactly the three update names remain for the profile; overlapping rows are upserted, not recreated.
	assertCount(t, repo, `select count(*) from profile_achievements where profile_id=? and appid=2968420`, 3, profile.ID)
	assertCount(t, repo, `select count(*) from profile_achievements where profile_id=? and appid=2968420 and apiname='ACH_2968420_0_0' and unlocked=0`, 1, profile.ID)
	assertCount(t, repo, `select count(*) from profile_achievements where profile_id=? and appid=2968420 and apiname='ACH_2968420_0_1' and unlocked=1`, 1, profile.ID)
	assertCount(t, repo, `select count(*) from profile_achievements where profile_id=? and appid=2968420 and apiname='ACH_NEW' and unlocked=1`, 1, profile.ID)
	assertCount(t, repo, `select count(*) from profile_achievements where profile_id=? and appid=2968420 and apiname glob 'ACH_2968420_1_*'`, 0, profile.ID)
	// Shared achievement catalogue: only the three update names remain for the app.
	assertCount(t, repo, `select count(*) from achievements where appid=2968420`, 3)
	assertCount(t, repo, `select count(*) from achievements where appid=2968420 and apiname glob 'ACH_2968420_1_*'`, 0)
}

func TestSaveGameSyncStoresArchivedAchievementsPerGroup(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:         profile.ID,
		AppID:             440,
		Name:              "Team Fortress 2",
		PlaytimeForever:   10,
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets: []SetSyncUpdate{
			{GroupID: 0, TotalAchievements: 1, Achievements: []AchievementRecord{{APIName: "BASE_A", Unlocked: false}}},
			{GroupID: 11, Name: "Replay", TotalAchievements: 4, UnlockedAchievements: 4, CompletionPercent: 100, IsCompleted: true, Achievements: []AchievementRecord{
				{APIName: "REPLAY_ARCH", Archived: true, Unlocked: true},
				{APIName: "REPLAY_LIVE_1", Unlocked: true},
				{APIName: "REPLAY_LIVE_2", Unlocked: true},
				{APIName: "REPLAY_LIVE_3", Unlocked: true},
			}},
		},
		SyncedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from achievements where appid=440 and apiname='REPLAY_ARCH' and groupid=11 and archived=1`, 1)
	assertCount(t, repo, `select count(*) from achievements where appid=440 and apiname='BASE_A' and groupid=0 and archived=0`, 1)
	assertCount(t, repo, `select count(*) from game_sets where appid=440 and groupid=11 and name='Replay'`, 1)

	cards, err := repo.GameCards(profile.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "completed", cards, []string{"440:11"})
	if cards[0].Name != "Team Fortress 2 – Replay" || cards[0].TotalAchievements != 4 {
		t.Fatalf("replay card = %#v, want 'Team Fortress 2 – Replay' with 4 countable achievements", cards[0])
	}
}

func TestPortalStyleGameWithoutBaseEntryHasOnlyNamedSets(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	// Group 0 has no achievements in Steam's data: it must not produce an entry.
	saveSeedGame(t, repo, profile.ID, 620, "Portal 2", 0,
		dashboardSeed{GroupID: 1, SetName: "Cooperative", Total: 22, Unlocked: 22, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 2, SetName: "Single Player", Total: 29, Unlocked: 10, Percent: 34})

	cards, err := repo.GameCards(profile.ID, "suggestions")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "suggestions", cards, []string{"620:2"})
	completed, err := repo.GameCards(profile.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "completed", completed, []string{"620:1"})
	if completed[0].Name != "Portal 2 – Cooperative" {
		t.Fatalf("name = %q, want 'Portal 2 – Cooperative'", completed[0].Name)
	}
}

func TestSaveCoverRecomputesMissingCoverTagForEntryImage(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	saveSeedGame(t, repo, profile.ID, 2968420, "PowerWash Simulator 2", 300,
		dashboardSeed{GroupID: 0, Total: 2, Unlocked: 1, Percent: 50},
		dashboardSeed{GroupID: 1, DLCAppID: 4173830, SetName: "PowerWash Simulator 2 DLC Pack", Total: 2, Unlocked: 1, Percent: 50})
	assertTag(t, repo, profile.ID, 2968420, 1, "missing_cover_art", true)

	mustSaveHeroCover(t, repo, 4173830)
	assertTag(t, repo, profile.ID, 2968420, 1, "missing_cover_art", false)
	assertTag(t, repo, profile.ID, 2968420, 0, "missing_cover_art", true)
}

func TestCoverRoundTripKeepsDownloadTimeWhenNotDownloaded(t *testing.T) {
	repo := testRepository(t)
	if err := repo.SaveCover(CoverUpdate{ImageAppID: 42, Kind: "library", FileName: "library_600x900.jpg", Path: "/cache/42/library_600x900.jpg", SourceURL: "https://example.com/42.jpg", Downloaded: true}); err != nil {
		t.Fatal(err)
	}
	first, ok, err := repo.GetCover(42)
	if err != nil || !ok {
		t.Fatalf("GetCover() ok=%v err=%v, want stored cover", ok, err)
	}
	if first.Kind != "library" || first.FileName != "library_600x900.jpg" || first.Path == "" || first.SourceURL == "" || first.CheckedAt == nil || first.DownloadedAt == nil {
		t.Fatalf("cover = %#v, want library cover with timestamps", first)
	}

	// Seed an old download time so a preserved value cannot be confused with a same-second rewrite.
	const oldDownload = "2020-01-02T03:04:05Z"
	if _, err := repo.db.Exec(`update cover_images set downloaded_at=?, checked_at=? where image_appid=42`, oldDownload, oldDownload); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveCover(CoverUpdate{ImageAppID: 42, Kind: "library", FileName: "library_600x900.jpg", Path: "/cache/42/library_600x900.jpg", SourceURL: "https://example.com/42.jpg", Downloaded: false}); err != nil {
		t.Fatal(err)
	}
	second, _, err := repo.GetCover(42)
	if err != nil {
		t.Fatal(err)
	}
	if second.DownloadedAt == nil || *second.DownloadedAt != oldDownload {
		t.Fatalf("downloaded_at = %v, want preserved %q", second.DownloadedAt, oldDownload)
	}
	if second.CheckedAt == nil || *second.CheckedAt == oldDownload {
		t.Fatalf("checked_at = %v, want refreshed by the non-download save", second.CheckedAt)
	}

	// A real download replaces the stored time.
	if err := repo.SaveCover(CoverUpdate{ImageAppID: 42, Kind: "library", FileName: "library_600x900.jpg", Path: "/cache/42/library_600x900.jpg", SourceURL: "https://example.com/42.jpg", Downloaded: true}); err != nil {
		t.Fatal(err)
	}
	downloaded, _, err := repo.GetCover(42)
	if err != nil {
		t.Fatal(err)
	}
	if downloaded.DownloadedAt == nil || *downloaded.DownloadedAt == oldDownload {
		t.Fatalf("downloaded_at = %v, want refreshed by a download", downloaded.DownloadedAt)
	}

	if _, ok, err := repo.GetCover(999); err != nil || ok {
		t.Fatalf("GetCover(999) ok=%v err=%v, want no row", ok, err)
	}
	covers, err := repo.ListCovers()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := covers[42]; !ok || len(covers) != 1 {
		t.Fatalf("ListCovers() = %#v, want only image app 42", covers)
	}
}

func TestGameCardCoverURLCacheBusts(t *testing.T) {
	repo := testRepository(t)
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	seedGameSync(t, repo, profile.ID, 42)
	if err := repo.SaveCover(CoverUpdate{ImageAppID: 42, Kind: "library", FileName: "library_600x900.jpg", Path: "/tmp/cache/images/games/42/library_600x900.jpg", SourceURL: "https://example.com/cover.jpg", Downloaded: true}); err != nil {
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
		ProfileID:         profile.ID,
		AppID:             11,
		Name:              "Untouched Game",
		PlaytimeForever:   0,
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets: []SetSyncUpdate{{
			GroupID:              0,
			TotalAchievements:    10,
			UnlockedAchievements: 0,
			CompletionPercent:    0,
			IsCompleted:          false,
			Achievements:         []AchievementRecord{{APIName: "ACH", Unlocked: false}},
		}},
		SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 11, 0, "untouched", true)
	assertTag(t, repo, profile.ID, 11, 0, "in_progress", false)
	assertTag(t, repo, profile.ID, 11, 0, "almost_there", false)

	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:         profile.ID,
		AppID:             12,
		Name:              "Started Game",
		PlaytimeForever:   120,
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets: []SetSyncUpdate{{
			GroupID:              0,
			TotalAchievements:    10,
			UnlockedAchievements: 0,
			CompletionPercent:    0,
			IsCompleted:          false,
			Achievements:         []AchievementRecord{{APIName: "ACH", Unlocked: false}},
		}},
		SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 12, 0, "untouched", false)
	assertTag(t, repo, profile.ID, 12, 0, "in_progress", true)
	assertTag(t, repo, profile.ID, 12, 0, "almost_there", false)

	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:         profile.ID,
		AppID:             13,
		Name:              "No Achievements Untouched Game",
		PlaytimeForever:   0,
		HasAchievements:   false,
		AchievementStatus: "no_achievements",
		Sets:              []SetSyncUpdate{{GroupID: 0}},
		SyncedAt:          now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 13, 0, "no_achievements", true)
	assertTag(t, repo, profile.ID, 13, 0, "untouched", true)
	assertTag(t, repo, profile.ID, 13, 0, "in_progress", false)
	assertTag(t, repo, profile.ID, 13, 0, "almost_there", false)

	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:         profile.ID,
		AppID:             14,
		Name:              "No Achievements Played Game",
		PlaytimeForever:   120,
		HasAchievements:   false,
		AchievementStatus: "no_achievements",
		Sets:              []SetSyncUpdate{{GroupID: 0}},
		SyncedAt:          now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 14, 0, "no_achievements", true)
	assertTag(t, repo, profile.ID, 14, 0, "untouched", false)
	assertTag(t, repo, profile.ID, 14, 0, "in_progress", false)
	assertTag(t, repo, profile.ID, 14, 0, "almost_there", false)
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
		ProfileID:         profile.ID,
		AppID:             13,
		Name:              "Suggestion Driven Game",
		PlaytimeForever:   60,
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets: []SetSyncUpdate{{
			GroupID:              0,
			TotalAchievements:    100,
			UnlockedAchievements: 1,
			CompletionPercent:    1,
			MissingAvgUnlock:     &missingAvg,
			SuggestionScore:      &suggestionScore,
			IsCompleted:          false,
			Achievements:         []AchievementRecord{{APIName: "ACH", Unlocked: false}},
		}},
		SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 13, 0, "almost_there", true)
	assertTag(t, repo, profile.ID, 13, 0, "in_progress", false)
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
		ProfileID:         profile.ID,
		AppID:             14,
		Name:              "Almost But Not Quite",
		PlaytimeForever:   60,
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets: []SetSyncUpdate{{
			GroupID:              0,
			TotalAchievements:    100,
			UnlockedAchievements: 1,
			CompletionPercent:    1,
			SuggestionScore:      &suggestionScore,
			IsCompleted:          false,
			Achievements:         []AchievementRecord{{APIName: "ACH", Unlocked: false}},
		}},
		SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	assertTag(t, repo, profile.ID, 14, 0, "almost_there", false)
	assertTag(t, repo, profile.ID, 14, 0, "in_progress", true)
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

// seedGameSync saves a single completed base entry (group 0) for the game.
func seedGameSync(t *testing.T, repo *Repository, profileID, appID int64) {
	t.Helper()
	global := 42.0
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:         profileID,
		AppID:             appID,
		Name:              "Game",
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets: []SetSyncUpdate{{
			GroupID:              0,
			TotalAchievements:    1,
			UnlockedAchievements: 1,
			CompletionPercent:    100,
			IsCompleted:          true,
			WasCompleted:         true,
			Achievements:         []AchievementRecord{{APIName: "ACH", GlobalPercent: &global, Unlocked: true}},
		}},
		SyncedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

type dashboardSeed struct {
	GroupID         int64
	DLCAppID        int64
	SetName         string
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
	DLCNotOwned     bool
	SyncWarning     bool
	Disabled        bool
	Playtime        int
}

// seedDashboardGame saves one single-entry game described by seed.
func seedDashboardGame(t *testing.T, repo *Repository, profileID int64, seed dashboardSeed) {
	t.Helper()
	seed.GroupID = 0
	saveSeedGame(t, repo, profileID, seed.AppID, seed.Name, int64(seed.Playtime), seed)
}

// saveSeedGame syncs one game with every seed as an entry (seed.GroupID selects the entry) and applies the seed flags.
func saveSeedGame(t *testing.T, repo *Repository, profileID, appID int64, gameName string, playtime int64, seeds ...dashboardSeed) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	missingAvg := 42.0
	hasAchievements := false
	var sets []SetSyncUpdate
	for _, seed := range seeds {
		if seed.Total > 0 {
			hasAchievements = true
		}
		score := seed.Score
		var achievements []AchievementRecord
		for i := 0; i < seed.Total; i++ {
			achievements = append(achievements, AchievementRecord{
				APIName:       fmt.Sprintf("ACH_%d_%d_%d", appID, seed.GroupID, i),
				GlobalPercent: &missingAvg,
				Unlocked:      i < seed.Unlocked,
			})
		}
		var lastError *string
		if seed.SyncWarning {
			lastError = StringPtr("temporary Steam failure")
		}
		sets = append(sets, SetSyncUpdate{
			GroupID:              seed.GroupID,
			Name:                 seed.SetName,
			DLCAppID:             seed.DLCAppID,
			DLCNotOwned:          seed.DLCNotOwned,
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
		})
	}
	status := "ok"
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:         profileID,
		AppID:             appID,
		Name:              gameName,
		PlaytimeForever:   int(playtime),
		HasAchievements:   hasAchievements,
		AchievementStatus: status,
		Sets:              sets,
		SyncedAt:          now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, seed := range seeds {
		if seed.MissingDLC {
			if err := repo.ToggleMissingAchievementsInDLC(profileID, appID, seed.GroupID); err != nil {
				t.Fatal(err)
			}
		}
		if seed.Disabled {
			if err := repo.DisableProfileGame(profileID, appID, seed.GroupID); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func mustSaveHeroCover(t *testing.T, repo *Repository, imageAppID int64) {
	t.Helper()
	if err := repo.SaveCover(CoverUpdate{ImageAppID: imageAppID, Kind: "hero", FileName: "hero_capsule.jpg", Path: fmt.Sprintf("/cache/images/games/%d/hero_capsule.jpg", imageAppID), SourceURL: "https://example.com/hero.jpg", Downloaded: true}); err != nil {
		t.Fatal(err)
	}
}

func mustSaveLibraryCover(t *testing.T, repo *Repository, imageAppID int64) {
	t.Helper()
	if err := repo.SaveCover(CoverUpdate{ImageAppID: imageAppID, Kind: "library", FileName: "library_600x900.jpg", Path: fmt.Sprintf("/cache/images/games/%d/library_600x900.jpg", imageAppID), SourceURL: "https://example.com/library.jpg", Downloaded: true}); err != nil {
		t.Fatal(err)
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

func assertEntryKeys(t *testing.T, label string, cards []GameCard, want []string) {
	t.Helper()
	got := make([]string, len(cards))
	for i, card := range cards {
		got[i] = card.EntryKey
	}
	if len(got) != len(want) {
		t.Fatalf("%s entry keys = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s entry keys = %v, want %v", label, got, want)
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

func assertTag(t *testing.T, repo *Repository, profileID, appID, groupID int64, tag string, want bool) {
	t.Helper()
	var count int
	if err := repo.db.QueryRow(`select count(*) from profile_game_tags where profile_id=? and appid=? and groupid=? and tag=?`, profileID, appID, groupID, tag).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if got := count > 0; got != want {
		t.Fatalf("tag %q for %d:%d exists = %v, want %v", tag, appID, groupID, got, want)
	}
}
