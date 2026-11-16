package logging

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"
)

// mu guards current, the lumberjack logger that log currently writes to through Init. Holding an open file blocks
// removal of its directory on Windows (t.TempDir cleanup, for example), so Init and Fallback close it when they
// switch away from it.
//
// Every write goes through Writer, which takes mu. Init and Fallback swap current and close the old logger while
// holding mu, so no write can reach a logger after it is closed, and nothing reopens a released file. log is pointed
// at Writer once, in init, and never through SetOutput under mu: log holds its own lock while it calls Write, so a
// SetOutput under mu would invert the lock order.
var (
	mu      sync.Mutex
	current *lumberjack.Logger
)

func init() {
	log.SetOutput(Writer())
}

// Writer returns the writer behind the standard logger. Each Write resolves the current file under mu, and writes to
// os.Stderr when no file is open (before Init, or after Fallback).
func Writer() io.Writer {
	return writer{}
}

type writer struct{}

func (writer) Write(p []byte) (int, error) {
	mu.Lock()
	defer mu.Unlock()
	if current == nil {
		return os.Stderr.Write(p)
	}
	return current.Write(p)
}

func Init(path string) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		_ = os.Chmod(filepath.Dir(path), 0o700)
	}
	if file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_ = file.Close()
		_ = os.Chmod(path, 0o600)
	}
	next := &lumberjack.Logger{
		Filename:   path,
		MaxSize:    5,
		MaxBackups: 5,
		Compress:   false,
	}
	mu.Lock()
	prev := current
	current = next
	if prev != nil {
		_ = prev.Close()
	}
	mu.Unlock()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
	log.Printf("logging initialized")
}

func Fallback() {
	mu.Lock()
	defer mu.Unlock()
	if current != nil {
		_ = current.Close()
		current = nil
	}
}
