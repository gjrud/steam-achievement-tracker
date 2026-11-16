package main

import (
	"context"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/logging"
)

func TestLogWriterForwardsSlogToAppLog(t *testing.T) {
	prevFlags := log.Flags()
	// TempDir is registered first so its removal runs last: the Fallback cleanup must close the log file before
	// the directory is removed (cleanups run last-in first-out).
	dir := t.TempDir()
	t.Cleanup(func() { log.SetFlags(prevFlags) })
	t.Cleanup(logging.Fallback)

	// Build the logger before the log file is opened: logWriter must resolve the file at write time.
	logger := newWailsLogger(logWriter{})

	first := filepath.Join(dir, "first", "app.log")
	logging.Init(first)
	logger.Warn("wails message", "source", "test")
	logger.Info("wails info")

	got := readFileString(t, first)
	if !strings.Contains(got, `msg="wails message"`) || !strings.Contains(got, "source=test") {
		t.Fatalf("first log = %q, want the warning record", got)
	}
	if strings.Contains(got, "wails info") {
		t.Fatalf("first log = %q, info record passed a warn-level handler", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Fatalf("first log = %q, want one record per line", got)
	}

	// Opening a new log file after the writer exists must be honored.
	second := filepath.Join(dir, "second", "app.log")
	logging.Init(second)
	logger.Error("after swap")

	if got := readFileString(t, second); !strings.Contains(got, `msg="after swap"`) {
		t.Fatalf("second log = %q, want the record after Init", got)
	}
	if strings.Contains(readFileString(t, first), "after swap") {
		t.Fatal("first log file still receives records after Init switched files")
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// Wails logs bound-method call arguments, which include the API key, at Debug. The logger must keep Debug disabled.
func TestWailsLoggerDisablesDebug(t *testing.T) {
	if newWailsLogger(io.Discard).Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("Wails logger enables Debug; bound-call arguments (including the API key) would be logged")
	}
}
