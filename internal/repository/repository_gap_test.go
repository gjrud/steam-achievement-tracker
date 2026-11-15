package repository

import (
	"database/sql"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"
)

func saveTestProfile(t *testing.T, repo *Repository, steamID, name string) *Profile {
	t.Helper()
	profile, err := repo.SaveProfile(steamID, name, "")
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func timestampNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func ptrFloat(v float64) *float64 { return &v }

func assertStrings(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", label, got, want)
		}
	}
}

// allCards returns every card of the profile across the four dashboard filters.
func allCards(t *testing.T, repo *Repository, profileID int64) []GameCard {
	t.Helper()
	var cards []GameCard
	for _, filter := range []string{"suggestions", "completed", "warnings", "disabled"} {
		got, err := repo.GameCards(profileID, filter)
		if err != nil {
			t.Fatal(err)
		}
		cards = append(cards, got...)
	}
	return cards
}

func scanOne[T any](t *testing.T, repo *Repository, query string, args ...any) T {
	t.Helper()
	var v T
	if err := repo.db.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// syncGame writes one game sync for the profile with exactly the given entries.
func syncGame(t *testing.T, repo *Repository, profileID, appID int64, sets ...SetSyncUpdate) {
	t.Helper()
	if err := repo.SaveGameSync(GameSyncUpdate{
		ProfileID:         profileID,
		AppID:             appID,
		Name:              "Synced Game",
		PlaytimeForever:   60,
		HasAchievements:   true,
		AchievementStatus: "ok",
		Sets:              sets,
		SyncedAt:          timestampNow(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSaveGameSyncWithoutAchievementsDeletesOnlyUnsharedRows(t *testing.T) {
	repo := testRepository(t)
	p1 := saveTestProfile(t, repo, "11111111111111111", "User One")
	p2 := saveTestProfile(t, repo, "22222222222222222", "User Two")
	// Both profiles share ACH_700_0_0 (same generated name); only p1 also has ACH_700_0_1.
	saveSeedGame(t, repo, p1.ID, 700, "Shared Game", 0, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})
	saveSeedGame(t, repo, p2.ID, 700, "Shared Game", 0, dashboardSeed{GroupID: 0, Total: 1, Unlocked: 0, Percent: 0})
	assertCount(t, repo, `select count(*) from achievements where appid=700`, 2)

	// p1 syncs with no achievements at all: the empty-achievements branch clears p1 rows only.
	syncGame(t, repo, p1.ID, 700, SetSyncUpdate{GroupID: 0})
	assertCount(t, repo, `select count(*) from profile_achievements where profile_id=? and appid=700`, 0, p1.ID)
	assertCount(t, repo, `select count(*) from profile_achievements where profile_id=? and appid=700`, 1, p2.ID)
	assertCount(t, repo, `select count(*) from achievements where appid=700 and apiname='ACH_700_0_0'`, 1)
	assertCount(t, repo, `select count(*) from achievements where appid=700 and apiname='ACH_700_0_1'`, 0)
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=700`, 1, p1.ID)
}

func TestSummaryIsolatesProfilesAndComputesOverallPercentage(t *testing.T) {
	repo := testRepository(t)
	p1 := saveTestProfile(t, repo, "11111111111111111", "User One")
	p2 := saveTestProfile(t, repo, "22222222222222222", "User Two")
	p3 := saveTestProfile(t, repo, "33333333333333333", "User Three")
	// p1: 1 of 4 unlocked in game 10, 2 of 2 in completed game 20.
	saveSeedGame(t, repo, p1.ID, 10, "Game Ten", 60, dashboardSeed{GroupID: 0, Total: 4, Unlocked: 1, Percent: 25})
	saveSeedGame(t, repo, p1.ID, 20, "Game Twenty", 300, dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true})
	// p2 shares game 10 with p1: 4 of 4 unlocked there, and 0 of 10 in game 30.
	saveSeedGame(t, repo, p2.ID, 10, "Game Ten", 60, dashboardSeed{GroupID: 0, Total: 4, Unlocked: 4, Percent: 100, Completed: true, WasCompleted: true})
	saveSeedGame(t, repo, p2.ID, 30, "Game Thirty", 0, dashboardSeed{GroupID: 0, Total: 10, Unlocked: 0, Percent: 0})
	// p3 owns a single game without achievements: zero available achievements.
	saveSeedGame(t, repo, p3.ID, 40, "No Achievements", 0, dashboardSeed{GroupID: 0, Total: 0})

	tests := []struct {
		label   string
		profile int64
		want    Summary
		pct     float64
	}{
		{label: "p1", profile: p1.ID, want: Summary{CompletedSetsCount: 1, CompletedGamesCount: 1, TotalUnlockedCount: 3, OwnedGamesCount: 2, GamesWithAchievementsCount: 2, TotalAchievementsUnlocked: 3, TotalAchievementsAvailable: 6}, pct: 50},
		{label: "p2", profile: p2.ID, want: Summary{CompletedSetsCount: 1, CompletedGamesCount: 1, TotalUnlockedCount: 4, OwnedGamesCount: 2, GamesWithAchievementsCount: 2, TotalAchievementsUnlocked: 4, TotalAchievementsAvailable: 14}, pct: 200.0 / 7},
		{label: "p3 zero total", profile: p3.ID, want: Summary{OwnedGamesCount: 1}, pct: 0},
	}
	for _, tt := range tests {
		got, err := repo.Summary(tt.profile)
		if err != nil {
			t.Fatal(err)
		}
		gotPct := got.OverallCompletionPercentage
		got.OverallCompletionPercentage = 0
		if got != tt.want {
			t.Fatalf("%s summary = %#v, want %#v", tt.label, got, tt.want)
		}
		// Negated so that NaN (from a zero-total division) fails too.
		if !(math.Abs(gotPct-tt.pct) <= 1e-9) {
			t.Fatalf("%s overall percentage = %v, want %v", tt.label, gotPct, tt.pct)
		}
	}
}

func TestGameTagsAreScopedToTheirProfile(t *testing.T) {
	repo := testRepository(t)
	p1 := saveTestProfile(t, repo, "11111111111111111", "User One")
	p2 := saveTestProfile(t, repo, "22222222222222222", "User Two")
	// Same game and entry, different progress per profile.
	saveSeedGame(t, repo, p1.ID, 10, "Game Ten", 60, dashboardSeed{GroupID: 0, Total: 4, Unlocked: 1, Percent: 25, Score: 10})
	saveSeedGame(t, repo, p2.ID, 10, "Game Ten", 60, dashboardSeed{GroupID: 0, Total: 4, Unlocked: 4, Percent: 100, Completed: true, WasCompleted: true})

	p1Cards, err := repo.GameCards(p1.ID, "suggestions")
	if err != nil {
		t.Fatal(err)
	}
	assertStrings(t, "p1 tags", cardByEntryKey(t, p1Cards, "10:0").Tags, []string{"in_progress", "missing_cover_art"})
	p2Cards, err := repo.GameCards(p2.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	assertStrings(t, "p2 tags", cardByEntryKey(t, p2Cards, "10:0").Tags, []string{"completed", "missing_cover_art"})
}

func TestCardTagsUseFilterChipOrder(t *testing.T) {
	repo := testRepository(t)
	profile := saveTestProfile(t, repo, "11111111111111111", "User One")
	// Every issue and progress tag at once. The alphabetical order differs from the chip order on purpose.
	saveSeedGame(t, repo, profile.ID, 77, "Ordered Tags", 60,
		dashboardSeed{GroupID: 0, Total: 10, Unlocked: 3, Percent: 30, Score: 10, WasCompleted: true, NewlyIncomplete: true, MissingDLC: true, SyncWarning: true})

	cards, err := repo.GameCards(profile.ID, "suggestions")
	if err != nil {
		t.Fatal(err)
	}
	assertStrings(t, "tags", cardByEntryKey(t, cards, "77:0").Tags, []string{
		"new_achievements_added", "in_progress", "missing_achievements_in_dlc", "missing_cover_art", "sync_warning",
	})
}

// saveFallbackGame syncs one 100-achievement entry whose suggestion score and missing average may be NULL.
func saveFallbackGame(t *testing.T, repo *Repository, profileID, appID int64, completion float64, missingAvg, score *float64) {
	t.Helper()
	syncGame(t, repo, profileID, appID, SetSyncUpdate{
		GroupID:              0,
		TotalAchievements:    100,
		UnlockedAchievements: int(completion),
		CompletionPercent:    completion,
		MissingAvgUnlock:     missingAvg,
		SuggestionScore:      score,
	})
}

func TestNullSuggestionScoreFallbackTable(t *testing.T) {
	// Fallback weights: completion*0.50 + missingAvg*0.50, completion*0.50 without missing average.
	// The threshold for almost_there is 40 (inclusive).
	tests := []struct {
		name    string
		appID   int64
		done    float64
		missing *float64
		score   *float64
		tag     string
		other   string
		want    float64
	}{
		// 60*0.5 + 25*0.5 = 42.5. Other splits give different sums: 0.4/0.6 gives 39.0.
		{name: "50/50 split clears 40", appID: 610, done: 60, missing: ptrFloat(25), tag: "almost_there", other: "in_progress", want: 42.5},
		// 60*0.5 + 15*0.5 = 37.5. A 0.6/0.4 split would give 42.0 and clear 40.
		{name: "50/50 split stays below 40", appID: 611, done: 60, missing: ptrFloat(15), tag: "in_progress", other: "almost_there", want: 37.5},
		// Completion only: 80*0.5 = 40 exactly, which is inclusive.
		{name: "completion only reaches 40", appID: 612, done: 80, tag: "almost_there", other: "in_progress", want: 40},
		{name: "completion only stays below 40", appID: 613, done: 79, tag: "in_progress", other: "almost_there", want: 39.5},
		// A stored score always wins over the fallback, even when the fallback would clear 40.
		{name: "stored score wins over fallback", appID: 614, done: 60, missing: ptrFloat(25), score: ptrFloat(10), tag: "in_progress", other: "almost_there", want: 10},
	}
	repo := testRepository(t)
	profile := saveTestProfile(t, repo, "11111111111111111", "User One")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saveFallbackGame(t, repo, profile.ID, tt.appID, tt.done, tt.missing, tt.score)
			assertTag(t, repo, profile.ID, tt.appID, 0, tt.tag, true)
			assertTag(t, repo, profile.ID, tt.appID, 0, tt.other, false)
			got := scanOne[float64](t, repo, `select score from profile_game_tags where profile_id=? and appid=? and groupid=0 and tag=?`, profile.ID, tt.appID, tt.tag)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("stored score = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSortGameCardsTable(t *testing.T) {
	score := ptrFloat
	tests := []struct {
		name   string
		filter string
		cards  []GameCard
		want   []string
	}{
		{
			name:   "newly incomplete candidates before others regardless of score",
			filter: "suggestions",
			cards: []GameCard{
				{EntryKey: "2:0", Name: "Beta", TotalAchievements: 10, UnlockedAchievements: 1, CompletionPercent: 10, SuggestionScore: score(90)},
				{EntryKey: "1:0", Name: "Alpha", TotalAchievements: 10, UnlockedAchievements: 9, CompletionPercent: 90, SuggestionScore: score(5), NewlyIncomplete: true},
			},
			want: []string{"1:0", "2:0"},
		},
		{
			name:   "newly incomplete ordered by fewest missing achievements",
			filter: "suggestions",
			cards: []GameCard{
				{EntryKey: "3:0", Name: "Gamma", TotalAchievements: 10, UnlockedAchievements: 3, CompletionPercent: 30, SuggestionScore: score(1), NewlyIncomplete: true},
				{EntryKey: "4:0", Name: "Delta", TotalAchievements: 10, UnlockedAchievements: 8, CompletionPercent: 80, SuggestionScore: score(1), NewlyIncomplete: true},
			},
			want: []string{"4:0", "3:0"},
		},
		{
			name:   "same missing count ordered by completion descending",
			filter: "suggestions",
			cards: []GameCard{
				{EntryKey: "6:0", Name: "Eta", TotalAchievements: 10, UnlockedAchievements: 5, CompletionPercent: 50, SuggestionScore: score(1), NewlyIncomplete: true},
				{EntryKey: "5:0", Name: "Zeta", TotalAchievements: 20, UnlockedAchievements: 15, CompletionPercent: 75, SuggestionScore: score(1), NewlyIncomplete: true},
			},
			want: []string{"5:0", "6:0"},
		},
		{
			name:   "same missing count and completion ordered by name then entry key",
			filter: "suggestions",
			cards: []GameCard{
				{EntryKey: "8:0", Name: "Same", TotalAchievements: 10, UnlockedAchievements: 5, CompletionPercent: 50, SuggestionScore: score(1), NewlyIncomplete: true},
				{EntryKey: "7:0", Name: "same", TotalAchievements: 10, UnlockedAchievements: 5, CompletionPercent: 50, SuggestionScore: score(1), NewlyIncomplete: true},
			},
			want: []string{"7:0", "8:0"},
		},
		{
			name:   "normal candidates ordered by score ignoring missing count",
			filter: "suggestions",
			cards: []GameCard{
				{EntryKey: "9:0", Name: "Low Score", TotalAchievements: 10, UnlockedAchievements: 1, CompletionPercent: 10, SuggestionScore: score(10)},
				{EntryKey: "10:0", Name: "High Score", TotalAchievements: 10, UnlockedAchievements: 9, CompletionPercent: 90, SuggestionScore: score(50)},
			},
			want: []string{"10:0", "9:0"},
		},
		{
			name:   "equal score falls back to name",
			filter: "suggestions",
			cards: []GameCard{
				{EntryKey: "12:0", Name: "Bravo", TotalAchievements: 10, UnlockedAchievements: 1, CompletionPercent: 10, SuggestionScore: score(30)},
				{EntryKey: "11:0", Name: "alpha", TotalAchievements: 10, UnlockedAchievements: 1, CompletionPercent: 10, SuggestionScore: score(30)},
			},
			want: []string{"11:0", "12:0"},
		},
		{
			name:   "category order candidate, not owned, DLC flagged, no achievements",
			filter: "suggestions",
			cards: []GameCard{
				{EntryKey: "20:0", Name: "No Ach"},
				{EntryKey: "21:1", Name: "Flagged Top", TotalAchievements: 10, UnlockedAchievements: 1, SuggestionScore: score(99), MissingDLC: true},
				{EntryKey: "22:1", Name: "Unowned Mid", TotalAchievements: 10, SuggestionScore: score(80), DLCNotOwned: true},
				{EntryKey: "23:0", Name: "Candidate Low", TotalAchievements: 10, UnlockedAchievements: 1, SuggestionScore: score(1)},
			},
			want: []string{"23:0", "22:1", "21:1", "20:0"},
		},
		{
			name:   "DLC not owned and flagged belongs to the not owned group",
			filter: "suggestions",
			cards: []GameCard{
				{EntryKey: "31:1", Name: "Flagged", TotalAchievements: 10, SuggestionScore: score(50), MissingDLC: true},
				{EntryKey: "30:1", Name: "Flag Both", TotalAchievements: 10, SuggestionScore: score(99), DLCNotOwned: true, MissingDLC: true},
				{EntryKey: "32:0", Name: "Candidate", TotalAchievements: 10, SuggestionScore: score(1)},
			},
			want: []string{"32:0", "30:1", "31:1"},
		},
		{
			name:   "not owned group ordered by name",
			filter: "suggestions",
			cards: []GameCard{
				{EntryKey: "40:1", Name: "Zed Unowned", TotalAchievements: 10, DLCNotOwned: true},
				{EntryKey: "41:1", Name: "Aaa Unowned", TotalAchievements: 10, DLCNotOwned: true},
			},
			want: []string{"41:1", "40:1"},
		},
		{
			name:   "completed filter sorts by name only, case-insensitive",
			filter: "completed",
			cards: []GameCard{
				{EntryKey: "50:0", Name: "beta", IsCompleted: true, SuggestionScore: score(0)},
				{EntryKey: "51:0", Name: "Alpha", IsCompleted: true, SuggestionScore: score(100)},
			},
			want: []string{"51:0", "50:0"},
		},
		{
			name:   "warnings filter sorts by name",
			filter: "warnings",
			cards: []GameCard{
				{EntryKey: "60:0", Name: "Zulu", SyncWarning: true},
				{EntryKey: "61:0", Name: "Mike", SyncWarning: true},
			},
			want: []string{"61:0", "60:0"},
		},
		{
			name:   "disabled filter ties on name go to entry key",
			filter: "disabled",
			cards: []GameCard{
				{EntryKey: "71:0", Name: "Same", Disabled: true},
				{EntryKey: "70:0", Name: "same", Disabled: true},
			},
			want: []string{"70:0", "71:0"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sortGameCards(tt.cards, tt.filter)
			got := make([]string, len(tt.cards))
			for i, c := range tt.cards {
				got[i] = c.EntryKey
			}
			assertStrings(t, "order", got, tt.want)
		})
	}
}

func TestGameCardFlagsFlipIndependently(t *testing.T) {
	type flags struct {
		HasAchievements, IsCompleted, WasCompleted, NewlyIncomplete, SyncWarning bool
		ManualWasCompleted, MissingDLC, ManualDisabled, Disabled                 bool
	}
	base := func(repo *Repository, profileID int64, mutate func(seed *dashboardSeed)) {
		seed := dashboardSeed{AppID: 10, Name: "Flag Game", GroupID: 0, Total: 4, Unlocked: 1, Percent: 25, Score: 10, Playtime: 60}
		if mutate != nil {
			mutate(&seed)
		}
		seedDashboardGame(t, repo, profileID, seed)
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, repo *Repository, profileID int64)
		want  flags
	}{
		{
			name: "HasAchievements",
			setup: func(t *testing.T, repo *Repository, profileID int64) {
				base(repo, profileID, func(s *dashboardSeed) { s.Total, s.Unlocked, s.Percent = 0, 0, 0 })
			},
			want: flags{},
		},
		{
			name: "IsCompleted",
			setup: func(t *testing.T, repo *Repository, profileID int64) {
				base(repo, profileID, func(s *dashboardSeed) { s.Total, s.Unlocked, s.Percent, s.Completed = 2, 2, 100, true })
			},
			want: flags{HasAchievements: true, IsCompleted: true},
		},
		{
			name: "WasCompleted",
			setup: func(t *testing.T, repo *Repository, profileID int64) {
				base(repo, profileID, func(s *dashboardSeed) { s.WasCompleted = true })
			},
			want: flags{HasAchievements: true, WasCompleted: true},
		},
		{
			name: "NewlyIncomplete",
			setup: func(t *testing.T, repo *Repository, profileID int64) {
				base(repo, profileID, func(s *dashboardSeed) { s.NewlyIncomplete = true })
			},
			want: flags{HasAchievements: true, NewlyIncomplete: true},
		},
		{
			name: "SyncWarning",
			setup: func(t *testing.T, repo *Repository, profileID int64) {
				base(repo, profileID, func(s *dashboardSeed) { s.SyncWarning = true })
			},
			want: flags{HasAchievements: true, SyncWarning: true},
		},
		{
			name: "ManualWasCompleted",
			setup: func(t *testing.T, repo *Repository, profileID int64) {
				base(repo, profileID, nil)
				if err := repo.MarkGamePreviouslyCompleted(profileID, 10, 0); err != nil {
					t.Fatal(err)
				}
			},
			// Marking also makes the entry newly incomplete and was completed.
			want: flags{HasAchievements: true, WasCompleted: true, NewlyIncomplete: true, ManualWasCompleted: true},
		},
		{
			name: "MissingDLC",
			setup: func(t *testing.T, repo *Repository, profileID int64) {
				base(repo, profileID, nil)
				if err := repo.ToggleMissingAchievementsInDLC(profileID, 10, 0); err != nil {
					t.Fatal(err)
				}
			},
			want: flags{HasAchievements: true, MissingDLC: true},
		},
		{
			name: "Disabled by not-owned sweep",
			setup: func(t *testing.T, repo *Repository, profileID int64) {
				base(repo, profileID, nil)
				if err := repo.DisableMissingOwnedGames(profileID, nil); err != nil {
					t.Fatal(err)
				}
			},
			want: flags{HasAchievements: true, Disabled: true},
		},
		{
			name: "ManualDisabled",
			setup: func(t *testing.T, repo *Repository, profileID int64) {
				base(repo, profileID, nil)
				if err := repo.DisableProfileGame(profileID, 10, 0); err != nil {
					t.Fatal(err)
				}
			},
			want: flags{HasAchievements: true, Disabled: true, ManualDisabled: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testRepository(t)
			profile := saveTestProfile(t, repo, "11111111111111111", "User One")
			tt.setup(t, repo, profile.ID)
			card := cardByEntryKey(t, allCards(t, repo, profile.ID), "10:0")
			got := flags{
				HasAchievements:    card.HasAchievements,
				IsCompleted:        card.IsCompleted,
				WasCompleted:       card.WasCompleted,
				NewlyIncomplete:    card.NewlyIncomplete,
				SyncWarning:        card.SyncWarning,
				ManualWasCompleted: card.ManualWasCompleted,
				MissingDLC:         card.MissingDLC,
				ManualDisabled:     card.ManualDisabled,
				Disabled:           card.Disabled,
			}
			if got != tt.want {
				t.Fatalf("flags = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestActiveProfileFallbacks(t *testing.T) {
	repo := testRepository(t)
	if active, err := repo.ActiveProfile(); err != nil || active != nil {
		t.Fatalf("empty db active = %#v err=%v, want nil", active, err)
	}
	p1 := saveTestProfile(t, repo, "11111111111111111", "User One")
	p2 := saveTestProfile(t, repo, "22222222222222222", "User Two")

	// SaveProfile makes the saved profile active.
	if active, err := repo.ActiveProfile(); err != nil || active == nil || active.ID != p2.ID {
		t.Fatalf("active after save = %#v err=%v, want profile %d", active, err, p2.ID)
	}
	// A setting that points at a missing profile falls back to the lowest id.
	if _, err := repo.db.Exec(`update settings set value='999' where key='active_profile_id'`); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.ActiveProfile(); err != nil || active == nil || active.ID != p1.ID {
		t.Fatalf("stale setting active = %#v err=%v, want lowest id %d", active, err, p1.ID)
	}
	// No setting row at all (sql.ErrNoRows) also falls back to the lowest id.
	if _, err := repo.db.Exec(`delete from settings where key='active_profile_id'`); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.ActiveProfile(); err != nil || active == nil || active.ID != p1.ID {
		t.Fatalf("missing setting active = %#v err=%v, want lowest id %d", active, err, p1.ID)
	}
	// An explicit setting wins over the lowest id.
	if _, err := repo.db.Exec(`insert into settings(key, value) values('active_profile_id', ?)`, strconv.FormatInt(p2.ID, 10)); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.ActiveProfile(); err != nil || active == nil || active.ID != p2.ID {
		t.Fatalf("explicit setting active = %#v err=%v, want profile %d", active, err, p2.ID)
	}
}

func TestSaveProfileUpdatesExistingProfile(t *testing.T) {
	repo := testRepository(t)
	first, err := repo.SaveProfile("11111111111111111", "Old Name", "https://example.com/avatar.jpg")
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.SaveProfile("11111111111111111", "New Name", "")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.CreatedAt != first.CreatedAt {
		t.Fatalf("updated profile id/created = %d/%q, want %d/%q", second.ID, second.CreatedAt, first.ID, first.CreatedAt)
	}
	if second.DisplayName != "New Name" || second.AvatarURL != nil {
		t.Fatalf("updated profile = %#v, want new name and no avatar", second)
	}
	assertCount(t, repo, `select count(*) from profiles`, 1)
	assertCount(t, repo, `select count(*) from settings where key='active_profile_id' and value=?`, 1, strconv.FormatInt(first.ID, 10))
}

func TestMergeAppIDsDedupesAndSorts(t *testing.T) {
	got := mergeAppIDs([]int64{30, 10, 30}, []int64{20, 10, 40})
	if len(got) != 4 || got[0] != 10 || got[1] != 20 || got[2] != 30 || got[3] != 40 {
		t.Fatalf("mergeAppIDs = %v, want [10 20 30 40]", got)
	}
	if empty := mergeAppIDs(nil, nil); len(empty) != 0 {
		t.Fatalf("mergeAppIDs(nil, nil) = %v, want empty", empty)
	}
}

func TestSyncRunProgressCancelAndProfileTouch(t *testing.T) {
	repo := testRepository(t)
	p1 := saveTestProfile(t, repo, "11111111111111111", "User One")
	p2 := saveTestProfile(t, repo, "22222222222222222", "User Two")
	run1, err := repo.StartSyncRun(p1.ID)
	if err != nil {
		t.Fatal(err)
	}
	run2, err := repo.StartSyncRun(p2.ID)
	if err != nil {
		t.Fatal(err)
	}
	run3, err := repo.StartSyncRun(p1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.FinishSyncRun(run3, "success", 1, 1, 0, nil); err != nil {
		t.Fatal(err)
	}

	if err := repo.UpdateSyncRunProgress(run1, 10, 4, 1); err != nil {
		t.Fatal(err)
	}
	// Progress is written without touching status or finish time.
	var total, synced, failed int
	var status string
	if err := repo.db.QueryRow(`select games_total, games_synced, games_failed, status from sync_runs where id=?`, run1).Scan(&total, &synced, &failed, &status); err != nil {
		t.Fatal(err)
	}
	if total != 10 || synced != 4 || failed != 1 || status != "running" {
		t.Fatalf("progress = %d/%d/%d %q, want 10/4/1 running", total, synced, failed, status)
	}

	// Startup cleanup cancels running runs of every profile and leaves finished runs alone.
	if err := repo.CancelRunningSyncRuns("app closed"); err != nil {
		t.Fatal(err)
	}
	type runRow struct {
		status   string
		errText  sql.NullString
		finished sql.NullString
		total    int
	}
	readRun := func(id int64) runRow {
		var r runRow
		if err := repo.db.QueryRow(`select status, error, finished_at, games_total from sync_runs where id=?`, id).Scan(&r.status, &r.errText, &r.finished, &r.total); err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, id := range []int64{run1, run2} {
		r := readRun(id)
		if r.status != "canceled" || !r.errText.Valid || r.errText.String != "app closed" || !r.finished.Valid {
			t.Fatalf("run %d = %#v, want canceled with reason and finish time", id, r)
		}
	}
	if r := readRun(run1); r.total != 10 {
		t.Fatalf("canceled run total = %d, want progress kept at 10", r.total)
	}
	if r := readRun(run3); r.status != "success" || r.errText.Valid {
		t.Fatalf("finished run = %#v, want success without error", r)
	}

	// Touch stamps only the given profile.
	if err := repo.TouchProfileSynced(p1.ID); err != nil {
		t.Fatal(err)
	}
	assertCount(t, repo, `select count(*) from profiles where id=? and last_synced_at is not null`, 1, p1.ID)
	assertCount(t, repo, `select count(*) from profiles where id=? and last_synced_at is not null`, 0, p2.ID)
	synced1 := scanOne[string](t, repo, `select last_synced_at from profiles where id=?`, p1.ID)
	if _, err := time.Parse(time.RFC3339, synced1); err != nil {
		t.Fatalf("last_synced_at %q is not RFC3339: %v", synced1, err)
	}
}

func TestSaveGameSyncKeepListLastErrorAndUnlockTime(t *testing.T) {
	repo := testRepository(t)
	profile := saveTestProfile(t, repo, "11111111111111111", "User One")
	saveSeedGame(t, repo, profile.ID, 50, "Keep Game", 60,
		dashboardSeed{GroupID: 0, Total: 1, Unlocked: 1, Percent: 100, Completed: true, WasCompleted: true},
		dashboardSeed{GroupID: 1, SetName: "Pack One", Total: 1},
		dashboardSeed{GroupID: 2, SetName: "Pack Two", Total: 1},
		dashboardSeed{GroupID: 3, SetName: "Pack Three", Total: 1})
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=50`, 4, profile.ID)

	firstUnlock := int64(1700000000)
	warning := StringPtr("Steam 503")
	// Groups 0 and 2 stay eligible; groups 1 and 3 must be pruned. Group 0 gets a sync error and an unlock time.
	syncGame(t, repo, profile.ID, 50,
		SetSyncUpdate{GroupID: 0, TotalAchievements: 1, UnlockedAchievements: 1, CompletionPercent: 100, IsCompleted: true, WasCompleted: true, LastError: warning,
			Achievements: []AchievementRecord{{APIName: "ACH_50_0_0", Unlocked: true, UnlockTime: &firstUnlock}}},
		SetSyncUpdate{GroupID: 2, TotalAchievements: 1, Achievements: []AchievementRecord{{APIName: "ACH_50_2_0"}}})

	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=50 and groupid in (0,2)`, 2, profile.ID)
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=50 and groupid in (1,3)`, 0, profile.ID)
	assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=? and appid=50 and groupid in (1,3)`, 0, profile.ID)
	assertCount(t, repo, `select count(*) from profile_achievements where profile_id=? and appid=50`, 2, profile.ID)
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=50 and groupid=0 and last_error='Steam 503' and last_error_at is not null`, 1, profile.ID)
	assertCount(t, repo, `select count(*) from profile_achievements where profile_id=? and appid=50 and apiname='ACH_50_0_0' and unlock_time=1700000000`, 1, profile.ID)

	// Next sync without an error clears the stored error; a new unlock time overwrites the old one.
	secondUnlock := int64(1700000500)
	syncGame(t, repo, profile.ID, 50,
		SetSyncUpdate{GroupID: 0, TotalAchievements: 1, UnlockedAchievements: 1, CompletionPercent: 100, IsCompleted: true, WasCompleted: true,
			Achievements: []AchievementRecord{{APIName: "ACH_50_0_0", Unlocked: true, UnlockTime: &secondUnlock}}},
		SetSyncUpdate{GroupID: 2, TotalAchievements: 1, Achievements: []AchievementRecord{{APIName: "ACH_50_2_0"}}})
	assertCount(t, repo, `select count(*) from profile_games where profile_id=? and appid=50 and groupid=0 and last_error is null and last_error_at is null`, 1, profile.ID)
	assertCount(t, repo, `select count(*) from profile_achievements where profile_id=? and appid=50 and apiname='ACH_50_0_0' and unlock_time=1700000500`, 1, profile.ID)
}

// Tags are not recomputed by a re-own call; the next full sync restores them. Pins the decided behavior.
// The sweep has two upsert paths: an empty owned list (first upsert) and a non-empty list (second upsert,
// the one syncer uses). Both must keep a manual disable's reason when the game is not owned.
func TestDisableMissingOwnedGamesReownClearsNotOwnedOnly(t *testing.T) {
	tests := []struct {
		name  string
		owned []int64
	}{
		{name: "empty owned list", owned: nil},
		{name: "non-empty owned list without game 50", owned: []int64{999}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testRepository(t)
			profile := saveTestProfile(t, repo, "11111111111111111", "User One")
			saveSeedGame(t, repo, profile.ID, 50, "Re-own Game", 60,
				dashboardSeed{GroupID: 0, Total: 2, Unlocked: 2, Percent: 100, Completed: true, WasCompleted: true},
				dashboardSeed{GroupID: 1, SetName: "Pack", Total: 2, Unlocked: 1, Percent: 50})
			if err := repo.DisableProfileGame(profile.ID, 50, 1); err != nil {
				t.Fatal(err)
			}

			// Game no longer owned: every entry is flagged not_owned and all profile tags are dropped.
			// The manual disable on group 1 keeps its manual reason.
			if err := repo.DisableMissingOwnedGames(profile.ID, tt.owned); err != nil {
				t.Fatal(err)
			}
			assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=? and appid=50`, 0, profile.ID)
			assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid=50 and groupid=0 and disabled=1 and disabled_reason='not_owned'`, 1, profile.ID)
			assertCount(t, repo, `select count(*) from profile_game_flags where profile_id=? and appid=50 and groupid=1 and disabled=1 and disabled_reason='manual'`, 1, profile.ID)

			// Game owned again: only the not_owned flag of group 0 is cleared. The manual disable on group 1 survives.
			if err := repo.DisableMissingOwnedGames(profile.ID, []int64{50}); err != nil {
				t.Fatal(err)
			}
			completed, err := repo.GameCards(profile.ID, "completed")
			if err != nil {
				t.Fatal(err)
			}
			assertEntryKeys(t, "completed after re-own", completed, []string{"50:0"})
			disabled, err := repo.GameCards(profile.ID, "disabled")
			if err != nil {
				t.Fatal(err)
			}
			assertEntryKeys(t, "disabled after re-own", disabled, []string{"50:1"})
			if !disabled[0].ManualDisabled || disabled[0].DisabledReason == nil || *disabled[0].DisabledReason != "manual" {
				t.Fatalf("manual disable after re-own = %#v, want manual reason kept", disabled[0])
			}
			// Re-own does not recompute tags: the re-enabled entry has none until the next sync.
			assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=? and appid=50 and groupid=0`, 0, profile.ID)

			// The following full sync restores the re-enabled entry's tags. The manual entry stays tagless.
			syncGame(t, repo, profile.ID, 50,
				SetSyncUpdate{GroupID: 0, TotalAchievements: 2, UnlockedAchievements: 2, CompletionPercent: 100, IsCompleted: true, WasCompleted: true},
				SetSyncUpdate{GroupID: 1, TotalAchievements: 2, UnlockedAchievements: 1, CompletionPercent: 50})
			assertTag(t, repo, profile.ID, 50, 0, "completed", true)
			assertTag(t, repo, profile.ID, 50, 0, "missing_cover_art", true)
			assertCount(t, repo, `select count(*) from profile_game_tags where profile_id=? and appid=50 and groupid=1`, 0, profile.ID)
		})
	}
}

func TestSaveCoverRejectsNonPositiveImageAppID(t *testing.T) {
	repo := testRepository(t)
	for _, id := range []int64{0, -1} {
		if err := repo.SaveCover(CoverUpdate{ImageAppID: id, Kind: "library", FileName: "library_600x900.jpg"}); err == nil {
			t.Fatalf("SaveCover(image %d) error = nil, want invalid appid error", id)
		}
	}
	assertCount(t, repo, `select count(*) from cover_images`, 0)
}

func TestCoverURLWithoutImageAppIDIsEmpty(t *testing.T) {
	downloaded := sql.NullString{String: "2026-11-14T10:00:00Z", Valid: true}
	if got := coverURL(0, "library_600x900.jpg", "/cache/0/library_600x900.jpg", downloaded); got != "" {
		t.Fatalf("cover url for image appid 0 = %q, want empty", got)
	}
}

func TestErrorPtrAndFormatGameError(t *testing.T) {
	if got := ErrorPtr(nil); got != nil {
		t.Fatalf("ErrorPtr(nil) = %q, want nil", *got)
	}
	if got := ErrorPtr(errors.New("boom")); got == nil || *got != "boom" {
		t.Fatalf("ErrorPtr(boom) = %v, want boom", got)
	}
	if got := FormatGameError(42, errors.New("boom")); got != "appid 42: boom" {
		t.Fatalf("FormatGameError = %q, want %q", got, "appid 42: boom")
	}
}

// ProfileGame reports the entry flags directly, separately from the dashboard cards.
func TestProfileGameStateReportsEntryFlags(t *testing.T) {
	repo := testRepository(t)
	profile := saveTestProfile(t, repo, "11111111111111111", "User One")
	saveSeedGame(t, repo, profile.ID, 10, "Flag Game", 60, dashboardSeed{GroupID: 0, Total: 4, Unlocked: 1, Percent: 25, Score: 10})
	state := func() ProfileGameState {
		t.Helper()
		s, err := repo.ProfileGame(profile.ID, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	if s := state(); !s.Exists || s.MissingDLC || s.Disabled || s.ManualWasCompleted {
		t.Fatalf("plain entry state = %#v, want existing with no flags", s)
	}
	if err := repo.ToggleMissingAchievementsInDLC(profile.ID, 10, 0); err != nil {
		t.Fatal(err)
	}
	if s := state(); !s.MissingDLC || s.Disabled {
		t.Fatalf("missing-DLC state = %#v, want MissingDLC only", s)
	}
	if err := repo.ToggleMissingAchievementsInDLC(profile.ID, 10, 0); err != nil {
		t.Fatal(err)
	}
	if s := state(); s.MissingDLC {
		t.Fatalf("missing-DLC state after second toggle = %#v, want cleared", s)
	}
	if err := repo.DisableProfileGame(profile.ID, 10, 0); err != nil {
		t.Fatal(err)
	}
	if s := state(); !s.Disabled || s.MissingDLC {
		t.Fatalf("disabled state = %#v, want Disabled only", s)
	}
}
