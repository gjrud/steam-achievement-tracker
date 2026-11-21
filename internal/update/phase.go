package update

import "github.com/wailsapp/wails/v3/pkg/updater"

// Phase is the update phase shown to the UI. The phase is the exclusion mechanism: an operation starts only by moving
// the phase under the Service state lock, and the work runs outside that lock.
type Phase string

const (
	PhaseIdle        Phase = "idle"
	PhaseChecking    Phase = "checking"
	PhaseUpToDate    Phase = "up-to-date"
	PhaseAvailable   Phase = "available"
	PhaseDownloading Phase = "downloading"
	PhaseVerifying   Phase = "verifying"
	PhaseInstalling  Phase = "installing"
	PhaseReady       Phase = "ready"
	PhaseRestarting  Phase = "restarting"
	PhaseError       Phase = "error"
)

// operation is a user or startup request that moves the phase.
type operation int

const (
	opCheckManual operation = iota
	opCheckStartup
	opInstall
	opRestart
)

// canStart reports whether op may begin from phase. Anything else is refused at once.
func canStart(from Phase, op operation) bool {
	switch op {
	case opCheckManual:
		return from == PhaseIdle || from == PhaseUpToDate || from == PhaseAvailable || from == PhaseError
	case opCheckStartup:
		return from == PhaseIdle
	case opInstall:
		return from == PhaseAvailable || from == PhaseError
	case opRestart:
		return from == PhaseReady
	}
	return false
}

// cancelable reports whether a running install may be cancelled from phase. Cancel only raises a flag and cancels the
// install context; it never sets the phase.
func cancelable(from Phase) bool {
	return from == PhaseDownloading || from == PhaseVerifying || from == PhaseInstalling
}

// frameworkEvent is the forward-only guard for framework events. It returns the phase the event moves to and whether
// the event is accepted. Framework events reach Go listeners asynchronously and unordered, so an event that would move
// backwards, or that arrives outside the download, is dropped. Progress has no phase change: it is accepted only while
// downloading and returns PhaseDownloading.
func frameworkEvent(from Phase, event string) (Phase, bool) {
	switch event {
	case updater.EventDownloadProgress:
		if from == PhaseDownloading {
			return PhaseDownloading, true
		}
	case updater.EventVerifying:
		if from == PhaseDownloading {
			return PhaseVerifying, true
		}
	case updater.EventInstalling:
		if from == PhaseDownloading || from == PhaseVerifying {
			return PhaseInstalling, true
		}
	}
	return from, false
}
