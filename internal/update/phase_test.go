package update

import (
	"testing"

	"github.com/wailsapp/wails/v3/pkg/updater"
)

var allPhases = []Phase{
	PhaseIdle, PhaseChecking, PhaseUpToDate, PhaseAvailable, PhaseDownloading,
	PhaseVerifying, PhaseInstalling, PhaseReady, PhaseRestarting, PhaseError,
}

func TestCanStart(t *testing.T) {
	allowed := map[operation][]Phase{
		opCheckManual:  {PhaseIdle, PhaseUpToDate, PhaseAvailable, PhaseError},
		opCheckStartup: {PhaseIdle},
		opInstall:      {PhaseAvailable, PhaseError},
		opRestart:      {PhaseReady},
	}
	names := map[operation]string{opCheckManual: "manual check", opCheckStartup: "startup check", opInstall: "install", opRestart: "restart"}
	for op, want := range allowed {
		for _, from := range allPhases {
			expect := false
			for _, w := range want {
				if w == from {
					expect = true
				}
			}
			if got := canStart(from, op); got != expect {
				t.Errorf("canStart(%s, %s) = %v, want %v", from, names[op], got, expect)
			}
		}
	}
}

func TestCancelable(t *testing.T) {
	for _, from := range allPhases {
		want := from == PhaseDownloading || from == PhaseVerifying || from == PhaseInstalling
		if got := cancelable(from); got != want {
			t.Errorf("cancelable(%s) = %v, want %v", from, got, want)
		}
	}
}

func TestFrameworkEventGuard(t *testing.T) {
	cases := []struct {
		from  Phase
		event string
		to    Phase
		ok    bool
	}{
		{PhaseDownloading, updater.EventDownloadProgress, PhaseDownloading, true},
		{PhaseAvailable, updater.EventDownloadProgress, PhaseAvailable, false},
		{PhaseVerifying, updater.EventDownloadProgress, PhaseVerifying, false},
		{PhaseReady, updater.EventDownloadProgress, PhaseReady, false},
		{PhaseDownloading, updater.EventVerifying, PhaseVerifying, true},
		{PhaseAvailable, updater.EventVerifying, PhaseAvailable, false},
		{PhaseInstalling, updater.EventVerifying, PhaseInstalling, false},
		{PhaseDownloading, updater.EventInstalling, PhaseInstalling, true},
		{PhaseVerifying, updater.EventInstalling, PhaseInstalling, true},
		{PhaseAvailable, updater.EventInstalling, PhaseAvailable, false},
		{PhaseReady, updater.EventInstalling, PhaseReady, false},
		{PhaseDownloading, updater.EventError, PhaseDownloading, false},
		{PhaseAvailable, updater.EventError, PhaseAvailable, false},
		{PhaseDownloading, updater.EventDownloadComplete, PhaseDownloading, false},
		{PhaseDownloading, updater.EventUpdateReady, PhaseDownloading, false},
	}
	for _, tc := range cases {
		to, ok := frameworkEvent(tc.from, tc.event)
		if to != tc.to || ok != tc.ok {
			t.Errorf("frameworkEvent(%s, %s) = (%s, %v), want (%s, %v)", tc.from, tc.event, to, ok, tc.to, tc.ok)
		}
	}
}

func TestRestartRefusalSentences(t *testing.T) {
	busy := []Phase{PhaseChecking, PhaseDownloading, PhaseVerifying, PhaseInstalling, PhaseRestarting}
	for _, from := range busy {
		mustSentence(t, restartRefusal(from), msgBusy)
	}
	for _, from := range []Phase{PhaseIdle, PhaseUpToDate, PhaseAvailable, PhaseError} {
		mustSentence(t, restartRefusal(from), msgNotReady)
	}
	for _, from := range allPhases {
		if from != PhaseReady && canStart(from, opRestart) {
			t.Fatalf("restart accepted from %s", from)
		}
	}
}

func TestOnEventFeedsOnlyForwardPhases(t *testing.T) {
	s := testService(t, Deps{})
	s.phase = PhaseDownloading
	s.onEvent(updater.EventDownloadProgress, updater.Progress{Written: 5, Total: 10})
	if s.progress != (DownloadProgress{Written: 5, Total: 10}) {
		t.Fatalf("progress = %+v", s.progress)
	}
	s.onEvent(updater.EventVerifying, nil)
	if s.phase != PhaseVerifying {
		t.Fatalf("phase = %s, want verifying", s.phase)
	}
	s.onEvent(updater.EventDownloadProgress, updater.Progress{Written: 9, Total: 10})
	if s.progress.Written != 5 {
		t.Fatalf("progress moved while verifying: %+v", s.progress)
	}
	s.onEvent(updater.EventInstalling, nil)
	if s.phase != PhaseInstalling {
		t.Fatalf("phase = %s, want installing", s.phase)
	}
	s.onEvent(updater.EventVerifying, nil)
	if s.phase != PhaseInstalling {
		t.Fatalf("installing moved backwards to %s", s.phase)
	}
}

// A late framework error after a cancel must not change the phase: EventError is not a listened event at all.
func TestFrameworkErrorEventIsNotConsumed(t *testing.T) {
	for _, name := range frameworkEvents {
		if name == updater.EventError {
			t.Fatalf("frameworkEvents includes %s", updater.EventError)
		}
	}
	if _, ok := frameworkEvent(PhaseDownloading, updater.EventError); ok {
		t.Fatal("frameworkEvent accepts EventError")
	}
}
