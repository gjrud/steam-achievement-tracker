package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	"github.com/gjrud/steam-achievement-tracker/internal/appdata/appdatatest"
	"github.com/gjrud/steam-achievement-tracker/internal/logging"
	zk "github.com/zalando/go-keyring"
)

// The cover service receives paths with the /game-covers route prefix already stripped, so these tests send
// "/<appid>/<file>". TestCoverServiceMountedRouteStripsPrefix covers the full mounted form.

func TestCoverServiceRejectsNonPositiveAppIDEvenWhenFileExists(t *testing.T) {
	root := t.TempDir()
	// Files exist for every id, so a 404 can only come from the appid check and not from a missing file.
	for _, id := range []string{"0", "-5", "1"} {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "library_600x900.jpg"), []byte("cover-"+id), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewCoverService(&App{paths: appdata.Paths{GameImages: root}})

	tests := []struct {
		id     string
		status int
	}{
		{"0", http.StatusNotFound},
		{"-5", http.StatusNotFound},
		{"1", http.StatusOK},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodGet, "/"+tt.id+"/library_600x900.jpg", nil)
		rec := httptest.NewRecorder()
		svc.ServeHTTP(rec, req)
		if rec.Code != tt.status {
			t.Fatalf("appid %s status = %d, want %d", tt.id, rec.Code, tt.status)
		}
	}
}

// TestCoverServiceReadsPathsSafelyWhileInitializing races ensureInitialized against CoverService requests.
// It only detects a regression (unlocked read of the app paths) under `go test -race`; without the race detector
// the unsynchronized read usually passes silently.
func TestCoverServiceReadsPathsSafelyWhileInitializing(t *testing.T) {
	appdatatest.Isolate(t)
	t.Cleanup(logging.Fallback)
	zk.MockInit()
	app := NewApp()
	t.Cleanup(func() { _ = app.ServiceShutdown() })
	svc := NewCoverService(app)

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		if err := app.ensureInitialized(); err != nil {
			t.Errorf("ensureInitialized: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 200; i++ {
			rec := httptest.NewRecorder()
			svc.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/10/library_600x900.jpg", nil))
		}
	}()
	close(start)
	wg.Wait()

	coverDir := filepath.Join(app.paths.GameImages, "10")
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coverDir, "library_600x900.jpg"), []byte("after-init"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	svc.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/10/library_600x900.jpg", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "after-init" {
		t.Fatalf("after init status = %d body = %q, want 200 with cover bytes", rec.Code, rec.Body.String())
	}
}

// TestCoverServiceServesCoverKinds covers every happy-path cover route: a cached
// per-game cover plus the hero, main and header fallbacks for DLC image apps.
// Each case writes one file under <GameImages>/<appID>/<file> and asserts the
// service returns 200 with exactly those bytes.
func TestCoverServiceServesCoverKinds(t *testing.T) {
	tests := []struct {
		kind  string
		appID string
		file  string
		body  string
	}{
		{kind: "cached", appID: "123", file: "library_600x900.jpg", body: "cover-bytes"},
		{kind: "hero", appID: "4173830", file: "hero_capsule.jpg", body: "hero-bytes"},
		{kind: "main", appID: "4173830", file: "main_capsule.jpg", body: "main-bytes"},
		{kind: "header", appID: "4173830", file: "header.jpg", body: "header-bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			imagesDir := filepath.Join(t.TempDir(), "cache", "images", "games")
			coverPath := filepath.Join(imagesDir, tt.appID, tt.file)
			if err := os.MkdirAll(filepath.Dir(coverPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(coverPath, []byte(tt.body), 0o644); err != nil {
				t.Fatal(err)
			}
			svc := NewCoverService(&App{paths: appdata.Paths{GameImages: imagesDir}})

			req := httptest.NewRequest(http.MethodGet, "/"+tt.appID+"/"+tt.file, nil)
			rec := httptest.NewRecorder()
			svc.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Body.String(); got != tt.body {
				t.Fatalf("body = %q, want %q", got, tt.body)
			}
		})
	}
}

// TestCoverServiceMountedRouteStripsPrefix only simulates Wails's StripPrefix mounting: it wraps the service in
// http.StripPrefix("/game-covers") and requests the full public path. It does not verify the Route string in
// main.go; that is checked by reading main.go.
func TestCoverServiceMountedRouteStripsPrefix(t *testing.T) {
	imagesDir := t.TempDir()
	coverDir := filepath.Join(imagesDir, "123")
	if err := os.MkdirAll(coverDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(coverDir, "library_600x900.jpg"), []byte("mounted-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	mounted := http.StripPrefix("/game-covers", NewCoverService(&App{paths: appdata.Paths{GameImages: imagesDir}}))

	rec := httptest.NewRecorder()
	mounted.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/game-covers/123/library_600x900.jpg", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "mounted-bytes" {
		t.Fatalf("mounted status = %d body = %q, want 200 with cover bytes", rec.Code, rec.Body.String())
	}
}

// TestCoverServiceRejectsPathsWithoutLeadingSlash covers paths that reach the service without a leading slash.
// httptest.NewRequest refuses such targets, so the path is set on the parsed URL directly, the way a
// mis-stripped prefix would leave it. The "/game-covers123/..." case goes through StripPrefix, which trims the
// route prefix by raw string match and leaves "123/..." behind.
func TestCoverServiceRejectsPathsWithoutLeadingSlash(t *testing.T) {
	imagesDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(imagesDir, "123"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(imagesDir, "123", "library_600x900.jpg"), []byte("cover-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewCoverService(&App{paths: appdata.Paths{GameImages: imagesDir}})

	direct := httptest.NewRequest(http.MethodGet, "/", nil)
	direct.URL.Path = "123/library_600x900.jpg"
	rec := httptest.NewRecorder()
	svc.ServeHTTP(rec, direct)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("path without leading slash status = %d, want %d", rec.Code, http.StatusNotFound)
	}

	mounted := http.StripPrefix("/game-covers", svc)
	rec = httptest.NewRecorder()
	mounted.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/game-covers123/library_600x900.jpg", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/game-covers123 through StripPrefix status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestCoverServiceRejectsInvalidCoverRoutes(t *testing.T) {
	svc := NewCoverService(&App{paths: appdata.Paths{GameImages: t.TempDir()}})
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/123/library_600x900.jpg"},
		{http.MethodGet, "/not-an-id/library_600x900.jpg"},
		{http.MethodGet, "/0/library_600x900.jpg"},
		{http.MethodGet, "/-5/library_600x900.jpg"},
		{http.MethodGet, "/123/other.jpg"},
		{http.MethodGet, "/123/header.png"},
		{http.MethodGet, "/123/../library_600x900.jpg"},
		{http.MethodGet, "/123/nested/library_600x900.jpg"},
		{http.MethodGet, "/123/hero_capsule.png"},
		{http.MethodGet, "/123/main_capsule.png"},
		{http.MethodGet, "/123/main_capsule.jpg.tmp"},
		{http.MethodGet, "/123/library_600x900.jpg/"},
		{http.MethodGet, "/123"},
		{http.MethodGet, "/"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		rec := httptest.NewRecorder()
		svc.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want %d", tt.method, tt.path, rec.Code, http.StatusNotFound)
		}
	}
}
