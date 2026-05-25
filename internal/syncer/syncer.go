package syncer

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/images"
	"github.com/gjrud/steam-achievement-tracker/internal/repository"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
)

type Service struct {
	Repo   *repository.Repository
	Steam  *steam.Client
	Images *images.Cache
}

type Result struct {
	GamesTotal  int
	GamesSynced int
	GamesFailed int
	Status      string
	Error       *string
}

type fullSyncRun struct {
	s       *Service
	profile repository.Profile
	runID   int64
	result  Result
	mu      sync.Mutex
}

func (s *Service) FullSync(ctx context.Context, profile repository.Profile) (Result, error) {
	runID, err := s.Repo.StartSyncRun(profile.ID)
	if err != nil {
		return Result{}, err
	}
	run := &fullSyncRun{s: s, profile: profile, runID: runID, result: Result{Status: "success"}}

	owned, err := s.prepareOwnedGames(ctx, profile)
	if err != nil {
		return run.fail(err)
	}
	disabledIDs, err := s.Repo.DisabledProfileGameIDs(profile.ID)
	if err != nil {
		return run.fail(err)
	}
	syncable, syncableIDs := syncableGames(owned, disabledIDs)
	run.result.GamesTotal = len(syncable)
	coverSources := s.loadCoverSources(ctx, syncableIDs)
	if err := run.syncGames(ctx, syncable, coverSources); err != nil {
		return run.fail(err)
	}
	run.finalize()
	run.finish()
	return run.result, nil
}

func (s *Service) prepareOwnedGames(ctx context.Context, profile repository.Profile) ([]steam.OwnedGame, error) {
	owned, err := s.Steam.GetOwnedGames(ctx, profile.SteamID64)
	if err != nil {
		return nil, err
	}
	ownedIDs := make([]int64, 0, len(owned))
	for _, g := range owned {
		ownedIDs = append(ownedIDs, g.AppID)
		if err := s.Repo.UpsertOwnedGame(gameRecord(g)); err != nil {
			log.Printf("upsert owned game %d: %v", g.AppID, err)
		}
	}
	if err := s.Repo.DisableMissingOwnedGames(profile.ID, ownedIDs); err != nil {
		log.Printf("disable missing games: %v", err)
	}
	return owned, nil
}

func syncableGames(owned []steam.OwnedGame, disabledIDs map[int64]bool) ([]steam.OwnedGame, []int64) {
	syncable := make([]steam.OwnedGame, 0, len(owned))
	ids := make([]int64, 0, len(owned))
	for _, g := range owned {
		if disabledIDs[g.AppID] {
			continue
		}
		syncable = append(syncable, g)
		ids = append(ids, g.AppID)
	}
	return syncable, ids
}

func gameRecord(g steam.OwnedGame) repository.GameRecord {
	return repository.GameRecord{AppID: g.AppID, Name: g.Name, PlaytimeForever: g.PlaytimeForever}
}

func (r *fullSyncRun) syncGames(ctx context.Context, games []steam.OwnedGame, coverSources map[int64]string) error {
	jobs := make(chan steam.OwnedGame)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go r.syncWorker(ctx, jobs, coverSources, &wg)
	}
	err := dispatchSyncJobs(ctx, jobs, games)
	close(jobs)
	wg.Wait()
	return err
}

func (r *fullSyncRun) syncWorker(ctx context.Context, jobs <-chan steam.OwnedGame, coverSources map[int64]string, wg *sync.WaitGroup) {
	defer wg.Done()
	for game := range jobs {
		if err := r.s.syncGame(ctx, r.profile, game, coverSources[game.AppID]); err != nil {
			r.recordGameFailure(game, err)
			continue
		}
		r.recordGameSuccess()
	}
}

func dispatchSyncJobs(ctx context.Context, jobs chan<- steam.OwnedGame, games []steam.OwnedGame) error {
	for _, game := range games {
		select {
		case jobs <- game:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (r *fullSyncRun) recordGameFailure(game steam.OwnedGame, err error) {
	warning := repository.FormatGameError(game.AppID, err)
	log.Print(warning)
	if saveErr := r.s.Repo.SaveGameWarning(r.profile.ID, gameRecord(game), err.Error()); saveErr != nil {
		log.Printf("save game warning %d: %v", game.AppID, saveErr)
	}
	r.mu.Lock()
	r.result.GamesFailed++
	total, synced, failed := r.progressLocked()
	r.mu.Unlock()
	r.updateProgress(total, synced, failed)
}

func (r *fullSyncRun) recordGameSuccess() {
	r.mu.Lock()
	r.result.GamesSynced++
	total, synced, failed := r.progressLocked()
	r.mu.Unlock()
	r.updateProgress(total, synced, failed)
}

func (r *fullSyncRun) progressLocked() (int, int, int) {
	return r.result.GamesTotal, r.result.GamesSynced, r.result.GamesFailed
}

func (r *fullSyncRun) updateProgress(total, synced, failed int) {
	if err := r.s.Repo.UpdateSyncRunProgress(r.runID, total, synced, failed); err != nil {
		log.Printf("update sync progress: %v", err)
	}
}

func (r *fullSyncRun) finalize() {
	if r.result.GamesFailed > 0 {
		r.result.Status = "partial"
		errText := fmt.Sprintf("%d of %d games failed to sync", r.result.GamesFailed, r.result.GamesTotal)
		r.result.Error = &errText
	}
	if r.result.GamesSynced > 0 {
		r.touchProfileSynced()
	}
}

func (r *fullSyncRun) touchProfileSynced() {
	if err := r.s.Repo.TouchProfileSynced(r.profile.ID); err != nil {
		log.Printf("touch profile synced: %v", err)
	}
}

func (r *fullSyncRun) fail(err error) (Result, error) {
	errText := err.Error()
	r.result.Status = "failed"
	r.result.Error = &errText
	r.finish()
	return r.result, err
}

func (r *fullSyncRun) finish() {
	if err := r.s.Repo.FinishSyncRun(r.runID, r.result.Status, r.result.GamesTotal, r.result.GamesSynced, r.result.GamesFailed, r.result.Error); err != nil {
		log.Printf("finish sync run: %v", err)
	}
}

func (s *Service) syncGame(ctx context.Context, profile repository.Profile, game steam.OwnedGame, coverSourceURL string) error {
	schema, err := s.Steam.GetSchemaForGame(ctx, game.AppID)
	if err != nil {
		return fmt.Errorf("achievement schema: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	prev, err := s.Repo.ProfileGame(profile.ID, game.AppID)
	if err != nil {
		return err
	}
	if len(schema) == 0 {
		return s.saveGameWithoutAchievements(ctx, profile, game, prev, coverSourceURL, now)
	}
	playerAchievements, err := s.Steam.GetPlayerAchievements(ctx, profile.SteamID64, game.AppID)
	if err != nil {
		return fmt.Errorf("player achievements: %w", err)
	}
	globalPercentages, err := s.Steam.GetGlobalAchievementPercentages(ctx, game.AppID)
	if err != nil {
		return fmt.Errorf("global percentages: %w", err)
	}
	stats := buildAchievementStats(schema, playerAchievements, globalPercentages)
	update := achievementSyncUpdate(profile, game, prev, stats, now)
	if err := s.Repo.SaveGameSync(update); err != nil {
		return err
	}
	s.refreshCover(ctx, game.AppID, coverSourceURL)
	return nil
}

func (s *Service) saveGameWithoutAchievements(ctx context.Context, profile repository.Profile, game steam.OwnedGame, prev repository.ProfileGameState, coverSourceURL, now string) error {
	update := repository.GameSyncUpdate{
		ProfileID: profile.ID, AppID: game.AppID, Name: game.Name, PlaytimeForever: game.PlaytimeForever,
		HasAchievements: false, AchievementStatus: "no_achievements", WasCompleted: wasEverCompleted(prev), NewlyIncomplete: prev.ManualWasCompleted, SyncedAt: now,
	}
	if err := s.Repo.SaveGameSync(update); err != nil {
		return err
	}
	s.refreshCover(ctx, game.AppID, coverSourceURL)
	return nil
}

type achievementStats struct {
	records    []repository.AchievementRecord
	total      int
	unlocked   int
	completion float64
	missingAvg *float64
	score      float64
}

func buildAchievementStats(schema []steam.SchemaAchievement, playerAchievements []steam.PlayerAchievement, globalPercentages map[string]float64) achievementStats {
	playerMap := playerAchievementMap(playerAchievements)
	stats := achievementStats{records: make([]repository.AchievementRecord, 0, len(schema)), total: len(schema)}
	missingSum := 0.0
	missingKnown := 0
	for _, a := range schema {
		pa := playerMap[a.APIName]
		gp := globalPercentPtr(globalPercentages, a.APIName)
		stats.addAchievement(a, pa, gp, &missingSum, &missingKnown)
	}
	stats.completion = completionPercent(stats.unlocked, stats.total)
	stats.missingAvg = averagePtr(missingSum, missingKnown)
	stats.score = suggestionScore(stats.completion, stats.missingAvg)
	return stats
}

func playerAchievementMap(playerAchievements []steam.PlayerAchievement) map[string]steam.PlayerAchievement {
	playerMap := make(map[string]steam.PlayerAchievement, len(playerAchievements))
	for _, a := range playerAchievements {
		playerMap[a.APIName] = a
	}
	return playerMap
}

func (s *achievementStats) addAchievement(a steam.SchemaAchievement, pa steam.PlayerAchievement, gp *float64, missingSum *float64, missingKnown *int) {
	if pa.Achieved {
		s.unlocked++
	}
	if missingAchievementWithKnownGlobal(pa, gp) {
		*missingSum += *gp
		*missingKnown++
	}
	s.records = append(s.records, repository.AchievementRecord{APIName: a.APIName, GlobalPercent: gp, Unlocked: pa.Achieved, UnlockTime: pa.UnlockTime})
}

func globalPercentPtr(globalPercentages map[string]float64, apiName string) *float64 {
	value, ok := globalPercentages[apiName]
	if !ok {
		return nil
	}
	return &value
}

func missingAchievementWithKnownGlobal(pa steam.PlayerAchievement, gp *float64) bool {
	return !pa.Achieved && gp != nil
}

func completionPercent(unlocked, total int) float64 {
	if total == 0 {
		return 0
	}
	return float64(unlocked) / float64(total) * 100
}

func averagePtr(sum float64, count int) *float64 {
	if count == 0 {
		return nil
	}
	value := sum / float64(count)
	return &value
}

func suggestionScore(completion float64, missingAvg *float64) float64 {
	score := completion * 0.5
	if missingAvg != nil {
		score += *missingAvg * 0.5
	}
	return score
}

func achievementSyncUpdate(profile repository.Profile, game steam.OwnedGame, prev repository.ProfileGameState, stats achievementStats, now string) repository.GameSyncUpdate {
	isCompleted := stats.total > 0 && stats.unlocked == stats.total
	return repository.GameSyncUpdate{
		ProfileID: profile.ID, AppID: game.AppID, Name: game.Name, PlaytimeForever: game.PlaytimeForever,
		HasAchievements: true, AchievementStatus: "ok", TotalAchievements: stats.total, UnlockedAchievements: stats.unlocked,
		CompletionPercent: stats.completion, MissingAvgUnlock: stats.missingAvg, SuggestionScore: &stats.score, IsCompleted: isCompleted,
		WasCompleted: wasEverCompletedNow(prev, isCompleted), NewlyIncomplete: newlyIncomplete(prev, isCompleted, stats.total), Achievements: stats.records, SyncedAt: now,
	}
}

func wasEverCompleted(prev repository.ProfileGameState) bool {
	return prev.ManualWasCompleted || prev.WasCompleted || prev.IsCompleted
}

func wasEverCompletedNow(prev repository.ProfileGameState, isCompleted bool) bool {
	return wasEverCompleted(prev) || isCompleted
}

func newlyIncomplete(prev repository.ProfileGameState, isCompleted bool, total int) bool {
	if incompleteAfterManualFlag(prev, isCompleted) {
		return true
	}
	return incompleteAfterNewAchievements(prev, isCompleted, total)
}

func incompleteAfterManualFlag(prev repository.ProfileGameState, isCompleted bool) bool {
	return (prev.NewlyIncomplete || prev.ManualWasCompleted) && !isCompleted
}

func incompleteAfterNewAchievements(prev repository.ProfileGameState, isCompleted bool, total int) bool {
	return prev.Exists && wasCompletedBefore(prev) && !isCompleted && total > prev.TotalAchievements
}

func wasCompletedBefore(prev repository.ProfileGameState) bool {
	return prev.WasCompleted || prev.IsCompleted
}

func (s *Service) loadCoverSources(ctx context.Context, appIDs []int64) map[int64]string {
	if s.Images == nil {
		return nil
	}
	sources, err := s.Images.LibraryCapsuleSourceURLs(ctx, appIDs)
	if err != nil {
		log.Printf("load cover sources: %v", err)
		return nil
	}
	return sources
}

func (s *Service) refreshCover(ctx context.Context, appID int64, sourceURL string) {
	if s.Images == nil {
		return
	}
	res, err := s.Images.RefreshCover(ctx, appID, sourceURL)
	if err != nil {
		log.Printf("refresh cover %d: %v", appID, err)
		return
	}
	if err := s.Repo.SaveCover(appID, res.Path, res.SourceURL, res.Downloaded); err != nil {
		log.Printf("save cover %d: %v", appID, err)
	}
}
