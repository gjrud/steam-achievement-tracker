//go:build !(linux && cgo && !gtk3 && !android && !server && !purego)

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// forceClientDecorations is a no-op outside the GTK4 build (see window_csd_gtk4.go). Windows and macOS need no extra
// step. Linux purego and GTK3 builds (not shipped) do not get it, so a compositor title bar may appear there on GTK < 4.16.
func forceClientDecorations(*application.App, *application.WebviewWindow) {}
