package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/steam"
)

const (
	userSafeGeneric  = "Something went wrong. See the log file for details."
	userSafeCanceled = "The request was canceled."
)

// seedV2Database writes a populated database at version 2 with rows that hold the text earlier versions stored: raw
// error text (rewritten by migration 3) and the sentences the app writes (kept). It is the state an installed app
// leaves behind before migration 3.
func seedV2Database(t *testing.T, path string) {
	t.Helper()
	raw := openRawDB(t, path)
	if _, err := raw.Exec(initialSchemaSQL + coverCachedFlagSQL + schemaMigrationsSQL + `
		INSERT INTO schema_migrations(version, name, applied_at) VALUES(1, 'initial_schema', '2026-01-01T00:00:00Z'), (2, 'cover_cached_flag', '2026-01-01T00:00:00Z');
		PRAGMA user_version = 2;
		INSERT INTO profiles(id, steam_id64, display_name, created_at, updated_at) VALUES(1, '11111111111111111', 'User One', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO games(appid, name, has_achievements, achievement_status, created_at, updated_at) VALUES
			(42, 'Game 42', 1, 'ok', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
			(43, 'Game 43', 1, 'ok', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		INSERT INTO profile_games(profile_id, appid, groupid, total_achievements, unlocked_achievements, completion_percent, is_completed, last_error, last_synced_at) VALUES
			(1, 42, 0, 10, 7, 70, 0, 'Steam API /IPlayerService/GetGameAchievements/v1/ returned 500 Internal Server Error', '2026-01-02T00:00:00Z'),
			(1, 42, 1, 10, 7, 70, 0, 'owned games unavailable; profile game details may be private', '2026-01-02T00:00:00Z'),
			(1, 42, 2, 10, 7, 70, 0, 'context canceled', '2026-01-02T00:00:00Z'),
			(1, 42, 3, 10, 7, 70, 0, 'Steam did not share achievements for this game. The profile''s game details may be private.', '2026-01-02T00:00:00Z'),
			(1, 43, 0, 4, 4, 100, 1, 'Get "https://api.steampowered.com/IPlayerService/GetOwnedGames/v0001/?key=REDACTED": EOF', '2026-01-02T00:00:00Z'),
			(1, 43, 1, 4, 4, 100, 1, NULL, '2026-01-02T00:00:00Z'),
			(1, 43, 2, 4, 4, 100, 1, '', '2026-01-02T00:00:00Z'),
			(1, 43, 3, 4, 4, 100, 1, 'Steam returned an incomplete game list. Try again later.', '2026-01-02T00:00:00Z'),
			(1, 43, 4, 4, 4, 100, 1, 'Something went wrong. See the log file for details.', '2026-01-02T00:00:00Z');
		INSERT INTO sync_runs(id, profile_id, started_at, finished_at, status, games_total, games_synced, games_failed, error) VALUES
			(1, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'failed', 2, 0, 0, 'Steam API /ISteamUser/ResolveVanityURL/v1/ returned 403 Forbidden'),
			(2, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'failed', 2, 0, 0, 'context canceled'),
			(3, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'partial', 5, 3, 2, '2 of 5 games failed to sync'),
			(4, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'canceled', 5, 3, 2, 'app restarted before sync completed'),
			(5, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'failed', 5, 3, 2, 'sync ended but its final status could not be saved'),
			(6, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'failed', 5, 3, 2, 'Steam returned an unexpected response (HTTP 418).'),
			(7, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'failed', 5, 3, 2, 'The request was canceled.'),
			(8, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'failed', 5, 3, 2, NULL),
			(9, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'failed', 5, 3, 2, ''),
			(10, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'partial', 5, 3, 2, '2 of 5 games failed to sync: key=abc'),
			(11, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'partial', 5, 3, 2, '1x of 2 games failed to sync'),
			(12, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'partial', 5, 3, 2, '2 of 5x games failed to sync'),
			(13, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'partial', 5, 3, 2, 'x 2 of 5 games failed to sync'),
			(14, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'partial', 5, 3, 2, '2 of 5 games failed to sync '),
			(15, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'partial', 5, 3, 2, ' of 5 games failed to sync'),
			(16, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'partial', 5, 3, 2, '2 of  games failed to sync'),
			(17, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'partial', 5, 3, 2, '2 of 5 games failed to syncx'),
			(18, 1, '2026-01-02T00:00:00Z', '2026-01-02T00:01:00Z', 'partial', 5, 3, 2, '12 of 345 games failed to sync');
	`); err != nil {
		t.Fatalf("seed v2 db: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close v2 db: %v", err)
	}
}

// profileGameTexts returns the stored last_error per "appid/groupid", with NULL shown as "<NULL>".
func profileGameTexts(t *testing.T, conn *sql.DB) map[string]string {
	t.Helper()
	rows, err := conn.Query(`select appid, groupid, coalesce(last_error, '<NULL>') from profile_games`)
	if err != nil {
		t.Fatalf("query profile_games: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var appID, groupID int64
		var text string
		if err := rows.Scan(&appID, &groupID, &text); err != nil {
			t.Fatalf("scan profile_games: %v", err)
		}
		got[fmtKey(appID, groupID)] = text
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate profile_games: %v", err)
	}
	return got
}

// syncRunErrors returns the stored error per sync_runs id, with NULL shown as "<NULL>".
func syncRunErrors(t *testing.T, conn *sql.DB) map[int64]string {
	t.Helper()
	rows, err := conn.Query(`select id, coalesce(error, '<NULL>') from sync_runs`)
	if err != nil {
		t.Fatalf("query sync_runs: %v", err)
	}
	defer rows.Close()
	got := map[int64]string{}
	for rows.Next() {
		var id int64
		var text string
		if err := rows.Scan(&id, &text); err != nil {
			t.Fatalf("scan sync_runs: %v", err)
		}
		got[id] = text
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sync_runs: %v", err)
	}
	return got
}

func fmtKey(appID, groupID int64) string {
	return fmt.Sprintf("%d/%d", appID, groupID)
}

// syncTextSafeSet returns the sentences in syncTextSafeSentences, unquoted, parsed from the SQL text.
func syncTextSafeSet(t *testing.T) map[string]bool {
	t.Helper()
	set := map[string]bool{}
	for _, line := range strings.Split(syncTextSafeSentences, "\n") {
		s := strings.TrimSuffix(strings.TrimSpace(line), ",")
		if s == "" {
			continue
		}
		if len(s) < 2 || s[0] != '\'' || s[len(s)-1] != '\'' {
			t.Fatalf("syncTextSafeSentences line %q is not a quoted sentence", line)
		}
		set[strings.ReplaceAll(s[1:len(s)-1], "''", "'")] = true
	}
	if len(set) == 0 {
		t.Fatal("syncTextSafeSentences parsed to no sentences")
	}
	return set
}

// TestSyncTextSafeSentencesCoverAppSentences fails when a sentence the app can store in these columns is missing from
// the migration 3 list. Such a sentence would be rewritten to the generic text on upgrade, so a change to it must be
// seen here. The app texts are listed by hand where the package cannot import them (the root package and the syncer
// write them); the locations are noted next to each.
func TestSyncTextSafeSentencesCoverAppSentences(t *testing.T) {
	listed := syncTextSafeSet(t)
	want := append([]string{}, steam.UserMessageSentences()...)
	want = append(want,
		// Run text app.go writes when it cancels runs left running at startup (CancelRunningSyncRuns).
		"app restarted before sync completed",
		// app.go staleRunErrorText: a run whose final status could not be saved.
		"sync ended but its final status could not be saved",
		// The cancellation sentence, and the generic sentence the migration rewrites other text to.
		userSafeCanceled, userSafeGeneric,
	)
	for _, s := range want {
		if !listed[s] {
			t.Errorf("sentence %q is not in syncTextSafeSentences; migration 3 would rewrite a stored copy of it", s)
		}
	}

	// The partial run summary is kept by pattern, not by the list. The sentence syncer.go writes must match the SQL
	// pattern, and the near misses must not (TestRewriteStoredSyncTextMigration covers those).
	conn := openRawDB(t, filepath.Join(t.TempDir(), "summary.db"))
	for _, counts := range [][2]int{{2, 5}, {0, 3}, {12, 345}} {
		text := fmt.Sprintf("%d of %d games failed to sync", counts[0], counts[1])
		var matched int64
		if err := conn.QueryRow(`SELECT `+partialRunSummarySQL+` FROM (SELECT ? AS error)`, text).Scan(&matched); err != nil {
			t.Fatalf("match %q: %v", text, err)
		}
		if matched != 1 {
			t.Errorf("partial run summary %q does not match partialRunSummarySQL", text)
		}
	}
}

// Migration 3 keeps only the sentences the app writes and replaces every other stored error text. Rows with NULL or
// empty text are untouched. Re-running the migration changes nothing.
func TestRewriteStoredSyncTextMigration(t *testing.T) {
	paths := testPaths(t)
	seedV2Database(t, paths.DB)

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()
	assertUserVersion(t, conn, 3)
	assertMigration(t, conn, 3)

	wantGames := map[string]string{
		"42/0": userSafeGeneric,
		"42/1": userSafeGeneric,
		"42/2": userSafeCanceled,
		"42/3": "Steam did not share achievements for this game. The profile's game details may be private.",
		"43/0": userSafeGeneric,
		"43/1": "<NULL>",
		"43/2": "",
		"43/3": "Steam returned an incomplete game list. Try again later.",
		"43/4": userSafeGeneric,
	}
	wantRuns := map[int64]string{
		1:  userSafeGeneric,
		2:  userSafeCanceled,
		3:  "2 of 5 games failed to sync",
		4:  "app restarted before sync completed",
		5:  "sync ended but its final status could not be saved",
		6:  userSafeGeneric,
		7:  userSafeCanceled,
		8:  "<NULL>",
		9:  "",
		10: userSafeGeneric,
		// Near misses of the run summary: each is rewritten, because the summary is digits, " of ", digits, and the
		// fixed words, with nothing else.
		11: userSafeGeneric,
		12: userSafeGeneric,
		13: userSafeGeneric,
		14: userSafeGeneric,
		15: userSafeGeneric,
		16: userSafeGeneric,
		17: userSafeGeneric,
		18: "12 of 345 games failed to sync",
	}

	gotGames := profileGameTexts(t, conn)
	for key, want := range wantGames {
		if gotGames[key] != want {
			t.Errorf("profile_games %s last_error = %q, want %q", key, gotGames[key], want)
		}
	}
	gotRuns := syncRunErrors(t, conn)
	for id, want := range wantRuns {
		if gotRuns[id] != want {
			t.Errorf("sync_runs %d error = %q, want %q", id, gotRuns[id], want)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	// Running the migration SQL again on the migrated rows changes nothing.
	if _, err := conn.Exec(rewriteStoredSyncTextSQL); err != nil {
		t.Fatalf("rerun migration 3: %v", err)
	}
	if again := profileGameTexts(t, conn); !reflect.DeepEqual(again, gotGames) {
		t.Fatalf("profile_games changed on rerun: %v, want %v", again, gotGames)
	}
	if again := syncRunErrors(t, conn); !reflect.DeepEqual(again, gotRuns) {
		t.Fatalf("sync_runs changed on rerun: %v, want %v", again, gotRuns)
	}
}
