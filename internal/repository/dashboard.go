package repository

import (
	"database/sql"
	"sort"
	"strings"
)

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

func (r *Repository) Summary(profileID int64) (Summary, error) {
	var s Summary
	row := r.db.QueryRow(`select
		coalesce(sum(case when pg.is_completed=1 and coalesce(f.disabled,0)=0 then 1 else 0 end),0),
		coalesce(sum(case when coalesce(f.disabled,0)=0 then pg.unlocked_achievements else 0 end),0),
		coalesce(sum(case when coalesce(f.disabled,0)=0 then 1 else 0 end),0),
		coalesce(sum(case when coalesce(f.disabled,0)=0 and g.has_achievements=1 then 1 else 0 end),0),
		coalesce(sum(case when coalesce(f.disabled,0)=0 then pg.unlocked_achievements else 0 end),0),
		coalesce(sum(case when coalesce(f.disabled,0)=0 then pg.total_achievements else 0 end),0),
		coalesce(sum(case when coalesce(f.disabled,0)=0 and pg.newly_incomplete=1 then 1 else 0 end),0)
		from profile_games pg join games g on g.appid=pg.appid left join profile_game_flags f on f.profile_id=pg.profile_id and f.appid=pg.appid where pg.profile_id=?`, profileID)
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
	rows, err := r.db.Query(`select g.appid, g.name, g.playtime_forever, g.cover_path, g.cover_source_url, g.cover_downloaded_at, g.has_achievements, g.achievement_status,
		pg.total_achievements, pg.unlocked_achievements, pg.completion_percent, pg.missing_avg_unlock, pg.suggestion_score, pg.is_completed, pg.was_completed, pg.newly_incomplete, pg.sync_warning, pg.last_error, pg.last_error_at, pg.last_synced_at,
		coalesce(f.manual_was_completed,0), coalesce(f.missing_achievements_in_dlc,0), coalesce(f.disabled,0), f.disabled_reason
		from profile_games pg join games g on g.appid=pg.appid left join profile_game_flags f on f.profile_id=pg.profile_id and f.appid=pg.appid
		where pg.profile_id=?`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []GameCard
	for rows.Next() {
		var c GameCard
		var coverPath, coverSource, coverDownloadedAt, lastErr, lastErrAt, lastSynced, flagDisabledReason sql.NullString
		var missing, score sql.NullFloat64
		var has, completed, was, newly, warning, manual, dlc, flagDisabled int
		if err := rows.Scan(&c.AppID, &c.Name, &c.PlaytimeForever, &coverPath, &coverSource, &coverDownloadedAt, &has, &c.AchievementStatus, &c.TotalAchievements, &c.UnlockedAchievements, &c.CompletionPercent, &missing, &score, &completed, &was, &newly, &warning, &lastErr, &lastErrAt, &lastSynced, &manual, &dlc, &flagDisabled, &flagDisabledReason); err != nil {
			return nil, err
		}
		c.CoverURL = coverURL(coverPath, coverDownloadedAt)
		c.HasAchievements = has == 1
		c.MissingAvgUnlock = floatPtr(missing)
		c.SuggestionScore = floatPtr(score)
		c.IsCompleted = completed == 1
		c.WasCompleted = was == 1
		c.NewlyIncomplete = newly == 1
		c.SyncWarning = warning == 1
		c.LastError = stringPtr(lastErr)
		c.LastErrorAt = stringPtr(lastErrAt)
		c.LastSyncedAt = stringPtr(lastSynced)
		c.ManualWasCompleted = manual == 1
		c.MissingDLC = dlc == 1
		c.Disabled = flagDisabled == 1
		c.Tags = tagMap[c.AppID]
		if flagDisabledReason.Valid {
			c.DisabledReason = stringPtr(flagDisabledReason)
			c.ManualDisabled = flagDisabled == 1 && flagDisabledReason.String == "manual"
		}
		switch filter {
		case "completed":
			if !c.Disabled && c.IsCompleted {
				cards = append(cards, c)
			}
		case "warnings":
			if !c.Disabled && c.SyncWarning {
				cards = append(cards, c)
			}
		case "disabled":
			if c.Disabled {
				cards = append(cards, c)
			}
		default:
			if !c.Disabled && !c.IsCompleted {
				cards = append(cards, c)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortGameCards(cards, filter)
	return cards, nil
}

func (r *Repository) ProfileGame(profileID, appID int64) (ProfileGameState, error) {
	row := r.db.QueryRow(`select pg.total_achievements, pg.unlocked_achievements, pg.completion_percent, pg.missing_avg_unlock, pg.suggestion_score, pg.is_completed, pg.was_completed, pg.newly_incomplete, pg.sync_warning, pg.last_synced_at,
		coalesce(f.manual_was_completed,0), coalesce(f.missing_achievements_in_dlc,0), coalesce(f.disabled,0)
		from profile_games pg left join profile_game_flags f on f.profile_id=pg.profile_id and f.appid=pg.appid where pg.profile_id=? and pg.appid=?`, profileID, appID)
	var state ProfileGameState
	var missing, score sql.NullFloat64
	var last sql.NullString
	var completed, was, newly, warning, manual, dlc, disabled int
	err := row.Scan(&state.TotalAchievements, &state.UnlockedAchievements, &state.CompletionPercent, &missing, &score, &completed, &was, &newly, &warning, &last, &manual, &dlc, &disabled)
	if err == sql.ErrNoRows {
		return ProfileGameState{}, nil
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

func (r *Repository) DisabledProfileGameIDs(profileID int64) (map[int64]bool, error) {
	rows, err := r.db.Query(`select appid from profile_game_flags where profile_id=? and disabled=1`, profileID)
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
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		categoryA := suggestionCategory(a)
		categoryB := suggestionCategory(b)
		if categoryA != categoryB {
			return categoryA < categoryB
		}
		if categoryA == 0 {
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
				return strings.ToLower(a.Name) < strings.ToLower(b.Name)
			}
		}
		if categoryA == 2 {
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
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
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}

func suggestionCategory(c GameCard) int {
	if c.TotalAchievements == 0 {
		return 2
	}
	if c.MissingDLC {
		return 1
	}
	return 0
}
