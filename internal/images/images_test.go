package images

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
)

const (
	baseAppID = int64(2968420)
	dlcAppID  = int64(4173830)
	storeAPI  = "https://api.steampowered.com/"
	akamai    = "https://shared.akamai.steamstatic.com/store_item_assets/"
	mirror    = "https://shared.steamstatic.com/store_item_assets/"
)

// dlcStoreJSON: DLC with no library capsule, a hashed hero and main capsule, plus an
// unsuccessful item that must be omitted.
const dlcStoreJSON = `{"response":{"store_items":[
 {"id":4173830,"appid":4173830,"success":1,"type":4,"related":{"parent_appid":2968420},
  "assets":{"library_capsule":null,"hero_capsule":"abc123/hero_capsule.jpg","main_capsule":"def456/main_capsule.jpg","header":"abc123/header.jpg"}},
 {"id":5,"appid":5,"success":0}
]}}`

// route answers any request whose URL starts with prefix. Unmatched requests get 404.
type route struct {
	prefix      string
	status      int
	contentType string
	body        string
	err         error
	noLength    bool
}

type routeTransport struct {
	routes   []route
	requests []string
}

func (t *routeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u := req.URL.String()
	t.requests = append(t.requests, u)
	for _, r := range t.routes {
		if !strings.HasPrefix(u, r.prefix) {
			continue
		}
		if r.err != nil {
			return nil, r.err
		}
		status := r.status
		if status == 0 {
			status = http.StatusOK
		}
		resp := &http.Response{
			StatusCode:    status,
			Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
			Header:        http.Header{"Content-Type": {r.contentType}},
			Body:          io.NopCloser(strings.NewReader(r.body)),
			ContentLength: int64(len(r.body)),
			Request:       req,
		}
		if r.noLength {
			resp.ContentLength = -1
		}
		return resp, nil
	}
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Status:     "404 Not Found",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

// statusTransport answers every request with one status code.
type statusTransport struct {
	status int
	count  int
}

func (t *statusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.count++
	return &http.Response{
		StatusCode: t.status,
		Status:     fmt.Sprintf("%d %s", t.status, http.StatusText(t.status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func testCache(t *testing.T, transport http.RoundTripper) (*Cache, string) {
	t.Helper()
	root := t.TempDir()
	imagesDir := filepath.Join(root, "cache", "images", "games")
	return &Cache{
		Paths:      appdata.Paths{GameImages: imagesDir},
		HTTPClient: &http.Client{Transport: transport},
	}, imagesDir
}

func jpegRoute(prefix, body string) route {
	return route{prefix: prefix, status: http.StatusOK, contentType: "image/jpeg", body: body}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assertNoFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat %s error = %v, want not exist", path, err)
	}
}

func TestStoreItemAssetsDecodesCapsulesAndSkipsUnsuccessful(t *testing.T) {
	transport := &routeTransport{routes: []route{{prefix: storeAPI, contentType: "application/json", body: dlcStoreJSON}}}
	cache, _ := testCache(t, transport)

	assets, err := cache.StoreItemAssets(context.Background(), []int64{dlcAppID, 5, dlcAppID})
	if err != nil {
		t.Fatalf("store item assets: %v", err)
	}
	if len(assets) != 1 {
		t.Fatalf("assets len = %d, want 1 (unsuccessful item omitted): %+v", len(assets), assets)
	}
	got := assets[dlcAppID]
	want := ItemAssets{AppID: dlcAppID, Type: 4, ParentAppID: baseAppID, LibraryCapsule: "", HeroCapsule: "abc123/hero_capsule.jpg", MainCapsule: "def456/main_capsule.jpg"}
	if got != want {
		t.Fatalf("assets = %+v, want %+v", got, want)
	}
}

func TestLibraryCapsuleSourceURLsBuiltOnStoreAssets(t *testing.T) {
	json := `{"response":{"store_items":[
	 {"id":1,"appid":1,"success":1,"assets":{"library_capsule":"h/library_600x900.jpg"}},
	 {"id":2,"appid":2,"success":1,"assets":{"library_capsule":null}}
	]}}`
	transport := &routeTransport{routes: []route{{prefix: storeAPI, contentType: "application/json", body: json}}}
	cache, _ := testCache(t, transport)

	urls, err := cache.LibraryCapsuleSourceURLs(context.Background(), []int64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := urls[1], akamai+"steam/apps/1/h/library_600x900.jpg"; got != want {
		t.Fatalf("urls[1] = %q, want %q", got, want)
	}
	if got, want := urls[2], akamai+"steam/apps/2/library_600x900.jpg"; got != want {
		t.Fatalf("urls[2] = %q, want %q", got, want)
	}
}

func TestRefreshCoverDLCChainFallsBackToHero(t *testing.T) {
	transport := &routeTransport{routes: []route{
		{prefix: storeAPI, contentType: "application/json", body: dlcStoreJSON},
		// default library_600x900.jpg for the DLC is 404 (not routed); hashed hero is 200.
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/abc123/hero_capsule.jpg", dlcAppID), "hero-bytes"),
	}}
	cache, imagesDir := testCache(t, transport)
	ctx := context.Background()

	assets, err := cache.StoreItemAssets(ctx, []int64{dlcAppID})
	if err != nil {
		t.Fatal(err)
	}
	res, err := cache.RefreshCover(ctx, dlcAppID, assets[dlcAppID], Stored{})
	if err != nil {
		t.Fatalf("refresh dlc cover: %v", err)
	}
	if res.Kind != KindHero || res.FileName != HeroFileName {
		t.Fatalf("kind/file = %q/%q, want hero/%s", res.Kind, res.FileName, HeroFileName)
	}
	heroPath := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID), HeroFileName)
	if res.Path != heroPath {
		t.Fatalf("path = %q, want %q", res.Path, heroPath)
	}
	if got := readFile(t, heroPath); got != "hero-bytes" {
		t.Fatalf("hero contents = %q", got)
	}
	if !res.Downloaded || !res.Changed || res.Skipped {
		t.Fatalf("flags downloaded/changed/skipped = %v/%v/%v, want true/true/false", res.Downloaded, res.Changed, res.Skipped)
	}
	if res.CheckedAt.IsZero() {
		t.Fatal("CheckedAt is zero")
	}

	// Library default was tried before hero; header.jpg never requested.
	libIdx, heroIdx := -1, -1
	for i, u := range transport.requests {
		if strings.Contains(u, "library_600x900.jpg") && libIdx < 0 {
			libIdx = i
		}
		if strings.Contains(u, "hero_capsule.jpg") && heroIdx < 0 {
			heroIdx = i
		}
		if strings.Contains(u, "header.jpg") {
			t.Fatalf("header.jpg requested: %s", u)
		}
	}
	if libIdx < 0 || heroIdx < 0 || libIdx > heroIdx {
		t.Fatalf("request order library=%d hero=%d, want library before hero", libIdx, heroIdx)
	}
	assertNoFile(t, filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID), LibraryFileName))
}

func TestRefreshCoverPrefersLibraryAndSkipsHero(t *testing.T) {
	transport := &routeTransport{routes: []route{
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/library_600x900.jpg", baseAppID), "lib-bytes"),
	}}
	cache, imagesDir := testCache(t, transport)

	assets := ItemAssets{AppID: baseAppID, HeroCapsule: "h/hero_capsule.jpg"}
	res, err := cache.RefreshCover(context.Background(), baseAppID, assets, Stored{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindLibrary || res.FileName != LibraryFileName {
		t.Fatalf("kind/file = %q/%q, want library/%s", res.Kind, res.FileName, LibraryFileName)
	}
	if got := readFile(t, filepath.Join(imagesDir, fmt.Sprintf("%d", baseAppID), LibraryFileName)); got != "lib-bytes" {
		t.Fatalf("library contents = %q", got)
	}
	for _, u := range transport.requests {
		if strings.Contains(u, "hero_capsule") {
			t.Fatalf("hero requested although library found: %s", u)
		}
	}
}

func TestRefreshCoverLibraryCapsuleFromStoreAssets(t *testing.T) {
	transport := &routeTransport{routes: []route{
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/cap/custom.jpg", baseAppID), "custom"),
	}}
	cache, imagesDir := testCache(t, transport)

	res, err := cache.RefreshCover(context.Background(), baseAppID, ItemAssets{LibraryCapsule: "cap/custom.jpg"}, Stored{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindLibrary || res.SourceURL != akamai+fmt.Sprintf("steam/apps/%d/cap/custom.jpg", baseAppID) {
		t.Fatalf("kind=%q source=%q", res.Kind, res.SourceURL)
	}
	if got := readFile(t, filepath.Join(imagesDir, fmt.Sprintf("%d", baseAppID), LibraryFileName)); got != "custom" {
		t.Fatalf("contents = %q", got)
	}
}

func TestRefreshCoverAllMissingReturnsEmptyWithoutError(t *testing.T) {
	transport := &routeTransport{}
	cache, imagesDir := testCache(t, transport)
	ctx := context.Background()

	res, err := cache.RefreshCover(ctx, dlcAppID, ItemAssets{AppID: dlcAppID, Type: 4, ParentAppID: baseAppID}, Stored{})
	if err != nil {
		t.Fatalf("refresh missing cover: %v", err)
	}
	if res.Path != "" || res.Kind != "" || res.FileName != "" {
		t.Fatalf("result = %+v, want empty kind/file/path", res)
	}
	if res.CheckedAt.IsZero() {
		t.Fatal("CheckedAt is zero for missing cover")
	}
	if res.Downloaded {
		t.Fatal("downloaded = true for missing cover")
	}
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	assertNoFile(t, filepath.Join(dir, LibraryFileName))
	assertNoFile(t, filepath.Join(dir, HeroFileName))
	assertNoFile(t, filepath.Join(dir, MainFileName))
	for _, u := range transport.requests {
		if strings.Contains(u, "header.jpg") {
			t.Fatalf("header.jpg requested: %s", u)
		}
		if strings.Contains(u, fmt.Sprintf("steam/apps/%d/", baseAppID)) {
			t.Fatalf("base game cover requested for DLC: %s", u)
		}
	}

	// Missing (Kind "") is trusted for MissingCoverRecheckInterval: a fresh check makes no requests.
	before := len(transport.requests)
	prev := Stored{Kind: res.Kind, FileName: res.FileName, CheckedAt: res.CheckedAt}
	if cache.NeedsRefresh(dlcAppID, prev, res.CheckedAt.Add(time.Hour)) {
		t.Fatal("NeedsRefresh(fresh missing) = true, want false")
	}
	again, err := cache.RefreshCover(ctx, dlcAppID, ItemAssets{}, prev)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if !again.Skipped || len(transport.requests) != before {
		t.Fatalf("second refresh of fresh missing cover: skipped=%v requests=%d, want skipped with no requests", again.Skipped, len(transport.requests)-before)
	}

	// Once the missing check is older than MissingCoverRecheckInterval it is re-checked.
	if !cache.NeedsRefresh(dlcAppID, prev, res.CheckedAt.Add(MissingCoverRecheckInterval)) {
		t.Fatal("NeedsRefresh(missing older than 7 days) = false, want true")
	}
	if _, err := cache.RefreshCover(ctx, dlcAppID, ItemAssets{}, Stored{Kind: prev.Kind, FileName: prev.FileName, CheckedAt: res.CheckedAt.Add(-MissingCoverRecheckInterval)}); err != nil {
		t.Fatalf("stale missing refresh: %v", err)
	}
	if len(transport.requests) <= before {
		t.Fatal("refresh of missing cover older than 7 days made no requests")
	}
}

func TestRefreshCoverMirrorFallback(t *testing.T) {
	primary := akamai + fmt.Sprintf("steam/apps/%d/library_600x900.jpg", baseAppID)
	tests := []struct {
		name    string
		primary route
	}{
		{name: "transport error", primary: route{prefix: primary, err: errors.New("dial tcp: timeout")}},
		{name: "server error", primary: route{prefix: primary, status: http.StatusInternalServerError}},
		{name: "not found", primary: route{prefix: primary, status: http.StatusNotFound}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := &routeTransport{routes: []route{
				tc.primary,
				jpegRoute(mirror+fmt.Sprintf("steam/apps/%d/library_600x900.jpg", baseAppID), "mirror-bytes"),
			}}
			cache, imagesDir := testCache(t, transport)

			res, err := cache.RefreshCover(context.Background(), baseAppID, ItemAssets{}, Stored{})
			if err != nil {
				t.Fatalf("refresh with mirror: %v", err)
			}
			if res.Kind != KindLibrary {
				t.Fatalf("kind = %q, want library", res.Kind)
			}
			if !strings.HasPrefix(res.SourceURL, mirror) {
				t.Fatalf("source = %q, want mirror", res.SourceURL)
			}
			if got := readFile(t, filepath.Join(imagesDir, fmt.Sprintf("%d", baseAppID), LibraryFileName)); got != "mirror-bytes" {
				t.Fatalf("contents = %q", got)
			}
		})
	}
}

func TestRefreshCoverHeroMirrorFallback(t *testing.T) {
	heroPrimary := akamai + fmt.Sprintf("steam/apps/%d/abc123/hero_capsule.jpg", dlcAppID)
	transport := &routeTransport{routes: []route{
		{prefix: heroPrimary, err: errors.New("reset")},
		jpegRoute(mirror+fmt.Sprintf("steam/apps/%d/abc123/hero_capsule.jpg", dlcAppID), "hero-mirror"),
	}}
	cache, imagesDir := testCache(t, transport)

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{HeroCapsule: "abc123/hero_capsule.jpg"}, Stored{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindHero || !strings.HasPrefix(res.SourceURL, mirror) {
		t.Fatalf("kind=%q source=%q, want hero via mirror", res.Kind, res.SourceURL)
	}
	if got := readFile(t, filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID), HeroFileName)); got != "hero-mirror" {
		t.Fatalf("contents = %q", got)
	}
}

func TestRefreshCoverStaleExistingFailureKeepsFileAndDefers(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			transport := &statusTransport{status: status}
			cache, imagesDir := testCache(t, transport)
			coverPath := filepath.Join(imagesDir, "456", LibraryFileName)
			if err := os.MkdirAll(filepath.Dir(coverPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(coverPath, []byte("old-cover"), 0o644); err != nil {
				t.Fatal(err)
			}
			prev := Stored{Kind: KindLibrary, FileName: LibraryFileName, CheckedAt: time.Now().Add(-31 * 24 * time.Hour)}

			res, err := cache.RefreshCover(context.Background(), 456, ItemAssets{}, prev)
			if err != nil {
				t.Fatalf("refresh stale existing cover: %v", err)
			}
			if res.Path != coverPath || res.Kind != KindLibrary || res.FileName != LibraryFileName {
				t.Fatalf("result = %+v, want kept library at %q", res, coverPath)
			}
			if res.Downloaded || res.Changed || res.Skipped {
				t.Fatalf("flags = %+v, want none set", res)
			}
			if got := readFile(t, coverPath); got != "old-cover" {
				t.Fatalf("cover contents = %q, want old-cover", got)
			}
			afterFirst := transport.count
			if afterFirst == 0 {
				t.Fatal("first refresh made no requests")
			}

			// Caller persists res; next call within 30 days must not hit the network.
			next := Stored{Kind: res.Kind, FileName: res.FileName, CheckedAt: res.CheckedAt}
			res2, err := cache.RefreshCover(context.Background(), 456, ItemAssets{}, next)
			if err != nil {
				t.Fatalf("refresh deferred cover: %v", err)
			}
			if !res2.Skipped || res2.Path != coverPath {
				t.Fatalf("deferred result = %+v, want skipped at %q", res2, coverPath)
			}
			if transport.count != afterFirst {
				t.Fatalf("request count after deferred refresh = %d, want %d", transport.count, afterFirst)
			}
		})
	}
}

func TestRefreshCoverFailureKeepsHeroWhenLibraryNowMissing(t *testing.T) {
	transport := &statusTransport{status: http.StatusNotFound}
	cache, imagesDir := testCache(t, transport)
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	heroPath := filepath.Join(dir, HeroFileName)
	if err := os.WriteFile(heroPath, []byte("hero-old"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := Stored{Kind: KindHero, FileName: HeroFileName, CheckedAt: time.Now().Add(-40 * 24 * time.Hour)}

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{}, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindHero || res.Path != heroPath {
		t.Fatalf("result = %+v, want kept hero", res)
	}
	if got := readFile(t, heroPath); got != "hero-old" {
		t.Fatalf("hero contents = %q", got)
	}
}

func TestRefreshCoverReplacesHeroWithLibraryAndDeletesOldFile(t *testing.T) {
	transport := &routeTransport{routes: []route{
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/library_600x900.jpg", dlcAppID), "lib-new"),
	}}
	cache, imagesDir := testCache(t, transport)
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	heroPath := filepath.Join(dir, HeroFileName)
	if err := os.WriteFile(heroPath, []byte("hero-old"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := Stored{Kind: KindHero, FileName: HeroFileName, CheckedAt: time.Now().Add(-31 * 24 * time.Hour)}

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{}, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindLibrary || !res.Downloaded || !res.Changed {
		t.Fatalf("result = %+v, want downloaded+changed library", res)
	}
	if got := readFile(t, filepath.Join(dir, LibraryFileName)); got != "lib-new" {
		t.Fatalf("library contents = %q", got)
	}
	assertNoFile(t, heroPath)
}

func TestRefreshCoverFreshStoredStateSkipsNetwork(t *testing.T) {
	transport := &statusTransport{status: http.StatusInternalServerError}
	cache, imagesDir := testCache(t, transport)
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", baseAppID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, LibraryFileName)
	if err := os.WriteFile(path, []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	checked := time.Now().Add(-24 * time.Hour)
	prev := Stored{Kind: KindLibrary, FileName: LibraryFileName, CheckedAt: checked}

	res, err := cache.RefreshCover(context.Background(), baseAppID, ItemAssets{}, prev)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Skipped || res.Path != path || !res.CheckedAt.Equal(checked) {
		t.Fatalf("result = %+v, want skipped with stored checked_at", res)
	}
	if transport.count != 0 {
		t.Fatalf("requests = %d, want 0", transport.count)
	}
}

func TestNeedsRefreshRules(t *testing.T) {
	cache, imagesDir := testCache(t, &statusTransport{status: http.StatusNotFound})
	now := time.Now()
	dir := filepath.Join(imagesDir, "7")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, LibraryFileName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh := now.Add(-time.Hour)
	tests := []struct {
		name string
		prev Stored
		want bool
	}{
		{"never checked", Stored{}, true},
		{"never checked with kind", Stored{Kind: KindLibrary, FileName: LibraryFileName}, true},
		{"missing kind checked an hour ago", Stored{CheckedAt: fresh}, false},
		{"missing kind checked 6 days ago", Stored{CheckedAt: now.Add(-6 * 24 * time.Hour)}, false},
		{"missing kind checked 7 days ago", Stored{CheckedAt: now.Add(-MissingCoverRecheckInterval)}, true},
		{"missing kind checked 30 days ago", Stored{CheckedAt: now.Add(-CoverRefreshInterval)}, true},
		{"stale", Stored{Kind: KindLibrary, FileName: LibraryFileName, CheckedAt: now.Add(-CoverRefreshInterval)}, true},
		{"kind/file mismatch", Stored{Kind: KindHero, FileName: LibraryFileName, CheckedAt: fresh}, true},
		{"file gone", Stored{Kind: KindHero, FileName: HeroFileName, CheckedAt: fresh}, true},
		{"main kind/file mismatch", Stored{Kind: KindMain, FileName: HeroFileName, CheckedAt: fresh}, true},
		{"main file gone", Stored{Kind: KindMain, FileName: MainFileName, CheckedAt: fresh}, true},
		{"fresh and present", Stored{Kind: KindLibrary, FileName: LibraryFileName, CheckedAt: fresh}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cache.NeedsRefresh(7, tc.prev, now); got != tc.want {
				t.Fatalf("NeedsRefresh = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRefreshCoverRejectsUnsupportedContentType(t *testing.T) {
	transport := &routeTransport{routes: []route{
		{prefix: akamai, status: http.StatusOK, contentType: "text/plain", body: "not an image"},
	}}
	cache, imagesDir := testCache(t, transport)

	_, err := cache.RefreshCover(context.Background(), 789, ItemAssets{}, Stored{})
	if err == nil {
		t.Fatal("refresh cover error = nil, want unsupported content type error")
	}
	assertNoFile(t, filepath.Join(imagesDir, "789", LibraryFileName))
}

func TestRefreshCoverLimitsDownloadedBytes(t *testing.T) {
	transport := &routeTransport{routes: []route{
		{prefix: akamai, status: http.StatusOK, contentType: "image/jpeg", body: strings.Repeat("x", maxCoverBytes+1), noLength: true},
	}}
	cache, imagesDir := testCache(t, transport)

	_, err := cache.RefreshCover(context.Background(), 790, ItemAssets{}, Stored{})
	if err == nil {
		t.Fatal("refresh cover error = nil, want max bytes error")
	}
	assertNoFile(t, filepath.Join(imagesDir, "790", LibraryFileName))
	assertNoFile(t, filepath.Join(imagesDir, "790", LibraryFileName+".tmp"))
}

func TestRefreshCoverMainChainFallsBackToMain(t *testing.T) {
	// library default 404 and hero capsule 404 (not routed); hashed main capsule is 200.
	transport := &routeTransport{routes: []route{
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/m/main_capsule.jpg", dlcAppID), "main-bytes"),
	}}
	cache, imagesDir := testCache(t, transport)

	assets := ItemAssets{AppID: dlcAppID, HeroCapsule: "h/hero_capsule.jpg", MainCapsule: "m/main_capsule.jpg"}
	res, err := cache.RefreshCover(context.Background(), dlcAppID, assets, Stored{})
	if err != nil {
		t.Fatalf("refresh main cover: %v", err)
	}
	if res.Kind != KindMain || res.FileName != MainFileName {
		t.Fatalf("kind/file = %q/%q, want main/%s", res.Kind, res.FileName, MainFileName)
	}
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	if got := readFile(t, filepath.Join(dir, MainFileName)); got != "main-bytes" {
		t.Fatalf("main contents = %q", got)
	}
	if !res.Downloaded || !res.Changed {
		t.Fatalf("downloaded/changed = %v/%v, want true/true", res.Downloaded, res.Changed)
	}
	assertNoFile(t, filepath.Join(dir, LibraryFileName))
	assertNoFile(t, filepath.Join(dir, HeroFileName))
	for _, u := range transport.requests {
		if strings.Contains(u, "header.jpg") {
			t.Fatalf("header.jpg requested: %s", u)
		}
	}
}

func TestRefreshCoverHeroPreferredOverMain(t *testing.T) {
	transport := &routeTransport{routes: []route{
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/h/hero_capsule.jpg", dlcAppID), "hero-bytes"),
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/m/main_capsule.jpg", dlcAppID), "main-bytes"),
	}}
	cache, imagesDir := testCache(t, transport)

	assets := ItemAssets{AppID: dlcAppID, HeroCapsule: "h/hero_capsule.jpg", MainCapsule: "m/main_capsule.jpg"}
	res, err := cache.RefreshCover(context.Background(), dlcAppID, assets, Stored{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindHero || res.FileName != HeroFileName {
		t.Fatalf("kind/file = %q/%q, want hero/%s", res.Kind, res.FileName, HeroFileName)
	}
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	assertNoFile(t, filepath.Join(dir, MainFileName))
	for _, u := range transport.requests {
		if strings.Contains(u, "main_capsule") {
			t.Fatalf("main requested although hero found: %s", u)
		}
	}
}

func TestRefreshCoverAllMissingWithMainCapsuleReturnsEmpty(t *testing.T) {
	transport := &routeTransport{}
	cache, imagesDir := testCache(t, transport)

	assets := ItemAssets{AppID: dlcAppID, HeroCapsule: "h/hero_capsule.jpg", MainCapsule: "m/main_capsule.jpg"}
	res, err := cache.RefreshCover(context.Background(), dlcAppID, assets, Stored{})
	if err != nil {
		t.Fatalf("refresh all-missing: %v", err)
	}
	if res.Path != "" || res.Kind != "" || res.FileName != "" || res.Downloaded {
		t.Fatalf("result = %+v, want empty", res)
	}
	assertNoFile(t, filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID), MainFileName))
	if !strings.Contains(strings.Join(transport.requests, "\n"), "m/main_capsule.jpg") {
		t.Fatalf("main capsule never requested: %v", transport.requests)
	}
}

func TestRefreshCoverMainMirrorFallback(t *testing.T) {
	primary := akamai + fmt.Sprintf("steam/apps/%d/m/main_capsule.jpg", dlcAppID)
	transport := &routeTransport{routes: []route{
		{prefix: primary, err: errors.New("dial tcp: timeout")},
		jpegRoute(mirror+fmt.Sprintf("steam/apps/%d/m/main_capsule.jpg", dlcAppID), "main-mirror"),
	}}
	cache, imagesDir := testCache(t, transport)

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{MainCapsule: "m/main_capsule.jpg"}, Stored{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindMain || !strings.HasPrefix(res.SourceURL, mirror) {
		t.Fatalf("kind=%q source=%q, want main via mirror", res.Kind, res.SourceURL)
	}
	if got := readFile(t, filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID), MainFileName)); got != "main-mirror" {
		t.Fatalf("contents = %q", got)
	}
}

func TestRefreshCoverReplacesMainWithLibraryAndDeletesOldFile(t *testing.T) {
	transport := &routeTransport{routes: []route{
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/library_600x900.jpg", dlcAppID), "lib-new"),
	}}
	cache, imagesDir := testCache(t, transport)
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, MainFileName)
	if err := os.WriteFile(mainPath, []byte("main-old"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := Stored{Kind: KindMain, FileName: MainFileName, CheckedAt: time.Now().Add(-31 * 24 * time.Hour)}

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{MainCapsule: "m/main_capsule.jpg"}, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindLibrary || !res.Downloaded || !res.Changed {
		t.Fatalf("result = %+v, want downloaded+changed library", res)
	}
	if got := readFile(t, filepath.Join(dir, LibraryFileName)); got != "lib-new" {
		t.Fatalf("library contents = %q", got)
	}
	assertNoFile(t, mainPath)
}

func TestRefreshCoverReplacesMainWithHeroAndDeletesOldFile(t *testing.T) {
	transport := &routeTransport{routes: []route{
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/h/hero_capsule.jpg", dlcAppID), "hero-new"),
	}}
	cache, imagesDir := testCache(t, transport)
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, MainFileName)
	if err := os.WriteFile(mainPath, []byte("main-old"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := Stored{Kind: KindMain, FileName: MainFileName, CheckedAt: time.Now().Add(-31 * 24 * time.Hour)}

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{HeroCapsule: "h/hero_capsule.jpg", MainCapsule: "m/main_capsule.jpg"}, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindHero || !res.Changed {
		t.Fatalf("result = %+v, want changed hero", res)
	}
	assertNoFile(t, mainPath)
}

func TestRefreshCoverFailureKeepsMainWhenNowMissing(t *testing.T) {
	transport := &statusTransport{status: http.StatusNotFound}
	cache, imagesDir := testCache(t, transport)
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, MainFileName)
	if err := os.WriteFile(mainPath, []byte("main-old"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := Stored{Kind: KindMain, FileName: MainFileName, CheckedAt: time.Now().Add(-40 * 24 * time.Hour)}

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{MainCapsule: "m/main_capsule.jpg"}, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindMain || res.Path != mainPath || res.Downloaded || res.Changed {
		t.Fatalf("result = %+v, want kept main at %q", res, mainPath)
	}
	if got := readFile(t, mainPath); got != "main-old" {
		t.Fatalf("main contents = %q", got)
	}
}

func TestRefreshCoverRejectsInvalidAppID(t *testing.T) {
	cache, _ := testCache(t, &statusTransport{status: http.StatusNotFound})
	if _, err := cache.RefreshCover(context.Background(), 0, ItemAssets{}, Stored{}); err == nil {
		t.Fatal("error = nil for app id 0")
	}
}
