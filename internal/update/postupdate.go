package update

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/fsretry"
)

// cleanupDelay is how long the leftover sweep waits after startup. The framework's helper removes the .bak file and the
// staging directory itself right after it starts the new app, so the sweep waits for it. Tests may shrink it.
var cleanupDelay = 5 * time.Second

// leftoverMinAge is how long a staging directory (its own mtime and its entries'), a .bak file and a helper log must be
// idle before the sweep removes them. There is no single-instance lock, and the target folder and the temp directory
// can be shared with another app or a second instance of this app. A concurrent update writes its files continuously,
// so a young file is left alone.
var leftoverMinAge = 10 * time.Minute

// removeFile removes one file or symlink for the sweep. Tests replace it to simulate a file that vanished or is held.
var removeFile = fsretry.Remove

// tempLogPattern matches the helper log the framework writes to the OS temp directory: wails-update-<pid>.log.
var tempLogPattern = regexp.MustCompile(`^wails-update-[0-9]+\.log$`)

// RunPostUpdate starts the work that follows an update: the installed version sync (Windows) and the leftover sweep.
// It runs on every start of a stamped build, and does nothing when that is not true (no valid stamp, or the updater is
// unavailable). It never blocks and never fails: it starts one goroutine, and later calls do nothing.
func (s *Service) RunPostUpdate() {
	if s.unavailableNow() {
		return
	}
	s.postOnce.Do(func() {
		s.mu.Lock()
		cur := s.current
		s.mu.Unlock()
		// Read on the caller's goroutine, so tests can change cleanupDelay before they start the service.
		env, delay := s.env, cleanupDelay
		done := make(chan struct{})
		s.postDone = done
		go func() {
			defer close(done)
			s.postUpdate(env, cur, delay)
		}()
	})
}

// postUpdate is the goroutine behind RunPostUpdate. The version sync runs at once. The sweep waits for delay, or for
// shutdown, whichever comes first.
func (s *Service) postUpdate(env hostEnv, cur string, delay time.Duration) {
	if exe, err := env.executable(); err == nil {
		syncInstalledVersion(exe, cur)
	}
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-s.life.Done():
		return
	case <-t.C:
	}
	s.sweepLeftovers(env, postUpdateTarget(env), env.tempDir())
}

// postUpdateTarget returns the file the framework replaces: the running exe on Windows, the AppImage on Linux when the
// process runs from one, and "" otherwise. Linux package installs have no target, so nothing is swept there.
func postUpdateTarget(env hostEnv) string {
	switch env.goos {
	case "windows":
		exe, err := env.executable()
		if err != nil {
			return ""
		}
		return exe
	case "linux":
		return appImageTarget(env)
	}
	return ""
}

// sweepLeftovers removes what an update left behind. target is the replaced file (never removed); tempDir is the OS
// temp directory ("" skips it). Removal is limited to exact names and shapes, each checked before the removal:
//   - <target>.bak: a regular file or symlink, not a directory, idle for more than leftoverMinAge;
//   - on Windows, <base>.old.<digits> regular files next to the exe, with no age limit;
//   - wails-update-* directories next to the target: flat (regular files only) and idle for more than leftoverMinAge.
//     Each listed file is removed by name, then the empty directory with a non-recursive remove. Never the staging
//     directory of the staged payload, and none at all while an update is in flight;
//   - wails-update-<digits>.log regular files in tempDir, idle for more than leftoverMinAge. Skipped while restarting,
//     when the log is the live helper's.
//
// Nothing is removed recursively. Anything that does not match is skipped silently. So is a file that vanished since
// it was listed, or a temp log the OS refuses to remove because another user owns it. Any other removal error counts
// as a failure. Logging holds no path and no error text: one line for the count removed, one for the count that
// failed.
func (s *Service) sweepLeftovers(env hostEnv, target, tempDir string) {
	if target == "" {
		return
	}
	removed, failed := 0, 0
	count := func(err error) {
		switch {
		case err == nil:
			removed++
		case errors.Is(err, fs.ErrNotExist):
			// Gone since it was listed: neither a removal nor a failure.
		default:
			failed++
		}
	}
	now := time.Now()

	bak := target + ".bak"
	if bakRemovable(bak, now) {
		count(removeFile(bak))
	}

	dir, base := filepath.Dir(target), filepath.Base(target)
	if env.goos == "windows" {
		for _, e := range readDirEntries(dir) {
			if e.Type().IsRegular() && isOldExe(env.goos, e.Name(), base) {
				count(removeFile(filepath.Join(dir, e.Name())))
			}
		}
	}

	// skip is the staging directory of the staged payload, cleaned like the entries below. It is "" when nothing is
	// staged, and filepath.Clean("") is "." which no entry path equals.
	skip := ""
	if s.deps.Updater != nil {
		skip = stagingDirFor(s.deps.Updater.DownloadedPath())
	}
	inFlight := s.stagingInFlight()
	for _, e := range readDirEntries(dir) {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), stagingPrefix) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if p == filepath.Clean(skip) || inFlight {
			continue
		}
		names, ok := stagingRemovable(p, now)
		if !ok {
			continue
		}
		removeStaging(p, names, count)
	}

	if tempDir != "" && s.phaseNow() != PhaseRestarting {
		for _, e := range readDirEntries(tempDir) {
			if !e.Type().IsRegular() || !tempLogPattern.MatchString(e.Name()) || !idleLong(e, now) {
				continue
			}
			err := removeFile(filepath.Join(tempDir, e.Name()))
			if errors.Is(err, fs.ErrPermission) {
				// Another user's log in a shared temp directory (sticky /tmp). Not ours to remove.
				continue
			}
			count(err)
		}
	}

	if removed > 0 {
		log.Printf("update: cleanup: removed %d leftovers", removed)
	}
	if failed > 0 {
		log.Printf("update: cleanup: %d leftovers could not be removed", failed)
	}
}

// bakRemovable reports whether path is a <target>.bak the sweep may remove: a regular file or a symlink, never a
// directory, and last modified more than leftoverMinAge before now. A missing path is not removable.
func bakRemovable(path string, now time.Time) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if !fi.Mode().IsRegular() && fi.Mode()&fs.ModeSymlink == 0 {
		return false
	}
	return now.Sub(fi.ModTime()) > leftoverMinAge
}

// stagingRemovable reports whether dir is a staging directory the sweep may remove: a real directory that holds only
// regular files (no subdirectories, no symlinks), and whose own mtime and every entry's mtime are more than
// leftoverMinAge before now. When it is, it returns the names of those entries. Anything else is another program's or
// a live update's, and stays. An entry that vanishes during the check makes the directory not removable.
func stagingRemovable(dir string, now time.Time) ([]string, bool) {
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		return nil, false
	}
	newest := fi.ModTime()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.Type().IsRegular() {
			return nil, false
		}
		info, err := e.Info()
		if err != nil {
			return nil, false
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
		names = append(names, e.Name())
	}
	if now.Sub(newest) <= leftoverMinAge {
		return nil, false
	}
	return names, true
}

// removeStaging removes the entries of the staging directory dir that stagingRemovable listed, each by name, then dir
// itself with a non-recursive remove. A name added after the listing makes that last remove fail, so nothing unlisted
// is deleted. If an entry could not be removed, dir is not attempted: it still holds files, and that failure is
// already counted. count receives the result of each removal.
func removeStaging(dir string, names []string, count func(error)) {
	for _, n := range names {
		err := removeFile(filepath.Join(dir, n))
		count(err)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return
		}
	}
	count(fsretry.Remove(dir))
}

// idleLong reports whether the regular file e was last modified more than leftoverMinAge before now. A helper log is
// written while its update runs, so a young one may belong to a live helper of another app or instance.
func idleLong(e fs.DirEntry, now time.Time) bool {
	info, err := e.Info()
	if err != nil {
		return false
	}
	return now.Sub(info.ModTime()) > leftoverMinAge
}

// stagingInFlight reports whether an update is downloading, verifying, installing, staged or restarting. The staging
// directory is then not the sweep's to remove.
func (s *Service) stagingInFlight() bool {
	switch s.phaseNow() {
	case PhaseDownloading, PhaseVerifying, PhaseInstalling, PhaseReady, PhaseRestarting:
		return true
	}
	return false
}

// phaseNow reads the phase under the state lock.
func (s *Service) phaseNow() Phase {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase
}

// readDirEntries lists dir. A missing or unreadable directory has nothing to sweep, so it gives no entries and no error.
func readDirEntries(dir string) []fs.DirEntry {
	entries, _ := os.ReadDir(dir)
	return entries
}

// isOldExe reports whether name is <base>.old.<digits>, the name the framework gives the renamed-aside exe. The digits
// are ASCII only, and at least one is required. On Windows (goos "windows") the ".old." part matches in any letter
// case, as Windows file names do. Elsewhere it is case-sensitive.
func isOldExe(goos, name, base string) bool {
	prefix := base + ".old."
	if len(name) <= len(prefix) {
		return false
	}
	head := name[:len(prefix)]
	if goos == "windows" {
		if !strings.EqualFold(head, prefix) {
			return false
		}
	} else if head != prefix {
		return false
	}
	rest := name[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' {
			return false
		}
	}
	return true
}

// displayVersion is the version the installer records in Apps & Features: the numeric core, with any pre-release part
// dropped. It matches VERSION_CORE in build/windows/Taskfile.yml.
func displayVersion(cur string) string {
	if i := strings.IndexByte(cur, '-'); i >= 0 {
		return cur[:i]
	}
	return cur
}

// sameFile reports whether a DisplayIcon value names the running exe. It is used only for the Windows Apps & Features
// check. The first path may be quoted. Comparison ignores case, as Windows paths do.
func sameFile(a, b string) bool {
	return strings.EqualFold(filepath.Clean(strings.Trim(a, `"`)), filepath.Clean(b))
}

// shouldWriteDisplayVersion reports whether the Apps & Features entry at iconValue belongs to the running exe and
// records a version other than the running one. Another install's entry is never touched.
func shouldWriteDisplayVersion(iconValue, exePath, storedVersion, cur string) bool {
	return sameFile(iconValue, exePath) && storedVersion != displayVersion(cur)
}
