package images

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	"github.com/gjrud/steam-achievement-tracker/internal/fsretry"
)

const (
	// KindLibrary, KindHero, KindHeader and KindMain are the values stored in cover_images.kind.
	KindLibrary = "library"
	KindHero    = "hero"
	KindHeader  = "header"
	KindMain    = "main"

	// LibraryFileName, HeroFileName, HeaderFileName and MainFileName are the only file names
	// written under GameImages/<imageAppID>/. Serving must whitelist exactly these.
	LibraryFileName = "library_600x900.jpg"
	HeroFileName    = "hero_capsule.jpg"
	HeaderFileName  = "header.jpg"
	MainFileName    = "main_capsule.jpg"

	// CoverRefreshInterval is how long a checked cover (found or not) is trusted.
	CoverRefreshInterval = 30 * 24 * time.Hour

	// MissingCoverRecheckInterval is how long a check that found no cover (Kind "") is trusted.
	MissingCoverRecheckInterval = 7 * 24 * time.Hour

	primaryCoverURLBase  = "https://shared.akamai.steamstatic.com/store_item_assets/"
	fallbackCoverURLBase = "https://shared.steamstatic.com/store_item_assets/"
	storeItemsEndpoint   = "https://api.steampowered.com/IStoreBrowseService/GetItems/v1/"
	storeItemsBatchSize  = 100
	maxCoverBytes        = 5 << 20
)

type Cache struct {
	Paths      appdata.Paths
	HTTPClient *http.Client
}

// Stored is the cover state the caller persisted from an earlier check
// (a cover_images row). The zero value means "never checked".
type Stored struct {
	Kind      string
	FileName  string
	CheckedAt time.Time
}

// Result is the outcome of RefreshCover.
//
// Persist Kind, FileName, SourceURL and CheckedAt for every returned result.
// Set downloaded_at only when Downloaded is true. Path is "" when no image
// exists (Kind == ""). Skipped means the stored state was still fresh and no
// network I/O happened. Changed means Kind/FileName differ from the Stored
// input; the previous file (if any) has been deleted in that case.
type Result struct {
	Kind       string
	FileName   string
	Path       string
	SourceURL  string
	CheckedAt  time.Time
	Downloaded bool
	Changed    bool
	Skipped    bool
}

// ItemAssets is the subset of a Steam store item used for cover lookup.
// Empty capsule fields mean Steam did not report that asset.
type ItemAssets struct {
	AppID       int64
	Type        int
	ParentAppID int64
	// AssetURLFormat is Steam's assets.asset_url_format, e.g.
	// "steam/apps/735730/${FILENAME}?t=1732050499". ${FILENAME} is replaced by a capsule value.
	AssetURLFormat string
	LibraryCapsule string
	HeroCapsule    string
	Header         string
	MainCapsule    string
}

type storeItemID struct {
	AppID int64 `json:"appid"`
}

type storeItemsRequest struct {
	IDs     []storeItemID `json:"ids"`
	Context struct {
		CountryCode string `json:"country_code"`
	} `json:"context"`
	DataRequest struct {
		IncludeAssets bool `json:"include_assets"`
	} `json:"data_request"`
}

type storeItemsResponse struct {
	Response struct {
		StoreItems []struct {
			ID      int64 `json:"id"`
			AppID   int64 `json:"appid"`
			Success int   `json:"success"`
			Type    int   `json:"type"`
			Assets  struct {
				AssetURLFormat string `json:"asset_url_format"`
				LibraryCapsule string `json:"library_capsule"`
				HeroCapsule    string `json:"hero_capsule"`
				Header         string `json:"header"`
				MainCapsule    string `json:"main_capsule"`
			} `json:"assets"`
			Related struct {
				ParentAppID int64 `json:"parent_appid"`
			} `json:"related"`
		} `json:"store_items"`
	} `json:"response"`
}

func New(paths appdata.Paths) *Cache {
	return &Cache{Paths: paths, HTTPClient: &http.Client{Timeout: 30 * time.Second}}
}

// StoreItemAssets looks up store assets for app ids in batches of up to 100.
// The syncer calls it with one id at a time, only for a DLC cover or for a base
// game whose owned-games capsule_filename is empty or whose capsule file is absent. Items Steam reports as
// unsuccessful are omitted. On error the partial map collected so far is returned.
func (c *Cache) StoreItemAssets(ctx context.Context, appIDs []int64) (map[int64]ItemAssets, error) {
	unique := uniqueAppIDs(appIDs)
	out := make(map[int64]ItemAssets, len(unique))
	for start := 0; start < len(unique); start += storeItemsBatchSize {
		end := start + storeItemsBatchSize
		if end > len(unique) {
			end = len(unique)
		}
		batch, err := c.fetchStoreItemAssets(ctx, unique[start:end])
		for appID, assets := range batch {
			out[appID] = assets
		}
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// NeedsRefresh reports whether RefreshCover should contact Steam for this app.
// It is true when the app was never checked, when there is no stored kind and the
// last check is older than MissingCoverRecheckInterval, when a stored kind's check
// is older than CoverRefreshInterval, when the stored file name does not match the
// stored kind, or when the file is missing on disk. A check that found no cover is
// therefore trusted for MissingCoverRecheckInterval; a found cover for CoverRefreshInterval.
func (c *Cache) NeedsRefresh(imageAppID int64, prev Stored, now time.Time) bool {
	if prev.CheckedAt.IsZero() {
		return true
	}
	age := now.Sub(prev.CheckedAt)
	if prev.Kind == "" {
		return age >= MissingCoverRecheckInterval
	}
	if age >= CoverRefreshInterval {
		return true
	}
	if kind, ok := kindForFileName(prev.FileName); !ok || kind != prev.Kind {
		return true
	}
	info, err := os.Stat(c.coverPath(imageAppID, prev.FileName))
	return err != nil || !info.Mode().IsRegular()
}

// RefreshCover resolves and caches the cover for imageAppID.
//
// Chain: library capsule -> hero capsule -> header -> main capsule -> none. An
// asset is tried only when Steam lists it in assets; its URL is built from
// assets.AssetURLFormat with ${FILENAME} replaced by the listed value. Each
// listed asset is tried on the primary CDN, then the fallback CDN. The first
// valid image wins. Base-game covers are never used for a DLC. Header and main
// capsule are landscape; the UI renders those kinds differently.
//
// If prev is still fresh (see NeedsRefresh) nothing is fetched and the stored
// state is returned with Skipped=true. If nothing is found and a previous file
// exists, that file is kept and returned. If nothing is found and no file
// exists, an empty Result with nil error is returned. A transport or content
// error with no file to keep is returned as an error.
//
// Callers should first check NeedsRefresh and only then look up assets: for a
// DLC id through StoreItemAssets, for a base game through the library file name
// from GetOwnedGames (passed as LibraryCapsule), falling back to StoreItemAssets
// only when that name is empty or the file is not found.
func (c *Cache) RefreshCover(ctx context.Context, imageAppID int64, assets ItemAssets, prev Stored) (Result, error) {
	if imageAppID <= 0 {
		return Result{}, fmt.Errorf("invalid image app id %d", imageAppID)
	}
	now := time.Now()
	if !c.NeedsRefresh(imageAppID, prev, now) {
		return Result{
			Kind:      prev.Kind,
			FileName:  prev.FileName,
			Path:      c.coverPath(imageAppID, prev.FileName),
			CheckedAt: prev.CheckedAt,
			Skipped:   true,
		}, nil
	}

	resp, cand, source, fetchErr := c.fetchFirstCover(ctx, imageAppID, assets)
	if resp == nil {
		if name, path, ok := c.existingCover(imageAppID, prev); ok {
			kind, _ := kindForFileName(name)
			return Result{Kind: kind, FileName: name, Path: path, SourceURL: source, CheckedAt: now}, nil
		}
		if fetchErr != nil {
			return Result{SourceURL: source}, fetchErr
		}
		return Result{SourceURL: source, CheckedAt: now}, nil
	}
	defer resp.Body.Close()

	path, err := c.writeCover(imageAppID, cand.fileName, resp.Body)
	if err != nil {
		return Result{}, err
	}
	c.removeOtherCovers(imageAppID, cand.fileName)
	return Result{
		Kind:       cand.kind,
		FileName:   cand.fileName,
		Path:       path,
		SourceURL:  source,
		CheckedAt:  now,
		Downloaded: true,
		Changed:    prev.Kind != cand.kind || prev.FileName != cand.fileName,
	}, nil
}

type coverCandidate struct {
	kind     string
	fileName string
	urls     []string
}

// coverCandidates lists the chain in priority order. A capsule Steam does not
// list gets no URLs and is skipped.
func coverCandidates(imageAppID int64, assets ItemAssets) []coverCandidate {
	if assets.AppID == 0 {
		assets.AppID = imageAppID
	}
	return []coverCandidate{
		{kind: KindLibrary, fileName: LibraryFileName, urls: capsuleCandidateURLs(assets, assets.LibraryCapsule)},
		{kind: KindHero, fileName: HeroFileName, urls: capsuleCandidateURLs(assets, assets.HeroCapsule)},
		{kind: KindHeader, fileName: HeaderFileName, urls: capsuleCandidateURLs(assets, assets.Header)},
		{kind: KindMain, fileName: MainFileName, urls: capsuleCandidateURLs(assets, assets.MainCapsule)},
	}
}

// fetchFirstCover walks the chain and returns the first valid image response.
// The returned source is the last URL tried when nothing matched.
func (c *Cache) fetchFirstCover(ctx context.Context, imageAppID int64, assets ItemAssets) (*http.Response, coverCandidate, string, error) {
	var lastSource string
	var lastErr error
	for _, cand := range coverCandidates(imageAppID, assets) {
		for _, source := range cand.urls {
			lastSource = source
			resp, err := c.fetchCover(ctx, source)
			if err != nil {
				lastErr = err
				continue
			}
			if resp != nil {
				return resp, cand, source, nil
			}
		}
	}
	return nil, coverCandidate{}, lastSource, lastErr
}

// fetchCover returns a validated 2xx image response, (nil, nil) for 404, or an error.
func (c *Cache) fetchCover(ctx context.Context, source string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("cover download returned %s", resp.Status)
	}
	if !validCoverContentType(resp.Header.Get("Content-Type")) {
		resp.Body.Close()
		return nil, fmt.Errorf("cover download returned unsupported content type %q", resp.Header.Get("Content-Type"))
	}
	if resp.ContentLength > maxCoverBytes {
		resp.Body.Close()
		return nil, fmt.Errorf("cover download content length %d exceeds %d bytes", resp.ContentLength, maxCoverBytes)
	}
	return resp, nil
}

func (c *Cache) writeCover(imageAppID int64, fileName string, body io.Reader) (string, error) {
	dir := c.imageDir(imageAppID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fileName)
	tmp := path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", err
	}
	written, copyErr := io.Copy(out, io.LimitReader(body, maxCoverBytes+1))
	closeErr := out.Close()
	// The temp file is closed above; removing it before Close would fail on Windows.
	if copyErr != nil {
		_ = fsretry.Remove(tmp)
		return "", copyErr
	}
	if written > maxCoverBytes {
		_ = fsretry.Remove(tmp)
		return "", fmt.Errorf("cover download exceeded %d bytes", maxCoverBytes)
	}
	if closeErr != nil {
		_ = fsretry.Remove(tmp)
		return "", closeErr
	}
	if err := fsretry.Rename(tmp, path); err != nil {
		_ = fsretry.Remove(tmp)
		return "", err
	}
	_ = os.Chmod(path, 0o600)
	return path, nil
}

// removeOtherCovers deletes known cover files other than keep (e.g. a stale
// hero_capsule.jpg once a library capsule is stored). Errors are ignored. A file the cover server still has open
// is retried briefly on Windows (see fsretry) and otherwise left in place.
func (c *Cache) removeOtherCovers(imageAppID int64, keep string) {
	for _, known := range knownCoverFiles {
		if known.fileName != keep {
			_ = fsretry.Remove(c.coverPath(imageAppID, known.fileName))
		}
	}
}

// existingCover returns the first known cover file present on disk, preferring
// the stored file name.
func (c *Cache) existingCover(imageAppID int64, prev Stored) (string, string, bool) {
	names := make([]string, 0, len(knownCoverFiles)+1)
	if prev.FileName != "" {
		names = append(names, prev.FileName)
	}
	for _, known := range knownCoverFiles {
		names = append(names, known.fileName)
	}
	for _, name := range names {
		if _, ok := kindForFileName(name); !ok {
			continue
		}
		path := c.coverPath(imageAppID, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return name, path, true
		}
	}
	return "", "", false
}

func (c *Cache) imageDir(imageAppID int64) string {
	return filepath.Join(c.Paths.GameImages, fmt.Sprintf("%d", imageAppID))
}

func (c *Cache) coverPath(imageAppID int64, fileName string) string {
	if fileName == "" {
		return ""
	}
	return filepath.Join(c.imageDir(imageAppID), fileName)
}

// knownCoverFiles maps every cover file the cache writes to its kind.
var knownCoverFiles = []struct {
	kind     string
	fileName string
}{
	{KindLibrary, LibraryFileName},
	{KindHero, HeroFileName},
	{KindHeader, HeaderFileName},
	{KindMain, MainFileName},
}

func kindForFileName(fileName string) (string, bool) {
	for _, known := range knownCoverFiles {
		if known.fileName == fileName {
			return known.kind, true
		}
	}
	return "", false
}

func (c *Cache) fetchStoreItemAssets(ctx context.Context, appIDs []int64) (map[int64]ItemAssets, error) {
	endpoint, encodedForm, err := storeItemsRequestEndpoint(appIDs)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if err := waitStoreItemsRetry(ctx, attempt); err != nil {
			return nil, err
		}
		body, retry, err := c.doStoreItemsRequest(ctx, endpoint, encodedForm)
		if err != nil {
			lastErr = err
			if retry {
				continue
			}
			return nil, err
		}

		var payload storeItemsResponse
		if err := json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("decode Steam store items: %w", err)
		}
		return storeItemsAssets(payload), nil
	}
	return nil, lastErr
}

func storeItemsRequestEndpoint(appIDs []int64) (string, string, error) {
	request := storeItemsRequest{IDs: make([]storeItemID, 0, len(appIDs))}
	for _, appID := range appIDs {
		request.IDs = append(request.IDs, storeItemID{AppID: appID})
	}
	request.Context.CountryCode = "US"
	request.DataRequest.IncludeAssets = true

	inputJSON, err := json.Marshal(request)
	if err != nil {
		return "", "", err
	}
	encodedForm := url.Values{"input_json": {string(inputJSON)}}.Encode()
	return storeItemsEndpoint + "?" + encodedForm, encodedForm, nil
}

// storeItemsRetryDelay returns the wait before retry attempt (1-based).
// Unexported var so tests can inject a zero or fixed delay.
var storeItemsRetryDelay = func(attempt int) time.Duration {
	return time.Duration(300*(1<<uint(attempt-1))) * time.Millisecond
}

func waitStoreItemsRetry(ctx context.Context, attempt int) error {
	if attempt == 0 {
		return nil
	}
	d := storeItemsRetryDelay(attempt)
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Cache) doStoreItemsRequest(ctx context.Context, endpoint, encodedForm string) ([]byte, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, strings.NewReader(encodedForm))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, true, err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if readErr != nil {
		return nil, false, readErr
	}
	if storeItemsRetryStatus(resp.StatusCode) {
		return nil, true, fmt.Errorf("Steam store items returned %s", resp.Status)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("Steam store items returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return body, false, nil
}

func storeItemsRetryStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

func storeItemsAssets(payload storeItemsResponse) map[int64]ItemAssets {
	out := make(map[int64]ItemAssets, len(payload.Response.StoreItems))
	for _, item := range payload.Response.StoreItems {
		if item.Success != 1 {
			continue
		}
		appID := item.AppID
		if appID == 0 {
			appID = item.ID
		}
		if appID <= 0 {
			continue
		}
		out[appID] = ItemAssets{
			AppID:          appID,
			Type:           item.Type,
			ParentAppID:    item.Related.ParentAppID,
			AssetURLFormat: strings.TrimSpace(item.Assets.AssetURLFormat),
			LibraryCapsule: strings.TrimSpace(item.Assets.LibraryCapsule),
			HeroCapsule:    strings.TrimSpace(item.Assets.HeroCapsule),
			Header:         strings.TrimSpace(item.Assets.Header),
			MainCapsule:    strings.TrimSpace(item.Assets.MainCapsule),
		}
	}
	return out
}

const assetFilenamePlaceholder = "${FILENAME}"

// assetURL builds the asset path from Steam's asset_url_format by replacing
// ${FILENAME} with file. When Steam sent no format, the documented default
// "steam/apps/<appid>/${FILENAME}" is used. Returns "" when the format or file
// is unsafe (see unsafeAssetPath).
func assetURL(assets ItemAssets, file string) string {
	format := strings.TrimLeft(assets.AssetURLFormat, "/")
	if format == "" {
		format = fmt.Sprintf("steam/apps/%d/%s", assets.AppID, assetFilenamePlaceholder)
	}
	file = strings.TrimLeft(strings.TrimSpace(file), "/")
	if !strings.Contains(format, assetFilenamePlaceholder) || unsafeAssetPath(format) || unsafeAssetPath(file) {
		return ""
	}
	return primaryCoverURLBase + strings.ReplaceAll(format, assetFilenamePlaceholder, file)
}

// unsafeAssetPath reports whether a Steam-supplied asset value could leave the
// asset base: a ".." path segment, a "://" scheme separator or a backslash.
func unsafeAssetPath(value string) bool {
	if strings.Contains(value, "\\") || strings.Contains(value, "://") {
		return true
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

// capsuleCandidateURLs: the listed capsule on the primary CDN, then the fallback
// CDN. Nothing is guessed when Steam does not list the capsule, and an unsafe
// capsule or format is treated as unlisted.
func capsuleCandidateURLs(assets ItemAssets, capsule string) []string {
	capsule = strings.TrimSpace(capsule)
	if capsule == "" {
		return nil
	}
	primary := assetURL(assets, capsule)
	if primary == "" {
		return nil
	}
	return mirrorSourceURLs(primary)
}

// mirrorSourceURLs returns [primary, mirror] with the akamai/steamstatic swap.
func mirrorSourceURLs(primary string) []string {
	primary = strings.TrimSpace(primary)
	primary = strings.Replace(primary, "https://cdn.cloudflare.steamstatic.com/", primaryCoverURLBase, 1)
	primary = strings.Replace(primary, "http://cdn.cloudflare.steamstatic.com/", primaryCoverURLBase, 1)
	fallback := strings.Replace(primary, primaryCoverURLBase, fallbackCoverURLBase, 1)
	if fallback == primary {
		path := strings.TrimPrefix(primary, "https://")
		path = strings.TrimPrefix(path, "http://")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 {
			fallback = fallbackCoverURLBase + parts[1]
		}
	}
	return uniqueStrings([]string{primary, fallback})
}

func validCoverContentType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch contentType {
	case "image/jpeg", "image/png", "image/webp":
		return true
	default:
		return false
	}
}

func uniqueStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		duplicate := false
		for _, existing := range out {
			if existing == value {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, value)
		}
	}
	return out
}

func uniqueAppIDs(appIDs []int64) []int64 {
	seen := make(map[int64]struct{}, len(appIDs))
	unique := make([]int64, 0, len(appIDs))
	for _, appID := range appIDs {
		if appID <= 0 {
			continue
		}
		if _, ok := seen[appID]; ok {
			continue
		}
		seen[appID] = struct{}{}
		unique = append(unique, appID)
	}
	return unique
}
