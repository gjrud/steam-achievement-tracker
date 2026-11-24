package syncer

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/repository"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
)

// countingService returns a service on repo and client whose OnChange counts its calls. Read the count after FullSync.
// Throttling is off (ProgressInterval -1), so every finished game writes and fires. Throttled counts are in
// syncer_progress_throttle_test.go.
func countingService(repo *repository.Repository, client *steam.Client) (*Service, *atomic.Int32) {
	var calls atomic.Int32
	return &Service{Repo: repo, Steam: client, OnChange: func() { calls.Add(1) }, ProgressInterval: -1}, &calls
}

// With throttling off, OnChange fires for run start, each game's progress write and the final status write. Games
// sync on worker goroutines, so the counter is atomic.
func TestFullSyncOnChangeFiresForStartProgressAndFinish(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	owned := make([]steam.OwnedGame, 6)
	for i := range owned {
		appID := int64(2000 + i)
		owned[i] = steam.OwnedGame{AppID: appID, Name: fmt.Sprintf("Changed %d", appID), PlaytimeForever: i + 1}
	}
	fake := newFakeSteamServer(t, owned)
	for _, game := range owned {
		fake.schema[game.AppID] = []string{}
	}
	svc, calls := countingService(repo, fake.client)

	result := runServiceOK(t, svc, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: len(owned), synced: len(owned), failed: 0})
	if got, want := int(calls.Load()), len(owned)+2; got != want {
		t.Fatalf("OnChange calls = %d, want exactly %d (start, %d progress writes, finish)", got, want, len(owned))
	}
}

// Failing progress and finish writes still fire OnChange: the caller's view of the run changed either way.
func TestFullSyncOnChangeFiresWhenProgressAndFinishWritesFail(t *testing.T) {
	repo, paths, profile := faultyRepository(t)
	addTriggers(t, paths,
		`create trigger block_run_progress before update on sync_runs when new.status = 'running' begin select raise(abort, 'progress blocked'); end`,
		`create trigger block_run_finish before update of status on sync_runs begin select raise(abort, 'finish blocked'); end`,
	)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 60, Name: "Changed Fault", PlaytimeForever: 3}})
	fake.schema[60] = []string{}
	logs := captureLog(t)
	svc, calls := countingService(repo, fake.client)

	result := runServiceOK(t, svc, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	if !strings.Contains(logs.String(), "update sync progress:") || !strings.Contains(logs.String(), "finish sync run:") {
		t.Fatalf("log = %q, want progress and finish write failures", logs.String())
	}
	// One game: start, its progress write and the finish write, each reported once even though it failed.
	if got := int(calls.Load()); got != 3 {
		t.Fatalf("OnChange calls = %d, want exactly 3 (start, progress, finish) despite failed writes", got)
	}
}

// When StartSyncRun fails there is no run to report, so OnChange is not called and FullSync returns the error.
func TestFullSyncOnChangeNotCalledWhenRunStartFails(t *testing.T) {
	repo, paths, profile := faultyRepository(t)
	addTriggers(t, paths,
		`create trigger block_run_start before insert on sync_runs begin select raise(abort, 'start blocked'); end`,
	)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 60, Name: "Never Started", PlaytimeForever: 3}})
	svc, calls := countingService(repo, fake.client)

	if _, err := svc.FullSync(t.Context(), *profile); err == nil {
		t.Fatal("FullSync error = nil, want the start failure")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("OnChange calls = %d, want 0 when StartSyncRun fails", got)
	}
}

// A nil OnChange is allowed: changed() does nothing and the sync completes.
func TestServiceChangedIsNilSafe(t *testing.T) {
	(&Service{}).changed()
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 70, Name: "Nil Hook", PlaytimeForever: 1}})
	fake.schema[70] = []string{}
	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
}
