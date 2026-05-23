package repository

import (
	"database/sql"
	"fmt"
	"strings"
)

type Repository struct {
	db *sql.DB
}

const almostThereThreshold = 40

func New(db *sql.DB) *Repository { return &Repository{db: db} }

type Profile struct {
	ID           int64   `json:"id"`
	SteamID64    string  `json:"steamId64"`
	DisplayName  string  `json:"displayName"`
	AvatarURL    *string `json:"avatarUrl"`
	CreatedAt    string  `json:"createdAt"`
	UpdatedAt    string  `json:"updatedAt"`
	LastSyncedAt *string `json:"lastSyncedAt"`
}

type Summary struct {
	CompletedGamesCount         int     `json:"completedGamesCount"`
	TotalUnlockedCount          int     `json:"totalUnlockedCount"`
	OwnedGamesCount             int     `json:"ownedGamesCount"`
	GamesWithAchievementsCount  int     `json:"gamesWithAchievementsCount"`
	TotalAchievementsUnlocked   int     `json:"totalAchievementsUnlocked"`
	TotalAchievementsAvailable  int     `json:"totalAchievementsAvailable"`
	NewlyIncompleteGamesCount   int     `json:"newlyIncompleteGamesCount"`
	OverallCompletionPercentage float64 `json:"overallCompletionPercentage"`
}

type SyncRun struct {
	ID          int64   `json:"id"`
	ProfileID   *int64  `json:"profileId"`
	StartedAt   string  `json:"startedAt"`
	FinishedAt  *string `json:"finishedAt"`
	Status      string  `json:"status"`
	GamesTotal  int     `json:"gamesTotal"`
	GamesSynced int     `json:"gamesSynced"`
	GamesFailed int     `json:"gamesFailed"`
	Error       *string `json:"error"`
}

type GameCard struct {
	AppID                int64    `json:"appid"`
	Name                 string   `json:"name"`
	PlaytimeForever      int      `json:"playtimeForever"`
	CoverURL             string   `json:"coverUrl"`
	HasAchievements      bool     `json:"hasAchievements"`
	AchievementStatus    string   `json:"achievementStatus"`
	TotalAchievements    int      `json:"totalAchievements"`
	UnlockedAchievements int      `json:"unlockedAchievements"`
	CompletionPercent    float64  `json:"completionPercent"`
	MissingAvgUnlock     *float64 `json:"missingAvgUnlock"`
	SuggestionScore      *float64 `json:"suggestionScore"`
	IsCompleted          bool     `json:"isCompleted"`
	WasCompleted         bool     `json:"wasCompleted"`
	NewlyIncomplete      bool     `json:"newlyIncomplete"`
	SyncWarning          bool     `json:"syncWarning"`
	LastError            *string  `json:"lastError"`
	LastErrorAt          *string  `json:"lastErrorAt"`
	LastSyncedAt         *string  `json:"lastSyncedAt"`
	ManualWasCompleted   bool     `json:"manualWasCompleted"`
	MissingDLC           bool     `json:"missingAchievementsInDLC"`
	ManualDisabled       bool     `json:"manuallyDisabled"`
	Disabled             bool     `json:"disabled"`
	DisabledReason       *string  `json:"disabledReason"`
	Tags                 []string `json:"tags"`
}

type Dashboard struct {
	Summary       Summary    `json:"summary"`
	LatestSyncRun *SyncRun   `json:"latestSyncRun"`
	Suggestions   []GameCard `json:"suggestions"`
	Completed     []GameCard `json:"completed"`
	Warnings      []GameCard `json:"warnings"`
	Disabled      []GameCard `json:"disabled"`
}

type GameRecord struct {
	AppID           int64
	Name            string
	PlaytimeForever int
}

type AchievementRecord struct {
	APIName       string
	GlobalPercent *float64
	Unlocked      bool
	UnlockTime    *int64
}

type ProfileGameState struct {
	Exists               bool
	TotalAchievements    int
	UnlockedAchievements int
	CompletionPercent    float64
	MissingAvgUnlock     *float64
	SuggestionScore      *float64
	IsCompleted          bool
	WasCompleted         bool
	NewlyIncomplete      bool
	SyncWarning          bool
	LastSyncedAt         *string
	ManualWasCompleted   bool
	MissingDLC           bool
	Disabled             bool
}

type GameSyncUpdate struct {
	ProfileID            int64
	AppID                int64
	Name                 string
	PlaytimeForever      int
	HasAchievements      bool
	AchievementStatus    string
	TotalAchievements    int
	UnlockedAchievements int
	CompletionPercent    float64
	MissingAvgUnlock     *float64
	SuggestionScore      *float64
	IsCompleted          bool
	WasCompleted         bool
	NewlyIncomplete      bool
	SyncWarning          bool
	LastError            *string
	Achievements         []AchievementRecord
	SyncedAt             string
}

func (r *Repository) Close() error { return r.db.Close() }

func scanProfile(row *sql.Row) (*Profile, error) {
	var p Profile
	var avatar, last sql.NullString
	err := row.Scan(&p.ID, &p.SteamID64, &p.DisplayName, &avatar, &p.CreatedAt, &p.UpdatedAt, &last)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.AvatarURL = stringPtr(avatar)
	p.LastSyncedAt = stringPtr(last)
	return &p, nil
}

func scanSyncRun(row *sql.Row) (*SyncRun, error) {
	var r SyncRun
	var pid sql.NullInt64
	var finished, errText sql.NullString
	err := row.Scan(&r.ID, &pid, &r.StartedAt, &finished, &r.Status, &r.GamesTotal, &r.GamesSynced, &r.GamesFailed, &errText)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pid.Valid {
		v := pid.Int64
		r.ProfileID = &v
	}
	r.FinishedAt = stringPtr(finished)
	r.Error = stringPtr(errText)
	return &r, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func floatPtr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	x := v.Float64
	return &x
}
func nullFloat(v sql.NullFloat64) float64 {
	if !v.Valid {
		return 0
	}
	return v.Float64
}
func stringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	x := v.String
	return &x
}
func ptrString(v string) *string { return &v }

func ErrorPtr(err error) *string {
	if err == nil {
		return nil
	}
	return ptrString(err.Error())
}
func StringPtr(v string) *string                    { return &v }
func FormatGameError(appID int64, err error) string { return fmt.Sprintf("appid %d: %v", appID, err) }

func appIDFromCoverPath(path string) string {
	parts := strings.Split(strings.ReplaceAll(path, "\\", "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "library_600x900.jpg" && i > 0 {
			return parts[i-1]
		}
	}
	return ""
}

func coverURL(path, version sql.NullString) string {
	if path.Valid && path.String != "" {
		appid := appIDFromCoverPath(path.String)
		if appid != "" {
			url := "/game-covers/" + appid + "/library_600x900.jpg"
			if version.Valid && version.String != "" {
				url += "?v=" + version.String
			}
			return url
		}
	}
	return ""
}
