package main

import (
	"embed"
	"log"
	"net/http"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var icon []byte

func main() {
	// Create the backend. Its exported methods are bound to the frontend through the service below.
	backend := NewApp()

	app := application.New(application.Options{
		Name:        "steam-achievement-tracker",
		Description: "Track Steam achievement progress for your public library",
		Icon:        icon,
		Linux: application.LinuxOptions{
			// Reverse-DNS GTK application id. ProgramName stays the executable name so the Wayland app_id keeps
			// matching the packaged steam-achievement-tracker.desktop file.
			ApplicationID: "io.github.gjrud.steam-achievement-tracker",
			ProgramName:   "steam-achievement-tracker",
		},
		Services: []application.Service{
			application.NewService(backend),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
			// Game covers live on disk under the app data directory, not in the embedded frontend. Route them to the
			// cover handler before the embedded assets are consulted; every other request continues to the default chain.
			Middleware: func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasPrefix(r.URL.Path, "/game-covers/") {
						backend.assetHandler().ServeHTTP(w, r)
						return
					}
					next.ServeHTTP(w, r)
				})
			},
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Steam Achievement Tracker",
		Width:            1280,
		Height:           980,
		MinWidth:         900,
		MinHeight:        650,
		MaxWidth:         10000,
		MaxHeight:        10000,
		BackgroundColour: application.NewRGB(27, 38, 54),
		URL:              "/",
	})

	// Run blocks until the application exits. The app quits when its last window closes (the default on Linux).
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
