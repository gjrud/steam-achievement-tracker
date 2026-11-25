package syncer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	"github.com/gjrud/steam-achievement-tracker/internal/repository"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
)

// Progress throttle tests. The throttle clock is Service.now, so each test drives time by hand: no sleeps. Games run
// on workers, so write sequences are read back through progress_log, which a trigger fills in the order the database
// applies the progress writes.

// throttleFixture returns a fake Steam with n games that have no achievements (the quick path).
func throttleFixture(t *testing.T, n int) *fakeSteamServer {
	t.Helper()
	owned := make([]steam.OwnedGame, n)
	for i := range owned {
		appID := int64(3000 + i)
		owned[i] = steam.OwnedGame{AppID: appID, Name: fmt.Sprintf("Throttle %d", appID), PlaytimeForever: i + 1}
	}
	fake := newFakeSteamServer(t, owned)
	for _, game := range owned {
		fake.schema[game.AppID] = []string{}
	}
	return fake
}

// progressLog adds a table and a trigger that append (games_total, games_synced, games_failed) for every progress
// write. A progress write is a sync_runs update that leaves the run running, so FinishSyncRun is not logged. The
// returned function reads the rows in write order. Call it after FullSync returns.
func progressLog(t *testing.T, paths appdata.Paths) func() [][3]int {
	t.Helper()
	addTriggers(t, paths,
		`create table progress_log (seq integer primary key autoincrement, games_total integer, games_synced integer, games_failed integer)`,
		`create trigger log_run_progress after update on sync_runs when new.status = 'running' begin insert into progress_log(games_total, games_synced, games_failed) values (new.games_total, new.games_synced, new.games_failed); end`,
	)
	return func() [][3]int {
		t.Helper()
		db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?_busy_timeout=5000", paths.DB))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		rows, err := db.Query(`select games_total, games_synced, games_failed from progress_log order by seq`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out [][3]int
		for rows.Next() {
			var r [3]int
			if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
}

// frozenClock returns a clock that always reads at.
func frozenClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

// throttledService returns a service with the default interval, a clock, and an OnChange counter.
func throttledService(repo *repository.Repository, client *steam.Client, now func() time.Time) (*Service, *atomic.Int32) {
	var calls atomic.Int32
	return &Service{Repo: repo, Steam: client, OnChange: func() { calls.Add(1) }, now: now}, &calls
}

// With the clock frozen, only the first finished game writes. Every later update is skipped, and one trailing flush
// writes the full counters after the workers are done. OnChange fires for start, first write, flush and finish.
func TestProgressThrottleFrozenClockWritesFirstAndTrailingFlush(t *testing.T) {
	const games = 6
	repo, paths, profile := faultyRepository(t)
	writes := progressLog(t, paths)
	fake := throttleFixture(t, games)
	svc, calls := throttledService(repo, fake.client, frozenClock(time.Unix(1000, 0)))

	result := runServiceOK(t, svc, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: games, synced: games, failed: 0})
	if got := int(calls.Load()); got != 4 {
		t.Fatalf("OnChange calls = %d, want 4 (start, first progress write, trailing flush, finish)", got)
	}
	got := writes()
	if len(got) != 2 {
		t.Fatalf("progress writes = %v, want 2: the first game, then one trailing flush", got)
	}
	if got[0][0] != games || got[0][1] < 1 {
		t.Fatalf("first progress write = %v, want games_total %d and at least one game synced", got[0], games)
	}
	if want := [3]int{games, games, 0}; got[1] != want {
		t.Fatalf("trailing flush = %v, want %v", got[1], want)
	}
	requireLatestSyncRun(t, repo, profile.ID, expectedSyncResult{status: "success", total: games, synced: games, failed: 0})
}

// The clock moves 100 ms on each read, and each game's update reads it once, so reads 1..12 come at 100 ms steps. With
// a 500 ms interval, reads 1, 6 and 11 write. Read 12 is skipped and read 13 is the trailing flush: four progress
// writes and six OnChange calls (start, four progress, finish).
func TestProgressThrottleIntervalGatesWrites(t *testing.T) {
	const games = 12
	repo, paths, profile := faultyRepository(t)
	writes := progressLog(t, paths)
	fake := throttleFixture(t, games)
	base := time.Unix(2000, 0)
	var reads atomic.Int64
	now := func() time.Time {
		return base.Add(time.Duration(reads.Add(1)) * 100 * time.Millisecond)
	}
	svc, calls := throttledService(repo, fake.client, now)

	result := runServiceOK(t, svc, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: games, synced: games, failed: 0})
	if got := int(calls.Load()); got != 6 {
		t.Fatalf("OnChange calls = %d, want 6 (start, four progress writes, finish)", got)
	}
	if got := reads.Load(); got != games+1 {
		t.Fatalf("clock reads = %d, want %d (one per game, one for the flush)", got, games+1)
	}
	got := writes()
	if len(got) != 4 {
		t.Fatalf("progress writes = %v, want 4", got)
	}
	for i := 1; i < len(got); i++ {
		for c := range 3 {
			if got[i][c] < got[i-1][c] {
				t.Fatalf("progress write %d = %v after %v: a counter went backwards", i, got[i], got[i-1])
			}
		}
	}
	if want := [3]int{games, games, 0}; got[len(got)-1] != want {
		t.Fatalf("last progress write = %v, want %v", got[len(got)-1], want)
	}
}

// The first write is not throttled even when the clock reads the zero time. The interval since the last write would
// read 0 there, so only the first-write rule lets that write through.
func TestProgressThrottleFirstWriteIsNotThrottled(t *testing.T) {
	const games = 5
	repo, paths, profile := faultyRepository(t)
	writes := progressLog(t, paths)
	fake := throttleFixture(t, games)
	svc, calls := throttledService(repo, fake.client, frozenClock(time.Time{}))

	result := runServiceOK(t, svc, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: games, synced: games, failed: 0})
	got := writes()
	if len(got) != 2 || got[0][0] != games || got[0][1] < 1 {
		t.Fatalf("progress writes = %v, want 2: the first game (games_total %d), then the trailing flush", got, games)
	}
	if want := [3]int{games, games, 0}; got[1] != want {
		t.Fatalf("trailing flush = %v, want %v", got[1], want)
	}
	if n := int(calls.Load()); n != 4 {
		t.Fatalf("OnChange calls = %d, want 4", n)
	}
}

// FinishSyncRun fails after a throttled run. The row must keep the full counters the trailing flush wrote, not the
// stale counters of the first write.
func TestProgressThrottleFinishFailureKeepsTrailingFlushCounters(t *testing.T) {
	const games = 5
	repo, paths, profile := faultyRepository(t)
	addTriggers(t, paths,
		`create trigger block_run_finish before update of status on sync_runs begin select raise(abort, 'finish blocked'); end`,
	)
	fake := throttleFixture(t, games)
	svc, _ := throttledService(repo, fake.client, frozenClock(time.Unix(3000, 0)))
	logs := captureLog(t)

	result := runServiceOK(t, svc, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: games, synced: games, failed: 0})
	if !strings.Contains(logs.String(), "finish sync run:") {
		t.Fatalf("log = %q, want the finish failure", logs.String())
	}
	run := requireLatestRun(t, repo, profile.ID)
	if run.Status != "running" || run.FinishedAt != nil {
		t.Fatalf("sync run = %#v, want still running with no finish time", run)
	}
	if run.GamesTotal != games || run.GamesSynced != games || run.GamesFailed != 0 {
		t.Fatalf("sync run counters = %d/%d/%d, want the full counts from the trailing flush", run.GamesTotal, run.GamesSynced, run.GamesFailed)
	}
}

// A cancel right after the first write leaves finished games unwritten. With FinishSyncRun blocked, the row must hold
// the counters the trailing flush wrote, which equal the returned Result. Which games finish before the cancel takes
// effect depends on timing; the invariant does not.
func TestProgressThrottleCancelKeepsTrailingFlushCounters(t *testing.T) {
	const games = 8
	repo, paths, profile := faultyRepository(t)
	addTriggers(t, paths,
		`create trigger block_run_finish before update of status on sync_runs begin select raise(abort, 'finish blocked'); end`,
	)
	fake := throttleFixture(t, games)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var events atomic.Int32
	svc := &Service{Repo: repo, Steam: fake.client, now: frozenClock(time.Unix(4000, 0)), OnChange: func() {
		// Event 1 is the run start and event 2 the first progress write. Cancel after that one.
		if events.Add(1) == 2 {
			cancel()
		}
	}}

	result, err := svc.FullSync(ctx, *profile)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if result.Status != "canceled" {
		t.Fatalf("result = %#v, want canceled", result)
	}
	run := requireLatestRun(t, repo, profile.ID)
	if run.Status != "running" {
		t.Fatalf("sync run status = %q, want running (finish blocked)", run.Status)
	}
	if run.GamesTotal != result.GamesTotal || run.GamesSynced != result.GamesSynced || run.GamesFailed != result.GamesFailed {
		t.Fatalf("sync run counters = %d/%d/%d, want the returned %d/%d/%d",
			run.GamesTotal, run.GamesSynced, run.GamesFailed, result.GamesTotal, result.GamesSynced, result.GamesFailed)
	}
}

// Smoke test: with throttling off, four workers write as games finish. Every row the database applies must not go
// backwards in any counter, and the last one holds the full counts. The four workers make the interleaving timing
// dependent, so this test does not by itself pin the ordering. That is pinned deterministically by
// TestProgressSnapshotIsTakenAfterClockRead.
func TestProgressWritesNeverGoBackwards(t *testing.T) {
	const games = 24
	repo, paths, profile := faultyRepository(t)
	writes := progressLog(t, paths)
	fake := throttleFixture(t, games)
	svc := &Service{Repo: repo, Steam: fake.client, ProgressInterval: -1}

	result := runServiceOK(t, svc, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: games, synced: games, failed: 0})
	got := writes()
	if len(got) != games {
		t.Fatalf("progress writes = %d, want %d: one per game when throttling is off", len(got), games)
	}
	for i := 1; i < len(got); i++ {
		for c := range 3 {
			if got[i][c] < got[i-1][c] {
				t.Fatalf("progress write %d = %v after %v: a counter went backwards", i, got[i], got[i-1])
			}
		}
	}
	if want := [3]int{games, games, 0}; got[len(got)-1] != want {
		t.Fatalf("last progress write = %v, want %v", got[len(got)-1], want)
	}
}

// updateProgress takes its counter snapshot after the throttle clock is read, under progressMu. The clock hook bumps
// GamesSynced on its first read, as a game finishing on another worker would, and then returns. The row must carry
// the bumped count. A snapshot taken before the decision (before progressMu or the clock read) stores the stale 0.
// No goroutines or timing are involved: the hook runs inside progressWrite, at a fixed point.
func TestProgressSnapshotIsTakenAfterClockRead(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	runID, err := repo.StartSyncRun(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	var run *fullSyncRun
	var reads int
	svc := &Service{Repo: repo, now: func() time.Time {
		reads++
		if reads == 1 {
			run.mu.Lock()
			run.result.GamesSynced++
			run.mu.Unlock()
		}
		return time.Unix(6000, 0)
	}}
	run = &fullSyncRun{s: svc, profile: *profile, runID: runID, result: Result{Status: "success", GamesTotal: 1}}

	run.updateProgress()
	if reads != 1 {
		t.Fatalf("clock reads = %d, want 1", reads)
	}
	row := requireLatestRun(t, repo, profile.ID)
	if row.GamesTotal != 1 || row.GamesSynced != 1 || row.GamesFailed != 0 {
		t.Fatalf("progress row counters = %d/%d/%d, want 1/1/0: the snapshot must follow the clock read",
			row.GamesTotal, row.GamesSynced, row.GamesFailed)
	}
}

// A failing progress write is logged and still fires OnChange, and it is not retried for every game. Under the throttle
// there are two write attempts: the first game's and the trailing flush. The finish write still lands.
func TestProgressWriteFailureFiresOnChangeAndIsNotRetriedPerGame(t *testing.T) {
	const games = 5
	repo, paths, profile := faultyRepository(t)
	addTriggers(t, paths,
		`create trigger block_run_progress before update on sync_runs when new.status = 'running' begin select raise(abort, 'progress blocked'); end`,
	)
	fake := throttleFixture(t, games)
	logs := captureLog(t)
	svc, calls := throttledService(repo, fake.client, frozenClock(time.Unix(5000, 0)))

	result := runServiceOK(t, svc, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: games, synced: games, failed: 0})
	if got := strings.Count(logs.String(), "update sync progress:"); got != 2 {
		t.Fatalf("progress write failures logged = %d, want 2 (first write and trailing flush)", got)
	}
	if got := int(calls.Load()); got != 4 {
		t.Fatalf("OnChange calls = %d, want 4 (start, failed progress, failed flush, finish)", got)
	}
	requireLatestSyncRun(t, repo, profile.ID, expectedSyncResult{status: "success", total: games, synced: games, failed: 0})
}

// The zero value means the 500 ms default, a negative value means no throttling, and a positive value is used as set.
func TestServiceProgressIntervalResolution(t *testing.T) {
	if got := (&Service{}).progressInterval(); got != 500*time.Millisecond {
		t.Fatalf("zero ProgressInterval resolves to %s, want 500ms", got)
	}
	if got := (&Service{ProgressInterval: -1}).progressInterval(); got >= 0 {
		t.Fatalf("negative ProgressInterval resolves to %s, want negative (no throttling)", got)
	}
	if got := (&Service{ProgressInterval: 2 * time.Second}).progressInterval(); got != 2*time.Second {
		t.Fatalf("ProgressInterval 2s resolves to %s", got)
	}
}
