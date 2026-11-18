package repository

import (
	"database/sql"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"
)

// seedDashboardFixture covers each card class the four dashboard lists split on. Profile one owns the entries
// asserted in TestDashboardFixtureListsAndFlags. Profile two shares app 100 and adds app 900, so any leak between
// profiles would show up in profile one's lists.
func seedDashboardFixture(t *testing.T, repo *Repository, profileID, otherProfileID int64) {
	t.Helper()
	// Completed base entry.
	saveSeedGame(t, repo, profileID, 100, "Alpha Completed", 60, dashboardSeed{Total: 10, Unlocked: 10, Percent: 100, Completed: true, WasCompleted: true})
	// Incomplete, no flags.
	saveSeedGame(t, repo, profileID, 110, "Bravo Incomplete", 60, dashboardSeed{Total: 10, Unlocked: 4, Percent: 40, Score: 50})
	// Incomplete with sync warning: suggestions and warnings.
	saveSeedGame(t, repo, profileID, 120, "Charlie Incomplete Warning", 60, dashboardSeed{Total: 5, Unlocked: 1, Percent: 20, Score: 15, SyncWarning: true})
	// Completed with sync warning: completed and warnings.
	saveSeedGame(t, repo, profileID, 130, "Delta Completed Warning", 60, dashboardSeed{Total: 10, Unlocked: 10, Percent: 100, Completed: true, WasCompleted: true, SyncWarning: true})
	// Disabled with sync warning: disabled only.
	saveSeedGame(t, repo, profileID, 140, "Echo Disabled Warning", 60, dashboardSeed{Total: 10, Unlocked: 2, Percent: 20, Score: 25, SyncWarning: true, Disabled: true})
	// Disabled and completed: disabled only.
	saveSeedGame(t, repo, profileID, 150, "Foxtrot Disabled Completed", 60, dashboardSeed{Total: 10, Unlocked: 10, Percent: 100, Completed: true, WasCompleted: true, Disabled: true})
	// Completed base with an incomplete DLC set (160:1): the set is a suggestion, the base is completed.
	saveSeedGame(t, repo, profileID, 160, "Golf DLC Set", 60,
		dashboardSeed{GroupID: 0, Total: 10, Unlocked: 10, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, DLCAppID: 9160, SetName: "Golf DLC Pack", Total: 5, Unlocked: 1, Percent: 20, Score: 30})
	// Incomplete base plus a DLC set flagged missing achievements (170:1): the set sorts in the DLC-flagged group.
	saveSeedGame(t, repo, profileID, 170, "Hotel DLC Flagged", 60,
		dashboardSeed{GroupID: 0, Total: 10, Unlocked: 3, Percent: 30, Score: 20},
		dashboardSeed{GroupID: 1, DLCAppID: 9170, SetName: "Hotel Extra", Total: 4, Unlocked: 0, Percent: 0, Score: 5, MissingDLC: true})
	// DLC set the profile does not own (180:1): the not-owned group.
	saveSeedGame(t, repo, profileID, 180, "India DLC Game", 0,
		dashboardSeed{GroupID: 1, DLCAppID: 9180, SetName: "Season Pass", Total: 6, Unlocked: 0, Percent: 0, DLCNotOwned: true})
	// Newly incomplete entries: sorted first, fewer missing achievements first.
	saveSeedGame(t, repo, profileID, 190, "Juliet Newly Incomplete", 60, dashboardSeed{Total: 10, Unlocked: 7, Percent: 70, Score: 60, NewlyIncomplete: true})
	saveSeedGame(t, repo, profileID, 200, "Kilo Newly Incomplete", 60, dashboardSeed{Total: 10, Unlocked: 8, Percent: 80, Score: 10, NewlyIncomplete: true})
	// No achievements: the no-achievements group, by name.
	saveSeedGame(t, repo, profileID, 205, "Aardvark No Achievements", 0, dashboardSeed{Total: 0})
	saveSeedGame(t, repo, profileID, 210, "Lima No Achievements", 0, dashboardSeed{Total: 0})
	// Unvetted apps: one incomplete, one completed.
	mustUpsertOwnedGame(t, repo, profileID, GameRecord{AppID: 230, Name: "Oscar Unvetted", PlaytimeForever: 60, Unvetted: true})
	saveSeedGame(t, repo, profileID, 230, "Oscar Unvetted", 60, dashboardSeed{Total: 10, Unlocked: 5, Percent: 50, Score: 40})
	mustUpsertOwnedGame(t, repo, profileID, GameRecord{AppID: 240, Name: "Papa Unvetted Completed", PlaytimeForever: 60, Unvetted: true})
	saveSeedGame(t, repo, profileID, 240, "Papa Unvetted Completed", 60, dashboardSeed{Total: 10, Unlocked: 10, Percent: 100, Completed: true, WasCompleted: true})

	// Other profile: shares app 100 (incomplete for it) and owns app 900 (warning, incomplete).
	saveSeedGame(t, repo, otherProfileID, 100, "Alpha Completed", 60, dashboardSeed{Total: 10, Unlocked: 5, Percent: 50, Score: 80})
	saveSeedGame(t, repo, otherProfileID, 900, "Zulu Other Profile", 60, dashboardSeed{Total: 10, Unlocked: 3, Percent: 30, Score: 70, SyncWarning: true})
}

// Expected entry keys per dashboard list for the fixture above (profile one). Lists are in display order.
var (
	fixtureSuggestionKeys = []string{"200:0", "190:0", "110:0", "230:0", "160:1", "170:0", "120:0", "180:1", "170:1", "205:0", "210:0"}
	fixtureCompletedKeys  = []string{"100:0", "130:0", "160:0", "240:0"}
	fixtureWarningKeys    = []string{"120:0", "130:0"}
	fixtureDisabledKeys   = []string{"140:0", "150:0"}
)

func TestDashboardFixtureListsAndFlags(t *testing.T) {
	repo := testRepository(t)
	p1 := saveTestProfile(t, repo, "11111111111111111", "User One")
	p2 := saveTestProfile(t, repo, "22222222222222222", "User Two")
	p3 := saveTestProfile(t, repo, "33333333333333333", "User Three")
	seedDashboardFixture(t, repo, p1.ID, p2.ID)

	dashboard, err := repo.Dashboard(p1.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "suggestions", dashboard.Suggestions, fixtureSuggestionKeys)
	assertEntryKeys(t, "completed", dashboard.Completed, fixtureCompletedKeys)
	assertEntryKeys(t, "warnings", dashboard.Warnings, fixtureWarningKeys)
	assertEntryKeys(t, "disabled", dashboard.Disabled, fixtureDisabledKeys)

	// The exported GameCards must agree with the Dashboard lists for the same profile.
	for filter, want := range map[string][]string{
		"suggestions": fixtureSuggestionKeys,
		"completed":   fixtureCompletedKeys,
		"warnings":    fixtureWarningKeys,
		"disabled":    fixtureDisabledKeys,
	} {
		cards, err := repo.GameCards(p1.ID, filter)
		if err != nil {
			t.Fatal(err)
		}
		assertEntryKeys(t, "GameCards "+filter, cards, want)
	}
	unknown, err := repo.GameCards(p1.ID, "no-such-filter")
	if err != nil {
		t.Fatal(err)
	}
	assertEntryKeys(t, "GameCards unknown filter", unknown, fixtureSuggestionKeys)

	// Card flags the lists depend on.
	if c := cardByEntryKey(t, dashboard.Suggestions, "180:1"); !c.DLCNotOwned || !c.IsSet || c.DLCAppID != 9180 || c.SetName != "Season Pass" {
		t.Fatalf("not-owned DLC card = %#v, want DLCNotOwned set of 9180", c)
	}
	if c := cardByEntryKey(t, dashboard.Suggestions, "160:1"); c.DLCAppID != 9160 || c.IsCompleted || c.Unvetted {
		t.Fatalf("DLC set card = %#v, want incomplete vetted set of 9160", c)
	}
	if c := cardByEntryKey(t, dashboard.Suggestions, "170:1"); !c.MissingDLC || c.DLCNotOwned {
		t.Fatalf("missing-DLC card = %#v, want MissingDLC", c)
	}
	if c := cardByEntryKey(t, dashboard.Suggestions, "190:0"); !c.NewlyIncomplete {
		t.Fatalf("newly incomplete card = %#v, want NewlyIncomplete", c)
	}
	if c := cardByEntryKey(t, dashboard.Suggestions, "120:0"); !c.SyncWarning {
		t.Fatalf("warning card = %#v, want SyncWarning in suggestions", c)
	}
	if c := cardByEntryKey(t, dashboard.Suggestions, "230:0"); !c.Unvetted {
		t.Fatalf("unvetted card = %#v, want Unvetted", c)
	}
	if c := cardByEntryKey(t, dashboard.Completed, "240:0"); !c.Unvetted || !c.IsCompleted {
		t.Fatalf("unvetted completed card = %#v, want Unvetted and completed", c)
	}
	if c := cardByEntryKey(t, dashboard.Disabled, "140:0"); !c.Disabled || !c.ManualDisabled || !c.SyncWarning {
		t.Fatalf("disabled warning card = %#v, want manual disabled with sync warning", c)
	}
	if c := cardByEntryKey(t, dashboard.Disabled, "150:0"); !c.Disabled || !c.IsCompleted {
		t.Fatalf("disabled completed card = %#v, want disabled and completed", c)
	}
	if c := cardByEntryKey(t, dashboard.Completed, "130:0"); !c.SyncWarning || !c.IsCompleted {
		t.Fatalf("completed warning card = %#v, want completed with sync warning", c)
	}

	// Empty profile: every list is nil (marshals to JSON null, as before).
	empty, err := repo.Dashboard(p3.ID)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Suggestions != nil || empty.Completed != nil || empty.Warnings != nil || empty.Disabled != nil {
		t.Fatalf("empty profile lists = %v %v %v %v, want all nil", empty.Suggestions, empty.Completed, empty.Warnings, empty.Disabled)
	}
	for _, filter := range []string{"suggestions", "completed", "warnings", "disabled"} {
		cards, err := repo.GameCards(p3.ID, filter)
		if err != nil {
			t.Fatal(err)
		}
		if cards != nil {
			t.Fatalf("empty profile GameCards(%q) = %v, want nil", filter, cards)
		}
	}
}

// TestDashboardListsMatchGameCards pins each Dashboard list to GameCards for the same profile and filter. Both share
// loadGameCards and filterSorted, so this only guards the wiring (swapped filter names); ordering is pinned by the
// fixture test.
func TestDashboardListsMatchGameCards(t *testing.T) {
	repo := testRepository(t)
	p1 := saveTestProfile(t, repo, "11111111111111111", "User One")
	p2 := saveTestProfile(t, repo, "22222222222222222", "User Two")
	p3 := saveTestProfile(t, repo, "33333333333333333", "User Three")
	seedDashboardFixture(t, repo, p1.ID, p2.ID)

	for _, profileID := range []int64{p1.ID, p2.ID, p3.ID} {
		dashboard, err := repo.Dashboard(profileID)
		if err != nil {
			t.Fatal(err)
		}
		for filter, got := range map[string][]GameCard{
			"suggestions": dashboard.Suggestions,
			"completed":   dashboard.Completed,
			"warnings":    dashboard.Warnings,
			"disabled":    dashboard.Disabled,
		} {
			want, err := repo.GameCards(profileID, filter)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("profile %d %s: Dashboard list = %#v, GameCards = %#v", profileID, filter, got, want)
			}
		}
	}
}

// renameTableThroughSecondConnection renames a table through a second connection, the same way app_test.go breaks
// a read. The repository's own connection then fails on that table only.
func renameTableThroughSecondConnection(t *testing.T, path, from, to string) {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+path+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`ALTER TABLE ` + from + ` RENAME TO ` + to); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardPropagatesTagsQueryError(t *testing.T) {
	repo, path := testRepositoryPath(t)
	profile := saveTestProfile(t, repo, "11111111111111111", "User One")
	other := saveTestProfile(t, repo, "22222222222222222", "User Two")
	seedDashboardFixture(t, repo, profile.ID, other.ID)
	renameTableThroughSecondConnection(t, path, "profile_game_tags", "profile_game_tags_unreadable")
	if _, err := repo.Summary(profile.ID); err != nil {
		t.Fatalf("Summary must not depend on profile_game_tags: %v", err)
	}

	if _, err := repo.Dashboard(profile.ID); err == nil || !strings.Contains(err.Error(), "profile_game_tags") {
		t.Fatalf("Dashboard err = %v, want the unreadable profile_game_tags error", err)
	}
	if _, err := repo.GameCards(profile.ID, "suggestions"); err == nil || !strings.Contains(err.Error(), "profile_game_tags") {
		t.Fatalf("GameCards err = %v, want the unreadable profile_game_tags error", err)
	}
}

func TestDashboardPropagatesCardsQueryError(t *testing.T) {
	repo, path := testRepositoryPath(t)
	profile := saveTestProfile(t, repo, "11111111111111111", "User One")
	other := saveTestProfile(t, repo, "22222222222222222", "User Two")
	seedDashboardFixture(t, repo, profile.ID, other.ID)
	// Summary and the tag read do not touch cover_images, so only the cards query fails.
	renameTableThroughSecondConnection(t, path, "cover_images", "cover_images_unreadable")
	if _, err := repo.Summary(profile.ID); err != nil {
		t.Fatalf("Summary must not depend on cover_images: %v", err)
	}

	if _, err := repo.Dashboard(profile.ID); err == nil || !strings.Contains(err.Error(), "cover_images") {
		t.Fatalf("Dashboard err = %v, want the unreadable cover_images error", err)
	}
	if _, err := repo.GameCards(profile.ID, "completed"); err == nil || !strings.Contains(err.Error(), "cover_images") {
		t.Fatalf("GameCards err = %v, want the unreadable cover_images error", err)
	}
}

// Benchmark dataset: this many base entries, every second app also has a DLC set, every fifth app an extra set.
// Shape is fixed by the seed so before/after runs compare the same rows.
const benchmarkDashboardApps = 250

var sinkDashboard Dashboard

func BenchmarkDashboard(b *testing.B) {
	repo, _ := testRepositoryPath(b)
	profile, err := repo.SaveProfile("11111111111111111", "Bench User", "")
	if err != nil {
		b.Fatal(err)
	}
	seedSyntheticDashboard(b, repo, profile.ID, benchmarkDashboardApps)
	var entries int
	if err := repo.db.QueryRow(`select count(*) from profile_games where profile_id=?`, profile.ID).Scan(&entries); err != nil {
		b.Fatal(err)
	}
	b.Logf("synthetic entries = %d", entries)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dashboard, err := repo.Dashboard(profile.ID)
		if err != nil {
			b.Fatal(err)
		}
		sinkDashboard = dashboard
	}
}

// seedSyntheticDashboard writes `apps` synthetic games for one profile with tags, flags, covers, unvetted apps and
// DLC sets. Values come from a fixed-seed generator; no real Steam data.
func seedSyntheticDashboard(tb testing.TB, repo *Repository, profileID int64, apps int) {
	tb.Helper()
	rng := rand.New(rand.NewSource(42))
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range apps {
		appID := int64(1000 + i)
		playtime := 0
		if rng.Intn(4) != 0 {
			playtime = 1 + rng.Intn(600)
		}
		if i%7 == 3 {
			if err := repo.UpsertOwnedGame(profileID, GameRecord{AppID: appID, Name: fmt.Sprintf("Synthetic Game %03d", i), PlaytimeForever: playtime, Unvetted: true}); err != nil {
				tb.Fatal(err)
			}
		}
		sets := []SetSyncUpdate{syntheticSet(rng, appID, 0, "", 0)}
		if i%2 == 0 {
			sets = append(sets, syntheticSet(rng, appID, 1, "DLC Pack", 5000+appID))
		}
		if i%5 == 0 {
			sets = append(sets, syntheticSet(rng, appID, 2, "Expansion", 0))
		}
		if err := repo.SaveGameSync(GameSyncUpdate{
			ProfileID:         profileID,
			AppID:             appID,
			Name:              fmt.Sprintf("Synthetic Game %03d", i),
			PlaytimeForever:   playtime,
			HasAchievements:   true,
			AchievementStatus: "ok",
			Sets:              sets,
			SyncedAt:          now,
		}); err != nil {
			tb.Fatal(err)
		}
		for _, set := range sets {
			if rng.Intn(20) == 0 {
				if err := repo.ToggleMissingAchievementsInDLC(profileID, appID, set.GroupID); err != nil {
					tb.Fatal(err)
				}
			}
			if rng.Intn(25) == 0 {
				if err := repo.DisableProfileGame(profileID, appID, set.GroupID); err != nil {
					tb.Fatal(err)
				}
			}
		}
		if rng.Intn(5) != 0 {
			if err := repo.SaveCover(CoverUpdate{ImageAppID: appID, Kind: "library", FileName: "library_600x900.jpg", Cached: true, SourceURL: "https://example.com/library.jpg", Downloaded: true}); err != nil {
				tb.Fatal(err)
			}
		}
		for _, set := range sets {
			if set.DLCAppID != 0 && rng.Intn(2) == 0 {
				if err := repo.SaveCover(CoverUpdate{ImageAppID: set.DLCAppID, Kind: "hero", FileName: "hero_capsule.jpg", Cached: true, SourceURL: "https://example.com/hero.jpg", Downloaded: true}); err != nil {
					tb.Fatal(err)
				}
			}
		}
	}
}

func syntheticSet(rng *rand.Rand, appID, groupID int64, name string, dlcAppID int64) SetSyncUpdate {
	total := 5 + rng.Intn(36)
	unlocked := rng.Intn(total + 1)
	completed := unlocked == total
	missing := 1 + rng.Float64()*50
	percent := float64(unlocked) / float64(total) * 100
	score := percent*0.5 + missing*0.5
	var lastError *string
	warning := rng.Intn(20) == 0
	if warning {
		lastError = new("temporary Steam failure")
	}
	dlcNotOwned := dlcAppID != 0 && rng.Intn(10) == 0
	achievements := make([]AchievementRecord, 0, total)
	for j := range total {
		gp := missing
		achievements = append(achievements, AchievementRecord{APIName: fmt.Sprintf("ACH_%d_%d_%d", appID, groupID, j), GlobalPercent: &gp, Unlocked: j < unlocked})
	}
	return SetSyncUpdate{
		GroupID:              groupID,
		Name:                 name,
		DLCAppID:             dlcAppID,
		DLCNotOwned:          dlcNotOwned,
		Order:                int(groupID),
		TotalAchievements:    total,
		UnlockedAchievements: unlocked,
		CompletionPercent:    percent,
		MissingAvgUnlock:     &missing,
		SuggestionScore:      &score,
		IsCompleted:          completed,
		WasCompleted:         completed,
		NewlyIncomplete:      !completed && rng.Intn(30) == 0,
		SyncWarning:          warning,
		LastError:            lastError,
		Achievements:         achievements,
	}
}
