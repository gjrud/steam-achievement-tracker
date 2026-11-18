package main

import (
	"io"
	"log/slog"

	"github.com/gjrud/steam-achievement-tracker/internal/logging"
)

// logWriter forwards each write to the app log file. Wails's own logger writes to stderr, so main hands it a slog
// handler on top of logWriter to put its messages in the app log file.
//
// The file is resolved on every Write, through logging.Writer, not when the writer is built: logging.Init opens the
// file inside ServiceStartup, after main has created the application. Until then, and after logging.Fallback, writes
// go to stderr. Writes take the logging package lock, so they cannot reach a file that Init or Fallback has closed.
type logWriter struct{}

func (logWriter) Write(p []byte) (int, error) {
	return logging.Writer().Write(p)
}

// newWailsLogger returns the logger handed to Wails. It keeps warnings and errors only: at Info Wails logs one line
// per embedded asset request and platform details on every launch, which would only add noise to the app log. Errors
// of bound methods (for example a failed profile check) are logged at Error and are kept.
//
// Rules for this logger, because it writes to app.log:
//   - It must never enable Debug. Wails logs the arguments of every bound-method call at Debug, and those arguments
//     include the Steam Web API key passed to SaveAPIKey. Keep the level at Warn (or higher).
//   - Errors returned by bound methods are kept at Error with their text. Every bound method returns only the
//     user-facing sentence built by userFacingError (app_errors.go), so that text carries no path, SQL, backend text
//     or key. The raw error goes to the app log through userFacingError, with paths reduced by redact.Paths.
//   - Panics in bound methods are logged by callBound (app_errors.go), not by Wails. Their log line goes through
//     steam.RedactSecrets, so a panic value that carries the API key or a SteamID64 is scrubbed before it is written.
func newWailsLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelWarn}))
}
