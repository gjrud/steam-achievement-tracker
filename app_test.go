package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
)

func TestStartSyncSingleFlightGuards(t *testing.T) {
	app := &App{ctx: context.Background(), syncInProgress: true}

	if err := app.startSync(false); err != nil {
		t.Fatalf("non-forced startSync err = %v, want nil", err)
	}
	if !app.syncInProgress {
		t.Fatal("syncInProgress = false, want existing sync left running")
	}
	err := app.startSync(true)
	if err == nil || !strings.Contains(err.Error(), "sync already in progress") {
		t.Fatalf("forced startSync err = %v, want already-in-progress error", err)
	}
}

func TestRemoveCachedGameImagesOnlyRequestedDirs(t *testing.T) {
	root := t.TempDir()
	for _, appID := range []string{"10", "20", "30"} {
		dir := filepath.Join(root, appID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "library_600x900.jpg"), []byte("image"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := removeCachedGameImages(root, []int64{10, 0, -1, 20}); err != nil {
		t.Fatal(err)
	}

	for _, appID := range []string{"10", "20"} {
		if _, err := os.Stat(filepath.Join(root, appID)); !os.IsNotExist(err) {
			t.Fatalf("cache dir %s exists after removal, err=%v", appID, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "30", "library_600x900.jpg")); err != nil {
		t.Fatalf("unrequested cache dir removed: %v", err)
	}
}

func TestAssetHandlerServesCachedGameCover(t *testing.T) {
	root := t.TempDir()
	imagesDir := filepath.Join(root, "cache", "images", "games")
	coverPath := filepath.Join(imagesDir, "123", "library_600x900.jpg")
	if err := os.MkdirAll(filepath.Dir(coverPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(coverPath, []byte("cover-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{paths: appdata.Paths{GameImages: imagesDir}}

	req := httptest.NewRequest(http.MethodGet, "/game-covers/123/library_600x900.jpg", nil)
	rec := httptest.NewRecorder()
	app.assetHandler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "cover-bytes" {
		t.Fatalf("body = %q, want cover bytes", got)
	}
}

func TestAssetHandlerRejectsInvalidCoverRoutes(t *testing.T) {
	app := &App{paths: appdata.Paths{GameImages: t.TempDir()}}
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/game-covers/123/library_600x900.jpg"},
		{http.MethodGet, "/game-covers/not-an-id/library_600x900.jpg"},
		{http.MethodGet, "/game-covers/0/library_600x900.jpg"},
		{http.MethodGet, "/game-covers/123/other.jpg"},
		{http.MethodGet, "/game-covers/123/nested/library_600x900.jpg"},
		{http.MethodGet, "/other/123/library_600x900.jpg"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		rec := httptest.NewRecorder()
		app.assetHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want %d", tt.method, tt.path, rec.Code, http.StatusNotFound)
		}
	}
}
