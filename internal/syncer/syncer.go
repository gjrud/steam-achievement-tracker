package syncer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
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
	syncable := syncableGames(owned, disabledIDs)
	run.result.GamesTotal = len(syncable)
	if err := run.syncGames(ctx, syncable); err != nil {
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

func syncableGames(owned []steam.OwnedGame, disabledIDs map[int64]bool) []steam.OwnedGame {
	syncable := make([]steam.OwnedGame, 0, len(owned))
	for _, g := range owned {
		if disabledIDs[g.AppID] {
			continue
		}
		syncable = append(syncable, g)
	}
	return syncable
}

func gameRecord(g steam.OwnedGame) repository.GameRecord {
	return repository.GameRecord{AppID: g.AppID, Name: g.Name, PlaytimeForever: g.PlaytimeForever}
}

func (r *fullSyncRun) syncGames(ctx context.Context, games []steam.OwnedGame) error {
	jobs := make(chan steam.OwnedGame)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go r.syncWorker(ctx, jobs, &wg)
	}
	err := dispatchSyncJobs(ctx, jobs, games)
	close(jobs)
	wg.Wait()
	if err == nil {
		// Cancel can land after every game was handed off: dispatch then has no error, but in-flight games
		// were interrupted and skipped. The run must still end as canceled, not as success.
		err = ctx.Err()
	}
	return err
}

func (r *fullSyncRun) syncWorker(ctx context.Context, jobs <-chan steam.OwnedGame, wg *sync.WaitGroup) {
	defer wg.Done()
	for game := range jobs {
		if err := r.s.syncGame(ctx, r.profile, game); err != nil {
			// Interrupted by cancellation, not a game failure: no warning, and the game keeps its stored data.
			// On a failed (canceled) run, games interrupted mid-sync count as neither synced nor failed.
			if ctx.Err() == nil {
				r.recordGameFailure(game, err)
			}
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

// syncGame syncs one game for the profile: every eligible achievement set becomes one entry.
// The new IPlayerService API is used first; only non-transient failures fall back to the legacy API.
func (s *Service) syncGame(ctx context.Context, profile repository.Profile, game steam.OwnedGame) error {
	now := time.Now().UTC().Format(time.RFC3339)
	schema, err := s.Steam.GetGameAchievements(ctx, game.AppID)
	if err != nil {
		if !shouldFallbackToLegacy(err) {
			return fmt.Errorf("achievement schema: %w", err)
		}
		// Legacy writes one group-0 entry and prunes every other entry of the game, so it must not run
		// for an app that already has set data. The failure then surfaces and existing entries are kept.
		hasSets, checkErr := s.Repo.HasSetData(game.AppID)
		if checkErr != nil {
			return fmt.Errorf("achievement schema: %w (set data check: %v)", err, checkErr)
		}
		if hasSets {
			return fmt.Errorf("achievement schema: %w", err)
		}
		log.Printf("appid %d: GetGameAchievements failed, using legacy achievement API: %v", game.AppID, err)
		return s.syncGameLegacy(ctx, profile, game, now)
	}
	if len(schema.Achievements) == 0 {
		return s.saveGameWithoutAchievements(ctx, profile, game, now)
	}
	user, err := s.Steam.GetUserAchievements(ctx, profile.SteamID64, game.AppID)
	if err != nil {
		return fmt.Errorf("user achievements: %w", err)
	}
	entries := buildSetEntries(game.AppID, schema, user)
	return s.saveEntries(ctx, profile, game, entries, now)
}

// syncGameLegacy is the pre-IPlayerService path: one group-0 entry from the old Web API endpoints.
// Remove together with the legacy client methods once Steam documents GetGameAchievements.
func (s *Service) syncGameLegacy(ctx context.Context, profile repository.Profile, game steam.OwnedGame, now string) error {
	schema, err := s.Steam.GetSchemaForGame(ctx, game.AppID)
	if err != nil {
		return fmt.Errorf("achievement schema: %w", err)
	}
	if len(schema) == 0 {
		return s.saveGameWithoutAchievements(ctx, profile, game, now)
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
	entries := []setEntry{{GroupID: 0, ImageAppID: game.AppID, stats: stats}}
	return s.saveEntries(ctx, profile, game, entries, now)
}

// saveGameWithoutAchievements stores the game as no_achievements with its group-0 entry only.
func (s *Service) saveGameWithoutAchievements(ctx context.Context, profile repository.Profile, game steam.OwnedGame, now string) error {
	prev, err := s.Repo.ProfileGame(profile.ID, game.AppID, 0)
	if err != nil {
		return err
	}
	update := repository.GameSyncUpdate{
		ProfileID: profile.ID, AppID: game.AppID, Name: game.Name, PlaytimeForever: game.PlaytimeForever,
		HasAchievements: false, AchievementStatus: "no_achievements", SyncedAt: now,
		Sets: []repository.SetSyncUpdate{{GroupID: 0, WasCompleted: wasEverCompleted(prev), NewlyIncomplete: prev.ManualWasCompleted}},
	}
	if err := s.Repo.SaveGameSync(update); err != nil {
		return err
	}
	s.refreshCovers(ctx, game, []int64{game.AppID})
	return nil
}

// saveEntries writes one successful game sync with the given entries, then refreshes the covers they show.
// Entries may be empty (game has achievements but none is eligible); that prunes all entries of the game.
func (s *Service) saveEntries(ctx context.Context, profile repository.Profile, game steam.OwnedGame, entries []setEntry, now string) error {
	sets := make([]repository.SetSyncUpdate, 0, len(entries))
	for _, entry := range entries {
		prev, err := s.Repo.ProfileGame(profile.ID, game.AppID, entry.GroupID)
		if err != nil {
			return err
		}
		sets = append(sets, setSyncUpdate(entry, prev))
	}
	update := repository.GameSyncUpdate{
		ProfileID: profile.ID, AppID: game.AppID, Name: game.Name, PlaytimeForever: game.PlaytimeForever,
		HasAchievements: true, AchievementStatus: "ok", Sets: sets, SyncedAt: now,
	}
	if err := s.Repo.SaveGameSync(update); err != nil {
		return err
	}
	s.refreshCovers(ctx, game, coverImageAppIDs(game.AppID, entries))
	return nil
}

// setEntry is one eligible achievement set (appid + groupid) of a game for a profile.
type setEntry struct {
	GroupID     int64
	Name        string
	DLCAppID    int64
	ImageAppID  int64 // DLC id when the set is DLC-bound, else the game id
	Order       int
	DLCNotOwned bool // DLC-bound set the profile does not own; shown with the dlc_not_owned tag
	stats       achievementStats
}

// buildSetEntries applies the set eligibility rules to a game schema and a player's unlocks.
// A set is an entry when it is present, public, not developer-only and not archived, and has at least one
// non-archived achievement. DLC ownership does not hide a set: a DLC-bound set (groupid != 0, dlcappid != 0)
// the player does not own is still an entry, flagged DLCNotOwned.
// A DLC group counts as owned when it is present in the user response or the player has any unlocked
// achievement in it. Achievements whose groupid has no set metadata get a default set (see withDefaultSets).
// Totals count non-archived achievements only. Archived achievements are kept in the entry records.
// The result follows the schema set order; duplicate groupids keep the first set.
func buildSetEntries(appID int64, game steam.GameAchievements, user steam.UserAchievements) []setEntry {
	byGroup := make(map[int64][]steam.SetAchievement)
	for _, a := range game.Achievements {
		byGroup[a.GroupID] = append(byGroup[a.GroupID], a)
	}
	sets := withDefaultSets(game.Sets, byGroup)
	seen := make(map[int64]bool, len(sets))
	entries := make([]setEntry, 0, len(sets))
	for _, set := range sets {
		if seen[set.GroupID] || !setEligible(set) {
			continue
		}
		seen[set.GroupID] = true
		entry, ok := buildSetEntry(appID, set, byGroup[set.GroupID], user)
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

// withDefaultSets appends a default set (public, no DLC, unnamed) for every groupid that has achievements
// but no metadata in sets, including group 0 when sets is empty. Added sets follow in ascending groupid order.
func withDefaultSets(sets []steam.AchievementSet, byGroup map[int64][]steam.SetAchievement) []steam.AchievementSet {
	known := make(map[int64]bool, len(sets))
	for _, set := range sets {
		known[set.GroupID] = true
	}
	var missing []int64
	for groupID := range byGroup {
		if !known[groupID] {
			missing = append(missing, groupID)
		}
	}
	if len(missing) == 0 {
		return sets
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	out := make([]steam.AchievementSet, 0, len(sets)+len(missing))
	out = append(out, sets...)
	for _, groupID := range missing {
		out = append(out, steam.AchievementSet{GroupID: groupID, Public: true})
	}
	return out
}

// setEligible reports whether a set is shown at all. DLC ownership is not checked here; see dlcNotOwned.
func setEligible(set steam.AchievementSet) bool {
	return !(set.Archived || set.DeveloperOnly || !set.Public)
}

// dlcNotOwned reports a DLC-bound set whose DLC the player does not own: no group flag and no unlocked achievement.
func dlcNotOwned(set steam.AchievementSet, achievements []steam.SetAchievement, user steam.UserAchievements) bool {
	return set.GroupID != 0 && set.DLCAppID != 0 && !user.Groups[set.GroupID] && !anyUnlocked(achievements, user)
}

// anyUnlocked reports whether the player has unlocked at least one of the given achievements.
func anyUnlocked(achievements []steam.SetAchievement, user steam.UserAchievements) bool {
	for _, a := range achievements {
		if _, ok := user.Unlocked[a.Key]; ok {
			return true
		}
	}
	return false
}

func buildSetEntry(appID int64, set steam.AchievementSet, achievements []steam.SetAchievement, user steam.UserAchievements) (setEntry, bool) {
	var tally statsTally
	records := make([]repository.AchievementRecord, 0, len(achievements))
	for _, a := range achievements {
		unlockTime, unlocked := user.Unlocked[a.Key]
		records = append(records, repository.AchievementRecord{
			APIName: a.InternalName, GlobalPercent: a.GlobalPercent, Unlocked: unlocked, UnlockTime: unlockTime, Archived: a.Archived,
		})
		if !a.Archived {
			tally.add(unlocked, a.GlobalPercent)
		}
	}
	if tally.total == 0 {
		return setEntry{}, false
	}
	imageAppID := appID
	if set.DLCAppID != 0 {
		imageAppID = set.DLCAppID
	}
	return setEntry{
		GroupID: set.GroupID, Name: set.Name, DLCAppID: set.DLCAppID, ImageAppID: imageAppID, Order: set.Order,
		DLCNotOwned: dlcNotOwned(set, achievements, user),
		stats:       tally.stats(records),
	}, true
}

// setSyncUpdate converts an entry into its repository update. Completion and the was/newly-incomplete flags
// are evaluated against the entry's own previous state.
func setSyncUpdate(entry setEntry, prev repository.ProfileGameState) repository.SetSyncUpdate {
	stats := entry.stats
	isCompleted := stats.total > 0 && stats.unlocked == stats.total
	score := stats.score
	return repository.SetSyncUpdate{
		GroupID: entry.GroupID, Name: entry.Name, DLCAppID: entry.DLCAppID, DLCNotOwned: entry.DLCNotOwned, Order: entry.Order,
		TotalAchievements: stats.total, UnlockedAchievements: stats.unlocked, CompletionPercent: stats.completion,
		MissingAvgUnlock: stats.missingAvg, SuggestionScore: &score, IsCompleted: isCompleted,
		WasCompleted: wasEverCompletedNow(prev, isCompleted), NewlyIncomplete: newlyIncomplete(prev, isCompleted, stats.total),
		Achievements: stats.records,
	}
}

type achievementStats struct {
	records    []repository.AchievementRecord
	total      int
	unlocked   int
	completion float64
	missingAvg *float64
	score      float64
}

// statsTally counts countable achievements and the missing-average input.
type statsTally struct {
	total        int
	unlocked     int
	missingSum   float64
	missingKnown int
}

func (t *statsTally) add(unlocked bool, globalPercent *float64) {
	t.total++
	if unlocked {
		t.unlocked++
		return
	}
	if globalPercent != nil {
		t.missingSum += *globalPercent
		t.missingKnown++
	}
}

func (t statsTally) stats(records []repository.AchievementRecord) achievementStats {
	s := achievementStats{records: records, total: t.total, unlocked: t.unlocked}
	s.completion = completionPercent(s.unlocked, s.total)
	s.missingAvg = averagePtr(t.missingSum, t.missingKnown)
	s.score = suggestionScore(s.completion, s.missingAvg)
	return s
}

// buildAchievementStats is the legacy-API variant: schema plus player and global data, one group-0 entry.
func buildAchievementStats(schema []steam.SchemaAchievement, playerAchievements []steam.PlayerAchievement, globalPercentages map[string]float64) achievementStats {
	playerMap := playerAchievementMap(playerAchievements)
	var tally statsTally
	records := make([]repository.AchievementRecord, 0, len(schema))
	for _, a := range schema {
		pa := playerMap[a.APIName]
		gp := globalPercentPtr(globalPercentages, a.APIName)
		tally.add(pa.Achieved, gp)
		records = append(records, repository.AchievementRecord{APIName: a.APIName, GlobalPercent: gp, Unlocked: pa.Achieved, UnlockTime: pa.UnlockTime})
	}
	return tally.stats(records)
}

func playerAchievementMap(playerAchievements []steam.PlayerAchievement) map[string]steam.PlayerAchievement {
	playerMap := make(map[string]steam.PlayerAchievement, len(playerAchievements))
	for _, a := range playerAchievements {
		playerMap[a.APIName] = a
	}
	return playerMap
}

func globalPercentPtr(globalPercentages map[string]float64, apiName string) *float64 {
	value, ok := globalPercentages[apiName]
	if !ok {
		return nil
	}
	return &value
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

// shouldFallbackToLegacy reports whether a GetGameAchievements failure should switch to the legacy API.
// Endpoints are undocumented, so a non-transient HTTP status (4xx other than 429, or any other status that is
// not transient) or an EResult error falls back. Transient statuses (429, 500, 502, 503, 504) never fall back:
// 429/500/503 were already retried by the client, 502/504 are not retried, and all surface as a normal failure.
// Transport, context and decode errors surface too.
func shouldFallbackToLegacy(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return false
	}
	var statusErr *steam.HTTPStatusError
	if errors.As(err, &statusErr) {
		return !transientSteamStatus(statusErr.Code)
	}
	var eresultErr *steam.EResultError
	return errors.As(err, &eresultErr)
}

func transientSteamStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// coverImageAppIDs lists the image app ids shown by a game's entries: the game id, then each distinct DLC id.
func coverImageAppIDs(appID int64, entries []setEntry) []int64 {
	ids := []int64{appID}
	for _, entry := range entries {
		if entry.ImageAppID != 0 && !containsAppID(ids, entry.ImageAppID) {
			ids = append(ids, entry.ImageAppID)
		}
	}
	return ids
}

func containsAppID(ids []int64, id int64) bool {
	for _, existing := range ids {
		if existing == id {
			return true
		}
	}
	return false
}

// refreshCovers refreshes the cover of every image app id of a game. Failures are logged and never fail the game sync.
func (s *Service) refreshCovers(ctx context.Context, game steam.OwnedGame, imageAppIDs []int64) {
	if s.Images == nil {
		return
	}
	for _, imageAppID := range imageAppIDs {
		if err := s.refreshCover(ctx, game, imageAppID); err != nil {
			log.Printf("refresh cover %d: %v", imageAppID, err)
		}
	}
}

// refreshCover refreshes one cover, and only when the stored cover is stale (NeedsRefresh), so a fresh
// cover costs no request. A base game first tries the library file Steam lists in GetOwnedGames
// (capsule_filename). The store is asked only when capsule_filename is empty or that file is absent,
// or for DLC ids.
func (s *Service) refreshCover(ctx context.Context, game steam.OwnedGame, imageAppID int64) error {
	prev, err := s.storedCover(imageAppID)
	if err != nil {
		return err
	}
	if !s.Images.NeedsRefresh(imageAppID, prev, time.Now()) {
		return nil
	}
	if imageAppID == game.AppID && game.CapsuleFilename != "" {
		owned := images.ItemAssets{AppID: imageAppID, LibraryCapsule: game.CapsuleFilename}
		res, err := s.Images.RefreshCover(ctx, imageAppID, owned, prev)
		if err != nil {
			return err
		}
		if res.Downloaded {
			return s.saveCover(imageAppID, res)
		}
		// Not found under the owned-games name: ask the store, like a DLC cover.
	}
	assets, err := s.lookupItemAssets(ctx, imageAppID)
	if err != nil {
		return err
	}
	res, err := s.Images.RefreshCover(ctx, imageAppID, assets, prev)
	if err != nil || res.Skipped {
		return err
	}
	return s.saveCover(imageAppID, res)
}

// saveCover stores the cover state of one image app id.
func (s *Service) saveCover(imageAppID int64, res images.Result) error {
	// SaveCover overwrites kind, file, path and source; RefreshCover returns the kept file's values when
	// it could not download, so the stored cover stays consistent with what is on disk.
	return s.Repo.SaveCover(repository.CoverUpdate{
		ImageAppID: imageAppID, Kind: res.Kind, FileName: res.FileName, Path: res.Path,
		SourceURL: res.SourceURL, Downloaded: res.Downloaded,
	})
}

func (s *Service) storedCover(imageAppID int64) (images.Stored, error) {
	record, ok, err := s.Repo.GetCover(imageAppID)
	if err != nil || !ok {
		return images.Stored{}, err
	}
	stored := images.Stored{Kind: record.Kind, FileName: record.FileName}
	if record.CheckedAt != nil {
		if checked, parseErr := time.Parse(time.RFC3339, *record.CheckedAt); parseErr == nil {
			stored.CheckedAt = checked
		}
	}
	return stored, nil
}

func (s *Service) lookupItemAssets(ctx context.Context, imageAppID int64) (images.ItemAssets, error) {
	items, err := s.Images.StoreItemAssets(ctx, []int64{imageAppID})
	return items[imageAppID], err
}
