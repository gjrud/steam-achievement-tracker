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

// GameCard is one dashboard entry: a game (group 0) or one achievement set of a game (groupid != 0).
type GameCard struct {
	AppID                int64    `json:"appid"`
	GroupID              int64    `json:"groupId"`
	EntryKey             string   `json:"entryKey"`
	Name                 string   `json:"name"`
	SetName              string   `json:"setName"`
	IsSet                bool     `json:"isSet"`
	DLCAppID             int64    `json:"dlcAppId"`
	CoverKind            string   `json:"coverKind"`
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
	DLCNotOwned          bool     `json:"dlcNotOwned"`
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
	Archived      bool
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

// GameSyncUpdate is one game sync. Sets holds every eligible entry (achievement set) of the game for the profile.
// An empty Sets prunes all profile entries of the game.
type GameSyncUpdate struct {
	ProfileID         int64
	AppID             int64
	Name              string
	PlaytimeForever   int
	HasAchievements   bool
	AchievementStatus string
	Sets              []SetSyncUpdate
	SyncedAt          string
}

// SetSyncUpdate is one entry (appid + groupid) of a GameSyncUpdate.
// Achievements of the set are stored with groupid = GroupID; Archived marks achievements excluded from counts.
type SetSyncUpdate struct {
	GroupID              int64
	Name                 string // set name from Steam ("" for base set)
	DLCAppID             int64  // 0 unless the set is bound to a DLC
	DLCNotOwned          bool   // DLC-bound set whose DLC the profile does not own (no group flag, no unlock)
	Order                int
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
}

// CoverUpdate describes the cover cached for an image app id (appid, or DLC id for DLC-bound sets).
// Kind is "library", "hero" or "main"; empty Path means no cover is available.
type CoverUpdate struct {
	ImageAppID int64
	Kind       string
	FileName   string
	Path       string
	SourceURL  string
	Downloaded bool // true sets downloaded_at to now; false keeps the previous value
}

type CoverRecord struct {
	ImageAppID   int64
	Kind         string
	FileName     string
	Path         string
	SourceURL    string
	CheckedAt    *string
	DownloadedAt *string
}

// entryRef identifies one profile entry (appid + groupid).
type entryRef struct {
	AppID   int64
	GroupID int64
}

// imageAppIDSQL selects the app id whose store art is shown for an entry: DLC id when bound, else game id.
// Requires aliases pg (profile_games) and gs (game_sets).
const imageAppIDSQL = `(case when coalesce(gs.dlcappid,0)<>0 then gs.dlcappid else pg.appid end)`

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

// EntryKey is the stable frontend key of an entry: "<appid>:<groupid>".
func EntryKey(appID, groupID int64) string {
	return fmt.Sprintf("%d:%d", appID, groupID)
}

// DisplayName is the entry title. Group 0 is the game name. A set name that already contains the
// game name is used as is; otherwise "<game> – <set>". Unnamed sets become "<game> – Set <groupid>".
func DisplayName(gameName, setName string, groupID int64) string {
	if groupID == 0 {
		return gameName
	}
	setName = strings.TrimSpace(setName)
	if setName == "" {
		return fmt.Sprintf("%s – Set %d", gameName, groupID)
	}
	if gameName != "" && strings.Contains(strings.ToLower(setName), strings.ToLower(gameName)) {
		return setName
	}
	if gameName == "" {
		return setName
	}
	return gameName + " – " + setName
}

// coverURL builds the served cover URL for an image app. Empty when no cached file exists.
func coverURL(imageAppID int64, fileName, path string, downloadedAt sql.NullString) string {
	if strings.TrimSpace(path) == "" || fileName == "" || imageAppID <= 0 {
		return ""
	}
	url := fmt.Sprintf("/game-covers/%d/%s", imageAppID, fileName)
	if downloadedAt.Valid && downloadedAt.String != "" {
		url += "?v=" + downloadedAt.String
	}
	return url
}
