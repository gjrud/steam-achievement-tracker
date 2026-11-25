package update

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// touchFile creates a small regular file at p.
func touchFile(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// makeDir creates a directory at p.
func makeDir(t *testing.T, p string) {
	t.Helper()
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
}

// ageFiles sets the mtime of each path to just past leftoverMinAge ago, so the sweep's age gate sees it as idle. Call
// it after a directory's entries are made: adding an entry moves the directory's own mtime.
func ageFiles(t *testing.T, paths ...string) {
	t.Helper()
	old := time.Now().Add(-(leftoverMinAge + time.Hour))
	for _, p := range paths {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
}

// pathExists reports whether p exists, without following a symlink.
func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// captureLog routes the standard log to a buffer for the test. It is restored at cleanup.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// waitPostUpdate waits for the goroutine RunPostUpdate started. It fails when none started, or when it does not return
// in time. The test cancels the service context first, so the goroutine ends at the latest when the test does.
func waitPostUpdate(t *testing.T, s *Service) {
	t.Helper()
	if s.postDone == nil {
		t.Fatal("RunPostUpdate started no goroutine")
	}
	select {
	case <-s.postDone:
	case <-time.After(10 * time.Second):
		t.Fatal("post-update goroutine did not return")
	}
}

// The Windows sweep removes the .bak file, the renamed-aside exe with a digit suffix, a staging directory and the helper
// log. Names that only look like them stay, the target itself stays, and the log holds no path.
func TestSweepRemovesWindowsLeftovers(t *testing.T) {
	logs := captureLog(t)
	dir := t.TempDir()
	tmp := t.TempDir()
	target := filepath.Join(dir, "steam-achievement-tracker.exe")

	touchFile(t, target)
	touchFile(t, target+".bak")
	touchFile(t, target+".old.1763900000000000000")
	makeDir(t, filepath.Join(dir, "wails-update-123456"))
	touchFile(t, filepath.Join(tmp, "wails-update-4242.log"))

	touchFile(t, target+".old.abc")
	touchFile(t, target+".old.")
	touchFile(t, filepath.Join(dir, "other.exe.old.123"))
	touchFile(t, filepath.Join(dir, "unrelated.txt"))
	makeDir(t, filepath.Join(dir, "unrelated-dir"))
	makeDir(t, target+".old.77")
	touchFile(t, filepath.Join(tmp, "wails-update-4242-5.ready"))
	ageFiles(t, target+".bak", filepath.Join(dir, "wails-update-123456"), filepath.Join(tmp, "wails-update-4242.log"))

	s := testService(t, Deps{})
	s.sweepLeftovers(fakeEnv("windows", target, nil), target, tmp)

	for _, gone := range []string{target + ".bak", target + ".old.1763900000000000000", filepath.Join(dir, "wails-update-123456"), filepath.Join(tmp, "wails-update-4242.log")} {
		if pathExists(gone) {
			t.Errorf("leftover %s was not removed", filepath.Base(gone))
		}
	}
	for _, kept := range []string{target, target + ".old.abc", target + ".old.", filepath.Join(dir, "other.exe.old.123"), filepath.Join(dir, "unrelated.txt"), filepath.Join(dir, "unrelated-dir"), target + ".old.77", filepath.Join(tmp, "wails-update-4242-5.ready")} {
		if !pathExists(kept) {
			t.Errorf("unrelated path %s was removed", filepath.Base(kept))
		}
	}

	out := logs.String()
	if !strings.Contains(out, "update: cleanup: removed 4 leftovers") {
		t.Fatalf("log = %q, want the removed count", out)
	}
	if strings.Contains(out, "could not be removed") {
		t.Fatalf("log reports failures: %q", out)
	}
	if strings.Contains(out, dir) || strings.Contains(out, tmp) {
		t.Fatalf("log holds a path: %q", out)
	}
}

// The renamed-aside .old files are a Windows-only leftover. Under a non-Windows env they stay, and the .bak and the
// staging directory are still swept.
func TestSweepLinuxKeepsOldExeNames(t *testing.T) {
	dir := t.TempDir()
	tmp := t.TempDir()
	target := filepath.Join(dir, "steam-achievement-tracker.AppImage")

	touchFile(t, target)
	touchFile(t, target+".bak")
	touchFile(t, target+".old.123")
	makeDir(t, filepath.Join(dir, "wails-update-9"))
	ageFiles(t, target+".bak", filepath.Join(dir, "wails-update-9"))

	s := testService(t, Deps{})
	s.sweepLeftovers(fakeEnv("linux", target, nil), target, tmp)

	if pathExists(target + ".bak") {
		t.Error(".bak was not removed")
	}
	if pathExists(filepath.Join(dir, "wails-update-9")) {
		t.Error("staging directory was not removed")
	}
	if !pathExists(target + ".old.123") {
		t.Error(".old file removed on a non-Windows env")
	}
	if !pathExists(target) {
		t.Error("target removed")
	}
}

// Without a target there is nothing to sweep: no file is touched, not even the temp log.
func TestSweepSkipsWithoutTarget(t *testing.T) {
	tmp := t.TempDir()
	touchFile(t, filepath.Join(tmp, "wails-update-1.log"))

	s := testService(t, Deps{})
	s.sweepLeftovers(fakeEnv("linux", "/usr/local/bin/steam-achievement-tracker", nil), "", tmp)

	if !pathExists(filepath.Join(tmp, "wails-update-1.log")) {
		t.Fatal("sweep with no target removed the temp log")
	}
}

// Staging directories are not removed while an update is in flight or staged. Once the phase is idle again they are.
func TestSweepSkipsStagingWhileInFlight(t *testing.T) {
	inFlight := []Phase{PhaseDownloading, PhaseVerifying, PhaseInstalling, PhaseReady, PhaseRestarting}
	for _, p := range inFlight {
		t.Run(string(p), func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "steam-achievement-tracker.AppImage")
			staging := filepath.Join(dir, "wails-update-7")
			makeDir(t, staging)
			ageFiles(t, staging)

			s := testService(t, Deps{})
			s.phase = p
			s.sweepLeftovers(fakeEnv("linux", target, nil), target, "")
			if !pathExists(staging) {
				t.Fatalf("staging directory removed while %s", p)
			}
		})
	}

	t.Run("available", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "steam-achievement-tracker.AppImage")
		staging := filepath.Join(dir, "wails-update-7")
		makeDir(t, staging)
		ageFiles(t, staging)

		s := testService(t, Deps{})
		s.phase = PhaseAvailable
		s.sweepLeftovers(fakeEnv("linux", target, nil), target, "")
		if pathExists(staging) {
			t.Fatal("staging directory of an idle service was not removed")
		}
	})
}

// A staging directory is removed only when it is flat and idle for leftoverMinAge. Each other shape stays.
func TestSweepStagingDirShape(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, staging string)
		want  bool // true when the sweep removes it
	}{
		{"old and flat", func(t *testing.T, d string) {
			makeDir(t, d)
			touchFile(t, filepath.Join(d, "payload"))
			ageFiles(t, filepath.Join(d, "payload"), d)
		}, true},
		{"old and empty", func(t *testing.T, d string) {
			makeDir(t, d)
			ageFiles(t, d)
		}, true},
		{"too new", func(t *testing.T, d string) {
			makeDir(t, d)
			touchFile(t, filepath.Join(d, "payload"))
		}, false},
		{"old directory with a new file", func(t *testing.T, d string) {
			makeDir(t, d)
			touchFile(t, filepath.Join(d, "payload"))
			ageFiles(t, d)
		}, false},
		{"old with a subdirectory", func(t *testing.T, d string) {
			makeDir(t, d)
			makeDir(t, filepath.Join(d, "sub"))
			ageFiles(t, filepath.Join(d, "sub"), d)
		}, false},
		{"old with a symlink", func(t *testing.T, d string) {
			makeDir(t, d)
			if err := os.Symlink(filepath.Join(d, "missing"), filepath.Join(d, "link")); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			ageFiles(t, d)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "steam-achievement-tracker.AppImage")
			staging := filepath.Join(dir, "wails-update-3")
			tc.build(t, staging)

			s := testService(t, Deps{})
			s.sweepLeftovers(fakeEnv("linux", target, nil), target, "")
			if got := !pathExists(staging); got != tc.want {
				t.Fatalf("staging directory removed = %v, want %v", got, tc.want)
			}
		})
	}
}

// A file added to a staging directory after its listing is not deleted. The directory's final remove fails, so the
// directory stays and the failure is counted. Only the listed file is removed.
func TestSweepStagingLateEntryKept(t *testing.T) {
	logs := captureLog(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "steam-achievement-tracker.AppImage")
	staging := filepath.Join(dir, "wails-update-5")
	makeDir(t, staging)
	listed := filepath.Join(staging, "payload")
	late := filepath.Join(staging, "late")
	touchFile(t, listed)
	ageFiles(t, listed, staging)

	prev := removeFile
	removeFile = func(p string) error {
		touchFile(t, late) // a new file appears between the listing and the removals
		return os.Remove(p)
	}
	t.Cleanup(func() { removeFile = prev })

	s := testService(t, Deps{})
	s.sweepLeftovers(fakeEnv("linux", target, nil), target, "")

	if pathExists(listed) {
		t.Error("listed file was not removed")
	}
	if !pathExists(late) {
		t.Fatal("file added after the listing was removed")
	}
	if !pathExists(staging) {
		t.Fatal("staging directory with a late entry was removed")
	}
	if out := logs.String(); !strings.Contains(out, "update: cleanup: 1 leftovers could not be removed") {
		t.Fatalf("log = %q, want the failed directory counted", out)
	}
}

// The .bak is removed only as a file or symlink, and only once idle for leftoverMinAge. A directory with that name stays.
func TestSweepBakRules(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, p string)
		want  bool // true when the sweep removes it
	}{
		{"old file", func(t *testing.T, p string) {
			touchFile(t, p)
			ageFiles(t, p)
		}, true},
		{"too new file", func(t *testing.T, p string) {
			touchFile(t, p)
		}, false},
		{"old directory", func(t *testing.T, p string) {
			makeDir(t, p)
			ageFiles(t, p)
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "steam-achievement-tracker.AppImage")
			bak := target + ".bak"
			tc.build(t, bak)

			s := testService(t, Deps{})
			s.sweepLeftovers(fakeEnv("linux", target, nil), target, "")
			if got := !pathExists(bak); got != tc.want {
				t.Fatalf(".bak removed = %v, want %v", got, tc.want)
			}
		})
	}
}

// Only the exact helper log name is swept from the temp directory. Look-alikes stay.
func TestTempLogPattern(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"wails-update-123.log", true},
		{"wails-update-1.log", true},
		{"wails-update-123-456.ready", false},
		{"wails-update-abc.log", false},
		{"my-wails-update-1.log", false},
		{"wails-update-1.log.bak", false},
		{"wails-update-.log", false},
	}
	for _, tc := range cases {
		if got := tempLogPattern.MatchString(tc.name); got != tc.want {
			t.Errorf("tempLogPattern.MatchString(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The sweep removes only matching regular files from the temp directory, never a directory with the same name.
func TestSweepTempLogsOnlyRegularFiles(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(t.TempDir(), "steam-achievement-tracker.AppImage")
	touchFile(t, filepath.Join(tmp, "wails-update-55.log"))
	touchFile(t, filepath.Join(tmp, "wails-update-56.ready"))
	makeDir(t, filepath.Join(tmp, "wails-update-57.log"))
	ageFiles(t, filepath.Join(tmp, "wails-update-55.log"), filepath.Join(tmp, "wails-update-56.ready"))

	s := testService(t, Deps{})
	s.sweepLeftovers(fakeEnv("linux", target, nil), target, tmp)

	if pathExists(filepath.Join(tmp, "wails-update-55.log")) {
		t.Error("temp log not removed")
	}
	if !pathExists(filepath.Join(tmp, "wails-update-56.ready")) {
		t.Error("non-log file removed")
	}
	if !pathExists(filepath.Join(tmp, "wails-update-57.log")) {
		t.Error("directory removed from the temp directory")
	}
}

// A helper log younger than leftoverMinAge may belong to a live helper of another app or instance, so it stays. Once
// it is older than that, the sweep removes it. Two hours old is under the one-day gate, so it stays even though it is
// far past the old ten-minute gate.
func TestSweepTempLogNeedsIdleAge(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(t.TempDir(), "steam-achievement-tracker.AppImage")
	young := filepath.Join(tmp, "wails-update-58.log")
	old := filepath.Join(tmp, "wails-update-59.log")
	touchFile(t, young)
	touchFile(t, old)
	youngWhen := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(young, youngWhen, youngWhen); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-(leftoverMinAge + time.Hour))
	if err := os.Chtimes(old, when, when); err != nil {
		t.Fatal(err)
	}

	s := testService(t, Deps{})
	s.sweepLeftovers(fakeEnv("linux", target, nil), target, tmp)

	if !pathExists(young) {
		t.Error("young temp log removed")
	}
	if pathExists(old) {
		t.Error("idle temp log not removed")
	}
}

// The age gate is one day. Files idle for less stay; a file idle for more than a day is removable. The boundary itself
// is not tested: file systems round mtimes, so an exact 24 h value is not reliable.
func TestLeftoverMinAgeIsOneDay(t *testing.T) {
	if leftoverMinAge != 24*time.Hour {
		t.Fatalf("leftoverMinAge = %v, want 24h", leftoverMinAge)
	}
	bak := filepath.Join(t.TempDir(), "steam-achievement-tracker.AppImage.bak")
	touchFile(t, bak)
	now := time.Now()
	cases := []struct {
		age  time.Duration
		want bool
	}{
		{10 * time.Minute, false},
		{2 * time.Hour, false},
		{23 * time.Hour, false},
		{25 * time.Hour, true},
	}
	for _, tc := range cases {
		when := now.Add(-tc.age)
		if err := os.Chtimes(bak, when, when); err != nil {
			t.Fatal(err)
		}
		if got := bakRemovable(bak, now); got != tc.want {
			t.Errorf("bakRemovable at age %v = %v, want %v", tc.age, got, tc.want)
		}
	}
}

// The temp log is kept while the phase is restarting (it is the live helper's log), and swept once idle again.
func TestSweepTempLogSkippedWhileRestarting(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(t.TempDir(), "steam-achievement-tracker.AppImage")
	logFile := filepath.Join(tmp, "wails-update-8.log")
	touchFile(t, logFile)
	ageFiles(t, logFile)

	s := testService(t, Deps{})
	s.phase = PhaseRestarting
	s.sweepLeftovers(fakeEnv("linux", target, nil), target, tmp)
	if !pathExists(logFile) {
		t.Fatal("temp log removed while restarting")
	}

	s.phase = PhaseIdle
	s.sweepLeftovers(fakeEnv("linux", target, nil), target, tmp)
	if pathExists(logFile) {
		t.Fatal("temp log not removed once idle")
	}
}

// A temp log that vanishes before its removal, or that another user owns, is neither removed nor a failure. A removal
// that fails for another reason is counted.
func TestSweepTempLogErrors(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		wantLog string // substring of the log; "" means nothing is logged
	}{
		{"vanished", &fs.PathError{Op: "remove", Path: "x", Err: fs.ErrNotExist}, ""},
		{"another user's file", &fs.PathError{Op: "remove", Path: "x", Err: fs.ErrPermission}, ""},
		{"other failure", errors.New("busy"), "update: cleanup: 1 leftovers could not be removed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			tmp := t.TempDir()
			target := filepath.Join(t.TempDir(), "steam-achievement-tracker.AppImage")
			logFile := filepath.Join(tmp, "wails-update-9.log")
			touchFile(t, logFile)
			ageFiles(t, logFile)

			prev := removeFile
			removeFile = func(string) error { return tc.err }
			t.Cleanup(func() { removeFile = prev })

			s := testService(t, Deps{})
			s.sweepLeftovers(fakeEnv("linux", target, nil), target, tmp)

			out := logs.String()
			if tc.wantLog == "" {
				if out != "" {
					t.Fatalf("log = %q, want nothing", out)
				}
			} else if !strings.Contains(out, tc.wantLog) {
				t.Fatalf("log = %q, want %q", out, tc.wantLog)
			}
		})
	}
}

// A sweep that finds nothing logs nothing.
func TestSweepQuietWhenNothingFound(t *testing.T) {
	logs := captureLog(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "steam-achievement-tracker.AppImage")
	touchFile(t, target)

	s := testService(t, Deps{})
	s.sweepLeftovers(fakeEnv("linux", target, nil), target, t.TempDir())
	if logs.Len() != 0 {
		t.Fatalf("sweep with nothing to remove logged %q", logs.String())
	}
}

// Without a stamp the service does nothing: no goroutine starts, so a leftover is never swept.
func TestRunPostUpdateNoOpWhenUnavailable(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "steam-achievement-tracker.AppImage")
	mount := filepath.Join(dir, "mount")
	exe := filepath.Join(mount, "usr", "bin", "steam-achievement-tracker")
	touchFile(t, img)
	touchFile(t, img+".bak")
	ageFiles(t, img+".bak")

	s := testService(t, Deps{})
	s.Init() // no stamp in the test binary, so the service is unavailable
	s.env = fakeEnv("linux", exe, map[string]string{"APPIMAGE": img, "APPDIR": mount})
	s.RunPostUpdate()
	s.RunPostUpdate()
	if s.postDone != nil {
		t.Fatal("unavailable service started a post-update goroutine")
	}
	if !pathExists(img + ".bak") {
		t.Fatal("unavailable service swept its leftovers")
	}
}

// RunPostUpdate on an available service sweeps once. A second call starts no goroutine, so a file that appears later stays.
func TestRunPostUpdateSweepsOnce(t *testing.T) {
	prev := cleanupDelay
	cleanupDelay = 0
	t.Cleanup(func() { cleanupDelay = prev })

	dir := t.TempDir()
	img := filepath.Join(dir, "steam-achievement-tracker.AppImage")
	mount := filepath.Join(dir, "mount")
	exe := filepath.Join(mount, "usr", "bin", "steam-achievement-tracker")
	touchFile(t, img)
	touchFile(t, img+".bak")
	ageFiles(t, img+".bak")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := testService(t, Deps{Context: ctx})
	s.unavailable = false
	s.current = "1.2.3"
	// On a Windows host the version sync also runs, with this fake exe path. It never equals a real DisplayIcon, so the
	// installer's HKCU entry is not written.
	s.env = fakeEnv("linux", exe, map[string]string{"APPIMAGE": img, "APPDIR": mount})

	s.RunPostUpdate()
	waitPostUpdate(t, s)
	if pathExists(img + ".bak") {
		t.Fatal("first RunPostUpdate did not sweep the .bak")
	}

	first := s.postDone
	touchFile(t, img+".bak")
	ageFiles(t, img+".bak")
	s.RunPostUpdate()
	if s.postDone != first {
		t.Fatal("second RunPostUpdate started another goroutine")
	}
	if !pathExists(img + ".bak") {
		t.Fatal("second RunPostUpdate swept again")
	}
}

// The sweep target: the exe on Windows, the AppImage only for an AppImage run, and nothing for a package install.
func TestPostUpdateTarget(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "steam-achievement-tracker.AppImage")
	mount := filepath.Join(dir, "mount")
	inMount := filepath.Join(mount, "usr", "bin", "steam-achievement-tracker")
	touchFile(t, img)

	cases := []struct {
		name string
		env  hostEnv
		want string
	}{
		{"windows exe", fakeEnv("windows", `C:\app\sat.exe`, nil), `C:\app\sat.exe`},
		{"AppImage run", fakeEnv("linux", inMount, map[string]string{"APPIMAGE": img, "APPDIR": mount}), img},
		{"linux package install", fakeEnv("linux", "/usr/bin/steam-achievement-tracker", map[string]string{"APPIMAGE": img, "APPDIR": mount}), ""},
		{"other platform", fakeEnv("darwin", "/Applications/sat", nil), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := postUpdateTarget(tc.env); got != tc.want {
				t.Fatalf("postUpdateTarget = %q, want %q", got, tc.want)
			}
		})
	}
}

// The .old name matches on Windows in any letter case. Elsewhere the match is case-sensitive. The digits must be ASCII
// and at least one is required, on every platform.
func TestIsOldExe(t *testing.T) {
	const base = "steam-achievement-tracker.exe"
	cases := []struct {
		name string
		goos string
		want bool
	}{
		{"steam-achievement-tracker.exe.old.1763900000000000000", "windows", true},
		{"steam-achievement-tracker.exe.old.0", "windows", true},
		{"STEAM-ACHIEVEMENT-TRACKER.EXE.old.123", "windows", true},
		{"steam-achievement-tracker.exe.OLD.123", "windows", true},
		{"STEAM-ACHIEVEMENT-TRACKER.EXE.old.123", "linux", false},
		{"steam-achievement-tracker.exe.OLD.123", "linux", false},
		{"steam-achievement-tracker.exe.old.123", "linux", true},
		{"steam-achievement-tracker.exe.old.", "windows", false},
		{"steam-achievement-tracker.exe.old.", "linux", false},
		{"STEAM-ACHIEVEMENT-TRACKER.EXE.old.abc", "windows", false},
		{"steam-achievement-tracker.exe.old.abc", "windows", false},
		{"steam-achievement-tracker.exe.old.12a", "windows", false},
		{"steam-achievement-tracker.exe.old.-1", "windows", false},
		{"steam-achievement-tracker.exe.old.1.2", "windows", false},
		{"steam-achievement-tracker.exe.bak", "windows", false},
		{"steam-achievement-tracker.exe.old", "windows", false},
		{"other.exe.old.1", "windows", false},
	}
	for _, tc := range cases {
		if got := isOldExe(tc.goos, tc.name, base); got != tc.want {
			t.Errorf("isOldExe(%q, %q) = %v, want %v", tc.goos, tc.name, got, tc.want)
		}
	}
}

func TestDisplayVersion(t *testing.T) {
	cases := map[string]string{
		"1.2.3":      "1.2.3",
		"1.2.3-rc.1": "1.2.3",
		"0.0.1":      "0.0.1",
		"10.20.30-x": "10.20.30",
	}
	for in, want := range cases {
		if got := displayVersion(in); got != want {
			t.Errorf("displayVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameFile(t *testing.T) {
	exe := filepath.Join("/opt", "App", "sat.exe")
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"same path", exe, exe, true},
		{"quoted icon value", `"` + exe + `"`, exe, true},
		{"case differs", filepath.Join("/opt", "app", "SAT.EXE"), exe, true},
		{"doubled separator", "/opt//App/sat.exe", exe, true},
		{"other file", filepath.Join("/opt", "Other", "sat.exe"), exe, false},
		{"old renamed exe", exe + ".old.1", exe, false},
		{"empty", "", exe, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameFile(tc.a, tc.b); got != tc.want {
				t.Fatalf("sameFile(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// The stored version is written only for this exe, and only when it differs from the running version. Another
// install's entry is never changed.
func TestShouldWriteDisplayVersion(t *testing.T) {
	exe := filepath.Join("/opt", "App", "sat.exe")
	cases := []struct {
		name   string
		icon   string
		stored string
		cur    string
		want   bool
	}{
		{"older entry for this exe", exe, "1.0.0", "1.2.3", true},
		{"no stored version", exe, "", "1.2.3", true},
		{"equal version", exe, "1.2.3", "1.2.3", false},
		{"pre-release equals its core", exe, "1.2.3", "1.2.3-rc.1", false},
		{"different exe", filepath.Join("/other", "sat.exe"), "1.0.0", "1.2.3", false},
		{"quoted icon of this exe", `"` + exe + `"`, "1.0.0", "1.2.3", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldWriteDisplayVersion(tc.icon, exe, tc.stored, tc.cur); got != tc.want {
				t.Fatalf("shouldWriteDisplayVersion = %v, want %v", got, tc.want)
			}
		})
	}
}
