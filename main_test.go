package main

import "testing"

// A second launch can arrive before the main window exists, so a nil window must be ignored without a panic. A visible
// window is brought forward only with a running Wails app, which these unit tests do not start.
func TestFocusMainWindowNilSafe(t *testing.T) {
	focusMainWindow(nil)
}
