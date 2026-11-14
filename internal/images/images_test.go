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
	want := ItemAssets{AppID: dlcAppID, Type: 4, ParentAppID: baseAppID, LibraryCapsule: "", HeroCapsule: "abc123/hero_capsule.jpg", Header: "abc123/header.jpg", MainCapsule: "def456/main_capsule.jpg"}
	if got != want {
		t.Fatalf("assets = %+v, want %+v", got, want)
	}
}

func TestCapsuleCandidateURLsRejectsUnsafeValues(t *testing.T) {
	cases := []struct {
		name    string
		format  string
		capsule string
		wantLen int
	}{
		{"plain listed capsule", "", "h/library_600x900.jpg", 2},
		{"dots inside a name are fine", "", "h/a..b.jpg", 2},
		{"dotdot capsule segment", "", "../../other/header.jpg", 0},
		{"dotdot capsule mid path", "", "h/../header.jpg", 0},
		{"dotdot bare capsule", "", "..", 0},
		{"scheme in capsule", "", "https://evil.example/x.jpg", 0},
		{"backslash in capsule", "", `h\header.jpg`, 0},
		{"dotdot in format", "steam/apps/1/../../${FILENAME}", "header.jpg", 0},
		{"scheme in format", "https://evil.example/${FILENAME}", "header.jpg", 0},
		{"backslash in format", `steam\apps\${FILENAME}`, "header.jpg", 0},
		{"valid format with query", "steam/apps/1/${FILENAME}?t=99", "header.jpg", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assets := ItemAssets{AppID: 1, AssetURLFormat: tc.format}
			got := capsuleCandidateURLs(assets, tc.capsule)
			if len(got) != tc.wantLen {
				t.Fatalf("capsuleCandidateURLs(format=%q, capsule=%q) = %v, want %d URLs", tc.format, tc.capsule, got, tc.wantLen)
			}
			for _, u := range got {
				if strings.Contains(u, "/../") || strings.Contains(u, "\\") || strings.Count(u, "://") != 1 {
					t.Fatalf("unsafe URL %q leaked through", u)
				}
			}
		})
	}
}

func TestRefreshCoverDLCChainFallsBackToHero(t *testing.T) {
	transport := &routeTransport{routes: []route{
		{prefix: storeAPI, contentType: "application/json", body: dlcStoreJSON},
		// listed library capsule is 404 (not routed); hashed hero is 200.
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/abc123/hero_capsule.jpg", dlcAppID), "hero-bytes"),
	}}
	cache, imagesDir := testCache(t, transport)
	ctx := context.Background()

	assets, err := cache.StoreItemAssets(ctx, []int64{dlcAppID})
	if err != nil {
		t.Fatal(err)
	}
	dlc := assets[dlcAppID]
	dlc.LibraryCapsule = "abc123/library_600x900.jpg" // listed by Steam for this test, but 404 (not routed)
	res, err := cache.RefreshCover(ctx, dlcAppID, dlc, Stored{})
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

	// Listed library capsule was tried before hero; hero found, so header.jpg never requested.
	libIdx, heroIdx := -1, -1
	for i, u := range transport.requests {
		if strings.Contains(u, "library_600x900.jpg") && libIdx < 0 {
			libIdx = i
		}
		if strings.Contains(u, "hero_capsule.jpg") && heroIdx < 0 {
			heroIdx = i
		}
		if strings.Contains(u, "header.jpg") {
			t.Fatalf("header.jpg requested although hero found: %s", u)
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

	assets := ItemAssets{AppID: baseAppID, LibraryCapsule: "library_600x900.jpg", HeroCapsule: "h/hero_capsule.jpg"}
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

	// Every listed capsule 404s; nothing else may be requested.
	missing := ItemAssets{AppID: dlcAppID, Type: 4, ParentAppID: baseAppID, LibraryCapsule: "library_600x900.jpg", HeroCapsule: "h/hero_capsule.jpg", Header: "h/header.jpg", MainCapsule: "m/main_capsule.jpg"}
	res, err := cache.RefreshCover(ctx, dlcAppID, missing, Stored{})
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
	assertNoFile(t, filepath.Join(dir, HeaderFileName))
	assertNoFile(t, filepath.Join(dir, MainFileName))
	if !strings.Contains(strings.Join(transport.requests, "\n"), "h/header.jpg") {
		t.Fatalf("header capsule never requested: %v", transport.requests)
	}
	for _, u := range transport.requests {
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
	again, err := cache.RefreshCover(ctx, dlcAppID, missing, prev)
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
	if _, err := cache.RefreshCover(ctx, dlcAppID, missing, Stored{Kind: prev.Kind, FileName: prev.FileName, CheckedAt: res.CheckedAt.Add(-MissingCoverRecheckInterval)}); err != nil {
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

			res, err := cache.RefreshCover(context.Background(), baseAppID, ItemAssets{AppID: baseAppID, LibraryCapsule: "library_600x900.jpg"}, Stored{})
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
			listed := ItemAssets{LibraryCapsule: "library_600x900.jpg"}
			coverPath := filepath.Join(imagesDir, "456", LibraryFileName)
			if err := os.MkdirAll(filepath.Dir(coverPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(coverPath, []byte("old-cover"), 0o644); err != nil {
				t.Fatal(err)
			}
			prev := Stored{Kind: KindLibrary, FileName: LibraryFileName, CheckedAt: time.Now().Add(-31 * 24 * time.Hour)}

			res, err := cache.RefreshCover(context.Background(), 456, listed, prev)
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
			res2, err := cache.RefreshCover(context.Background(), 456, listed, next)
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

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{LibraryCapsule: "library_600x900.jpg"}, prev)
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
		{"header kind/file mismatch", Stored{Kind: KindHeader, FileName: MainFileName, CheckedAt: fresh}, true},
		{"header file gone", Stored{Kind: KindHeader, FileName: HeaderFileName, CheckedAt: fresh}, true},
		{"header stale", Stored{Kind: KindHeader, FileName: HeaderFileName, CheckedAt: now.Add(-CoverRefreshInterval)}, true},
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

	_, err := cache.RefreshCover(context.Background(), 789, ItemAssets{LibraryCapsule: "library_600x900.jpg"}, Stored{})
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

	_, err := cache.RefreshCover(context.Background(), 790, ItemAssets{LibraryCapsule: "library_600x900.jpg"}, Stored{})
	if err == nil {
		t.Fatal("refresh cover error = nil, want max bytes error")
	}
	assertNoFile(t, filepath.Join(imagesDir, "790", LibraryFileName))
	assertNoFile(t, filepath.Join(imagesDir, "790", LibraryFileName+".tmp"))
}

func TestRefreshCoverMainChainFallsBackToMain(t *testing.T) {
	// library, hero and header 404 (not routed); hashed main capsule is 200.
	transport := &routeTransport{routes: []route{
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/m/main_capsule.jpg", dlcAppID), "main-bytes"),
	}}
	cache, imagesDir := testCache(t, transport)

	assets := ItemAssets{AppID: dlcAppID, HeroCapsule: "h/hero_capsule.jpg", Header: "h/header.jpg", MainCapsule: "m/main_capsule.jpg"}
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
	assertNoFile(t, filepath.Join(dir, HeaderFileName))
	assertURLs(t, transport.requests, []string{
		akamai + fmt.Sprintf("steam/apps/%d/h/hero_capsule.jpg", dlcAppID),
		mirror + fmt.Sprintf("steam/apps/%d/h/hero_capsule.jpg", dlcAppID),
		akamai + fmt.Sprintf("steam/apps/%d/h/header.jpg", dlcAppID),
		mirror + fmt.Sprintf("steam/apps/%d/h/header.jpg", dlcAppID),
		akamai + fmt.Sprintf("steam/apps/%d/m/main_capsule.jpg", dlcAppID),
	})
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

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{LibraryCapsule: "library_600x900.jpg", MainCapsule: "m/main_capsule.jpg"}, prev)
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

func TestRefreshCoverHeaderChosenWhenLibraryAndHeroMissing(t *testing.T) {
	// library and hero 404 (not routed); header is 200. Main is listed but must never be requested.
	transport := &routeTransport{routes: []route{
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/hd/header.jpg", dlcAppID), "header-bytes"),
	}}
	cache, imagesDir := testCache(t, transport)
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	heroPath := filepath.Join(dir, HeroFileName)
	mainPath := filepath.Join(dir, MainFileName)
	for _, p := range []string{heroPath, mainPath} {
		if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev := Stored{Kind: KindMain, FileName: MainFileName, CheckedAt: time.Now().Add(-31 * 24 * time.Hour)}

	assets := ItemAssets{AppID: dlcAppID, LibraryCapsule: "library_600x900.jpg", HeroCapsule: "h/hero_capsule.jpg", Header: "hd/header.jpg", MainCapsule: "m/main_capsule.jpg"}
	res, err := cache.RefreshCover(context.Background(), dlcAppID, assets, prev)
	if err != nil {
		t.Fatalf("refresh header cover: %v", err)
	}
	if res.Kind != KindHeader || res.FileName != HeaderFileName || !res.Downloaded || !res.Changed {
		t.Fatalf("result = %+v, want downloaded+changed header", res)
	}
	if res.Path != filepath.Join(dir, HeaderFileName) {
		t.Fatalf("path = %q, want %q", res.Path, filepath.Join(dir, HeaderFileName))
	}
	if got := readFile(t, filepath.Join(dir, HeaderFileName)); got != "header-bytes" {
		t.Fatalf("header contents = %q", got)
	}
	assertNoFile(t, heroPath)
	assertNoFile(t, mainPath)
	assertNoFile(t, filepath.Join(dir, LibraryFileName))
	for _, u := range transport.requests {
		if strings.Contains(u, "main_capsule") {
			t.Fatalf("main requested although header found: %s", u)
		}
	}
}

func TestRefreshCoverHeaderPreferredOverMain(t *testing.T) {
	transport := &routeTransport{routes: []route{
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/hd/header.jpg", dlcAppID), "header-bytes"),
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/m/main_capsule.jpg", dlcAppID), "main-bytes"),
	}}
	cache, imagesDir := testCache(t, transport)

	assets := ItemAssets{AppID: dlcAppID, Header: "hd/header.jpg", MainCapsule: "m/main_capsule.jpg"}
	res, err := cache.RefreshCover(context.Background(), dlcAppID, assets, Stored{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindHeader || res.FileName != HeaderFileName {
		t.Fatalf("kind/file = %q/%q, want header/%s", res.Kind, res.FileName, HeaderFileName)
	}
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	assertNoFile(t, filepath.Join(dir, MainFileName))
	for _, u := range transport.requests {
		if strings.Contains(u, "main_capsule") {
			t.Fatalf("main requested although header found: %s", u)
		}
	}
}

func TestRefreshCoverFallsBackToMainWhenHeaderMissing(t *testing.T) {
	// header listed but 404 (not routed); main is 200.
	transport := &routeTransport{routes: []route{
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/m/main_capsule.jpg", dlcAppID), "main-bytes"),
	}}
	cache, imagesDir := testCache(t, transport)

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{AppID: dlcAppID, Header: "hd/header.jpg", MainCapsule: "m/main_capsule.jpg"}, Stored{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindMain || res.FileName != MainFileName {
		t.Fatalf("kind/file = %q/%q, want main/%s", res.Kind, res.FileName, MainFileName)
	}
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	assertNoFile(t, filepath.Join(dir, HeaderFileName))
	if got := readFile(t, filepath.Join(dir, MainFileName)); got != "main-bytes" {
		t.Fatalf("main contents = %q", got)
	}
}

func TestRefreshCoverHeaderMirrorFallback(t *testing.T) {
	headerPrimary := akamai + fmt.Sprintf("steam/apps/%d/hd/header.jpg", dlcAppID)
	transport := &routeTransport{routes: []route{
		{prefix: headerPrimary, err: errors.New("dial tcp: timeout")},
		jpegRoute(mirror+fmt.Sprintf("steam/apps/%d/hd/header.jpg", dlcAppID), "header-mirror"),
	}}
	cache, imagesDir := testCache(t, transport)

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{Header: "hd/header.jpg"}, Stored{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindHeader || !strings.HasPrefix(res.SourceURL, mirror) {
		t.Fatalf("kind=%q source=%q, want header via mirror", res.Kind, res.SourceURL)
	}
	if got := readFile(t, filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID), HeaderFileName)); got != "header-mirror" {
		t.Fatalf("contents = %q", got)
	}
	assertURLs(t, transport.requests, []string{headerPrimary, mirror + fmt.Sprintf("steam/apps/%d/hd/header.jpg", dlcAppID)})
}

func TestRefreshCoverLibraryReplacesStaleHeaderAndDeletesOldFile(t *testing.T) {
	transport := &routeTransport{routes: []route{
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/library_600x900.jpg", dlcAppID), "lib-new"),
	}}
	cache, imagesDir := testCache(t, transport)
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	headerPath := filepath.Join(dir, HeaderFileName)
	if err := os.WriteFile(headerPath, []byte("header-old"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := Stored{Kind: KindHeader, FileName: HeaderFileName, CheckedAt: time.Now().Add(-31 * 24 * time.Hour)}

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{LibraryCapsule: "library_600x900.jpg"}, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindLibrary || !res.Changed {
		t.Fatalf("result = %+v, want changed library", res)
	}
	assertNoFile(t, headerPath)
}

func TestRefreshCoverHeaderReplacesStaleCoversAndDeletesOldFiles(t *testing.T) {
	transport := &routeTransport{routes: []route{
		jpegRoute(akamai+fmt.Sprintf("steam/apps/%d/hd/header.jpg", dlcAppID), "header-new"),
	}}
	cache, imagesDir := testCache(t, transport)
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := []string{LibraryFileName, HeroFileName, MainFileName}
	for _, name := range stale {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev := Stored{Kind: KindHero, FileName: HeroFileName, CheckedAt: time.Now().Add(-31 * 24 * time.Hour)}

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{HeroCapsule: "h/hero_capsule.jpg", Header: "hd/header.jpg"}, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindHeader || res.FileName != HeaderFileName || !res.Changed {
		t.Fatalf("result = %+v, want changed header", res)
	}
	if got := readFile(t, filepath.Join(dir, HeaderFileName)); got != "header-new" {
		t.Fatalf("header contents = %q", got)
	}
	for _, name := range stale {
		assertNoFile(t, filepath.Join(dir, name))
	}
}

func TestNeedsRefreshHeaderPresentOnDisk(t *testing.T) {
	cache, imagesDir := testCache(t, &statusTransport{status: http.StatusNotFound})
	now := time.Now()
	dir := filepath.Join(imagesDir, "8")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, HeaderFileName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := Stored{Kind: KindHeader, FileName: HeaderFileName, CheckedAt: now.Add(-time.Hour)}
	if cache.NeedsRefresh(8, prev, now) {
		t.Fatal("NeedsRefresh(fresh header present) = true, want false")
	}
}

func assertURLs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("urls = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("url[%d] = %q, want %q (all: %q)", i, got[i], want[i], got)
		}
	}
}

// AssetURLFormat with a ?t= query must be substituted per capsule and keep the query on
// both the primary CDN and the mirror.
func TestAssetURLFormatSubstitutesFilenameWithQuery(t *testing.T) {
	const appID = int64(735730)
	const query = "?t=1732050499"
	assets := ItemAssets{
		AppID:          appID,
		AssetURLFormat: "steam/apps/735730/${FILENAME}" + query,
		LibraryCapsule: "library_600x900.jpg",
		HeroCapsule:    "hero_capsule.jpg",
		Header:         "header.jpg",
		MainCapsule:    "capsule_616x353.jpg",
	}

	t.Run("every capsule 404s: exact primary then mirror order", func(t *testing.T) {
		transport := &routeTransport{}
		cache, _ := testCache(t, transport)
		res, err := cache.RefreshCover(context.Background(), appID, assets, Stored{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Kind != "" {
			t.Fatalf("kind = %q, want none", res.Kind)
		}
		assertURLs(t, transport.requests, []string{
			akamai + "steam/apps/735730/library_600x900.jpg" + query,
			mirror + "steam/apps/735730/library_600x900.jpg" + query,
			akamai + "steam/apps/735730/hero_capsule.jpg" + query,
			mirror + "steam/apps/735730/hero_capsule.jpg" + query,
			akamai + "steam/apps/735730/header.jpg" + query,
			mirror + "steam/apps/735730/header.jpg" + query,
			akamai + "steam/apps/735730/capsule_616x353.jpg" + query,
			mirror + "steam/apps/735730/capsule_616x353.jpg" + query,
		})
	})

	tests := []struct {
		name    string
		assets  ItemAssets
		kind    string
		file    string
		capsule string
	}{
		{"library", ItemAssets{AppID: appID, AssetURLFormat: assets.AssetURLFormat, LibraryCapsule: "library_600x900.jpg"}, KindLibrary, LibraryFileName, "library_600x900.jpg"},
		{"hero", ItemAssets{AppID: appID, AssetURLFormat: assets.AssetURLFormat, HeroCapsule: "hero_capsule.jpg"}, KindHero, HeroFileName, "hero_capsule.jpg"},
		{"header", ItemAssets{AppID: appID, AssetURLFormat: assets.AssetURLFormat, Header: "header.jpg"}, KindHeader, HeaderFileName, "header.jpg"},
		{"main", ItemAssets{AppID: appID, AssetURLFormat: assets.AssetURLFormat, MainCapsule: "capsule_616x353.jpg"}, KindMain, MainFileName, "capsule_616x353.jpg"},
	}
	for _, tc := range tests {
		t.Run("primary error uses mirror/"+tc.name, func(t *testing.T) {
			primary := akamai + "steam/apps/735730/" + tc.capsule + query
			mirrored := mirror + "steam/apps/735730/" + tc.capsule + query
			transport := &routeTransport{routes: []route{
				{prefix: primary, err: errors.New("reset")},
				jpegRoute(mirrored, "mirror-bytes"),
			}}
			cache, imagesDir := testCache(t, transport)
			res, err := cache.RefreshCover(context.Background(), appID, tc.assets, Stored{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Kind != tc.kind || res.FileName != tc.file || res.SourceURL != mirrored {
				t.Fatalf("result kind/file/source = %q/%q/%q, want %q/%q/%q", res.Kind, res.FileName, res.SourceURL, tc.kind, tc.file, mirrored)
			}
			assertURLs(t, transport.requests, []string{primary, mirrored})
			if got := readFile(t, filepath.Join(imagesDir, "735730", tc.file)); got != "mirror-bytes" {
				t.Fatalf("contents = %q", got)
			}
		})
	}
}

// Unlisted capsules are skipped with zero requests for that capsule.
func TestUnlistedCapsulesAreSkippedWithoutRequests(t *testing.T) {
	ctx := context.Background()

	t.Run("unlisted library falls to hero", func(t *testing.T) {
		transport := &routeTransport{routes: []route{
			jpegRoute(akamai+"steam/apps/10/h/hero_capsule.jpg", "hero-bytes"),
		}}
		cache, _ := testCache(t, transport)
		res, err := cache.RefreshCover(ctx, 10, ItemAssets{HeroCapsule: "h/hero_capsule.jpg"}, Stored{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Kind != KindHero {
			t.Fatalf("kind = %q, want hero", res.Kind)
		}
		assertURLs(t, transport.requests, []string{akamai + "steam/apps/10/h/hero_capsule.jpg"})
	})

	t.Run("unlisted library and hero goes to main", func(t *testing.T) {
		transport := &routeTransport{routes: []route{
			jpegRoute(akamai+"steam/apps/10/m/main_capsule.jpg", "main-bytes"),
		}}
		cache, _ := testCache(t, transport)
		res, err := cache.RefreshCover(ctx, 10, ItemAssets{MainCapsule: "m/main_capsule.jpg"}, Stored{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Kind != KindMain {
			t.Fatalf("kind = %q, want main", res.Kind)
		}
		assertURLs(t, transport.requests, []string{akamai + "steam/apps/10/m/main_capsule.jpg"})
	})

	t.Run("nothing listed makes no requests", func(t *testing.T) {
		transport := &routeTransport{}
		cache, _ := testCache(t, transport)
		res, err := cache.RefreshCover(ctx, 10, ItemAssets{}, Stored{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Kind != "" || res.Path != "" {
			t.Fatalf("result = %+v, want empty", res)
		}
		if len(transport.requests) != 0 {
			t.Fatalf("requests = %q, want none", transport.requests)
		}
	})
}

// Regression: Darkest Dungeon DLC 735730 has only a format and a main capsule.
func TestDarkestDungeonDLCMainCapsuleFromAssetURLFormat(t *testing.T) {
	const want = "https://shared.akamai.steamstatic.com/store_item_assets/steam/apps/735730/capsule_616x353.jpg?t=1732050499"
	transport := &routeTransport{routes: []route{jpegRoute(want, "main-bytes")}}
	cache, imagesDir := testCache(t, transport)

	assets := ItemAssets{AssetURLFormat: "steam/apps/735730/${FILENAME}?t=1732050499", MainCapsule: "capsule_616x353.jpg"}
	res, err := cache.RefreshCover(context.Background(), 735730, assets, Stored{})
	if err != nil {
		t.Fatalf("refresh DLC 735730: %v", err)
	}
	if res.Kind != KindMain || res.FileName != MainFileName || !res.Downloaded {
		t.Fatalf("result = %+v, want downloaded main", res)
	}
	if res.SourceURL != want {
		t.Fatalf("source = %q, want %q", res.SourceURL, want)
	}
	if got := readFile(t, filepath.Join(imagesDir, "735730", MainFileName)); got != "main-bytes" {
		t.Fatalf("main contents = %q", got)
	}
	assertURLs(t, transport.requests, []string{want})
}

func TestStoreItemAssetsParsesAssetURLFormat(t *testing.T) {
	const body = `{"response":{"store_items":[
	 {"id":735730,"appid":735730,"success":1,"type":4,"assets":{"asset_url_format":"steam/apps/735730/${FILENAME}?t=1732050499","main_capsule":"capsule_616x353.jpg"}}
	]}}`
	transport := &routeTransport{routes: []route{{prefix: storeAPI, contentType: "application/json", body: body}}}
	cache, _ := testCache(t, transport)

	assets, err := cache.StoreItemAssets(context.Background(), []int64{735730})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := assets[735730]
	if !ok {
		t.Fatalf("assets = %+v, want app 735730", assets)
	}
	if want := "steam/apps/735730/${FILENAME}?t=1732050499"; got.AssetURLFormat != want {
		t.Fatalf("AssetURLFormat = %q, want %q", got.AssetURLFormat, want)
	}
	if got.MainCapsule != "capsule_616x353.jpg" || got.LibraryCapsule != "" || got.HeroCapsule != "" {
		t.Fatalf("capsules = %+v", got)
	}
}

func TestMirrorSourceURLsKeepsQuery(t *testing.T) {
	const path = "steam/apps/735730/capsule_616x353.jpg?t=1732050499"
	want := []string{akamai + path, mirror + path}
	assertURLs(t, mirrorSourceURLs(akamai+path), want)
	assertURLs(t, mirrorSourceURLs("https://cdn.cloudflare.steamstatic.com/"+path), want)
}
