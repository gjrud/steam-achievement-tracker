package images

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
)

// policyCache is a Cache built by New (so it carries the production redirect policy) whose transport is fake.
func policyCache(t *testing.T, transport http.RoundTripper) (*Cache, string) {
	t.Helper()
	cache := New(appdata.Paths{GameImages: filepath.Join(t.TempDir(), "games")})
	cache.HTTPClient.Transport = transport
	return cache, cache.Paths.GameImages
}

func TestNewInstallsRedirectPolicy(t *testing.T) {
	c := New(appdata.Paths{})
	if c.HTTPClient.CheckRedirect == nil {
		t.Fatal("CheckRedirect = nil, want the redirect policy")
	}
}

func TestCheckRedirectPolicy(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		via     int
		allowed bool
	}{
		{"akamai https", akamai + "steam/apps/1/library_600x900.jpg", 1, true},
		{"mirror https", mirror + "steam/apps/1/library_600x900.jpg", 1, true},
		{"store api https", storeAPI + "IStoreBrowseService/GetItems/v1/", 1, true},
		{"host case-insensitive", "https://SHARED.STEAMSTATIC.COM/store_item_assets/x.jpg", 1, true},
		{"allowed host with port", "https://shared.steamstatic.com:443/store_item_assets/x.jpg", 1, true},
		{"http to allowed host", "http://shared.steamstatic.com/store_item_assets/x.jpg", 1, false},
		{"unknown host", "https://evil.example.com/x.jpg", 1, false},
		{"lookalike suffix host", "https://shared.steamstatic.com.evil.example/x.jpg", 1, false},
		{"file scheme", "file:///etc/passwd", 1, false},
		{"redirect limit reached", akamai + "x.jpg", maxRedirects, false},
		{"one below redirect limit", akamai + "x.jpg", maxRedirects - 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			via := make([]*http.Request, tc.via)
			err = checkRedirect(req, via)
			if tc.allowed && err != nil {
				t.Fatalf("checkRedirect(%s) = %v, want allowed", tc.url, err)
			}
			if !tc.allowed && err == nil {
				t.Fatalf("checkRedirect(%s) = nil, want rejected", tc.url)
			}
			if !tc.allowed && !errors.Is(err, errRedirectNotAllowed) {
				t.Fatalf("checkRedirect(%s) = %v, want error wrapping errRedirectNotAllowed (permanent for store retries)", tc.url, err)
			}
		})
	}
}

// A store-lookup redirect loop hits the redirect limit. That refusal wraps errRedirectNotAllowed, so the store lookup
// does not retry it. The limit counts requests already sent, so one attempt sends exactly maxRedirects requests in total
// (the same count TestRefreshCoverStopsRedirectLoop checks), then returns. Four retried attempts would send 4x that.
func TestStoreItemAssetsRedirectLoopIsNotRetried(t *testing.T) {
	withStoreItemsRetryDelay(t, noStoreItemsRetryDelay)
	loop := storeAPI + "IStoreBrowseService/GetItemsLoop/v1/"
	transport := &routeTransport{routes: []route{
		{prefix: storeAPI, status: http.StatusFound, location: loop},
	}}
	cache, _ := policyCache(t, transport)

	_, err := cache.StoreItemAssets(context.Background(), []int64{dlcAppID})
	if !errors.Is(err, errRedirectNotAllowed) {
		t.Fatalf("error = %v, want errRedirectNotAllowed (redirect limit)", err)
	}
	if len(transport.requests) != maxRedirects {
		t.Fatalf("requests = %d, want %d (one attempt, no retry)", len(transport.requests), maxRedirects)
	}
}

// A redirect from the primary CDN to a plain http URL is refused: no request reaches that URL and no file is written.
func TestRefreshCoverRejectsRedirectToHTTP(t *testing.T) {
	const target = "http://shared.steamstatic.com/store_item_assets/steam/apps/42/library_600x900.jpg"
	transport := &routeTransport{routes: []route{
		{prefix: akamai, status: http.StatusFound, location: target},
	}}
	cache, imagesDir := policyCache(t, transport)

	_, err := cache.RefreshCover(context.Background(), 42, ItemAssets{LibraryCapsule: "library_600x900.jpg"}, Stored{})
	if err == nil {
		t.Fatal("RefreshCover error = nil, want redirect rejected")
	}
	if !errors.Is(err, errRedirectNotAllowed) {
		t.Fatalf("RefreshCover error = %v, want errRedirectNotAllowed", err)
	}
	for _, u := range transport.requests {
		if strings.HasPrefix(u, "http://") {
			t.Fatalf("request %q was sent to an http URL", u)
		}
	}
	assertNoFile(t, filepath.Join(imagesDir, "42", LibraryFileName))
	assertNoTempFiles(t, filepath.Join(imagesDir, "42"))
}

// A redirect to a host outside the allowlist is refused, even when that host would serve a valid image.
func TestRefreshCoverRejectsRedirectToUnknownHost(t *testing.T) {
	const target = "https://evil.example.com/library_600x900.jpg"
	transport := &routeTransport{routes: []route{
		{prefix: akamai, status: http.StatusFound, location: target},
		jpegRoute("https://evil.example.com/", "evil-bytes"),
	}}
	cache, imagesDir := policyCache(t, transport)

	_, err := cache.RefreshCover(context.Background(), 43, ItemAssets{LibraryCapsule: "library_600x900.jpg"}, Stored{})
	if err == nil {
		t.Fatal("RefreshCover error = nil, want redirect rejected")
	}
	for _, u := range transport.requests {
		if strings.Contains(u, "evil.example.com") {
			t.Fatalf("request %q reached a host outside the allowlist", u)
		}
	}
	assertNoFile(t, filepath.Join(imagesDir, "43", LibraryFileName))
}

// A redirect between two allowed hosts (primary CDN to mirror) is followed and the file is stored.
func TestRefreshCoverFollowsRedirectBetweenAllowedHosts(t *testing.T) {
	transport := &routeTransport{routes: []route{
		{prefix: akamai, status: http.StatusFound, location: mirror + "steam/apps/44/library_600x900.jpg"},
		jpegRoute(mirror+"steam/apps/44/library_600x900.jpg", "mirror-bytes"),
	}}
	cache, imagesDir := policyCache(t, transport)

	res, err := cache.RefreshCover(context.Background(), 44, ItemAssets{LibraryCapsule: "library_600x900.jpg"}, Stored{})
	if err != nil {
		t.Fatalf("RefreshCover: %v", err)
	}
	if !res.Downloaded || res.Kind != KindLibrary {
		t.Fatalf("result = %#v, want downloaded library cover", res)
	}
	if got := readFile(t, filepath.Join(imagesDir, "44", LibraryFileName)); got != "mirror-bytes" {
		t.Fatalf("stored bytes = %q, want mirror bytes", got)
	}
}

// A redirect loop stops at maxRedirects: the client sends exactly that many requests to the looping URL.
func TestRefreshCoverStopsRedirectLoop(t *testing.T) {
	loop := akamai + "steam/apps/45/library_600x900.jpg"
	transport := &routeTransport{routes: []route{
		{prefix: akamai, status: http.StatusFound, location: loop},
	}}
	cache, _ := policyCache(t, transport)

	if _, err := cache.RefreshCover(context.Background(), 45, ItemAssets{LibraryCapsule: "library_600x900.jpg"}, Stored{}); err == nil {
		t.Fatal("RefreshCover error = nil, want redirect limit error")
	}
	looped := 0
	for _, u := range transport.requests {
		if u == loop {
			looped++
		}
	}
	if looped != maxRedirects {
		t.Fatalf("requests to looping URL = %d, want %d", looped, maxRedirects)
	}
}

// Leftovers from the old fixed name never collide with a new write and are left alone.
func TestWriteCoverDoesNotReuseLeftoverTempName(t *testing.T) {
	cache, imagesDir := testCache(t, &routeTransport{})
	dir := filepath.Join(imagesDir, "50")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(dir, LibraryFileName+".tmp")
	if err := os.WriteFile(leftover, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := cache.writeCover(50, LibraryFileName, strings.NewReader("new")); err != nil {
		t.Fatalf("writeCover: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, LibraryFileName)); got != "new" {
		t.Fatalf("stored bytes = %q, want new", got)
	}
	if got := readFile(t, leftover); got != "old" {
		t.Fatalf("leftover temp = %q, want untouched", got)
	}
}

// Concurrent writes for one file each use their own temp file: every call succeeds, the final file is one
// complete body, and no temp file remains.
func TestWriteCoverConcurrentWritesDoNotCollide(t *testing.T) {
	cache, imagesDir := testCache(t, &routeTransport{})
	const writers = 8
	bodies := make([]string, writers)
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		bodies[i] = strings.Repeat(fmt.Sprint(i), 4096)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = cache.writeCover(51, LibraryFileName, strings.NewReader(bodies[i]))
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	got := readFile(t, filepath.Join(imagesDir, "51", LibraryFileName))
	if !slices.Contains(bodies, got) {
		t.Fatalf("stored bytes are not one complete body (len %d)", len(got))
	}
	assertNoTempFiles(t, filepath.Join(imagesDir, "51"))
}

// A body that fails mid-copy removes its temp file and writes no cover.
func TestWriteCoverRemovesTempFileOnCopyError(t *testing.T) {
	cache, imagesDir := testCache(t, &routeTransport{})
	body := io.MultiReader(strings.NewReader("partial"), errReader{errors.New("connection reset")})

	if _, err := cache.writeCover(52, LibraryFileName, body); err == nil {
		t.Fatal("writeCover error = nil, want copy error")
	}
	assertNoFile(t, filepath.Join(imagesDir, "52", LibraryFileName))
	assertNoTempFiles(t, filepath.Join(imagesDir, "52"))
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestTruncateForLog(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		limit int
		want  string
	}{
		{"short unchanged", "short", 256, "short"},
		{"exact limit unchanged", strings.Repeat("a", 256), 256, strings.Repeat("a", 256)},
		{"ascii cut", strings.Repeat("a", 300), 256, strings.Repeat("a", 256) + "..."},
		{"two-byte runes on boundary", strings.Repeat("é", 200), 256, strings.Repeat("é", 128) + "..."},
		{"two-byte runes odd limit", strings.Repeat("é", 200), 255, strings.Repeat("é", 127) + "..."},
		{"three-byte runes", strings.Repeat("€", 100), 256, strings.Repeat("€", 85) + "..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateForLog(tc.in, tc.limit)
			if got != tc.want {
				t.Fatalf("truncateForLog() = %q, want %q", got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncateForLog() produced invalid UTF-8: %q", got)
			}
		})
	}
}

// A store error quotes at most maxLoggedBodyBytes of the response body, cut on a rune boundary.
func TestStoreItemAssetsErrorBodyIsTruncated(t *testing.T) {
	transport := &routeTransport{routes: []route{
		{prefix: storeAPI, status: http.StatusBadRequest, contentType: "text/plain", body: strings.Repeat("é", 300)},
	}}
	cache, _ := testCache(t, transport)

	_, err := cache.StoreItemAssets(context.Background(), []int64{10})
	if err == nil {
		t.Fatal("StoreItemAssets error = nil, want status error")
	}
	want := "Steam store items returned 400 Bad Request: " + strings.Repeat("é", 128) + "..."
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestRemoveCover(t *testing.T) {
	cache, imagesDir := testCache(t, &routeTransport{})
	dir := filepath.Join(imagesDir, "60")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stored := filepath.Join(dir, HeroFileName)
	if err := os.WriteFile(stored, []byte("hero"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("unknown file name is refused and nothing is removed", func(t *testing.T) {
		if err := cache.RemoveCover(60, "notes.txt"); err == nil {
			t.Fatal("RemoveCover error = nil, want unknown file error")
		}
		if got := readFile(t, stored); got != "hero" {
			t.Fatalf("stored file = %q, want untouched", got)
		}
	})
	t.Run("invalid app id is refused", func(t *testing.T) {
		if err := cache.RemoveCover(0, HeroFileName); err == nil {
			t.Fatal("RemoveCover error = nil, want invalid app id error")
		}
	})
	t.Run("known file is removed", func(t *testing.T) {
		if err := cache.RemoveCover(60, HeroFileName); err != nil {
			t.Fatalf("RemoveCover: %v", err)
		}
		assertNoFile(t, stored)
	})
	t.Run("missing file is not an error", func(t *testing.T) {
		if err := cache.RemoveCover(60, HeroFileName); err != nil {
			t.Fatalf("RemoveCover on missing file: %v", err)
		}
	})
}
