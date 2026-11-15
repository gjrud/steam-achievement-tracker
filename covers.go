package main

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	"github.com/gjrud/steam-achievement-tracker/internal/images"
)

// CoverService serves cached game covers from the app data directory. It is mounted as a Wails v3 service at the
// /game-covers route, so Wails strips that prefix before calling ServeHTTP: a request for
// /game-covers/<appid>/<file> reaches the handler as /<appid>/<file>. It is not bound to the frontend.
type CoverService struct {
	app *App
}

// NewCoverService returns the cover handler service for app.
func NewCoverService(app *App) *CoverService {
	return &CoverService{app: app}
}

// servableCoverFile reports whether name is a cover file the image cache writes.
func servableCoverFile(name string) bool {
	return name == images.LibraryFileName || name == images.HeroFileName || name == images.HeaderFileName || name == images.MainFileName
}

// ServeHTTP serves GET /<appid>/<file> for a cached cover file. Anything else is a 404.
func (s *CoverService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	// Wails matches the route by raw prefix, so /game-covers123/... arrives here as "123/..." without a leading slash.
	if !strings.HasPrefix(r.URL.Path, "/") {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(r.URL.Path[1:], "/")
	if len(parts) != 2 || !servableCoverFile(parts[1]) {
		http.NotFound(w, r)
		return
	}
	appid, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || appid <= 0 {
		http.NotFound(w, r)
		return
	}
	s.app.mu.Lock()
	paths := s.app.paths
	s.app.mu.Unlock()
	if paths.GameImages == "" {
		resolved, err := appdata.Resolve()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		paths = resolved
	}
	path := filepath.Join(paths.GameImages, strconv.FormatInt(appid, 10), parts[1])
	http.ServeFile(w, r, path)
}
