package update

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/usererr"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

func mustSentence(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error %q, got nil", want)
	}
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
	if usererr.Raw(err) != err {
		t.Fatal("update error carries a cause")
	}
}

// testService returns a service with a live lifetime context that the test cleanup cancels. Its phase starts idle
// (PhaseIdle) unless the test sets otherwise; no framework is configured, so a test must never reach a framework call.
func testService(t *testing.T, d Deps) *Service {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if d.Context == nil {
		d.Context = ctx
	}
	s := New(d)
	s.env = fakeEnv("windows", `C:\app\sat.exe`, map[string]string{})
	return s
}

// No version stamp in the test binary, so Init leaves the service unavailable without touching the framework.
func TestUnavailableWithoutStamp(t *testing.T) {
	s := testService(t, Deps{})
	s.Init()
	snap := s.State()
	if !snap.Unavailable || snap.Phase != PhaseIdle || snap.Latest != nil || snap.ChecksEnabled {
		t.Fatalf("snapshot = %+v", snap)
	}
	mustSentence(t, s.CheckNow(), msgUnavailable)
	mustSentence(t, s.Install("0.3.0"), msgUnavailable)
	mustSentence(t, s.Restart(), msgUnavailable)
	mustSentence(t, s.Cancel(), msgUnavailable)
	mustSentence(t, s.SetChecksEnabled(true), msgUnavailable)
	s.StartupCheck()
	s.Shutdown()
}

func TestNewRequiresContext(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New without a Context must panic")
		}
	}()
	New(Deps{})
}

func TestRefusalsNeverReachTheFramework(t *testing.T) {
	s := testService(t, Deps{})
	s.phase = PhaseDownloading
	mustSentence(t, s.CheckNow(), msgBusy)
	mustSentence(t, s.Install("0.3.0"), msgBusy)
	mustSentence(t, s.Restart(), msgBusy)

	s.phase = PhaseChecking
	mustSentence(t, s.Restart(), msgBusy)

	s.phase = PhaseReady
	mustSentence(t, s.CheckNow(), msgRestartFirst)
	mustSentence(t, s.Install("0.3.0"), msgRestartFirst)

	s.phase = PhaseRestarting
	mustSentence(t, s.Restart(), msgBusy)
	mustSentence(t, s.CheckNow(), msgBusy)

	s.phase = PhaseIdle
	mustSentence(t, s.Restart(), msgNotReady)
}

// A package install is refused by the host condition before anything changes: no transition, no cancel context, no
// notification. The package reason is shown even though the release may also lack a checksum.
func TestHostRefusalBeforeTransition(t *testing.T) {
	var notified atomic.Int32
	s := testService(t, Deps{OnChanged: func() { notified.Add(1) }})
	s.env = fakeEnv("linux", "/usr/local/bin/steam-achievement-tracker", map[string]string{})
	s.phase = PhaseAvailable
	s.latest = &latestRelease{version: "0.3.0"}
	mustSentence(t, s.Install("0.3.0"), reasonPackage)
	if s.phase != PhaseAvailable || s.installCancel != nil || s.cancelled || s.latest.version != "0.3.0" {
		t.Fatalf("package refusal changed state: phase %s, cancel %v, cancelled %v", s.phase, s.installCancel != nil, s.cancelled)
	}
	if notified.Load() != 0 {
		t.Fatalf("package refusal notified %d times", notified.Load())
	}
}

// A notify-only release is refused whatever version is asked for, and the refusal changes nothing: the phase stays
// available, the cancel flag is untouched and the context set by an earlier install is not cancelled.
func TestInstallNotifyOnlyLeavesStateUntouched(t *testing.T) {
	var notified atomic.Int32
	s := testService(t, Deps{OnChanged: func() { notified.Add(1) }})
	s.phase = PhaseAvailable
	s.latest = &latestRelease{version: "0.3.0", notifyOnly: true, reason: reasonNoChecksum}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.installCancel = cancel

	mustSentence(t, s.Install("0.3.0"), reasonNoChecksum)
	mustSentence(t, s.Install("0.4.0"), reasonNoChecksum)

	if s.phase != PhaseAvailable {
		t.Fatalf("phase = %s, want available", s.phase)
	}
	if s.cancelled {
		t.Fatal("cancel flag set by a refused install")
	}
	if ctx.Err() != nil {
		t.Fatal("install context cancelled by a refused install")
	}
	if s.errText != "" || s.progress != (DownloadProgress{}) {
		t.Fatalf("refusal set error %q or progress %+v", s.errText, s.progress)
	}
	if notified.Load() != 0 {
		t.Fatalf("refusal notified %d times", notified.Load())
	}
}

func TestLatestFromUsesGate(t *testing.T) {
	s := testService(t, Deps{})
	s.env = fakeEnv("linux", "/usr/local/bin/steam-achievement-tracker", map[string]string{})

	if got := s.latestFrom(nil); got != nil {
		t.Fatalf("latestFrom(nil) = %+v", got)
	}
	info := s.latestFrom(&updater.Release{Version: "0.3.0", Artifact: updater.Artifact{Size: 42}, Verification: &updater.Verification{DigestAlgo: "sha256", Digest: make([]byte, 32)}})
	if info == nil || !info.notifyOnly || info.reason != reasonPackage || info.size != 42 {
		t.Fatalf("package install latestFrom = %+v", info)
	}
	if info.releaseURL != "https://github.com/gjrud/steam-achievement-tracker/releases/tag/v0.3.0" {
		t.Fatalf("releaseURL = %q", info.releaseURL)
	}

	s.env = fakeEnv("windows", `C:\app\sat.exe`, map[string]string{})
	info = s.latestFrom(&updater.Release{Version: "0.3.0", Verification: &updater.Verification{DigestAlgo: "sha256", Digest: make([]byte, 32)}})
	if info.notifyOnly {
		t.Fatalf("windows release should be installable: %+v", info)
	}
}

func TestScreenRelease(t *testing.T) {
	cases := []struct {
		name    string
		rel     *updater.Release
		wantNil bool
		wantErr bool
	}{
		{"no release", nil, true, false},
		{"stable release", &updater.Release{Version: "0.3.0"}, false, false},
		{"prerelease is never offered", &updater.Release{Version: "0.3.0-rc.1"}, true, false},
		{"v prefix is not a version", &updater.Release{Version: "v0.3.0"}, true, true},
		{"partial version", &updater.Release{Version: "0.3"}, true, true},
		{"garbage", &updater.Release{Version: "latest"}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := screenRelease(tc.rel)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if (got == nil) != tc.wantNil {
				t.Fatalf("got = %+v, wantNil %v", got, tc.wantNil)
			}
		})
	}
}

// State never reads the setting: the getter is not called, so GetUpdateState causes no database read.
func TestStateDoesNoIO(t *testing.T) {
	var reads atomic.Int32
	s := testService(t, Deps{ChecksEnabled: func() (bool, error) {
		reads.Add(1)
		return true, nil
	}})
	_ = s.State()
	_ = s.State()
	if reads.Load() != 0 {
		t.Fatalf("State read the setting %d times", reads.Load())
	}
}

// Until the toggle is known, State reports checks off. SetChecksEnabled sets the cache and notifies.
func TestSetChecksEnabledUpdatesSnapshot(t *testing.T) {
	var notified atomic.Int32
	s := testService(t, Deps{OnChanged: func() { notified.Add(1) }})
	if s.State().ChecksEnabled {
		t.Fatal("unknown toggle must read as off")
	}
	if err := s.SetChecksEnabled(true); err != nil {
		t.Fatalf("SetChecksEnabled(true): %v", err)
	}
	if !s.State().ChecksEnabled {
		t.Fatal("snapshot after SetChecksEnabled(true) = off")
	}
	if err := s.SetChecksEnabled(false); err != nil {
		t.Fatalf("SetChecksEnabled(false): %v", err)
	}
	if s.State().ChecksEnabled {
		t.Fatal("snapshot after SetChecksEnabled(false) = on")
	}
	if notified.Load() != 2 {
		t.Fatalf("notified %d times, want 2", notified.Load())
	}
}

// longStartupDelay keeps a scheduled startup check from firing during the test. The service has no framework updater,
// so a check that fired would have nothing to ask. The delay is restored at cleanup.
func longStartupDelay(t *testing.T) {
	t.Helper()
	prev := startupDelay
	startupDelay = time.Hour
	t.Cleanup(func() { startupDelay = prev })
}

// StartupCheck never reads the setting. With no load yet the toggle is unknown: nothing is scheduled and the snapshot
// reads off.
func TestStartupCheckWithoutLoadNeverReads(t *testing.T) {
	longStartupDelay(t)
	var reads atomic.Int32
	s := testService(t, Deps{ChecksEnabled: func() (bool, error) {
		reads.Add(1)
		return true, nil
	}})
	s.StartupCheck()
	if reads.Load() != 0 {
		t.Fatalf("StartupCheck read the setting %d times, want 0", reads.Load())
	}
	if s.State().ChecksEnabled {
		t.Fatal("snapshot with no load = on, want off")
	}
}

// LoadChecksEnabled reads the setting once; StartupCheck then uses the cached toggle. On: the snapshot reports on.
func TestStartupCheckUsesLoadedToggle(t *testing.T) {
	longStartupDelay(t)
	var reads atomic.Int32
	s := testService(t, Deps{ChecksEnabled: func() (bool, error) {
		reads.Add(1)
		return true, nil
	}})
	s.LoadChecksEnabled()
	s.StartupCheck()
	if reads.Load() != 1 {
		t.Fatalf("setting read %d times, want 1", reads.Load())
	}
	if !s.State().ChecksEnabled {
		t.Fatal("snapshot after startup with the toggle on = off")
	}
}

// LoadChecksEnabled reads once and notifies when the toggle changes. The StartupCheck after it uses the cache: the
// getter is not called again, so a normal start reads the setting once in total.
func TestLoadChecksEnabledThenStartupReadsOnce(t *testing.T) {
	longStartupDelay(t)
	var reads, notified atomic.Int32
	s := testService(t, Deps{OnChanged: func() { notified.Add(1) }, ChecksEnabled: func() (bool, error) {
		reads.Add(1)
		return true, nil
	}})
	s.LoadChecksEnabled()
	if reads.Load() != 1 || !s.State().ChecksEnabled || notified.Load() != 1 {
		t.Fatalf("after load: reads %d, on %v, notified %d; want 1, true, 1", reads.Load(), s.State().ChecksEnabled, notified.Load())
	}
	s.StartupCheck()
	if reads.Load() != 1 {
		t.Fatalf("StartupCheck after a load read the setting again: %d reads", reads.Load())
	}
	s.LoadChecksEnabled()
	if notified.Load() != 1 {
		t.Fatalf("reload with the same value notified: %d", notified.Load())
	}
}

// A SetChecksEnabled that lands while the getter runs is newer than the read. The load must not overwrite it with the
// stale value, and must not notify for its own write.
func TestLoadChecksEnabledKeepsConcurrentSet(t *testing.T) {
	longStartupDelay(t)
	var notified atomic.Int32
	var s *Service
	s = testService(t, Deps{OnChanged: func() { notified.Add(1) }, ChecksEnabled: func() (bool, error) {
		// The user turns checks off while the database read is in flight.
		if err := s.SetChecksEnabled(false); err != nil {
			t.Errorf("SetChecksEnabled during load: %v", err)
		}
		return true, nil
	}})
	s.LoadChecksEnabled()
	if s.State().ChecksEnabled {
		t.Fatal("stale load overwrote the newer SetChecksEnabled(false)")
	}
	if notified.Load() != 1 {
		t.Fatalf("notified %d times, want 1 (the set only)", notified.Load())
	}
}

// An unreadable setting is read and logged once at startup: LoadChecksEnabled logs, and the StartupCheck after it does
// not read or log again.
func TestUnreadableSettingLoggedOnceAtStartup(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	var reads atomic.Int32
	s := testService(t, Deps{ChecksEnabled: func() (bool, error) {
		reads.Add(1)
		return false, errors.New("database is locked")
	}})
	s.LoadChecksEnabled()
	s.StartupCheck()
	s.StartupCheck()
	if reads.Load() != 1 {
		t.Fatalf("setting read %d times, want 1", reads.Load())
	}
	if n := strings.Count(buf.String(), "update: settings unreadable"); n != 1 {
		t.Fatalf("unreadable setting logged %d times, want 1: %q", n, buf.String())
	}
	if s.State().ChecksEnabled {
		t.Fatal("unreadable setting must read as off")
	}
}

// An unreadable setting leaves the toggle unknown and reads as off. A later load that succeeds (the app recovered from
// an init error) fills the cache and notifies, so the checkbox shows the stored value and not false.
func TestLoadChecksEnabledRecoversAfterUnreadable(t *testing.T) {
	longStartupDelay(t)
	var notified, reads atomic.Int32
	fail := true
	s := testService(t, Deps{OnChanged: func() { notified.Add(1) }, ChecksEnabled: func() (bool, error) {
		reads.Add(1)
		if fail {
			return false, errors.New("database is locked")
		}
		return true, nil
	}})
	s.LoadChecksEnabled()
	if s.State().ChecksEnabled || s.checksKnown || notified.Load() != 0 {
		t.Fatalf("unreadable load: on %v, known %v, notified %d", s.State().ChecksEnabled, s.checksKnown, notified.Load())
	}
	fail = false
	s.LoadChecksEnabled()
	if !s.State().ChecksEnabled || notified.Load() != 1 {
		t.Fatalf("recovered load: on %v, notified %d; want true, 1", s.State().ChecksEnabled, notified.Load())
	}
	s.StartupCheck()
	if reads.Load() != 2 {
		t.Fatalf("reads = %d, want 2 (the failed load and the recovery)", reads.Load())
	}
}

// An unavailable service never loads the toggle.
func TestLoadChecksEnabledSkipsWhenUnavailable(t *testing.T) {
	var reads atomic.Int32
	s := testService(t, Deps{ChecksEnabled: func() (bool, error) {
		reads.Add(1)
		return true, nil
	}})
	s.unavailable = true
	s.LoadChecksEnabled()
	if reads.Load() != 0 {
		t.Fatalf("unavailable service loaded the toggle %d times", reads.Load())
	}
}

// An Updater.Init refusal is recorded by configure and logged once, by whichever of LoadChecksEnabled, LogInitFailure
// or StartupCheck runs first after logging is up. Init itself logs nothing, since main calls it before logging is set
// up.
func TestInitFailureLogsOnceAfterLoggingIsUp(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	s := testService(t, Deps{Updater: updater.New(nil)})
	s.configure("") // an empty CurrentVersion makes the framework refuse the configuration
	if !s.unavailableNow() || s.initFailure != catOther {
		t.Fatalf("after refused configure: unavailable %v, initFailure %q", s.unavailableNow(), s.initFailure)
	}
	if buf.Len() != 0 {
		t.Fatalf("configure logged before logging was up: %q", buf.String())
	}
	s.LogInitFailure()
	s.LogInitFailure()
	s.LoadChecksEnabled()
	s.StartupCheck()
	if got := buf.String(); strings.Count(got, "update: unavailable: other") != 1 {
		t.Fatalf("log = %q, want the category once", got)
	}
}

// LogInitFailure logs a refused configuration even when nothing else runs, as ServiceStartup does when the app's own
// init failed. A service that configured fine has nothing to log.
func TestLogInitFailureStandsAlone(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	refused := testService(t, Deps{Updater: updater.New(nil)})
	refused.configure("")
	refused.LogInitFailure()
	if !strings.Contains(buf.String(), "update: unavailable: other") {
		t.Fatalf("refused service log = %q, want the category", buf.String())
	}

	buf.Reset()
	fine := testService(t, Deps{})
	fine.LogInitFailure()
	if buf.Len() != 0 {
		t.Fatalf("service with no refusal logged %q", buf.String())
	}
}

// A build with no version stamp is unavailable and logs nothing: that is the expected state of a dev build.
func TestNoStampLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	s := testService(t, Deps{})
	s.Init()
	s.LogInitFailure()
	s.StartupCheck()
	if buf.Len() != 0 || !s.unavailableNow() {
		t.Fatalf("no-stamp service: log %q, unavailable %v", buf.String(), s.unavailableNow())
	}
}

// Install with no known release is refused with a fixed sentence before anything changes: no transition, no cancel
// context, no notification. It is refused after the busy rule, so a busy phase still reads as busy.
func TestInstallWithoutKnownReleaseIsRefused(t *testing.T) {
	var notified atomic.Int32
	s := testService(t, Deps{OnChanged: func() { notified.Add(1) }})
	s.phase = PhaseError
	mustSentence(t, s.Install("0.3.0"), msgCheckFirst)
	if s.phase != PhaseError || s.installCancel != nil || s.cancelled || notified.Load() != 0 {
		t.Fatalf("refusal changed state: phase %s, cancel %v, cancelled %v, notified %d", s.phase, s.installCancel != nil, s.cancelled, notified.Load())
	}
	s.phase = PhaseDownloading
	mustSentence(t, s.Install("0.3.0"), msgBusy)
}

// Install from idle or up-to-date asks for a check first, not a busy wait: nothing is known to install yet. Other
// phases keep their own sentences.
func TestInstallFromIdleOrUpToDateAsksForCheck(t *testing.T) {
	var notified atomic.Int32
	s := testService(t, Deps{OnChanged: func() { notified.Add(1) }})
	for _, p := range []Phase{PhaseIdle, PhaseUpToDate} {
		s.phase = p
		mustSentence(t, s.Install("0.3.0"), msgCheckFirst)
		if s.phase != p || s.installCancel != nil || s.cancelled {
			t.Fatalf("refusal from %s changed state: phase %s, cancel %v", p, s.phase, s.installCancel != nil)
		}
	}
	if notified.Load() != 0 {
		t.Fatalf("refusals notified %d times", notified.Load())
	}
	s.phase = PhaseChecking
	mustSentence(t, s.Install("0.3.0"), msgBusy)
	s.phase = PhaseReady
	mustSentence(t, s.Install("0.3.0"), msgRestartFirst)
}

// A terminal transition of an install drops the progress and the cancel func the install held.
func TestFinishClearsInstallState(t *testing.T) {
	s := testService(t, Deps{})
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.phase = PhaseDownloading
	s.installCancel = cancel
	s.progress = DownloadProgress{Written: 5, Total: 10}
	s.finish(func() { s.phase = PhaseAvailable })
	if s.phase != PhaseAvailable || s.installCancel != nil || s.progress != (DownloadProgress{}) {
		t.Fatalf("after finish: phase %s, cancel %v, progress %+v", s.phase, s.installCancel != nil, s.progress)
	}
}

// Without a framework updater a manual check fails with the check sentence. The failure is logged only while the
// service is running: a failure caused by shutdown writes no log line.
func TestManualCheckFailureLogsOnlyWhileRunning(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	s := testService(t, Deps{})
	s.phase = PhaseIdle
	mustSentence(t, s.CheckNow(), msgCheckFailed)
	if s.phase != PhaseError || !strings.Contains(buf.String(), "update: check failed: check-failed") {
		t.Fatalf("running failure: phase %s, log %q", s.phase, buf.String())
	}

	buf.Reset()
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	quiet := New(Deps{Context: dead})
	_ = quiet.CheckNow()
	if buf.Len() != 0 {
		t.Fatalf("failure during shutdown logged: %q", buf.String())
	}
}

// A load with the toggle off reads once; the StartupCheck after it schedules nothing and reports off.
func TestStartupCheckOffReportsOff(t *testing.T) {
	var reads atomic.Int32
	s := testService(t, Deps{ChecksEnabled: func() (bool, error) {
		reads.Add(1)
		return false, nil
	}})
	s.LoadChecksEnabled()
	s.StartupCheck()
	if reads.Load() != 1 || s.State().ChecksEnabled {
		t.Fatalf("reads = %d, snapshot checks on = %v", reads.Load(), s.State().ChecksEnabled)
	}
}

// An unreadable setting skips the check and reports checks off, not on.
func TestStartupCheckUnreadableReportsOff(t *testing.T) {
	s := testService(t, Deps{ChecksEnabled: func() (bool, error) { return true, errors.New("unreadable") }})
	s.LoadChecksEnabled()
	s.StartupCheck()
	if s.State().ChecksEnabled {
		t.Fatal("unreadable setting must read as off")
	}
	if s.checksKnown {
		t.Fatal("unreadable setting must leave the toggle unknown")
	}
}

// A nil getter means off, as before.
func TestStartupCheckNilGetterIsOff(t *testing.T) {
	s := testService(t, Deps{})
	s.LoadChecksEnabled()
	s.StartupCheck()
	if s.State().ChecksEnabled {
		t.Fatal("nil getter must read as off")
	}
}

// An unavailable service never reads the setting, from either entry point.
func TestStartupCheckSkipsReadWhenUnavailable(t *testing.T) {
	var reads atomic.Int32
	s := testService(t, Deps{ChecksEnabled: func() (bool, error) {
		reads.Add(1)
		return true, nil
	}})
	s.unavailable = true
	s.LoadChecksEnabled()
	s.StartupCheck()
	if reads.Load() != 0 {
		t.Fatalf("unavailable service read the setting %d times", reads.Load())
	}
}

// A failed reload must not wipe a toggle the user already set: the unreadable read leaves the set value, and the
// snapshot keeps it. Nothing notifies for the failed read.
func TestLoadChecksEnabledUnreadableKeepsKnownToggle(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	var notified atomic.Int32
	s := testService(t, Deps{OnChanged: func() { notified.Add(1) }, ChecksEnabled: func() (bool, error) {
		return false, errors.New("database is locked")
	}})
	if err := s.SetChecksEnabled(true); err != nil {
		t.Fatalf("SetChecksEnabled: %v", err)
	}
	before := notified.Load()
	s.LoadChecksEnabled()
	if !s.State().ChecksEnabled || !s.checksKnown {
		t.Fatalf("failed reload wiped the known toggle: on %v, known %v", s.State().ChecksEnabled, s.checksKnown)
	}
	if notified.Load() != before {
		t.Fatalf("failed reload notified %d times", notified.Load()-before)
	}
}

// startupAfterDelay(0) runs the check body directly. A phase other than idle when it fires drops it: the phase, the
// latest release and the error are unchanged, nothing notifies and nothing is logged.
func TestStartupAfterDelayDropsWhenNotIdle(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	var notified atomic.Int32
	s := testService(t, Deps{OnChanged: func() { notified.Add(1) }, ChecksEnabled: func() (bool, error) { return true, nil }})
	s.LoadChecksEnabled()
	s.phase = PhaseAvailable
	s.latest = &latestRelease{version: "0.3.0"}
	before := notified.Load()

	s.startupAfterDelay(0)

	if s.phase != PhaseAvailable || s.latest == nil || s.latest.version != "0.3.0" || s.errText != "" {
		t.Fatalf("dropped check changed state: phase %s, latest %+v, error %q", s.phase, s.latest, s.errText)
	}
	if notified.Load() != before {
		t.Fatalf("dropped check notified %d times", notified.Load()-before)
	}
	if buf.Len() != 0 {
		t.Fatalf("dropped check logged %q", buf.String())
	}
}

// A toggle turned off after the check was scheduled: when the timer fires the toggle is read from the cache as off, so
// no check starts. The phase stays idle, nothing notifies after the set, and nothing is logged.
func TestStartupAfterDelayDropsWhenToggledOff(t *testing.T) {
	longStartupDelay(t)
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	var notified atomic.Int32
	s := testService(t, Deps{OnChanged: func() { notified.Add(1) }, ChecksEnabled: func() (bool, error) { return true, nil }})
	s.LoadChecksEnabled()
	// Schedules a check an hour out. The test cleanup cancels the lifetime context, which ends that goroutine.
	s.StartupCheck()
	if err := s.SetChecksEnabled(false); err != nil {
		t.Fatalf("SetChecksEnabled: %v", err)
	}
	before := notified.Load()

	s.startupAfterDelay(0)

	if s.phase != PhaseIdle || s.State().ChecksEnabled {
		t.Fatalf("toggled-off check: phase %s, checks on %v; want idle, off", s.phase, s.State().ChecksEnabled)
	}
	if notified.Load() != before {
		t.Fatalf("toggled-off check notified %d times", notified.Load()-before)
	}
	if buf.Len() != 0 {
		t.Fatalf("toggled-off check logged %q", buf.String())
	}
}

// With no framework updater the startup check fails quietly: the phase returns to idle, the snapshot error stays empty
// (no UI sentence), and the log carries only the category, not the cause or any path.
func TestStartupAfterDelayNoUpdaterFailsQuietly(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	s := testService(t, Deps{ChecksEnabled: func() (bool, error) { return true, nil }})
	s.LoadChecksEnabled()

	s.startupAfterDelay(0)

	snap := s.State()
	if s.phase != PhaseIdle || snap.Phase != PhaseIdle || snap.Error != "" {
		t.Fatalf("failed startup check: phase %s, error %q; want idle, empty", s.phase, snap.Error)
	}
	got := buf.String()
	if !strings.Contains(got, "update: startup check failed: check-failed") {
		t.Fatalf("log = %q, want the startup failure category", got)
	}
	if strings.Contains(got, `C:\`) || strings.Contains(got, "no framework updater") {
		t.Fatalf("startup failure log leaks detail: %q", got)
	}
}

func TestStateReportsLatest(t *testing.T) {
	s := testService(t, Deps{})
	s.phase = PhaseAvailable
	s.latest = &latestRelease{version: "0.3.0", size: 9, releaseURL: "https://example.invalid/x", notifyOnly: true, reason: reasonPackage}
	snap := s.State()
	if snap.Latest == nil || snap.Latest.Version != "0.3.0" || snap.Latest.Size != 9 || snap.Latest.ReleaseURL == "" {
		t.Fatalf("latest = %+v", snap.Latest)
	}
	if !snap.NotifyOnly || snap.NotifyReason != reasonPackage {
		t.Fatalf("notify = %v %q", snap.NotifyOnly, snap.NotifyReason)
	}
}

func TestCancelSetsFlagOnly(t *testing.T) {
	s := testService(t, Deps{})
	s.phase = PhaseDownloading
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.installCancel = cancel
	if err := s.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if ctx.Err() == nil || !s.cancelled || s.phase != PhaseDownloading {
		t.Fatalf("cancel: ctx err %v, cancelled %v, phase %s", ctx.Err(), s.cancelled, s.phase)
	}

	idle := testService(t, Deps{})
	idle.phase = PhaseAvailable
	if err := idle.Cancel(); err != nil {
		t.Fatalf("Cancel with no install: %v", err)
	}
	if idle.cancelled {
		t.Fatal("cancel with no install running must do nothing")
	}
}

func TestShutdownWithoutUpdaterMovesReadyToIdle(t *testing.T) {
	s := testService(t, Deps{})
	s.phase = PhaseReady
	s.Shutdown()
	if s.phase != PhaseIdle {
		t.Fatalf("phase = %s, want idle", s.phase)
	}
	s.phase = PhaseRestarting
	s.Shutdown()
	if s.phase != PhaseRestarting {
		t.Fatalf("restarting must not be touched, got %s", s.phase)
	}
}

// After Shutdown no snapshot change reaches OnChanged, so no Event.Emit happens after the app is closing.
func TestShutdownStopsNotifications(t *testing.T) {
	var notified atomic.Int32
	s := testService(t, Deps{OnChanged: func() { notified.Add(1) }})
	s.notify()
	if notified.Load() != 1 {
		t.Fatalf("notified %d times before shutdown, want 1", notified.Load())
	}
	s.Shutdown()
	s.notify()
	s.mutate(func() { s.phase = PhaseError })
	s.onEvent(updater.EventVerifying, nil)
	if err := s.SetChecksEnabled(true); err != nil {
		t.Fatalf("SetChecksEnabled after shutdown: %v", err)
	}
	if notified.Load() != 1 {
		t.Fatalf("notified %d times after shutdown, want 1", notified.Load())
	}
}

// Concurrent State, Cancel, framework event, refused Install and CheckNow, refused Restart and Shutdown, with the
// phase set to downloading. Run with -race. Nothing here may reach the framework, which is nil.
func TestConcurrentStateCancelEventsRefusalsAndShutdown(t *testing.T) {
	s := testService(t, Deps{OnChanged: func() {}})
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mu.Lock()
	s.phase = PhaseDownloading
	s.installCancel = cancel
	s.mu.Unlock()

	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				f()
			}
		}()
	}
	run(func() { _ = s.State() })
	run(func() { _ = s.Cancel() })
	run(func() { s.onEvent(updater.EventDownloadProgress, updater.Progress{Written: 1, Total: 2}) })
	run(func() { s.onEvent(updater.EventError, updater.ErrorInfo{Message: "context canceled"}) })
	run(func() { _ = s.Install("0.3.0") })
	run(func() { _ = s.CheckNow() })
	run(func() { _ = s.Restart() })
	run(func() { _ = s.SetChecksEnabled(true) })
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.Shutdown()
	}()
	wg.Wait()
}

// canceledFor reads both the flag and the context: a cancel through either one counts for the install operation.
func TestCanceledForReadsFlagAndContext(t *testing.T) {
	s := testService(t, Deps{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if s.canceledFor(ctx) {
		t.Fatal("fresh install reads as cancelled")
	}
	s.cancelled = true
	if !s.canceledFor(ctx) {
		t.Fatal("flag alone must count as cancelled")
	}
	s.cancelled = false
	cancel()
	if !s.canceledFor(ctx) {
		t.Fatal("cancelled context alone must count as cancelled")
	}
}

// fakeHost is the framework's Host for tests. Each framework event goes to onEmit, which runs on the goroutine that
// emits it, so a test can act at a chosen step of an install. Windows and quit are no-ops.
type fakeHost struct {
	onEmit func(name string)
}

func (h *fakeHost) Emit(name string, _ ...any) bool {
	if h.onEmit != nil {
		h.onEmit(name)
	}
	return true
}

func (h *fakeHost) OnEvent(string, func(any)) func() { return func() {} }

func (h *fakeHost) OpenWindow(updater.WindowOptions) updater.WindowHandle { return nil }

func (h *fakeHost) Quit() {}

// installStub serves a release whose every asset is size bytes, so the size checks of an install are exact.
func installStub(t *testing.T, size int) *httptest.Server {
	t.Helper()
	stub := &stubGitHub{tag: "v0.3.0", withSums: true}
	srv := newStubGitHub(t, stub)
	for name := range stub.bodies {
		stub.bodies[name] = bytes.Repeat([]byte{'x'}, size)
	}
	return srv
}

// stagedService returns a service whose framework updater is really configured against srv, with a client capped at
// limit bytes. Install runs for real: check, download, digest verification and staging. The framework stages next to
// the test binary, in a wails-update-* folder; the test cleanup runs Shutdown, which removes it if it is still staged.
// onEmit sees every framework event (see fakeHost). Install needs a linux or windows amd64 host, the platforms the
// matcher supports.
func stagedService(t *testing.T, srv *httptest.Server, limit int64, onEmit func(name string)) *Service {
	t.Helper()
	if (runtime.GOOS != "linux" && runtime.GOOS != "windows") || runtime.GOARCH != "amd64" {
		t.Skip("the install tests need a linux or windows amd64 host")
	}
	p, err := github.New(githubConfig(srv.URL, newLimitedClient(limit)))
	if err != nil {
		t.Fatal(err)
	}
	u := updater.New(&fakeHost{onEmit: onEmit})
	if err := u.Init(updaterConfig("0.2.0", p)); err != nil {
		t.Fatal(err)
	}
	s := testService(t, Deps{Updater: u})
	// t.Cleanup runs last-in first-out: this registered before Shutdown, so it runs after it. It removes whatever the
	// framework still has staged, so a failed assertion cannot leave a wails-update-* folder next to the test binary.
	t.Cleanup(func() {
		if dir := stagingDirFor(u.DownloadedPath()); dir != "" {
			_ = os.RemoveAll(dir)
		}
	})
	t.Cleanup(s.Shutdown)
	return s
}

// primeAvailable puts the service where the user has seen version 0.3.0 and can install it.
func primeAvailable(s *Service) {
	s.mu.Lock()
	s.phase = PhaseAvailable
	s.latest = &latestRelease{version: "0.3.0"}
	s.mu.Unlock()
}

// Shutdown during the framework's verify step: the framework does not check the context there, so the install
// returns nil with Shutdown already run. The staged folder must not survive that, and the phase must end idle.
func TestShutdownDuringFinaliseRemovesStagedUpdate(t *testing.T) {
	var s *Service
	srv := installStub(t, 64)
	s = stagedService(t, srv, maxDownloadBytes, func(name string) {
		if name == updater.EventVerifying {
			s.Shutdown()
		}
	})
	primeAvailable(s)
	if err := s.Install("0.3.0"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	staged := s.deps.Updater.DownloadedPath()
	if staged == "" {
		t.Fatal("the framework staged nothing")
	}
	if _, err := os.Stat(filepath.Dir(staged)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staged folder survived Shutdown during finalise: stat err %v", err)
	}
	if snap := s.State(); snap.Phase != PhaseIdle || snap.Latest != nil {
		t.Fatalf("snapshot after shutdown during finalise = phase %s, latest %+v", snap.Phase, snap.Latest)
	}
}

// A Cancel during verification is not stopped by the framework, which does not check the context there: the install
// completes, ends in ready and keeps the staged update. The folder is removed only by a later Shutdown. A Cancel after
// ready is a no-op: it finds no cancelable phase.
func TestCancelDuringVerifyEndsReadyKeepsStaged(t *testing.T) {
	var s *Service
	srv := installStub(t, 64)
	s = stagedService(t, srv, maxDownloadBytes, func(name string) {
		if name == updater.EventVerifying {
			if err := s.Cancel(); err != nil {
				t.Errorf("Cancel: %v", err)
			}
		}
	})
	primeAvailable(s)
	if err := s.Install("0.3.0"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if snap := s.State(); snap.Phase != PhaseReady {
		t.Fatalf("phase after cancel during verify = %s, want ready", snap.Phase)
	}
	dir := filepath.Dir(s.deps.Updater.DownloadedPath())
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("staged folder removed by a cancel: %v", err)
	}
	s.Shutdown()
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staged folder kept by Shutdown while ready: stat err %v", err)
	}
}

// A download one byte over the client's size cap fails through the framework as a download error: the install ends
// in error with the download sentence, and the framework leaves nothing staged.
func TestInstallOverSizeCapIsDownloadFailed(t *testing.T) {
	srv := installStub(t, 8193)
	s := stagedService(t, srv, 8192, nil)
	primeAvailable(s)
	mustSentence(t, s.Install("0.3.0"), msgDownloadFailed)
	snap := s.State()
	if snap.Phase != PhaseError || snap.Error != msgDownloadFailed {
		t.Fatalf("snapshot = phase %s, error %q", snap.Phase, snap.Error)
	}
	if staged := s.deps.Updater.DownloadedPath(); staged != "" {
		t.Fatalf("an over-cap download left a staged payload: %q", staged)
	}
}

// A download of exactly the size cap is accepted and staged. Shutdown then removes the staged folder.
func TestInstallAtSizeCapStagesThenShutdownRemoves(t *testing.T) {
	srv := installStub(t, 8192)
	s := stagedService(t, srv, 8192, nil)
	primeAvailable(s)
	if err := s.Install("0.3.0"); err != nil {
		t.Fatalf("Install at the cap: %v", err)
	}
	if snap := s.State(); snap.Phase != PhaseReady {
		t.Fatalf("phase = %s, want ready", snap.Phase)
	}
	dir := filepath.Dir(s.deps.Updater.DownloadedPath())
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("staged folder missing before shutdown: %v", err)
	}
	s.Shutdown()
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("staged folder survived shutdown: stat err %v", err)
	}
	if snap := s.State(); snap.Phase != PhaseIdle {
		t.Fatalf("phase after shutdown = %s, want idle", snap.Phase)
	}
}

// overCapJSONService serves a release whose JSON body (a 16 KiB release body) is over the 8 KiB cap the service's
// client uses, while every asset (64 bytes) is under it. Only the release JSON can trip the cap.
func overCapJSONService(t *testing.T) *Service {
	t.Helper()
	stub := &stubGitHub{tag: "v0.3.0", withSums: true, notes: strings.Repeat("n", 16<<10)}
	srv := newStubGitHub(t, stub)
	for name := range stub.bodies {
		stub.bodies[name] = bytes.Repeat([]byte{'x'}, 64)
	}
	return stagedService(t, srv, 8192, nil)
}

// A release JSON over the client's cap fails a manual check: the phase is error with the check sentence, and the log
// records check-failed.
func TestCheckOverSizeCapJSONIsCheckFailed(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	s := overCapJSONService(t)
	mustSentence(t, s.CheckNow(), msgCheckFailed)
	snap := s.State()
	if snap.Phase != PhaseError || snap.Error != msgCheckFailed {
		t.Fatalf("snapshot = phase %s, error %q", snap.Phase, snap.Error)
	}
	if !strings.Contains(buf.String(), "update: check failed: check-failed") {
		t.Fatalf("log = %q, want the check-failed category", buf.String())
	}
}

// A release JSON over the cap fails an Install's re-check: the install ends in available with the check sentence in the
// error field, nothing is downloaded, and the log records check-failed for the install.
func TestInstallOverSizeCapJSONIsCheckFailedAndAvailable(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	s := overCapJSONService(t)
	primeAvailable(s)
	mustSentence(t, s.Install("0.3.0"), msgCheckFailed)
	snap := s.State()
	if snap.Phase != PhaseAvailable || snap.Error != msgCheckFailed {
		t.Fatalf("snapshot = phase %s, error %q", snap.Phase, snap.Error)
	}
	if staged := s.deps.Updater.DownloadedPath(); staged != "" {
		t.Fatalf("a failed re-check left a staged payload: %q", staged)
	}
	if !strings.Contains(buf.String(), "update: install: check-failed") {
		t.Fatalf("log = %q, want the install check-failed category", buf.String())
	}
}

// The asset download is redirected to plain http. The client's redirect policy refuses it, so the download fails through
// the framework: the install ends in error with the download sentence and stages nothing. The redirect target serves
// the right bytes, so a policy that allowed http would stage the update and fail this test.
func TestInstallInsecureAssetRedirectIsDownloadFailed(t *testing.T) {
	stub := &stubGitHub{tag: "v0.3.0", withSums: true, insecureRedirect: true}
	srv := newStubGitHub(t, stub)
	for name := range stub.bodies {
		stub.bodies[name] = bytes.Repeat([]byte{'x'}, 64)
	}
	s := stagedService(t, srv, maxDownloadBytes, nil)
	primeAvailable(s)
	mustSentence(t, s.Install("0.3.0"), msgDownloadFailed)
	snap := s.State()
	if snap.Phase != PhaseError || snap.Error != msgDownloadFailed {
		t.Fatalf("snapshot = phase %s, error %q", snap.Phase, snap.Error)
	}
	if staged := s.deps.Updater.DownloadedPath(); staged != "" {
		t.Fatalf("a refused redirect left a staged payload: %q", staged)
	}
}
