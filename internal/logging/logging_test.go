package logging

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestInitCreatesPrivateLogFile(t *testing.T) {
	prev := log.Writer()
	// TempDir is registered first so its removal runs last: the Fallback cleanup below must close the log file
	// before the directory is removed (cleanups run last-in first-out).
	logDir := filepath.Join(t.TempDir(), "logs")
	logPath := filepath.Join(logDir, "app.log")
	t.Cleanup(func() {
		Fallback()
		log.SetOutput(prev)
	})

	Init(logPath)

	// POSIX permission bits are not enforced on Windows, so only the content check runs there.
	if runtime.GOOS != "windows" {
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
	stderr := redirectStderr(t)
	// TempDir first, so Fallback (registered after) runs before the directory is removed.
	dir := t.TempDir()
	t.Cleanup(Fallback)

	Init(filepath.Join(dir, "logs", "app.log"))
	Fallback()

	log.Print("to stderr")
	if data := readString(t, stderr); !strings.Contains(data, "to stderr") {
		t.Fatalf("stderr = %q, want the log line after Fallback", data)
	}
}

// TestWriteRacingInitAndFallback hammers Writer from several goroutines while Init and Fallback swap and close the
// file. It must not deadlock or panic, and after Fallback no write may have reopened a released file.
func TestWriteRacingInitAndFallback(t *testing.T) {
	redirectStderr(t)
	dir := t.TempDir()
	t.Cleanup(Fallback)

	paths := []string{filepath.Join(dir, "a", "app.log"), filepath.Join(dir, "b", "app.log")}

	var writers sync.WaitGroup
	for range 4 {
		writers.Go(func() {
			for range 5000 {
				if _, err := Writer().Write([]byte("race line\n")); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		})
	}
	for i := range 50 {
		Init(paths[i%2])
		if i%5 == 4 {
			Fallback()
		}
	}

	done := make(chan struct{})
	go func() {
		writers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("writers did not finish: deadlock between Writer, Init and Fallback")
	}

	Fallback()
	requireNoCurrent(t)
	for _, path := range paths {
		requireNotOpen(t, path)
		requireRenamable(t, path)
	}
}

// redirectStderr points os.Stderr at a temp file for the rest of the test and returns it. Writer resolves os.Stderr
// on each write, so log lines written while no file is open land here.
func redirectStderr(t *testing.T) *os.File {
	t.Helper()
	file, err := os.Create(filepath.Join(t.TempDir(), "stderr"))
	if err != nil {
		t.Fatalf("create stderr file: %v", err)
	}
	prev := os.Stderr
	os.Stderr = file
	t.Cleanup(func() {
		os.Stderr = prev
		_ = file.Close()
	})
	return file
}

func readString(t *testing.T, file *os.File) string {
	t.Helper()
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatalf("read %s: %v", file.Name(), err)
	}
	return string(data)
}

func TestFallbackReleasesLogFile(t *testing.T) {
	prev := log.Writer()
	dir := t.TempDir()
	t.Cleanup(func() { log.SetOutput(prev) })
	t.Cleanup(Fallback)

	logPath := filepath.Join(dir, "logs", "app.log")
	Init(logPath)
	Fallback()

	requireNotOpen(t, logPath)
	requireRenamable(t, logPath)
	requireNoCurrent(t)
}

func TestInitClosesPreviousLogFile(t *testing.T) {
	prev := log.Writer()
	dir := t.TempDir()
	t.Cleanup(func() { log.SetOutput(prev) })
	t.Cleanup(Fallback)

	first := filepath.Join(dir, "first", "app.log")
	second := filepath.Join(dir, "second", "app.log")
	Init(first)
	Init(second)

	requireNotOpen(t, first)
	requireRenamable(t, first)

	// The active file is still open; only the superseded one was released.
	if hasProcFD() {
		if _, ok := openPaths(t)[mustEval(t, second)]; !ok {
			t.Fatalf("active log file %s is not open", second)
		}
	}

	Fallback()
	requireNotOpen(t, second)
	requireRenamable(t, second)
	requireNoCurrent(t)
}

// requireNoCurrent checks the bookkeeping: after Fallback no lumberjack logger is held.
func requireNoCurrent(t *testing.T) {
	t.Helper()
	mu.Lock()
	defer mu.Unlock()
	if current != nil {
		t.Fatal("current logger still held after Fallback")
	}
}

// requireRenamable checks that the log file can be renamed, which Windows refuses while a handle is open without
// delete sharing. It moves the file aside and back again.
func requireRenamable(t *testing.T, path string) {
	t.Helper()
	moved := path + ".moved"
	if err := os.Rename(path, moved); err != nil {
		t.Fatalf("rename log file: %v", err)
	}
	if err := os.Rename(moved, path); err != nil {
		t.Fatalf("rename log file back: %v", err)
	}
}

// requireNotOpen checks that this process holds no open descriptor on path. It needs /proc/self/fd, so it is a
// no-op where that is unavailable; requireRenamable covers the same ground on Windows.
func requireNotOpen(t *testing.T, path string) {
	t.Helper()
	if !hasProcFD() {
		return
	}
	if _, ok := openPaths(t)[mustEval(t, path)]; ok {
		t.Fatalf("log file %s still open after release", path)
	}
}

func hasProcFD() bool {
	_, err := os.Stat("/proc/self/fd")
	return err == nil
}

// openPaths returns the set of file paths this process has open, as resolved from /proc/self/fd.
func openPaths(t *testing.T) map[string]struct{} {
	t.Helper()
	paths := make(map[string]struct{})
	if !hasProcFD() {
		return paths
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err != nil {
			continue // the descriptor may have closed since ReadDir
		}
		paths[target] = struct{}{}
	}
	return paths
}

// mustEval resolves symlinks in path so it compares equal to the targets reported by /proc/self/fd.
func mustEval(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return resolved
}
