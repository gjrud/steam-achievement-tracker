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

func (s *Service) FullSync(ctx context.Context, profile repository.Profile) (Result, error) {
	runID, err := s.Repo.StartSyncRun(profile.ID)
	if err != nil {
		return Result{}, err
	}
	result := Result{Status: "success"}
	finish := func(status string, errPtr *string) {
		if err := s.Repo.FinishSyncRun(runID, status, result.GamesTotal, result.GamesSynced, result.GamesFailed, errPtr); err != nil {
			log.Printf("finish sync run: %v", err)
		}
	}

	owned, err := s.Steam.GetOwnedGames(ctx, profile.SteamID64)
	if err != nil {
		errText := err.Error()
		result.Status = "failed"
		result.Error = &errText
		finish(result.Status, result.Error)
		return result, err
	}
	ownedIDs := make([]int64, 0, len(owned))
	for _, g := range owned {
		ownedIDs = append(ownedIDs, g.AppID)
		if err := s.Repo.UpsertOwnedGame(repository.GameRecord{AppID: g.AppID, Name: g.Name, PlaytimeForever: g.PlaytimeForever}); err != nil {
			log.Printf("upsert owned game %d: %v", g.AppID, err)
		}
	}
	if err := s.Repo.DisableMissingOwnedGames(profile.ID, ownedIDs); err != nil {
		log.Printf("disable missing games: %v", err)
	}
	disabledIDs, err := s.Repo.DisabledProfileGameIDs(profile.ID)
	if err != nil {
		errText := err.Error()
		result.Status = "failed"
		result.Error = &errText
		finish(result.Status, result.Error)
		return result, err
	}
	syncable := make([]steam.OwnedGame, 0, len(owned))
	syncableIDs := make([]int64, 0, len(owned))
	for _, g := range owned {
		if disabledIDs[g.AppID] {
			continue
		}
		syncable = append(syncable, g)
		syncableIDs = append(syncableIDs, g.AppID)
	}
	result.GamesTotal = len(syncable)
	coverSources := s.loadCoverSources(ctx, syncableIDs)

	jobs := make(chan steam.OwnedGame)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for game := range jobs {
				if err := s.syncGame(ctx, profile, game, coverSources[game.AppID]); err != nil {
					warning := repository.FormatGameError(game.AppID, err)
					log.Print(warning)
					if saveErr := s.Repo.SaveGameWarning(profile.ID, repository.GameRecord{AppID: game.AppID, Name: game.Name, PlaytimeForever: game.PlaytimeForever}, err.Error()); saveErr != nil {
						log.Printf("save game warning %d: %v", game.AppID, saveErr)
					}
					mu.Lock()
					result.GamesFailed++
					failed := result.GamesFailed
					synced := result.GamesSynced
					total := result.GamesTotal
					mu.Unlock()
					if progressErr := s.Repo.UpdateSyncRunProgress(runID, total, synced, failed); progressErr != nil {
						log.Printf("update sync progress: %v", progressErr)
					}
					continue
				}
				mu.Lock()
				result.GamesSynced++
				failed := result.GamesFailed
				synced := result.GamesSynced
				total := result.GamesTotal
				mu.Unlock()
				if progressErr := s.Repo.UpdateSyncRunProgress(runID, total, synced, failed); progressErr != nil {
					log.Printf("update sync progress: %v", progressErr)
				}
			}
		}()
	}
	for _, game := range syncable {
		select {
		case jobs <- game:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			errText := ctx.Err().Error()
			result.Status = "failed"
			result.Error = &errText
			finish(result.Status, result.Error)
			return result, ctx.Err()
		}
	}
	close(jobs)
	wg.Wait()
	if result.GamesFailed > 0 {
		result.Status = "partial"
		errText := fmt.Sprintf("%d of %d games failed to sync", result.GamesFailed, result.GamesTotal)
		result.Error = &errText
	}
	if result.GamesSynced > 0 {
		if err := s.Repo.TouchProfileSynced(profile.ID); err != nil {
			log.Printf("touch profile synced: %v", err)
		}
	}
	finish(result.Status, result.Error)
	return result, nil
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
		wasCompleted := prev.ManualWasCompleted || prev.WasCompleted || prev.IsCompleted
		update := repository.GameSyncUpdate{
			ProfileID: profile.ID, AppID: game.AppID, Name: game.Name, PlaytimeForever: game.PlaytimeForever,
			HasAchievements: false, AchievementStatus: "no_achievements", WasCompleted: wasCompleted, NewlyIncomplete: prev.ManualWasCompleted, SyncedAt: now,
		}
		if err := s.Repo.SaveGameSync(update); err != nil {
			return err
		}
		s.refreshCover(ctx, game.AppID, coverSourceURL)
		return nil
	}
	playerAchievements, err := s.Steam.GetPlayerAchievements(ctx, profile.SteamID64, game.AppID)
	if err != nil {
		return fmt.Errorf("player achievements: %w", err)
	}
	globalPercentages, err := s.Steam.GetGlobalAchievementPercentages(ctx, game.AppID)
	if err != nil {
		return fmt.Errorf("global percentages: %w", err)
	}

	playerMap := make(map[string]steam.PlayerAchievement, len(playerAchievements))
	for _, a := range playerAchievements {
		playerMap[a.APIName] = a
	}
	records := make([]repository.AchievementRecord, 0, len(schema))
	unlocked := 0
	missingSum := 0.0
	missingKnown := 0
	for _, a := range schema {
		pa := playerMap[a.APIName]
		if pa.Achieved {
			unlocked++
		}
		var gp *float64
		if value, ok := globalPercentages[a.APIName]; ok {
			v := value
			gp = &v
		}
		if !pa.Achieved && gp != nil {
			missingSum += *gp
			missingKnown++
		}
		records = append(records, repository.AchievementRecord{APIName: a.APIName, GlobalPercent: gp, Unlocked: pa.Achieved, UnlockTime: pa.UnlockTime})
	}
	total := len(schema)
	completion := 0.0
	if total > 0 {
		completion = float64(unlocked) / float64(total) * 100
	}
	var missingAvg *float64
	if missingKnown > 0 {
		v := missingSum / float64(missingKnown)
		missingAvg = &v
	}
	score := completion * 0.5
	if missingAvg != nil {
		score += *missingAvg * 0.5
	}
	isCompleted := total > 0 && unlocked == total
	wasCompleted := prev.ManualWasCompleted || prev.WasCompleted || prev.IsCompleted || isCompleted
	newlyIncomplete := (prev.NewlyIncomplete || prev.ManualWasCompleted) && !isCompleted
	if prev.Exists && (prev.WasCompleted || prev.IsCompleted) && !isCompleted && total > prev.TotalAchievements {
		newlyIncomplete = true
	}
	update := repository.GameSyncUpdate{
		ProfileID: profile.ID, AppID: game.AppID, Name: game.Name, PlaytimeForever: game.PlaytimeForever,
		HasAchievements: true, AchievementStatus: "ok", TotalAchievements: total, UnlockedAchievements: unlocked,
		CompletionPercent: completion, MissingAvgUnlock: missingAvg, SuggestionScore: &score, IsCompleted: isCompleted,
		WasCompleted: wasCompleted, NewlyIncomplete: newlyIncomplete, Achievements: records, SyncedAt: now,
	}
	if err := s.Repo.SaveGameSync(update); err != nil {
		return err
	}
	s.refreshCover(ctx, game.AppID, coverSourceURL)
	return nil
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
