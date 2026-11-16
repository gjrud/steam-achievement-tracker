package main

import (
	"embed"
	"log"

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
		// On GTK4 this does not set the window or taskbar icon (GTK4 removed that API). The shell takes the icon from
		// the installed .desktop file's Icon= entry (see build/linux/Taskfile.yml and nfpm.yaml). It is kept for
		// dialogs and for the app-level default.
		Icon: icon,
		// Wails warnings and errors (service shutdown, asset errors, failed bound calls) go to the app log file. Setting
		// Logger replaces Wails's default: LogLevel is ignored and the default release logger, which discards
		// everything, is not used. See newWailsLogger for the level.
		Logger: newWailsLogger(logWriter{}),
		Linux: application.LinuxOptions{
			// Reverse-DNS GTK application id. GTK sends it as the Wayland app_id, and Wails derives the program name
			// (X11 class) from it. The packaged .desktop file and icons use the same name (APP_ID in
			// build/linux/Taskfile.yml); keep them in step, or the shell shows the wrong icon.
			ApplicationID: "io.github.gjrud.steam-achievement-tracker",
		},
		Services: []application.Service{
			application.NewService(backend),
			// Game covers live on disk under the app data directory, not in the embedded frontend. The cover service
			// is mounted at /game-covers on the asset server, ahead of the embedded assets, and receives paths with
			// that prefix stripped.
			application.NewServiceWithOptions(NewCoverService(backend), application.ServiceOptions{
				Name:  "covers",
				Route: "/game-covers",
			}),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
	})

	// Sync state events reach the frontend through the app's event bus. The backend has no other route to it.
	backend.setEventEmitter(func(name string) { app.Event.Emit(name) })

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Steam Achievement Tracker",
		Width:            1280,
		Height:           980,
		MinWidth:         900,
		MinHeight:        650,
		BackgroundColour: application.NewRGB(27, 38, 54),
	})

	// Run blocks until the application exits. The app quits when its last window closes (the default on Linux).
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
