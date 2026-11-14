package repository

import (
	"database/sql"
	"sort"
	"strings"
)

// entryBaseFromSQL joins one profile entry with its game, set catalog row and manual flags.
const entryBaseFromSQL = `from profile_games pg
		join games g on g.appid=pg.appid
		left join game_sets gs on gs.appid=pg.appid and gs.groupid=pg.groupid
		left join profile_game_flags f on f.profile_id=pg.profile_id and f.appid=pg.appid and f.groupid=pg.groupid`

const coverJoinSQL = `left join cover_images ci on ci.image_appid=` + imageAppIDSQL

func (r *Repository) Dashboard(profileID int64) (Dashboard, error) {
	summary, err := r.Summary(profileID)
	if err != nil {
		return Dashboard{}, err
	}
	latest, err := r.LatestSyncRun(profileID)
	if err != nil {
		return Dashboard{}, err
	}
	suggestions, err := r.GameCards(profileID, "suggestions")
	if err != nil {
		return Dashboard{}, err
	}
	completed, err := r.GameCards(profileID, "completed")
	if err != nil {
		return Dashboard{}, err
	}
	warnings, err := r.GameCards(profileID, "warnings")
	if err != nil {
		return Dashboard{}, err
	}
	disabled, err := r.GameCards(profileID, "disabled")
	if err != nil {
		return Dashboard{}, err
	}
	return Dashboard{Summary: summary, LatestSyncRun: latest, Suggestions: suggestions, Completed: completed, Warnings: warnings, Disabled: disabled}, nil
}

// Summary counts completed/newly-incomplete per entry, and games (distinct appid) for owned/with-achievements.
// Unlocked/available sums are per entry; entries partition an app's achievements, so totals are not double counted.
// Entries of DLC the profile does not own are left out of the unlocked/available sums, so the overall
// percentage reflects owned content only.
func (r *Repository) Summary(profileID int64) (Summary, error) {
	var s Summary
	row := r.db.QueryRow(`select
		coalesce(sum(case when pg.is_completed=1 and coalesce(f.disabled,0)=0 then 1 else 0 end),0),
		coalesce(sum(case when coalesce(f.disabled,0)=0 and pg.dlc_not_owned=0 then pg.unlocked_achievements else 0 end),0),
		coalesce(count(distinct case when coalesce(f.disabled,0)=0 then pg.appid end),0),
		coalesce(count(distinct case when coalesce(f.disabled,0)=0 and g.has_achievements=1 then pg.appid end),0),
		coalesce(sum(case when coalesce(f.disabled,0)=0 and pg.dlc_not_owned=0 then pg.unlocked_achievements else 0 end),0),
		coalesce(sum(case when coalesce(f.disabled,0)=0 and pg.dlc_not_owned=0 then pg.total_achievements else 0 end),0),
		coalesce(sum(case when coalesce(f.disabled,0)=0 and pg.newly_incomplete=1 then 1 else 0 end),0)
		`+entryBaseFromSQL+` where pg.profile_id=?`, profileID)
	if err := row.Scan(&s.CompletedGamesCount, &s.TotalUnlockedCount, &s.OwnedGamesCount, &s.GamesWithAchievementsCount, &s.TotalAchievementsUnlocked, &s.TotalAchievementsAvailable, &s.NewlyIncompleteGamesCount); err != nil {
		return s, err
	}
	if s.TotalAchievementsAvailable > 0 {
		s.OverallCompletionPercentage = float64(s.TotalAchievementsUnlocked) / float64(s.TotalAchievementsAvailable) * 100
	}
	return s, nil
}

func (r *Repository) GameCards(profileID int64, filter string) ([]GameCard, error) {
	tagMap, err := r.profileGameTags(profileID)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.Query(`select pg.appid, pg.groupid, g.name, coalesce(gs.name,''), coalesce(gs.dlcappid,0), g.playtime_forever,
		coalesce(ci.kind,''), coalesce(ci.file_name,''), coalesce(ci.path,''), ci.downloaded_at,
		g.has_achievements, g.achievement_status,
		pg.total_achievements, pg.unlocked_achievements, pg.completion_percent, pg.missing_avg_unlock, pg.suggestion_score, pg.is_completed, pg.was_completed, pg.newly_incomplete, pg.sync_warning, pg.last_error, pg.last_error_at, pg.last_synced_at,
		coalesce(f.manual_was_completed,0), coalesce(f.missing_achievements_in_dlc,0), coalesce(f.disabled,0), f.disabled_reason, pg.dlc_not_owned
		`+entryBaseFromSQL+`
		`+coverJoinSQL+`
		where pg.profile_id=?`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []GameCard
	for rows.Next() {
		c, err := scanGameCard(rows, tagMap)
		if err != nil {
			return nil, err
		}
		if includeGameCard(c, filter) {
			cards = append(cards, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortGameCards(cards, filter)
	return cards, nil
}

type gameCardScanner interface {
	Scan(dest ...any) error
}

func scanGameCard(scanner gameCardScanner, tagMap map[entryRef][]string) (GameCard, error) {
	var c GameCard
	var coverKind, coverFileName, coverPath string
	var coverDownloadedAt, lastErr, lastErrAt, lastSynced, flagDisabledReason sql.NullString
	var setName string
	var missing, score sql.NullFloat64
	var has, completed, was, newly, warning, manual, dlc, flagDisabled, dlcNotOwned int
	if err := scanner.Scan(&c.AppID, &c.GroupID, &c.Name, &setName, &c.DLCAppID, &c.PlaytimeForever,
		&coverKind, &coverFileName, &coverPath, &coverDownloadedAt,
		&has, &c.AchievementStatus, &c.TotalAchievements, &c.UnlockedAchievements, &c.CompletionPercent, &missing, &score, &completed, &was, &newly, &warning, &lastErr, &lastErrAt, &lastSynced,
		&manual, &dlc, &flagDisabled, &flagDisabledReason, &dlcNotOwned); err != nil {
		return GameCard{}, err
	}
	applyGameCardScalars(&c, has, completed, was, newly, warning, manual, dlc, flagDisabled)
	c.DLCNotOwned = dlcNotOwned == 1
	imageAppID := c.AppID
	if c.DLCAppID != 0 {
		imageAppID = c.DLCAppID
	}
	c.EntryKey = EntryKey(c.AppID, c.GroupID)
	c.IsSet = c.GroupID != 0
	if c.IsSet {
		c.SetName = setName
	}
	c.Name = DisplayName(c.Name, setName, c.GroupID)
	c.CoverKind = coverKind
	c.CoverURL = coverURL(imageAppID, coverFileName, coverPath, coverDownloadedAt)
	c.MissingAvgUnlock = floatPtr(missing)
	c.SuggestionScore = floatPtr(score)
	c.LastError = stringPtr(lastErr)
	c.LastErrorAt = stringPtr(lastErrAt)
	c.LastSyncedAt = stringPtr(lastSynced)
	c.Tags = tagMap[entryRef{AppID: c.AppID, GroupID: c.GroupID}]
	applyDisabledReason(&c, flagDisabledReason)
	return c, nil
}

func applyGameCardScalars(c *GameCard, has, completed, was, newly, warning, manual, dlc, flagDisabled int) {
	c.HasAchievements = has == 1
	c.IsCompleted = completed == 1
	c.WasCompleted = was == 1
	c.NewlyIncomplete = newly == 1
	c.SyncWarning = warning == 1
	c.ManualWasCompleted = manual == 1
	c.MissingDLC = dlc == 1
	c.Disabled = flagDisabled == 1
}

func applyDisabledReason(c *GameCard, reason sql.NullString) {
	if !reason.Valid {
		return
	}
	c.DisabledReason = stringPtr(reason)
	c.ManualDisabled = c.Disabled && reason.String == "manual"
}

func includeGameCard(c GameCard, filter string) bool {
	switch filter {
	case "completed":
		return isActiveCompleted(c)
	case "warnings":
		return isActiveWarning(c)
	case "disabled":
		return c.Disabled
	default:
		return isSuggestionCard(c)
	}
}

func isActiveCompleted(c GameCard) bool {
	return !c.Disabled && c.IsCompleted
}

func isActiveWarning(c GameCard) bool {
	return !c.Disabled && c.SyncWarning
}

func isSuggestionCard(c GameCard) bool {
	return !c.Disabled && !c.IsCompleted
}

func (r *Repository) ProfileGame(profileID, appID, groupID int64) (ProfileGameState, error) {
	row := r.db.QueryRow(`select pg.total_achievements, pg.unlocked_achievements, pg.completion_percent, pg.missing_avg_unlock, pg.suggestion_score, pg.is_completed, pg.was_completed, pg.newly_incomplete, pg.sync_warning, pg.last_synced_at,
		coalesce(f.manual_was_completed,0), coalesce(f.missing_achievements_in_dlc,0), coalesce(f.disabled,0)
		from profile_games pg left join profile_game_flags f on f.profile_id=pg.profile_id and f.appid=pg.appid and f.groupid=pg.groupid
		where pg.profile_id=? and pg.appid=? and pg.groupid=?`, profileID, appID, groupID)
	var state ProfileGameState
	var missing, score sql.NullFloat64
	var last sql.NullString
	var completed, was, newly, warning, manual, dlc, disabled int
	err := row.Scan(&state.TotalAchievements, &state.UnlockedAchievements, &state.CompletionPercent, &missing, &score, &completed, &was, &newly, &warning, &last, &manual, &dlc, &disabled)
	if err == sql.ErrNoRows {
		return r.profileGameHistory(profileID, appID, groupID)
	}
	if err != nil {
		return ProfileGameState{}, err
	}
	state.Exists = true
	state.MissingAvgUnlock = floatPtr(missing)
	state.SuggestionScore = floatPtr(score)
	state.IsCompleted = completed == 1
	state.WasCompleted = was == 1
	state.NewlyIncomplete = newly == 1
	state.SyncWarning = warning == 1
	state.LastSyncedAt = stringPtr(last)
	state.ManualWasCompleted = manual == 1
	state.MissingDLC = dlc == 1
	state.Disabled = disabled == 1
	return state, nil
}

// profileGameHistory is the state of an entry without a profile_games row (its set was pruned, or never synced).
// Manual flags are kept by pruning, so they are read from profile_game_flags; was_completed falls back to earlier
// snapshots, mirroring how MarkGamePreviouslyCompleted derives it. Exists stays false.
func (r *Repository) profileGameHistory(profileID, appID, groupID int64) (ProfileGameState, error) {
	var manual, dlc, disabled, snapshotCompleted int
	err := r.db.QueryRow(`select coalesce(f.manual_was_completed,0), coalesce(f.missing_achievements_in_dlc,0), coalesce(f.disabled,0),
		exists(select 1 from game_snapshots s where s.profile_id=? and s.appid=? and s.groupid=? and s.is_completed=1)
		from (select 1) x
		left join profile_game_flags f on f.profile_id=? and f.appid=? and f.groupid=?`,
		profileID, appID, groupID, profileID, appID, groupID).Scan(&manual, &dlc, &disabled, &snapshotCompleted)
	if err != nil {
		return ProfileGameState{}, err
	}
	return ProfileGameState{
		WasCompleted:       snapshotCompleted == 1 || manual == 1,
		ManualWasCompleted: manual == 1,
		MissingDLC:         dlc == 1,
		Disabled:           disabled == 1,
	}, nil
}

// DisabledProfileGameIDs returns app ids the sync should skip: apps whose profile entries all have a disabled flag.
// An app with some enabled set stays synced so its enabled sets keep updating. A disabled flag with no profile_games
// row for the app does not skip it (orphan flag), except not_owned flags, which keep excluding the app as before.
func (r *Repository) DisabledProfileGameIDs(profileID int64) (map[int64]bool, error) {
	rows, err := r.db.Query(`select distinct f.appid from profile_game_flags f
		where f.profile_id=? and f.disabled=1
		and (f.disabled_reason='not_owned' or exists (
			select 1 from profile_games pg0 where pg0.profile_id=f.profile_id and pg0.appid=f.appid
		))
		and not exists (
			select 1 from profile_games pg
			where pg.profile_id=f.profile_id and pg.appid=f.appid
			and not exists (
				select 1 from profile_game_flags f2
				where f2.profile_id=pg.profile_id and f2.appid=pg.appid and f2.groupid=pg.groupid and f2.disabled=1
			)
		)`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	disabled := make(map[int64]bool)
	for rows.Next() {
		var appID int64
		if err := rows.Scan(&appID); err != nil {
			return nil, err
		}
		disabled[appID] = true
	}
	return disabled, rows.Err()
}

func sortGameCards(cards []GameCard, filter string) {
	sort.SliceStable(cards, func(i, j int) bool {
		a, b := cards[i], cards[j]
		if filter == "completed" || filter == "warnings" || filter == "disabled" {
			return lessByName(a, b)
		}
		categoryA := suggestionCategory(a)
		categoryB := suggestionCategory(b)
		if categoryA != categoryB {
			return categoryA < categoryB
		}
		if categoryA == suggestionCandidate {
			if a.NewlyIncomplete != b.NewlyIncomplete {
				return a.NewlyIncomplete
			}
			if a.NewlyIncomplete && b.NewlyIncomplete {
				missingA := a.TotalAchievements - a.UnlockedAchievements
				missingB := b.TotalAchievements - b.UnlockedAchievements
				if missingA != missingB {
					return missingA < missingB
				}
				if a.CompletionPercent != b.CompletionPercent {
					return a.CompletionPercent > b.CompletionPercent
				}
				return lessByName(a, b)
			}
		}
		if categoryA == suggestionNotOwned || categoryA == suggestionNoAchievements {
			return lessByName(a, b)
		}
		av, bv := 0.0, 0.0
		if a.SuggestionScore != nil {
			av = *a.SuggestionScore
		}
		if b.SuggestionScore != nil {
			bv = *b.SuggestionScore
		}
		if av != bv {
			return av > bv
		}
		return lessByName(a, b)
	})
}

// lessByName orders by display name, then entryKey as a stable tie-breaker.
func lessByName(a, b GameCard) bool {
	na, nb := strings.ToLower(a.Name), strings.ToLower(b.Name)
	if na != nb {
		return na < nb
	}
	return a.EntryKey < b.EntryKey
}

// Not completed groups in display order. Candidates sort by score (with new-achievement games first). Not-owned
// and no-achievements cards sort by name only, so unowned DLC never outranks a real candidate on its
// zero-progress score. Flagged DLC-missing cards sort by score, after the not-owned group.
const (
	suggestionCandidate      = 0
	suggestionNotOwned       = 1
	suggestionDLCFlagged     = 2
	suggestionNoAchievements = 3
)

// suggestionCategory places a Not completed card in its group. A card with no achievements stays in the
// no-achievements group even when its DLC is not owned. A card that is both DLC-not-owned and flagged
// DLC-missing belongs to the not-owned group.
func suggestionCategory(c GameCard) int {
	if c.TotalAchievements == 0 {
		return suggestionNoAchievements
	}
	if c.DLCNotOwned {
		return suggestionNotOwned
	}
	if c.MissingDLC {
		return suggestionDLCFlagged
	}
	return suggestionCandidate
}
