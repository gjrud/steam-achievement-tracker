package main

import (
	"embed"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	"github.com/gjrud/steam-achievement-tracker/internal/redact"
	"github.com/gjrud/steam-achievement-tracker/internal/update"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var icon []byte

// applicationID is the reverse-DNS application id. Linux uses it as the GTK application id (the Wayland app_id, and
// the name the desktop shell matches to the .desktop file; keep build/linux/Taskfile.yml APP_ID in step). It is also
// the single-instance UniqueID on every platform: Linux claims it as a D-Bus well-known name, and Windows names its
// mutex from it.
const applicationID = "io.github.gjrud.steam-achievement-tracker"

// mainWindow is the main window. main stores it once, after application.New and before app.Run. The second-instance
// callback runs on a Wails goroutine that can fire before that store, so the pointer is atomic.
var mainWindow atomic.Pointer[application.WebviewWindow]

// focusMainWindow brings the main window forward for a second launch. It does nothing when there is no window yet,
// or when the window is hidden: on Linux the window stays hidden until its page has loaded (window.go), and showing
// it through a focus call before then would expose an unpainted window. It calls Focus only, not Restore: Restore
// un-maximises the window in Wails v3.1.0, while Focus un-minimises it on GTK and Windows. The second instance's
// arguments are not used.
func focusMainWindow(win *application.WebviewWindow) {
	if win == nil || !win.IsVisible() {
		return
	}
	win.Focus()
}

func main() {
	// Create the backend. Its exported methods are bound to the frontend through the service below.
	backend := NewApp()

	// WebView2 fails hard if its profile folder cannot be created, which happens before the in-app startup error
	// screen can show. Create the folder here. If that fails, leave the option empty so Wails uses its default profile.
	// Windows only: nothing uses a WebView2 profile on Linux, so no folder is created there.
	webviewPath := ""
	if runtime.GOOS == "windows" {
		webviewPath = filepath.Join(appdata.Resolve().CacheDir, "webview")
		if err := os.MkdirAll(webviewPath, 0o700); err != nil {
			webviewPath = ""
		}
	}

	app := application.New(application.Options{
		Name:        "steam-achievement-tracker",
		Description: "Track Steam achievement progress for your library",
		// On GTK4 this does not set the window or taskbar icon (GTK4 removed that API). The shell takes the icon from
		// the installed .desktop file's Icon= entry (see build/linux/Taskfile.yml and nfpm.yaml). It is kept for
		// dialogs and for the app-level default.
		Icon: icon,
		// Wails warnings and errors (service shutdown, asset errors, failed bound calls) go to the app log file. Setting
		// Logger replaces Wails's default: LogLevel is ignored and the default release logger, which discards
		// everything, is not used. See newWailsLogger for the level.
		Logger: newWailsLogger(logWriter{}),
		Windows: application.WindowsOptions{
			// WebView2 profile (browser cache and state) under the app's cache directory, created above. Wails' default
			// is %AppData%\<exe name>, and releases rename the portable exe per version, so each version would start a
			// new profile and orphan the old one. This path is fixed. Ignored on Linux. The cache directory is kept on
			// uninstall (build/windows/nsis/project.nsi).
			WebviewUserDataPath: webviewPath,
		},
		Linux: application.LinuxOptions{
			// Reverse-DNS GTK application id. GTK sends it as the Wayland app_id, and Wails derives the program name
			// (X11 class) from it. The packaged .desktop file and icons use the same name (APP_ID in
			// build/linux/Taskfile.yml); keep them in step, or the shell shows the wrong icon.
			ApplicationID: applicationID,
		},
		// Only one instance runs. A second launch hands its arguments to the first instance, which focuses its window,
		// and then exits inside application.New, before any window or service starts. On Linux the lock is a D-Bus
		// session-bus name, so a session without a bus makes application.New fail (see docs/platform.md). The lock is
		// released when the process exits; the updater starts the new version only after the old process has exited
		// (see docs/platform.md).
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: applicationID,
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				focusMainWindow(mainWindow.Load())
			},
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

	// The in-app updater runs headless: its state reaches the frontend through update:changed, and the framework's
	// own events reach Go only, through OnEvent. Init reads the version stamp; a build without one has no updater. The
	// startup check starts later, from ServiceStartup.
	updates := update.New(update.Deps{
		Updater: app.Updater,
		Context: backend.context(),
		OnEvent: func(name string, fn func(data any)) func() {
			return app.Event.On(name, func(event *application.CustomEvent) { fn(event.Data) })
		},
		ChecksEnabled: backend.updateChecksEnabled,
		OnChanged:     func() { app.Event.Emit(updateChangedEvent) },
	})
	backend.setUpdater(updates)
	updates.Init()

	mainWindow.Store(setupMainWindow(app, runtime.GOOS))

	// Run blocks until the application exits. The app quits when its last window closes (the default on Linux).
	if err := app.Run(); err != nil {
		log.Fatal(redact.Paths(err))
	}
}
