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
)

const (
	coverFileName        = "library_600x900.jpg"
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

type Result struct {
	Path       string
	SourceURL  string
	Downloaded bool
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
			Assets  struct {
				LibraryCapsule string `json:"library_capsule"`
			} `json:"assets"`
		} `json:"store_items"`
	} `json:"response"`
}

func New(paths appdata.Paths) *Cache {
	return &Cache{Paths: paths, HTTPClient: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Cache) LibraryCapsuleSourceURLs(ctx context.Context, appIDs []int64) (map[int64]string, error) {
	unique := uniqueAppIDs(appIDs)
	urls := make(map[int64]string, len(unique))
	for start := 0; start < len(unique); start += storeItemsBatchSize {
		end := start + storeItemsBatchSize
		if end > len(unique) {
			end = len(unique)
		}
		batch, err := c.fetchLibraryCapsuleSourceURLs(ctx, unique[start:end])
		if err != nil {
			return urls, err
		}
		for appID, sourceURL := range batch {
			urls[appID] = sourceURL
		}
	}
	return urls, nil
}

func (c *Cache) RefreshCover(ctx context.Context, appID int64, source string) (Result, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		source = defaultCoverSourceURL(appID)
	}
	dir := filepath.Join(c.Paths.GameImages, fmt.Sprintf("%d", appID))
	path := filepath.Join(dir, coverFileName)
	info, statErr := os.Stat(path)
	hasExistingCover := statErr == nil
	if hasExistingCover && time.Since(info.ModTime()) < 30*24*time.Hour {
		return Result{Path: path, SourceURL: source, Downloaded: false}, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, err
	}
	resp, source, err := c.getCover(ctx, appID, source)
	if err != nil {
		if hasExistingCover {
			return c.keepExistingCover(path, source)
		}
		return Result{Path: path, SourceURL: source}, err
	}
	if resp == nil {
		if hasExistingCover {
			return c.keepExistingCover(path, source)
		}
		return Result{Path: "", SourceURL: source, Downloaded: false}, nil
	}
	defer resp.Body.Close()
	tmp := path + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return Result{}, err
	}
	written, copyErr := io.Copy(out, io.LimitReader(resp.Body, maxCoverBytes+1))
	closeErr := out.Close()
	if copyErr != nil {
		os.Remove(tmp)
		return Result{}, copyErr
	}
	if written > maxCoverBytes {
		os.Remove(tmp)
		return Result{}, fmt.Errorf("cover download exceeded %d bytes", maxCoverBytes)
	}
	if closeErr != nil {
		os.Remove(tmp)
		return Result{}, closeErr
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return Result{}, err
	}
	_ = os.Chmod(path, 0o600)
	return Result{Path: path, SourceURL: source, Downloaded: true}, nil
}

func (c *Cache) keepExistingCover(path, source string) (Result, error) {
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		return Result{Path: path, SourceURL: source, Downloaded: false}, err
	}
	return Result{Path: path, SourceURL: source, Downloaded: false}, nil
}

func (c *Cache) fetchLibraryCapsuleSourceURLs(ctx context.Context, appIDs []int64) (map[int64]string, error) {
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
		return storeItemsSourceURLs(payload), nil
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

func waitStoreItemsRetry(ctx context.Context, attempt int) error {
	if attempt == 0 {
		return nil
	}
	d := time.Duration(300*(1<<uint(attempt-1))) * time.Millisecond
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

func storeItemsSourceURLs(payload storeItemsResponse) map[int64]string {
	urls := make(map[int64]string, len(payload.Response.StoreItems))
	for _, item := range payload.Response.StoreItems {
		if item.Success != 1 {
			continue
		}
		appID := item.AppID
		if appID == 0 {
			appID = item.ID
		}
		sourceURL := coverSourceURL(appID, item.Assets.LibraryCapsule)
		if appID > 0 && sourceURL != "" {
			urls[appID] = sourceURL
		}
	}
	return urls
}

func coverSourceURL(appID int64, libraryCapsule string) string {
	libraryCapsule = strings.TrimSpace(libraryCapsule)
	if libraryCapsule == "" {
		return defaultCoverSourceURL(appID)
	}
	return primaryCoverURLBase + fmt.Sprintf("steam/apps/%d/%s", appID, strings.TrimLeft(libraryCapsule, "/"))
}

func defaultCoverSourceURL(appID int64) string {
	return primaryCoverURLBase + fmt.Sprintf("steam/apps/%d/%s", appID, coverFileName)
}

func (c *Cache) getCover(ctx context.Context, appID int64, primarySource string) (*http.Response, string, error) {
	sources := coverSourceURLs(appID, primarySource)
	var lastErr error
	lastSource := primarySource
	for _, source := range sources {
		lastSource = source
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			lastErr = err
			continue
		}
		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			lastErr = nil
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("cover download returned %s", resp.Status)
			resp.Body.Close()
			continue
		}
		if !validCoverContentType(resp.Header.Get("Content-Type")) {
			lastErr = fmt.Errorf("cover download returned unsupported content type %q", resp.Header.Get("Content-Type"))
			resp.Body.Close()
			continue
		}
		if resp.ContentLength > maxCoverBytes {
			lastErr = fmt.Errorf("cover download content length %d exceeds %d bytes", resp.ContentLength, maxCoverBytes)
			resp.Body.Close()
			continue
		}
		return resp, source, nil
	}
	return nil, lastSource, lastErr
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

func coverSourceURLs(appID int64, primarySource string) []string {
	primarySource = strings.TrimSpace(primarySource)
	if primarySource == "" {
		primarySource = defaultCoverSourceURL(appID)
	}
	primarySource = strings.Replace(primarySource, "https://cdn.cloudflare.steamstatic.com/", primaryCoverURLBase, 1)
	primarySource = strings.Replace(primarySource, "http://cdn.cloudflare.steamstatic.com/", primaryCoverURLBase, 1)
	fallbackSource := strings.Replace(primarySource, primaryCoverURLBase, fallbackCoverURLBase, 1)
	if fallbackSource == primarySource {
		path := strings.TrimPrefix(primarySource, "https://")
		path = strings.TrimPrefix(path, "http://")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 2 {
			fallbackSource = fallbackCoverURLBase + parts[1]
		}
	}
	defaultPrimarySource := defaultCoverSourceURL(appID)
	defaultFallbackSource := strings.Replace(defaultPrimarySource, primaryCoverURLBase, fallbackCoverURLBase, 1)

	sources := make([]string, 0, 4)
	for _, source := range []string{primarySource, fallbackSource, defaultPrimarySource, defaultFallbackSource} {
		if source == "" {
			continue
		}
		duplicate := false
		for _, existing := range sources {
			if existing == source {
				duplicate = true
				break
			}
		}
		if !duplicate {
			sources = append(sources, source)
		}
	}
	return sources
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
