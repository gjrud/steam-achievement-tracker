package logging

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesPrivateLogFile(t *testing.T) {
	prev := log.Writer()
	t.Cleanup(func() {
		Fallback()
		log.SetOutput(prev)
	})

	logDir := filepath.Join(t.TempDir(), "logs")
	logPath := filepath.Join(logDir, "app.log")

	Init(logPath)

	dirInfo, err := os.Stat(logDir)
	if err != nil {
		t.Fatalf("stat log dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("log dir mode = %o, want 700", perm)
	}

	fileInfo, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat log file: %v", err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("log file mode = %o, want 600", perm)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "logging initialized") {
		t.Fatalf("log file missing init message, got %q", data)
	}
}

func TestFallbackRoutesLogsToStderr(t *testing.T) {
	prev := log.Writer()
	t.Cleanup(func() { log.SetOutput(prev) })

	Fallback()

	if log.Writer() != os.Stderr {
		t.Fatalf("log writer = %v, want os.Stderr", log.Writer())
	}
}
