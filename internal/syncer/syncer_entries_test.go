package syncer

import (
	"fmt"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/repository"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
)

// An entry is completed only with countable achievements and all of them unlocked.
func TestSetSyncUpdateCompletedBoundary(t *testing.T) {
	cases := []struct {
		name     string
		total    int
		unlocked int
		want     bool
	}{
		{"no countable achievements", 0, 0, false},
		{"one of three", 3, 1, false},
		{"two of three", 3, 2, false},
		{"all three", 3, 3, true},
		{"single achievement unlocked", 1, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := setEntry{GroupID: 1, stats: achievementStats{total: tc.total, unlocked: tc.unlocked}}
			if got := setSyncUpdate(entry, repository.ProfileGameState{}).IsCompleted; got != tc.want {
				t.Fatalf("IsCompleted = %v for %d/%d, want %v", got, tc.unlocked, tc.total, tc.want)
			}
		})
	}
}

// The missing average is the mean of the countable locked percents; unlocked and archived ones are left out.
func TestBuildSetEntriesMissingAverageIsMeanOfLockedPercents(t *testing.T) {
	game := steam.GameAchievements{
		Sets: []steam.AchievementSet{{GroupID: 0, Public: true}},
		Achievements: []steam.SetAchievement{
			{InternalName: "DONE", Key: 1, GlobalPercent: pct(90)},
			{InternalName: "LOCK_A", Key: 2, GlobalPercent: pct(10)},
			{InternalName: "LOCK_B", Key: 3, GlobalPercent: pct(30)},
			{InternalName: "LOCK_C", Key: 4, GlobalPercent: pct(51), Archived: true},
		},
	}
	entries := buildSetEntries(42, game, steam.UserAchievements{Unlocked: unlockedKeys(1), Groups: groupSet(0)})
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	stats := entries[0].stats
	if stats.missingAvg == nil || *stats.missingAvg != 20 {
		t.Fatalf("missing average = %v, want (10+30)/2 = 20", stats.missingAvg)
	}
}

// Synthesized sets for groupids without metadata follow ascending groupid order. The groups come from a map,
// so the sort is the only thing that fixes the order; repeat to make a lucky order unlikely.
func TestBuildSetEntriesSynthesizedSetsFollowGroupIDOrder(t *testing.T) {
	groups := []int64{9, 3, 11, 5, 7}
	var achs []steam.SetAchievement
	key := int64(1)
	for _, g := range append([]int64{0}, groups...) {
		achs = append(achs, steam.SetAchievement{InternalName: fmt.Sprintf("G%d", g), Key: key, GroupID: g})
		key++
	}
	game := steam.GameAchievements{
		Sets:         []steam.AchievementSet{{GroupID: 0, Public: true}},
		Achievements: achs,
	}
	want := []int64{0, 3, 5, 7, 9, 11}
	for i := 0; i < 20; i++ {
		entries := buildSetEntries(88, game, steam.UserAchievements{Groups: groupSet(0)})
		got := make([]int64, 0, len(entries))
		for _, entry := range entries {
			got = append(got, entry.GroupID)
		}
		if !sameInt64s(got, want) {
			t.Fatalf("group order = %v, want %v", got, want)
		}
	}
}

func sameInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
