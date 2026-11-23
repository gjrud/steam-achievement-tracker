package main

import (
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// The window is frameless: the title bar is drawn by the frontend (TitleBar.svelte). See docs/app-service.md.

// mainWindowOptions returns the options for the main window. goos is runtime.GOOS; it is a parameter so the
// platform split can be tested.
//
// Windows: frameless from creation, with the Wails default frameless decorations.
//
// Linux: Wails skips gtk_window_set_title once Frameless is set at creation, so a frameless-at-creation window
// has no GTK title (the title shown in the taskbar, task switcher and overview). The window is therefore created
// hidden and decorated (so it gets its title), and setupMainWindow turns it frameless and shows it once the page
// has loaded.
func mainWindowOptions(goos string) application.WebviewWindowOptions {
	opts := application.WebviewWindowOptions{
		Title:            "Steam Achievement Tracker",
		Width:            1280,
		Height:           980,
		MinWidth:         900,
		MinHeight:        650,
		BackgroundColour: application.NewRGB(27, 38, 54),
	}
	if goos == "windows" {
		opts.Frameless = true
	} else {
		opts.Hidden = true
	}
	return opts
}

// setupMainWindow creates the main window. It must run before app.Run: the window is then created by Run, so the
// listener below is registered before the first event can fire.
func setupMainWindow(app *application.App, goos string) *application.WebviewWindow {
	win := app.Window.NewWithOptions(mainWindowOptions(goos))
	if goos != "windows" {
		// WindowLoadFinished, not WindowRuntimeReady: Wails' own WindowLoadFinished hook (webview_window_linux.go)
		// injects flags.frameless from the options, and hooks run before listeners for the same event, so
		// SetFrameless below always pushes its flag last. RuntimeReady has no such ordering (a race could leave the
		// JS flag false and the resize zones off), and it never fires if the frontend bundle fails to run, which
		// would leave the window hidden forever. LoadFinished is documented by WebKit to fire after a failed load
		// too (not tested here). The GTK window exists by then, so SetFrameless reaches it.
		//
		// After this, Wails ignores SetTitle on Linux (linux_cgo.go setTitle skips Frameless windows).
		var once sync.Once
		win.OnWindowEvent(events.Linux.WindowLoadFinished, func(*application.WindowEvent) {
			once.Do(func() {
				win.SetFrameless(true)
				win.Show()
			})
		})
	}
	return win
}
