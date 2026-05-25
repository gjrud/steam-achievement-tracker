package repository

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (r *Repository) profileGameTags(profileID int64) (map[int64][]string, error) {
	rows, err := r.db.Query(`select appid, tag from profile_game_tags where profile_id=? order by appid,
		case tag
			when 'new_achievements_added' then 0
			when 'completed' then 1
			when 'almost_there' then 2
			when 'in_progress' then 3
			when 'untouched' then 4
			when 'no_achievements' then 5
			when 'missing_achievements_in_dlc' then 6
			when 'missing_cover_art' then 7
			when 'sync_warning' then 8
			else 99
		end, tag`, profileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := make(map[int64][]string)
	for rows.Next() {
		var appID int64
		var tag string
		if err := rows.Scan(&appID, &tag); err != nil {
			return nil, err
		}
		tags[appID] = append(tags[appID], tag)
	}
	return tags, rows.Err()
}

func (r *Repository) RecomputeGameTags(profileID, appID int64) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := r.recomputeGameTagsTx(tx, profileID, appID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) recomputeGameTagsTx(tx *sql.Tx, profileID, appID int64) error {
	if _, err := tx.Exec(`delete from profile_game_tags where profile_id=? and appid=?`, profileID, appID); err != nil {
		return err
	}
	facts, ok, err := loadTagFacts(tx, profileID, appID)
	if err != nil {
		return err
	}
	if !ok || facts.disabled() {
		return nil
	}
	insert := tagInserter{tx: tx, profileID: profileID, appID: appID, now: time.Now().UTC().Format(time.RFC3339)}
	if err := insert.statTags(facts); err != nil {
		return err
	}
	return insert.issueTags(facts)
}

type gameTagFacts struct {
	hasAchievements int
	playtimeForever int
	coverPath       string
	flagDisabled    int
	missingDLC      int
	total           int
	unlocked        int
	completion      float64
	missingAvg      sql.NullFloat64
	suggestionScore sql.NullFloat64
	isCompleted     int
	newlyIncomplete int
	syncWarning     int
	lastSyncedAt    sql.NullString
}

func loadTagFacts(tx *sql.Tx, profileID, appID int64) (gameTagFacts, bool, error) {
	row := tx.QueryRow(`select g.has_achievements, g.playtime_forever, coalesce(g.cover_path,''), coalesce(f.disabled,0), coalesce(f.missing_achievements_in_dlc,0),
		pg.total_achievements, pg.unlocked_achievements, pg.completion_percent, pg.missing_avg_unlock, pg.suggestion_score, pg.is_completed, pg.newly_incomplete, pg.sync_warning, pg.last_synced_at
		from profile_games pg join games g on g.appid=pg.appid left join profile_game_flags f on f.profile_id=pg.profile_id and f.appid=pg.appid
		where pg.profile_id=? and pg.appid=?`, profileID, appID)
	var facts gameTagFacts
	err := row.Scan(&facts.hasAchievements, &facts.playtimeForever, &facts.coverPath, &facts.flagDisabled, &facts.missingDLC, &facts.total, &facts.unlocked, &facts.completion, &facts.missingAvg, &facts.suggestionScore, &facts.isCompleted, &facts.newlyIncomplete, &facts.syncWarning, &facts.lastSyncedAt)
	if err == sql.ErrNoRows {
		return gameTagFacts{}, false, nil
	}
	if err != nil {
		return gameTagFacts{}, false, err
	}
	return facts, true, nil
}

func (f gameTagFacts) disabled() bool {
	return f.flagDisabled == 1
}

func (f gameTagFacts) canInferStats() bool {
	return !(f.syncWarning == 1 && !f.lastSyncedAt.Valid)
}

func (f gameTagFacts) noAchievements() bool {
	return f.total == 0 || f.hasAchievements == 0
}

func (f gameTagFacts) untouched() bool {
	return f.playtimeForever <= 0
}

func (f gameTagFacts) inProgressCandidate() bool {
	return f.isCompleted == 0 && !f.noAchievements() && !f.untouched()
}

type tagInserter struct {
	tx        *sql.Tx
	profileID int64
	appID     int64
	now       string
}

func (i tagInserter) add(tag string, score *float64, reason string) error {
	_, err := i.tx.Exec(`insert into profile_game_tags(profile_id, appid, tag, score, reason, computed_at) values(?,?,?,?,?,?)`, i.profileID, i.appID, tag, score, nullString(reason), i.now)
	return err
}

func (i tagInserter) statTags(f gameTagFacts) error {
	if !f.canInferStats() {
		return nil
	}
	for _, tag := range statTagCandidates(f) {
		if err := i.add(tag.name, tag.score, tag.reason); err != nil {
			return err
		}
	}
	return nil
}

type tagCandidate struct {
	name   string
	score  *float64
	reason string
}

func statTagCandidates(f gameTagFacts) []tagCandidate {
	tags := make([]tagCandidate, 0, 4)
	if f.isCompleted == 1 {
		tags = append(tags, tagCandidate{name: "completed", reason: "completed"})
	}
	if f.noAchievements() {
		tags = append(tags, tagCandidate{name: "no_achievements", reason: "no achievements"})
	}
	if f.untouched() {
		tags = append(tags, tagCandidate{name: "untouched", reason: "0 playtime"})
	}
	if f.inProgressCandidate() {
		tags = append(tags, progressTagCandidate(f))
	}
	return tags
}

func progressTagCandidate(f gameTagFacts) tagCandidate {
	score := suggestionScoreOrFallback(f.suggestionScore, f.completion, f.missingAvg)
	tag := "in_progress"
	if score >= almostThereThreshold {
		tag = "almost_there"
	}
	reason := fmt.Sprintf("%.1f%% completion, %.1f%% missing avg unlock, suggestion score", f.completion, nullFloat(f.missingAvg))
	return tagCandidate{name: tag, score: &score, reason: reason}
}

func (i tagInserter) issueTags(f gameTagFacts) error {
	for _, tag := range issueTagCandidates(f) {
		if err := i.add(tag.name, nil, tag.reason); err != nil {
			return err
		}
	}
	return nil
}

func issueTagCandidates(f gameTagFacts) []tagCandidate {
	tags := make([]tagCandidate, 0, 4)
	if f.newlyIncomplete == 1 {
		tags = append(tags, tagCandidate{name: "new_achievements_added", reason: "previously completed, now incomplete"})
	}
	if f.missingDLC == 1 {
		tags = append(tags, tagCandidate{name: "missing_achievements_in_dlc", reason: "manual DLC-missing flag"})
	}
	if strings.TrimSpace(f.coverPath) == "" {
		tags = append(tags, tagCandidate{name: "missing_cover_art", reason: "no cached cover image"})
	}
	if f.syncWarning == 1 {
		tags = append(tags, tagCandidate{name: "sync_warning", reason: "latest sync warning"})
	}
	return tags
}

func suggestionScoreOrFallback(score sql.NullFloat64, completion float64, missingAvg sql.NullFloat64) float64 {
	if score.Valid {
		return score.Float64
	}
	if missingAvg.Valid {
		return completion*0.50 + missingAvg.Float64*0.50
	}
	return completion * 0.50
}
