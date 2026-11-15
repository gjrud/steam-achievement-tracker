package main

import (
	"io"
	"log"
	"log/slog"
)

// logWriter forwards each write to the standard library logger's current output. Wails's own logger writes to
// stderr, so main hands it a slog handler on top of logWriter to put its messages in the app log file.
//
// The output is resolved on every Write, not when the writer is built: logging.Init swaps log's output inside
// ServiceStartup, after main has created the application, so capturing it earlier would send Wails messages to
// the wrong place.
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	return log.Writer().Write(p)
}

// newWailsLogger returns the logger handed to Wails. It keeps warnings and errors only: at Info Wails logs one line
// per embedded asset request and platform details on every launch, which would only add noise to the app log. Errors
// of bound methods (for example a failed profile check) are logged at Error and are kept.
func newWailsLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelWarn}))
}
