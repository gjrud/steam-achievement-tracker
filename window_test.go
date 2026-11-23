package main

import "testing"

func TestMainWindowOptionsWindowsIsFramelessFromCreation(t *testing.T) {
	opts := mainWindowOptions("windows")
	if !opts.Frameless {
		t.Error("Frameless = false, want true on windows")
	}
	if opts.Hidden {
		t.Error("Hidden = true, want false on windows")
	}
	// Decision D2 of issue #31: keep the Wails default frameless decorations.
	if opts.Windows.DisableFramelessWindowDecorations {
		t.Error("DisableFramelessWindowDecorations = true, want false")
	}
}

func TestMainWindowOptionsLinuxStartsHiddenAndDecorated(t *testing.T) {
	// Frameless at creation would drop the GTK title (Wails skips gtk_window_set_title). The window is made
	// frameless and shown by setupMainWindow once the page has loaded (WindowLoadFinished).
	opts := mainWindowOptions("linux")
	if opts.Frameless {
		t.Error("Frameless = true, want false at creation on linux")
	}
	if !opts.Hidden {
		t.Error("Hidden = false, want true at creation on linux")
	}
}

func TestMainWindowOptionsCommon(t *testing.T) {
	for _, goos := range []string{"windows", "linux"} {
		opts := mainWindowOptions(goos)
		if opts.Title != "Steam Achievement Tracker" {
			t.Errorf("%s: Title = %q", goos, opts.Title)
		}
		if opts.Width != 1280 || opts.Height != 980 || opts.MinWidth != 900 || opts.MinHeight != 650 {
			t.Errorf("%s: size = %dx%d min %dx%d", goos, opts.Width, opts.Height, opts.MinWidth, opts.MinHeight)
		}
		if opts.BackgroundColour.Alpha != 255 {
			t.Errorf("%s: background is not opaque", goos)
		}
	}
}
