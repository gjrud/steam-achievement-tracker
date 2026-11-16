package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"log/slog"
	"strings"
	"testing"
)

func TestLogWriterForwardsSlogToStdLogOutput(t *testing.T) {
	prevOut := log.Writer()
	prevFlags := log.Flags()
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	// Build the logger before choosing the std log output: logWriter must pick the output at write time.
	logger := newWailsLogger(logWriter{})

	var first bytes.Buffer
	log.SetOutput(&first)
	logger.Warn("wails message", "source", "test")
	logger.Info("wails info")
	if got := first.String(); !strings.Contains(got, `msg="wails message"`) || !strings.Contains(got, "source=test") {
		t.Fatalf("first output = %q, want the warning record", got)
	}
	if strings.Contains(first.String(), "wails info") {
		t.Fatalf("first output = %q, info record passed a warn-level handler", first.String())
	}
	if !strings.HasSuffix(first.String(), "\n") {
		t.Fatalf("first output = %q, want one record per line", first.String())
	}

	// Swapping the std log output after the writer exists must be honored.
	var second bytes.Buffer
	log.SetOutput(&second)
	logger.Error("after swap")
	if got := second.String(); !strings.Contains(got, `msg="after swap"`) {
		t.Fatalf("second output = %q, want the record after SetOutput", got)
	}
	if strings.Contains(first.String(), "after swap") {
		t.Fatalf("first buffer still receives output after SetOutput: %q", first.String())
	}
}

// Wails logs bound-method call arguments, which include the API key, at Debug. The logger must keep Debug disabled.
func TestWailsLoggerDisablesDebug(t *testing.T) {
	if newWailsLogger(io.Discard).Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("Wails logger enables Debug; bound-call arguments (including the API key) would be logged")
	}
}
