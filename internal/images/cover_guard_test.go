package images

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
)

func TestCacheConstantsAreLiteral(t *testing.T) {
	if CoverRefreshInterval != 720*time.Hour {
		t.Fatalf("CoverRefreshInterval = %v, want 720h", CoverRefreshInterval)
	}
	if MissingCoverRecheckInterval != 168*time.Hour {
		t.Fatalf("MissingCoverRecheckInterval = %v, want 168h", MissingCoverRecheckInterval)
	}
}

func TestNewConfiguresPathsAndTimeout(t *testing.T) {
	paths := appdata.Paths{GameImages: "/tmp/gi"}
	c := New(paths)
	if c.Paths.GameImages != "/tmp/gi" {
		t.Fatalf("GameImages = %q", c.Paths.GameImages)
	}
	if c.HTTPClient == nil {
		t.Fatal("HTTPClient = nil")
	}
	if c.HTTPClient.Timeout != 30*time.Second {
		t.Fatalf("HTTPClient timeout = %v, want 30s", c.HTTPClient.Timeout)
	}
}

func TestRefreshCoverRejectsNonPositiveAppIDWithoutRequests(t *testing.T) {
	for _, id := range []int64{0, -1, -2968420} {
		t.Run(fmt.Sprint(id), func(t *testing.T) {
			transport := &routeTransport{routes: []route{{prefix: akamai, contentType: "image/jpeg", body: "x"}}}
			cache, _ := testCache(t, transport)
			if _, err := cache.RefreshCover(context.Background(), id, ItemAssets{LibraryCapsule: "library_600x900.jpg"}, Stored{}); err == nil {
				t.Fatalf("RefreshCover(%d) error = nil, want invalid app id", id)
			}
			if len(transport.requests) != 0 {
				t.Fatalf("requests = %q, want none", transport.requests)
			}
		})
	}
}

func TestCoverURLUsesAssetAppIDOrImageAppID(t *testing.T) {
	ctx := context.Background()

	t.Run("zero asset appid falls back to image appid in default format", func(t *testing.T) {
		transport := &routeTransport{routes: []route{jpegRoute(akamai+"steam/apps/42/library_600x900.jpg", "lib")}}
		cache, _ := testCache(t, transport)
		res, err := cache.RefreshCover(ctx, 42, ItemAssets{LibraryCapsule: "library_600x900.jpg"}, Stored{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Kind != KindLibrary {
			t.Fatalf("kind = %q, want library", res.Kind)
		}
		assertURLs(t, transport.requests, []string{akamai + "steam/apps/42/library_600x900.jpg"})
	})

	t.Run("listed asset appid wins over image appid", func(t *testing.T) {
		transport := &routeTransport{routes: []route{jpegRoute(akamai+"steam/apps/7/library_600x900.jpg", "lib")}}
		cache, _ := testCache(t, transport)
		res, err := cache.RefreshCover(ctx, 42, ItemAssets{AppID: 7, LibraryCapsule: "library_600x900.jpg"}, Stored{})
		if err != nil {
			t.Fatal(err)
		}
		if res.Kind != KindLibrary {
			t.Fatalf("kind = %q, want library", res.Kind)
		}
		assertURLs(t, transport.requests, []string{akamai + "steam/apps/7/library_600x900.jpg"})
	})
}

func TestMirrorSourceURLsForeignAndPathlessHosts(t *testing.T) {
	assertURLs(t, mirrorSourceURLs("https://other.example/x/y.jpg"), []string{
		"https://other.example/x/y.jpg",
		mirror + "x/y.jpg",
	})
	assertURLs(t, mirrorSourceURLs("https://nopath"), []string{"https://nopath"})
}

func TestUniqueStringsDropsEmptyAndDuplicates(t *testing.T) {
	assertURLs(t, uniqueStrings([]string{"", "a", "a", "b", ""}), []string{"a", "b"})
	assertURLs(t, uniqueStrings(nil), []string{})
}

func TestValidCoverContentType(t *testing.T) {
	valid := []string{"image/jpeg", "image/png", "image/webp", "IMAGE/JPEG", " image/png ", "image/webp; charset=binary"}
	invalid := []string{"", "text/html", "image/gif", "image/svg+xml", "application/octet-stream", "image/jpegx"}
	for _, ct := range valid {
		if !validCoverContentType(ct) {
			t.Errorf("validCoverContentType(%q) = false, want true", ct)
		}
	}
	for _, ct := range invalid {
		if validCoverContentType(ct) {
			t.Errorf("validCoverContentType(%q) = true, want false", ct)
		}
	}
}

func TestCoverDownloadStatusBounds(t *testing.T) {
	tests := []struct {
		status int
		wantOK bool
	}{
		{199, false},
		{200, true},
		{299, true},
		{300, false},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			transport := &routeTransport{routes: []route{
				{prefix: akamai, status: tc.status, contentType: "image/jpeg", body: "bytes"},
				{prefix: mirror, status: tc.status, contentType: "image/jpeg", body: "bytes"},
			}}
			cache, imagesDir := testCache(t, transport)
			res, err := cache.RefreshCover(context.Background(), baseAppID, ItemAssets{LibraryCapsule: "library_600x900.jpg"}, Stored{})
			path := filepath.Join(imagesDir, fmt.Sprintf("%d", baseAppID), LibraryFileName)
			if !tc.wantOK {
				if err == nil {
					t.Fatalf("status %d: error = nil, result %+v", tc.status, res)
				}
				assertNoFile(t, path)
				return
			}
			if err != nil {
				t.Fatalf("status %d: %v", tc.status, err)
			}
			if res.Kind != KindLibrary || !res.Downloaded {
				t.Fatalf("status %d: result = %+v, want downloaded library", tc.status, res)
			}
			if got := readFile(t, path); got != "bytes" {
				t.Fatalf("contents = %q", got)
			}
		})
	}
}

func TestCoverMaxBytesBoundary(t *testing.T) {
	exact := strings.Repeat("x", maxCoverBytes)
	tests := []struct {
		name     string
		body     string
		noLength bool
		wantOK   bool
	}{
		{"exact limit with content length", exact, false, true},
		{"exact limit streamed", exact, true, true},
		{"one byte over with content length", exact + "x", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := &routeTransport{routes: []route{
				{prefix: akamai, status: 200, contentType: "image/jpeg", body: tc.body, noLength: tc.noLength},
				{prefix: mirror, status: 200, contentType: "image/jpeg", body: tc.body, noLength: tc.noLength},
			}}
			cache, imagesDir := testCache(t, transport)
			dir := filepath.Join(imagesDir, "791")
			path := filepath.Join(dir, LibraryFileName)
			_, err := cache.RefreshCover(context.Background(), 791, ItemAssets{LibraryCapsule: "library_600x900.jpg"}, Stored{})
			if !tc.wantOK {
				if err == nil {
					t.Fatal("error = nil, want size error")
				}
				assertNoFile(t, path)
				assertNoTempFiles(t, dir)
				return
			}
			if err != nil {
				t.Fatalf("refresh at limit: %v", err)
			}
			info, statErr := os.Stat(path)
			if statErr != nil {
				t.Fatal(statErr)
			}
			if info.Size() != maxCoverBytes {
				t.Fatalf("size = %d, want %d", info.Size(), maxCoverBytes)
			}
		})
	}
}

func TestRefreshCoverExistingCoverPrefersStoredFileName(t *testing.T) {
	transport := &routeTransport{}
	cache, imagesDir := testCache(t, transport)
	dir := filepath.Join(imagesDir, fmt.Sprintf("%d", dlcAppID))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{LibraryFileName, HeroFileName} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev := Stored{Kind: KindHero, FileName: HeroFileName, CheckedAt: time.Now().Add(-40 * 24 * time.Hour)}

	res, err := cache.RefreshCover(context.Background(), dlcAppID, ItemAssets{}, prev)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != KindHero || res.FileName != HeroFileName || res.Path != filepath.Join(dir, HeroFileName) {
		t.Fatalf("result = %+v, want kept hero (stored file preferred over library)", res)
	}
	if res.Downloaded || res.Changed {
		t.Fatalf("flags = %+v, want none set", res)
	}
}

func TestRefreshCoverChangedFlag(t *testing.T) {
	const libraryURL = "https://shared.akamai.steamstatic.com/store_item_assets/steam/apps/%d/library_600x900.jpg"
	old := time.Now().Add(-31 * 24 * time.Hour)
	tests := []struct {
		name        string
		prev        Stored
		wantChanged bool
	}{
		{"never checked", Stored{}, true},
		{"same kind and file", Stored{Kind: KindLibrary, FileName: LibraryFileName, CheckedAt: old}, false},
		{"same kind, different stored file", Stored{Kind: KindLibrary, FileName: "stale.jpg", CheckedAt: old}, true},
		{"different kind", Stored{Kind: KindHero, FileName: HeroFileName, CheckedAt: old}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := &routeTransport{routes: []route{jpegRoute(fmt.Sprintf(libraryURL, 812), "lib")}}
			cache, _ := testCache(t, transport)
			res, err := cache.RefreshCover(context.Background(), 812, ItemAssets{LibraryCapsule: "library_600x900.jpg"}, tc.prev)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Downloaded {
				t.Fatalf("downloaded = false, want true: %+v", res)
			}
			if res.Changed != tc.wantChanged {
				t.Fatalf("changed = %v, want %v: %+v", res.Changed, tc.wantChanged, res)
			}
		})
	}
}

func TestRemoveOtherCoversKeepsOnlyNamedFile(t *testing.T) {
	cache, imagesDir := testCache(t, &routeTransport{})
	dir := filepath.Join(imagesDir, "813")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	all := []string{LibraryFileName, HeroFileName, HeaderFileName, MainFileName}
	for _, name := range append(append([]string{}, all...), "notes.txt", HeaderFileName+".tmp") {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cache.removeOtherCovers(813, HeroFileName)
	if got := readFile(t, filepath.Join(dir, HeroFileName)); got != HeroFileName {
		t.Fatalf("kept hero contents = %q", got)
	}
	for _, name := range []string{LibraryFileName, HeaderFileName, MainFileName} {
		assertNoFile(t, filepath.Join(dir, name))
	}
	for _, name := range []string{"notes.txt", HeaderFileName + ".tmp"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("unrelated file %s removed: %v", name, err)
		}
	}

	cache.removeOtherCovers(813, "")
	assertNoFile(t, filepath.Join(dir, HeroFileName))
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatalf("unrelated file removed with empty keep: %v", err)
	}
}
