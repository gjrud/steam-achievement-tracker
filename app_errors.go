package main

import (
	"errors"
	"fmt"
	"log"
	"runtime/debug"

	"github.com/gjrud/steam-achievement-tracker/internal/keyring"
	"github.com/gjrud/steam-achievement-tracker/internal/redact"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
	"github.com/gjrud/steam-achievement-tracker/internal/usererr"
)

// User-facing sentences of the App. The raw error behind each one is logged, never shown. Keep them short and plain:
// no paths, SQL, driver, OS or backend text.
const (
	// msgGeneric is shown for every error that has no more specific sentence. It is the text steam.UserMessage gives
	// for an error it does not know; a test pins the two together.
	msgGeneric = "Something went wrong. See the log file for details."
	// msgKeyring is shown for any failure of the system keyring itself (locked, unavailable, denied).
	msgKeyring = "Could not access the system keyring. Make sure it is unlocked, then try again."
	// msgInitDataFolder and msgInitDatabase are the startup failure sentences. The frontend names the log file, or the
	// app folder when no log file exists yet (AppState.LogFile and DataDir).
	msgInitDataFolder = "The app could not create its folders."
	msgInitDatabase   = "The app could not open its database."
	msgNotReady       = "The app database is not available. Restart the app, or see the log file for details."
	msgNoProfile      = "No Steam profile is active. Add your Steam profile first."
	msgSteamIDMissing = "A Steam profile is required."
)

// Sentinels for app-level outcomes. They are usererr values, so userFacingError returns them unchanged and callers
// can still match them with errors.Is.
var (
	// errSyncAlreadyInProgress is startSync's error for a forced start while a sync is running. Its text is matched
	// by syncAlreadyRunningMessage in frontend/src/lib/sync.js (checked by sync.test.js); change both together.
	// Migration 3's syncTextSafeSentences (internal/db/db.go) also lists it but is frozen: do not edit it there.
	errSyncAlreadyInProgress = usererr.New("A sync is already running. Try again when it finishes.")
	// errShuttingDown is startSync's error once ServiceShutdown has cancelled the lifetime context. Wails shuts services
	// down before it closes the windows, so the frontend can still call in while the repository is being closed.
	errShuttingDown   = usererr.New("The app is shutting down.")
	errNotReady       = usererr.New(msgNotReady)
	errNoProfile      = usererr.New(msgNoProfile)
	errSteamIDMissing = usererr.New(msgSteamIDMissing)
)

// callBound runs the body of one bound method and returns what the frontend receives. A panic in fn is logged with its
// stack and becomes msgGeneric, with a zero result. Without this, Wails turns the panic into a runtime error whose text
// (the method name and the panic value) would be shown raw. Any other error goes through userFacingError.
//
// The panic log is scrubbed by pattern with steam.RedactSecrets, because a panic value can carry the API key or a
// SteamID64 (saveAPIKey, validateProfile and the keyring calls run inside the guard). The cached key is not used: it
// may not be set yet, and reading it inside recover could deadlock. The stack is kept; it holds no argument text.
func callBound[T any](op string, fn func() (T, error)) (result T, err error) {
	defer func() {
		if r := recover(); r != nil {
			var zero T
			result, err = zero, usererr.New(msgGeneric)
			log.Print(steam.RedactSecrets(fmt.Sprintf("%s: panic: %s\n%s", op, redact.Paths(panicError(r)), debug.Stack())))
		}
	}()
	result, err = fn()
	return result, userFacingError(op, err)
}

// logText is the log text of err: usererr.LogText (paths reduced, raw cause kept) with secrets scrubbed by pattern
// (steam.RedactSecrets). It is used for the log lines of bound-method errors (userFacingError), startup init failures
// (ServiceStartup), start-sync failures (logStartSyncError) and the full-sync failure log in startSync. The per-game
// sync failure log in internal/syncer applies the same two steps inline, since it cannot import package main. These
// errors can carry text from any source, so a key or SteamID64 in them is scrubbed before it is written.
//
// The other log sites use redact.Paths only, because their errors carry no key, or are Steam client errors that the
// Client has already redacted. Examples are the keyring and
// cancel-stale-run logs in ensureInitialized, RetryAPIKey, the startup state log, normalizeStaleRun, the repository
// and sync-progress logs in internal/syncer, the GetGameAchievements fallback and cover refresh logs in internal/syncer,
// the migration log in internal/db, and the app.Run error in main.go. Steam client errors need no extra scrub: the steam
// Client already redacts its known key and profile identifiers (redactText) before they leave it.
func logText(err error) string { return steam.RedactSecrets(usererr.LogText(err)) }

// callBoundErr is callBound for a method that returns only an error.
func callBoundErr(op string, fn func() error) error {
	_, err := callBound(op, func() (struct{}, error) { return struct{}{}, fn() })
	return err
}

// panicError returns a recovered panic value as an error, so redact.Paths can reduce a *fs.PathError it carries.
func panicError(r any) error {
	if err, ok := r.(error); ok {
		return err
	}
	return fmt.Errorf("%v", r)
}

// userFacingError is the one place where an error from an exported bound method becomes the error the frontend
// receives. callBound passes every bound method's error through it, so none can be forgotten.
//
// It logs the raw error once, under op, with paths reduced to base names (see shouldLog for what is not logged). Then
// it returns:
//   - the usererr sentence of a user-facing error. An error with a raw cause (usererr.Wrap) is returned as a new
//     usererr with the same sentence, so the cause is not attached to the error handed to Wails;
//     a sentinel without a cause is returned unchanged, so errors.Is keeps matching it;
//   - msgKeyring for a failure of the system keyring;
//   - otherwise steam.UserMessage, which gives a Steam sentence for a Steam failure and msgGeneric for anything else.
//
// The returned text is never built from err's text. The raw error is kept only in the log.
func userFacingError(op string, err error) error {
	if err == nil {
		return nil
	}
	if shouldLog(err) {
		log.Printf("%s: %s", op, logText(err))
	}
	if ue := usererr.Find(err); ue != nil {
		if ue.Unwrap() != nil {
			return usererr.New(ue.Error())
		}
		return ue
	}
	if isKeyringFailure(err) {
		return usererr.New(msgKeyring)
	}
	return usererr.New(steam.UserMessage(err))
}

// shouldLog reports whether err is logged. A usererr without a cause is a sentence the app wrote for a normal state
// or an input check (a missing key, a sync already running, a start during shutdown, an empty profile). It is not a
// failure and is not logged. Every other error, and every usererr with a cause, is logged.
func shouldLog(err error) bool {
	if ue := usererr.Find(err); ue != nil {
		return ue.Unwrap() != nil
	}
	return true
}

// isKeyringFailure reports a failure of the system keyring. A missing key is not a failure (keyring.ErrMissing is a
// usererr sentinel, returned unchanged by userFacingError before this check), and ErrNotStored is one of the keyring's
// own failures.
func isKeyringFailure(err error) bool {
	var backend *keyring.BackendError
	return errors.As(err, &backend) || errors.Is(err, keyring.ErrNotStored)
}
