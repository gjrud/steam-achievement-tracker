// Package update runs the in-app updater: the version rule, the GitHub provider, the phase state machine and the
// user-facing errors. It wraps the framework updater (pkg/updater) and never uses its CheckAndInstall or CheckInterval,
// which download without asking. Nothing is downloaded until Install is called.
package update

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/fsretry"
	"github.com/gjrud/steam-achievement-tracker/internal/usererr"
	"github.com/gjrud/steam-achievement-tracker/internal/version"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

// errBadVersion is a release whose tag is not X.Y.Z[-pre]. It is a check failure.
var errBadVersion = errors.New("update: release version is not X.Y.Z[-pre]")

// errNoUpdater is a check on a service that has no framework updater. It is a check failure.
var errNoUpdater = errors.New("update: no framework updater")

// frameworkEvents are the only framework events Go listens to. Progress, verifying and installing feed the phase;
// EventError is not consumed (a cancel must never end in an error).
var frameworkEvents = []string{updater.EventDownloadProgress, updater.EventVerifying, updater.EventInstalling}

// Deps are the app pieces the service needs. The service takes concrete framework values, not interfaces.
type Deps struct {
	// Updater is app.Updater. Init configures it. Required: production always passes app.Updater.
	Updater *updater.Updater
	// Context is the app lifetime context. It is required. Checks and installs derive from it, so cancelling it stops
	// them.
	Context context.Context
	// OnEvent subscribes fn to a framework event and returns the unsubscribe func. Production wires app.Event.On and
	// passes the event's Data. Nil means no progress feed.
	OnEvent func(name string, fn func(data any)) func()
	// ChecksEnabled reads the update_checks_enabled setting. It is called once per load, by LoadChecksEnabled, which the
	// app calls when the database opens. It is never called by StartupCheck or State. An error means unreadable: the
	// startup check is skipped and, if the toggle was never known, the snapshot reports checks off. Nil means off.
	ChecksEnabled func() (bool, error)
	// OnChanged is called after every change to the snapshot, outside the state lock. Production emits update:changed.
	// It is not called after Shutdown.
	OnChanged func()
}

// Snapshot is what GetUpdateState returns. The frontend re-reads it on update:changed.
type Snapshot struct {
	Unavailable    bool             `json:"unavailable"`
	CurrentVersion string           `json:"currentVersion"`
	ChecksEnabled  bool             `json:"checksEnabled"`
	Phase          Phase            `json:"phase"`
	Latest         *Latest          `json:"latest"`
	Progress       DownloadProgress `json:"progress"`
	NotifyOnly     bool             `json:"notifyOnly"`
	NotifyReason   string           `json:"notifyReason,omitempty"`
	Error          string           `json:"error,omitempty"`
}

// Latest describes the newest stable release found by the last successful check.
type Latest struct {
	Version    string `json:"version"`
	Size       int64  `json:"size"`
	ReleaseURL string `json:"releaseURL"`
}

// DownloadProgress is the byte count of the running download.
type DownloadProgress struct {
	Written int64 `json:"written"`
	Total   int64 `json:"total"`
}

// latestRelease is the internal form of Latest, with the gate result.
type latestRelease struct {
	version    string
	size       int64
	releaseURL string
	notifyOnly bool
	reason     string
}

// Service is the updater. Its state lock is held only for short reads and writes; every network call, every framework
// call and every filesystem call runs outside it. Nothing waits on a running operation, so Shutdown and the event
// listener never block behind a download. State does no I/O: the checks toggle is cached by LoadChecksEnabled and
// SetChecksEnabled.
type Service struct {
	deps Deps
	life context.Context
	env  hostEnv
	// client is the HTTP client for the GitHub provider and the asset download.
	client *http.Client

	// emitMu serializes OnChanged calls and guards stopped. Shutdown takes it, so no OnChanged call starts after
	// Shutdown returns. OnChanged never calls back into the service.
	emitMu  sync.Mutex
	stopped bool

	mu            sync.Mutex
	unavailable   bool
	current       string
	checksEnabled bool   // the cached toggle; meaningful only when checksKnown
	checksKnown   bool   // true once the toggle was read or set
	checksSets    uint64 // counts SetChecksEnabled calls; a load whose read overlaps one does not write the cache
	phase         Phase
	latest        *latestRelease
	progress      DownloadProgress
	errText       string
	cancelled     bool
	installCancel context.CancelFunc
	unsubs        []func()
	// postOnce makes RunPostUpdate start its goroutine once. postDone is closed when that goroutine returns; it is nil
	// until RunPostUpdate starts one. Tests wait on it.
	postOnce sync.Once
	postDone chan struct{}
	// initFailure is the category of a framework refusal in configure. It is logged once, by LogInitFailure, after
	// logging is set up; empty when there is nothing to log.
	initFailure category
}

// New returns a service that is not yet initialized. Deps.Context is required. Call Init once, after application.New.
func New(d Deps) *Service {
	if d.Context == nil {
		panic("update: Deps.Context is required")
	}
	return &Service{deps: d, life: d.Context, env: realEnv(), client: newHTTPClient(), phase: PhaseIdle}
}

// Init reads the version stamp, configures the framework updater and subscribes to its events. Without a valid stamp,
// or when the framework refuses its configuration, the service is unavailable: no request is ever made and every
// operation returns one fixed sentence. A build without a stamp (dev, Docker, untagged local) is expected and is not
// logged. Init runs before logging is set up, so a framework refusal is logged later, by LogInitFailure.
func (s *Service) Init() {
	cur, ok := version.Current()
	if !ok {
		s.setUnavailable()
		return
	}
	s.configure(cur)
}

// configure gives the framework updater the stamped version cur and subscribes to its events. A refusal makes the
// service unavailable and records the category for LogInitFailure.
func (s *Service) configure(cur string) {
	p, err := github.New(githubConfig("", s.client))
	if err == nil {
		err = s.deps.Updater.Init(updaterConfig(cur, p))
	}
	if err != nil {
		s.mu.Lock()
		s.unavailable = true
		s.initFailure = catOther
		s.mu.Unlock()
		return
	}
	s.mu.Lock()
	s.current = cur
	s.mu.Unlock()
	if s.deps.OnEvent != nil {
		unsubs := make([]func(), 0, len(frameworkEvents))
		for _, name := range frameworkEvents {
			unsubs = append(unsubs, s.deps.OnEvent(name, func(data any) { s.onEvent(name, data) }))
		}
		s.mu.Lock()
		s.unsubs = unsubs
		s.mu.Unlock()
	}
}

func (s *Service) setUnavailable() {
	s.mu.Lock()
	s.unavailable = true
	s.mu.Unlock()
}

// LogInitFailure logs the category recorded by configure, once. It is idempotent: the first call after a refusal logs
// and clears the category, later calls do nothing. The app calls it from ServiceStartup right after the app's own init,
// so a refused Updater.Init is logged even when the app's init failed. LoadChecksEnabled and StartupCheck call it too.
func (s *Service) LogInitFailure() {
	s.mu.Lock()
	cat := s.initFailure
	s.initFailure = ""
	s.mu.Unlock()
	if cat != "" {
		log.Printf("update: unavailable: %s", cat)
	}
}

// unavailableNow reads the unavailable flag under the state lock.
func (s *Service) unavailableNow() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unavailable
}

// State returns the snapshot for GetUpdateState. It does no I/O. ChecksEnabled is the cached toggle, and it reads as
// false until the toggle is known: LoadChecksEnabled loads it, or SetChecksEnabled sets it. A startup that could not read
// the setting therefore reports checks off, so the UI never claims checks run when they do not.
func (s *Service) State() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		Unavailable:    s.unavailable,
		CurrentVersion: s.current,
		ChecksEnabled:  s.checksKnown && s.checksEnabled,
		Phase:          s.phase,
		Progress:       s.progress,
		Error:          s.errText,
	}
	if s.latest != nil {
		snap.Latest = &Latest{Version: s.latest.version, Size: s.latest.size, ReleaseURL: s.latest.releaseURL}
		snap.NotifyOnly = s.latest.notifyOnly
		snap.NotifyReason = s.latest.reason
	}
	return snap
}

// LoadChecksEnabled reads the checks toggle once and caches it. The app calls it when the database first opens. It
// notifies when the snapshot's toggle changes. An unreadable setting is logged and leaves the toggle unknown, which
// reads as off, so no check runs; an unreadable reload never overwrites a toggle that is already known. It never calls
// the getter under any lock. If SetChecksEnabled stored a value while the getter ran, the read may be stale, so the
// cache keeps the newer value and nothing is written.
func (s *Service) LoadChecksEnabled() {
	s.LogInitFailure()
	if s.unavailableNow() {
		return
	}
	s.mu.Lock()
	sets := s.checksSets
	s.mu.Unlock()
	on := false
	var err error
	if s.deps.ChecksEnabled != nil {
		on, err = s.deps.ChecksEnabled()
	}
	if err != nil {
		log.Print("update: settings unreadable: update checks off")
	}
	s.mu.Lock()
	// A newer SetChecksEnabled wins over this read. So does a failed read over a toggle already known: a value the user
	// set is never wiped by an unreadable reload.
	if s.checksSets != sets || (err != nil && s.checksKnown) {
		s.mu.Unlock()
		return
	}
	before := s.checksKnown && s.checksEnabled
	s.checksKnown = err == nil
	s.checksEnabled = err == nil && on
	after := s.checksKnown && s.checksEnabled
	s.mu.Unlock()
	if before != after {
		s.notify()
	}
}

// StartupCheck starts the background check when the cached toggle is on. It never reads the setting: LoadChecksEnabled
// does that once, when the database opens. Until then the toggle is unknown and nothing is scheduled. The check starts
// startupDelay later, runs only if the toggle is still on and the phase is idle then, and stops on app shutdown. The
// delay is read here, on the caller's goroutine, so tests can change it safely.
func (s *Service) StartupCheck() {
	s.LogInitFailure()
	if s.unavailableNow() {
		return
	}
	s.mu.Lock()
	on := s.checksKnown && s.checksEnabled
	s.mu.Unlock()
	if !on {
		return
	}
	go s.startupAfterDelay(startupDelay)
}

func (s *Service) startupAfterDelay(delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-s.life.Done():
		return
	case <-timer.C:
	}
	s.mu.Lock()
	if s.unavailable || !s.checksKnown || !s.checksEnabled || !canStart(s.phase, opCheckStartup) {
		s.mu.Unlock()
		return
	}
	s.phase = PhaseChecking
	s.errText = ""
	s.mu.Unlock()
	s.notify()
	_ = s.runCheck(false)
}

// SetChecksEnabled updates the cached toggle after the caller has stored it. It returns the fixed sentence when the
// updater is unavailable.
func (s *Service) SetChecksEnabled(enabled bool) error {
	s.mu.Lock()
	if s.unavailable {
		s.mu.Unlock()
		return usererr.New(msgUnavailable)
	}
	s.checksEnabled = enabled
	s.checksKnown = true
	s.checksSets++
	s.mu.Unlock()
	s.notify()
	return nil
}

// CheckNow is the manual "Check now". A failure sets the error phase and returns the sentence.
func (s *Service) CheckNow() error {
	s.mu.Lock()
	if s.unavailable {
		s.mu.Unlock()
		return usererr.New(msgUnavailable)
	}
	if !canStart(s.phase, opCheckManual) {
		err := refusal(s.phase)
		s.mu.Unlock()
		return err
	}
	s.phase = PhaseChecking
	s.errText = ""
	s.mu.Unlock()
	s.notify()
	return s.runCheck(true)
}

// runCheck performs one check outside the lock and applies the result. The caller has already moved to checking.
func (s *Service) runCheck(manual bool) error {
	ctx, cancel := context.WithTimeout(s.life, checkTimeout)
	rel, err := s.fetch(ctx)
	cancel()
	if err != nil {
		cat := categorize(stepCheck, false, err)
		// A failure caused by shutdown is not logged, for startup and manual checks alike.
		if !manual {
			if s.life.Err() == nil {
				log.Printf("update: startup check failed: %s", cat)
			}
			s.mutate(func() { s.phase = PhaseIdle })
			return nil
		}
		if s.life.Err() == nil {
			log.Printf("update: check failed: %s", cat)
		}
		s.mutate(func() {
			s.phase = PhaseError
			s.errText = cat.message()
		})
		return usererr.New(cat.message())
	}
	info := s.latestFrom(rel)
	phase := PhaseUpToDate
	if info != nil {
		phase = PhaseAvailable
	}
	s.mutate(func() {
		s.latest = info
		s.phase = phase
		s.errText = ""
		s.progress = DownloadProgress{}
	})
	return nil
}

// Install downloads, verifies and stages the release want, which the user saw. Host refusals (package install, unsupported
// platform) and notify-only releases are refused before anything changes, so a package install never reaches the
// network through Install. Otherwise it moves to downloading, re-checks, and proceeds only if the newest release is
// still want. A refusal returns at once and changes nothing. Operation failures set the snapshot and return the same
// sentence. A cancel returns nil.
func (s *Service) Install(want string) error {
	if s.unavailableNow() {
		return usererr.New(msgUnavailable)
	}
	if reason := hostRefusal(s.env); reason != "" {
		return usererr.New(reason)
	}
	s.mu.Lock()
	if s.latest != nil && s.latest.notifyOnly {
		reason := s.latest.reason
		s.mu.Unlock()
		return usererr.New(reason)
	}
	if s.phase == PhaseIdle || s.phase == PhaseUpToDate {
		// Nothing is known to install: a check comes first, not a busy wait.
		s.mu.Unlock()
		return usererr.New(msgCheckFirst)
	}
	if !canStart(s.phase, opInstall) {
		err := refusal(s.phase)
		s.mu.Unlock()
		return err
	}
	if s.latest == nil {
		// No known release: nothing to install until a check has found one.
		s.mu.Unlock()
		return usererr.New(msgCheckFirst)
	}
	ctx, cancel := context.WithCancel(s.life)
	s.phase = PhaseDownloading
	s.errText = ""
	s.progress = DownloadProgress{}
	s.cancelled = false
	s.installCancel = cancel
	s.mu.Unlock()
	s.notify()
	defer cancel()
	return s.runInstall(ctx, want)
}

// runInstall is the install operation after the downloading transition. Every phase it sets comes from a return value
// of its own calls, so a late framework error event cannot change the outcome. Each terminal transition goes through
// finish, which also drops the install's cancel func and the progress.
func (s *Service) runInstall(ctx context.Context, want string) error {
	chkCtx, chkCancel := context.WithTimeout(ctx, checkTimeout)
	rel, err := s.fetch(chkCtx)
	chkCancel()
	if err != nil {
		cat := categorize(stepCheck, s.canceledFor(ctx), err)
		log.Printf("update: install: %s", cat)
		if cat == catCancelled {
			s.finish(func() { s.phase = PhaseAvailable })
			return nil
		}
		s.finish(func() {
			s.phase = PhaseAvailable
			s.errText = cat.message()
		})
		return usererr.New(cat.message())
	}
	if rel == nil {
		s.finish(func() {
			s.phase = PhaseUpToDate
			s.latest = nil
		})
		return nil
	}
	info := s.latestFrom(rel)
	if rel.Version != want || info.notifyOnly || s.canceledFor(ctx) {
		// Something newer or unsafe to install appeared, or the user cancelled during the re-check: show it again.
		s.finish(func() {
			s.phase = PhaseAvailable
			s.latest = info
		})
		return nil
	}
	s.mutate(func() { s.latest = info })

	err = s.deps.Updater.DownloadAndInstall(ctx)
	if err == nil {
		s.finish(func() {
			s.phase = PhaseReady
			s.errText = ""
		})
		return nil
	}
	cat := categorize(stepDownload, s.canceledFor(ctx), err)
	log.Printf("update: install: %s", cat)
	if cat == catCancelled {
		s.finish(func() { s.phase = PhaseAvailable })
		return nil
	}
	s.finish(func() {
		s.phase = PhaseError
		s.errText = cat.message()
	})
	return usererr.New(cat.message())
}

// Cancel asks the running install to stop. It sets the cancel flag and cancels the install context, and leaves the
// phase to the install operation. It has no effect when no install is running. It returns the fixed sentence when the
// updater is unavailable.
func (s *Service) Cancel() error {
	s.mu.Lock()
	if s.unavailable {
		s.mu.Unlock()
		return usererr.New(msgUnavailable)
	}
	if !cancelable(s.phase) {
		s.mu.Unlock()
		return nil
	}
	s.cancelled = true
	cancel := s.installCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// Restart applies the staged update. Only the ready phase is accepted. The phase moves to restarting before the
// framework runs, because the framework's Restart calls host quit, which runs shutdown before it returns. A failed
// Restart goes back to ready so it can be retried.
func (s *Service) Restart() error {
	s.mu.Lock()
	if s.unavailable {
		s.mu.Unlock()
		return usererr.New(msgUnavailable)
	}
	if !canStart(s.phase, opRestart) {
		err := restartRefusal(s.phase)
		s.mu.Unlock()
		return err
	}
	s.phase = PhaseRestarting
	s.errText = ""
	s.mu.Unlock()
	s.notify()

	if err := s.deps.Updater.Restart(s.life); err != nil {
		cat := categorize(stepRestart, false, err)
		log.Printf("update: restart: %s", cat)
		s.mutate(func() {
			s.phase = PhaseReady
			s.errText = cat.message()
		})
		return usererr.New(cat.message())
	}
	return nil
}

// Shutdown is called from the app's service shutdown. It never waits on a running operation. It stops notifications,
// stops a running install through its context, drops the framework listeners, and, when an update was staged but not
// applied, moves to idle and removes the staging directory. It removes nothing while restarting: the helper owns that
// directory.
func (s *Service) Shutdown() {
	s.emitMu.Lock()
	s.stopped = true
	s.emitMu.Unlock()

	s.mu.Lock()
	cancel := s.installCancel
	wasReady := s.phase == PhaseReady
	if wasReady {
		s.phase = PhaseIdle
		s.latest = nil
		s.progress = DownloadProgress{}
	}
	unsubs := s.unsubs
	s.unsubs = nil
	s.mu.Unlock()

	for _, unsub := range unsubs {
		unsub()
	}
	if cancel != nil {
		cancel()
	}
	if !wasReady || s.deps.Updater == nil {
		return
	}
	dir := stagingDirFor(s.deps.Updater.DownloadedPath())
	if dir == "" {
		return
	}
	if err := fsretry.RemoveAll(dir); err != nil {
		log.Printf("update: remove staged update: %s", catOther)
	}
}

// fetch asks the framework for a newer release. Its error text is never returned to a caller. Without a framework
// updater there is nothing to ask, so the check fails (production always has one; only a test service may lack it).
func (s *Service) fetch(ctx context.Context) (*updater.Release, error) {
	if s.deps.Updater == nil {
		return nil, errNoUpdater
	}
	rel, err := s.deps.Updater.Check(ctx)
	if err != nil {
		return nil, err
	}
	return screenRelease(rel)
}

// screenRelease decides whether a release the framework found is offered. A pre-release is never offered and reads as
// up to date. A version that is not X.Y.Z[-pre] is a check failure.
func screenRelease(rel *updater.Release) (*updater.Release, error) {
	if rel == nil {
		return nil, nil
	}
	if _, ok := version.Valid(rel.Version); !ok {
		return nil, errBadVersion
	}
	if version.IsPrerelease(rel.Version) {
		return nil, nil
	}
	return rel, nil
}

// latestFrom turns a found release into the snapshot form, with the gate result. It returns nil for no release.
func (s *Service) latestFrom(rel *updater.Release) *latestRelease {
	if rel == nil {
		return nil
	}
	l := &latestRelease{
		version:    rel.Version,
		size:       rel.Artifact.Size,
		releaseURL: releasePageURL(rel.Version),
	}
	if reason := installRefusal(rel.Verification, s.env); reason != "" {
		l.notifyOnly = true
		l.reason = reason
	}
	return l
}

// onEvent is the framework event listener. The listener runs in its own goroutine and takes the state lock briefly.
func (s *Service) onEvent(name string, data any) {
	s.mu.Lock()
	next, ok := frameworkEvent(s.phase, name)
	if !ok {
		s.mu.Unlock()
		return
	}
	s.phase = next
	if p, isProgress := data.(updater.Progress); isProgress && name == updater.EventDownloadProgress {
		s.progress = DownloadProgress{Written: p.Written, Total: p.Total}
	}
	s.mu.Unlock()
	s.notify()
}

// canceledFor reports whether the install running on ctx was cancelled: by our flag (Cancel), or by its context
// (Shutdown or the app lifetime ending). Framework error text is never inspected.
func (s *Service) canceledFor(ctx context.Context) bool {
	s.mu.Lock()
	cancelled := s.cancelled
	s.mu.Unlock()
	return cancelled || ctx.Err() != nil
}

// mutate applies f under the state lock and then notifies. f must not call notify or take the lock.
func (s *Service) mutate(f func()) {
	s.mu.Lock()
	f()
	s.mu.Unlock()
	s.notify()
}

// finish applies the terminal transition of an install under the state lock. It then clears what the install held:
// the progress and the cancel func. It notifies after the lock is released. f must not take the lock.
func (s *Service) finish(f func()) {
	s.mu.Lock()
	f()
	s.progress = DownloadProgress{}
	s.installCancel = nil
	s.mu.Unlock()
	s.notify()
}

// notify calls OnChanged outside the state lock, and never after Shutdown.
func (s *Service) notify() {
	if s.deps.OnChanged == nil {
		return
	}
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	if s.stopped {
		return
	}
	s.deps.OnChanged()
}

// refusal is the sentence for an operation that canStart refused from phase, for CheckNow and Install.
func refusal(from Phase) error {
	if from == PhaseReady {
		return usererr.New(msgRestartFirst)
	}
	return usererr.New(msgBusy)
}

// restartRefusal is the sentence for a Restart refused from phase. While an operation runs the answer is busy; in any
// other phase nothing is staged to restart into.
func restartRefusal(from Phase) error {
	switch from {
	case PhaseChecking, PhaseDownloading, PhaseVerifying, PhaseInstalling, PhaseRestarting:
		return usererr.New(msgBusy)
	}
	return usererr.New(msgNotReady)
}
