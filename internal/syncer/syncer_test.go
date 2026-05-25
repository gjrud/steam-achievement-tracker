package syncer

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	dbinit "github.com/gjrud/steam-achievement-tracker/internal/db"
	"github.com/gjrud/steam-achievement-tracker/internal/repository"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
)

func TestFullSyncRecordsPartialFailureAndKeepsSuccessfulGame(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{
		{AppID: 10, Name: "Good Game", PlaytimeForever: 120},
		{AppID: 20, Name: "Broken Game", PlaytimeForever: 5},
	})
	fake.schema[10] = []string{"ACH_ONE", "ACH_TWO"}
	fake.player[10] = map[string]bool{"ACH_ONE": true, "ACH_TWO": false}
	fake.global[10] = map[string]float64{"ACH_ONE": 80, "ACH_TWO": 30}
	fake.schema[20] = []string{"ACH_FAIL"}
	fake.playerError[20] = "player achievements unavailable"

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "partial", total: 2, synced: 1, failed: 1, hasError: true})
	requireLatestSyncRun(t, repo, profile.ID, expectedSyncResult{status: "partial", total: 2, synced: 1, failed: 1, hasError: true})

	requireSummaryCounts(t, repo, profile.ID, summaryCounts{owned: 2, achievements: 2, unlocked: 1})
	requireWarningCard(t, repo, profile.ID, 20, "player achievements unavailable")
	requireGoodGameState(t, repo, profile.ID, 10)
}

func TestFullSyncClearsWarningAfterRecovery(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 20, Name: "Recovered Game", PlaytimeForever: 45}})
	fake.schema[20] = []string{"ACH_ONE", "ACH_TWO"}
	fake.playerError[20] = "temporary Steam failure"

	first := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, first, expectedSyncResult{status: "partial", total: 1, synced: 0, failed: 1, hasError: true})
	requireWarningCard(t, repo, profile.ID, 20, "temporary Steam failure")

	fake.playerError[20] = ""
	fake.player[20] = map[string]bool{"ACH_ONE": true, "ACH_TWO": true}
	fake.global[20] = map[string]float64{"ACH_ONE": 90, "ACH_TWO": 40}
	second := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, second, expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	requireRecoveredGameState(t, repo, profile.ID, 20)
	requireNoWarningCards(t, repo, profile.ID)
	requireCompletedCard(t, repo, profile.ID, 20)
}

func TestFullSyncCancellationPersistsFailedRun(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	owned := make([]steam.OwnedGame, 20)
	for i := range owned {
		appID := int64(2000 + i)
		owned[i] = steam.OwnedGame{AppID: appID, Name: fmt.Sprintf("Game %d", appID), PlaytimeForever: 1}
	}
	fake := newFakeSteamServer(t, owned)
	fake.delay = 50 * time.Millisecond
	for _, game := range owned {
		fake.schema[game.AppID] = []string{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(ctx, *profile)
	if err == nil || err != context.Canceled {
		t.Fatalf("err = %v, want context canceled", err)
	}
	if result.Status != "failed" || result.Error == nil || result.GamesTotal != len(owned) {
		t.Fatalf("result = %#v, want failed canceled sync with total games", result)
	}
	run, err := repo.LatestSyncRun(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil || run.Status != "failed" || run.Error == nil || !strings.Contains(*run.Error, "context canceled") {
		t.Fatalf("sync run = %#v, want persisted failed cancellation", run)
	}
}

func TestFullSyncSkipsManuallyDisabledGames(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	seedGameSync(t, repo, profile.ID, 99)
	if err := repo.DisableProfileGame(profile.ID, 99); err != nil {
		t.Fatal(err)
	}
	fake := newFakeSteamServer(t, []steam.OwnedGame{
		{AppID: 99, Name: "Disabled Game", PlaytimeForever: 10},
		{AppID: 100, Name: "Enabled Game", PlaytimeForever: 20},
	})
	fake.schema[100] = []string{"ACH"}
	fake.player[100] = map[string]bool{"ACH": true}
	fake.global[100] = map[string]float64{"ACH": 75}

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(context.Background(), *profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.GamesTotal != 1 || result.GamesSynced != 1 || result.GamesFailed != 0 {
		t.Fatalf("result = %#v, want only enabled game synced", result)
	}
	if got := fake.count("schema", 99); got != 0 {
		t.Fatalf("disabled game schema calls = %d, want 0", got)
	}
	disabled, err := repo.GameCards(profile.ID, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled) != 1 || disabled[0].AppID != 99 || !disabled[0].ManualDisabled {
		t.Fatalf("disabled cards = %#v, want manually disabled game only", disabled)
	}
}

func TestFullSyncDisablesNoLongerOwnedGames(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	seedGameSync(t, repo, profile.ID, 50)
	seedGameSync(t, repo, profile.ID, 60)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: 60, Name: "Still Owned", PlaytimeForever: 30}})
	fake.schema[60] = []string{}

	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(context.Background(), *profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "success" || result.GamesTotal != 1 || result.GamesSynced != 1 || result.GamesFailed != 0 {
		t.Fatalf("result = %#v, want only still-owned game synced", result)
	}
	disabled, err := repo.GameCards(profile.ID, "disabled")
	if err != nil {
		t.Fatal(err)
	}
	if len(disabled) != 1 || disabled[0].AppID != 50 || disabled[0].DisabledReason == nil || *disabled[0].DisabledReason != "not_owned" || len(disabled[0].Tags) != 0 {
		t.Fatalf("disabled cards = %#v, want not-owned game with cleared tags", disabled)
	}
	completed, err := repo.GameCards(profile.ID, "completed")
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 0 {
		t.Fatalf("completed cards = %#v, want disabled old completed game hidden", completed)
	}
}

func TestFullSyncLimitsConcurrencyAndPersistsProgress(t *testing.T) {
	repo := testRepository(t)
	profile := testProfile(t, repo)
	owned := make([]steam.OwnedGame, 12)
	for i := range owned {
		appID := int64(1000 + i)
		owned[i] = steam.OwnedGame{AppID: appID, Name: fmt.Sprintf("Game %d", appID), PlaytimeForever: i + 1}
	}
	fake := newFakeSteamServer(t, owned)
	fake.delay = 20 * time.Millisecond
	for _, game := range owned {
		fake.schema[game.AppID] = []string{}
	}

	result := runFullSyncOK(t, repo, fake, profile)
	requireSyncResult(t, result, expectedSyncResult{status: "success", total: len(owned), synced: len(owned), failed: 0})
	requireConcurrentRange(t, fake, 2, 4)
	requireLatestSyncRun(t, repo, profile.ID, expectedSyncResult{status: "success", total: len(owned), synced: len(owned), failed: 0})
	requireSummaryCounts(t, repo, profile.ID, summaryCounts{owned: len(owned)})
}

type expectedSyncResult struct {
	status   string
	total    int
	synced   int
	failed   int
	hasError bool
}

type summaryCounts struct {
	owned        int
	achievements int
	unlocked     int
}

func runFullSyncOK(t *testing.T, repo *repository.Repository, fake *fakeSteamServer, profile *repository.Profile) Result {
	t.Helper()
	result, err := (&Service{Repo: repo, Steam: fake.client}).FullSync(context.Background(), *profile)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requireSyncResult(t *testing.T, result Result, want expectedSyncResult) {
	t.Helper()
	if result.Status != want.status {
		t.Fatalf("status = %q, want %q; result = %#v", result.Status, want.status, result)
	}
	if result.GamesTotal != want.total {
		t.Fatalf("games total = %d, want %d; result = %#v", result.GamesTotal, want.total, result)
	}
	if result.GamesSynced != want.synced {
		t.Fatalf("games synced = %d, want %d; result = %#v", result.GamesSynced, want.synced, result)
	}
	if result.GamesFailed != want.failed {
		t.Fatalf("games failed = %d, want %d; result = %#v", result.GamesFailed, want.failed, result)
	}
	requireErrorPresence(t, result.Error, want.hasError, result)
}

func requireErrorPresence(t *testing.T, errText *string, want bool, context any) {
	t.Helper()
	if want && errText == nil {
		t.Fatalf("error = nil, want error; context = %#v", context)
	}
	if !want && errText != nil {
		t.Fatalf("error = %q, want nil; context = %#v", *errText, context)
	}
}

func requireLatestSyncRun(t *testing.T, repo *repository.Repository, profileID int64, want expectedSyncResult) {
	t.Helper()
	run, err := repo.LatestSyncRun(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if run == nil {
		t.Fatal("sync run = nil, want latest run")
	}
	got := Result{Status: run.Status, GamesTotal: run.GamesTotal, GamesSynced: run.GamesSynced, GamesFailed: run.GamesFailed, Error: run.Error}
	requireSyncResult(t, got, want)
}

func requireSummaryCounts(t *testing.T, repo *repository.Repository, profileID int64, want summaryCounts) {
	t.Helper()
	summary, err := repo.Summary(profileID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.OwnedGamesCount != want.owned {
		t.Fatalf("owned games count = %d, want %d", summary.OwnedGamesCount, want.owned)
	}
	if summary.TotalAchievementsAvailable != want.achievements {
		t.Fatalf("total achievements = %d, want %d", summary.TotalAchievementsAvailable, want.achievements)
	}
	if summary.TotalAchievementsUnlocked != want.unlocked {
		t.Fatalf("unlocked achievements = %d, want %d", summary.TotalAchievementsUnlocked, want.unlocked)
	}
}

func requireWarningCard(t *testing.T, repo *repository.Repository, profileID int64, appID int64, message string) {
	t.Helper()
	warnings := requireGameCards(t, repo, profileID, "warnings")
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want one warning", warnings)
	}
	card := warnings[0]
	if card.AppID != appID {
		t.Fatalf("warning appID = %d, want %d", card.AppID, appID)
	}
	if !card.SyncWarning {
		t.Fatalf("warning card = %#v, want sync warning", card)
	}
	requireStringPtrContains(t, card.LastError, message, card)
}

func requireGoodGameState(t *testing.T, repo *repository.Repository, profileID int64, appID int64) {
	t.Helper()
	state := requireProfileGame(t, repo, profileID, appID)
	if !state.Exists {
		t.Fatalf("good game state = %#v, want existing state", state)
	}
	if state.TotalAchievements != 2 {
		t.Fatalf("good total achievements = %d, want 2", state.TotalAchievements)
	}
	if state.UnlockedAchievements != 1 {
		t.Fatalf("good unlocked achievements = %d, want 1", state.UnlockedAchievements)
	}
	if state.SyncWarning {
		t.Fatalf("good game state = %#v, want no warning", state)
	}
}

func requireRecoveredGameState(t *testing.T, repo *repository.Repository, profileID int64, appID int64) {
	t.Helper()
	state := requireProfileGame(t, repo, profileID, appID)
	if !state.Exists {
		t.Fatalf("recovered state = %#v, want existing state", state)
	}
	if state.SyncWarning {
		t.Fatalf("recovered state = %#v, want no warning", state)
	}
	if state.TotalAchievements != 2 {
		t.Fatalf("recovered total achievements = %d, want 2", state.TotalAchievements)
	}
	if state.UnlockedAchievements != 2 {
		t.Fatalf("recovered unlocked achievements = %d, want 2", state.UnlockedAchievements)
	}
	if !state.IsCompleted {
		t.Fatalf("recovered state = %#v, want completed", state)
	}
}

func requireProfileGame(t *testing.T, repo *repository.Repository, profileID int64, appID int64) repository.ProfileGameState {
	t.Helper()
	state, err := repo.ProfileGame(profileID, appID)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func requireNoWarningCards(t *testing.T, repo *repository.Repository, profileID int64) {
	t.Helper()
	warnings := requireGameCards(t, repo, profileID, "warnings")
	if len(warnings) != 0 {
		t.Fatalf("warnings after recovery = %#v, want cleared warning", warnings)
	}
}

func requireCompletedCard(t *testing.T, repo *repository.Repository, profileID int64, appID int64) {
	t.Helper()
	completed := requireGameCards(t, repo, profileID, "completed")
	if len(completed) != 1 {
		t.Fatalf("completed after recovery = %#v, want one completed card", completed)
	}
	if completed[0].AppID != appID {
		t.Fatalf("completed appID = %d, want %d", completed[0].AppID, appID)
	}
	if completed[0].LastError != nil {
		t.Fatalf("completed after recovery = %#v, want no error", completed)
	}
}

func requireGameCards(t *testing.T, repo *repository.Repository, profileID int64, filter string) []repository.GameCard {
	t.Helper()
	cards, err := repo.GameCards(profileID, filter)
	if err != nil {
		t.Fatal(err)
	}
	return cards
}

func requireStringPtrContains(t *testing.T, got *string, want string, context any) {
	t.Helper()
	if got == nil {
		t.Fatalf("string = nil, want %q; context = %#v", want, context)
	}
	if !strings.Contains(*got, want) {
		t.Fatalf("string = %q, want substring %q; context = %#v", *got, want, context)
	}
}

func requireConcurrentRange(t *testing.T, fake *fakeSteamServer, min, max int) {
	t.Helper()
	got := fake.maxConcurrent()
	if got < min {
		t.Fatalf("max concurrent per-game requests = %d, want at least %d", got, min)
	}
	if got > max {
		t.Fatalf("max concurrent per-game requests = %d, want at most %d", got, max)
	}
}

type fakeSteamServer struct {
	server      *httptest.Server
	client      *steam.Client
	schema      map[int64][]string
	player      map[int64]map[string]bool
	global      map[int64]map[string]float64
	playerError map[int64]string
	delay       time.Duration
	mu          sync.Mutex
	counts      map[string]int
	active      int
	maxActive   int
}

func newFakeSteamServer(t *testing.T, owned []steam.OwnedGame) *fakeSteamServer {
	t.Helper()
	f := &fakeSteamServer{
		schema:      make(map[int64][]string),
		player:      make(map[int64]map[string]bool),
		global:      make(map[int64]map[string]float64),
		playerError: make(map[int64]string),
		counts:      make(map[string]int),
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/IPlayerService/GetOwnedGames/v0001/":
			f.increment("owned", 0)
			fmt.Fprint(w, ownedGamesJSON(owned))
		case "/ISteamUserStats/GetSchemaForGame/v2/":
			appID := int64Query(t, r, "appid")
			done := f.enterPerGameRequest()
			defer done()
			f.increment("schema", appID)
			fmt.Fprint(w, schemaJSON(f.schema[appID]))
		case "/ISteamUserStats/GetPlayerAchievements/v1/":
			appID := int64Query(t, r, "appid")
			done := f.enterPerGameRequest()
			defer done()
			f.increment("player", appID)
			if msg := f.playerError[appID]; msg != "" {
				fmt.Fprintf(w, `{"playerstats":{"success":false,"error":%q}}`, msg)
				return
			}
			fmt.Fprint(w, playerJSON(f.player[appID]))
		case "/ISteamUserStats/GetGlobalAchievementPercentagesForApp/v0002/":
			appID := int64Query(t, r, "gameid")
			done := f.enterPerGameRequest()
			defer done()
			f.increment("global", appID)
			fmt.Fprint(w, globalJSON(f.global[appID]))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	f.client = &steam.Client{APIKey: "test-key", BaseURL: f.server.URL, HTTPClient: f.server.Client()}
	return f
}

func (f *fakeSteamServer) increment(kind string, appID int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts[fmt.Sprintf("%s:%d", kind, appID)]++
}

func (f *fakeSteamServer) count(kind string, appID int64) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[fmt.Sprintf("%s:%d", kind, appID)]
}

func (f *fakeSteamServer) enterPerGameRequest() func() {
	f.mu.Lock()
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}
}

func (f *fakeSteamServer) maxConcurrent() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxActive
}

func ownedGamesJSON(games []steam.OwnedGame) string {
	parts := make([]string, 0, len(games))
	for _, g := range games {
		parts = append(parts, fmt.Sprintf(`{"appid":%d,"name":%q,"playtime_forever":%d}`, g.AppID, g.Name, g.PlaytimeForever))
	}
	return fmt.Sprintf(`{"response":{"game_count":%d,"games":[%s]}}`, len(games), strings.Join(parts, ","))
}

func schemaJSON(names []string) string {
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf(`{"name":%q,"displayName":%q}`, name, name))
	}
	return fmt.Sprintf(`{"game":{"availableGameStats":{"achievements":[%s]}}}`, strings.Join(parts, ","))
}

func playerJSON(values map[string]bool) string {
	parts := make([]string, 0, len(values))
	for name, achieved := range values {
		achievedInt := 0
		if achieved {
			achievedInt = 1
		}
		parts = append(parts, fmt.Sprintf(`{"apiname":%q,"achieved":%d,"unlocktime":0}`, name, achievedInt))
	}
	return fmt.Sprintf(`{"playerstats":{"success":true,"achievements":[%s]}}`, strings.Join(parts, ","))
}

func globalJSON(values map[string]float64) string {
	parts := make([]string, 0, len(values))
	for name, percent := range values {
		parts = append(parts, fmt.Sprintf(`{"name":%q,"percent":%.2f}`, name, percent))
	}
	return fmt.Sprintf(`{"achievementpercentages":{"achievements":[%s]}}`, strings.Join(parts, ","))
}

func int64Query(t *testing.T, r *http.Request, key string) int64 {
	t.Helper()
	value, err := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	if err != nil {
		t.Fatalf("bad %s query %q: %v", key, r.URL.Query().Get(key), err)
	}
	return value
}

func testProfile(t *testing.T, repo *repository.Repository) *repository.Profile {
	t.Helper()
	profile, err := repo.SaveProfile("11111111111111111", "User One", "")
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func testRepository(t *testing.T) *repository.Repository {
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
	return repository.New(db)
}

func seedGameSync(t *testing.T, repo *repository.Repository, profileID, appID int64) {
	t.Helper()
	global := 42.0
	now := time.Now().UTC().Format(time.RFC3339)
	if err := repo.SaveGameSync(repository.GameSyncUpdate{
		ProfileID:            profileID,
		AppID:                appID,
		Name:                 fmt.Sprintf("Game %d", appID),
		PlaytimeForever:      60,
		HasAchievements:      true,
		AchievementStatus:    "ok",
		TotalAchievements:    1,
		UnlockedAchievements: 1,
		CompletionPercent:    100,
		IsCompleted:          true,
		WasCompleted:         true,
		Achievements:         []repository.AchievementRecord{{APIName: "ACH", GlobalPercent: &global, Unlocked: true}},
		SyncedAt:             now,
	}); err != nil {
		t.Fatal(err)
	}
}
