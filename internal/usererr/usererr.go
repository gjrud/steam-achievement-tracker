// Package usererr marks errors whose text was written for the user. Only errors built here (or wrapped by Wrap) may
// reach the UI or be stored for display. Any other error is raw: its text can hold paths, SQL, driver or backend
// details, so the caller logs it and shows its own sentence.
package usererr

import (
	"errors"
	"fmt"

	"github.com/gjrud/steam-achievement-tracker/internal/redact"
)

// Error is an error with a user-facing message. Its fields are unexported on purpose: Wails marshals a returned
// error's fields into the frontend's error cause, and an unexported-field struct marshals to {}.
type Error struct {
	msg   string
	cause error
}

// Error returns the user-facing message.
func (e *Error) Error() string { return e.msg }

// Unwrap returns the raw cause set by Wrap, or nil. The cause is for the log only; never show it.
func (e *Error) Unwrap() error { return e.cause }

// New returns a user-facing error with msg as its text. Use it for sentinels and for messages built by the app.
func New(msg string) error { return &Error{msg: msg} }

// Wrap returns a user-facing error with msg as its text and cause kept in the chain for the log. Error returns
// msg and Raw returns cause.
func Wrap(msg string, cause error) error { return &Error{msg: msg, cause: cause} }

// Find returns the *Error in err's chain, or nil when there is none. The result is the error value itself, so
// errors.Is against a sentinel built with New still matches.
func Find(err error) *Error {
	var ue *Error
	if errors.As(err, &ue) {
		return ue
	}
	return nil
}

// Raw returns the cause of the first *Error in err's chain, if it has one, otherwise err itself. Do not return it;
// log it through LogText.
func Raw(err error) error {
	var ue *Error
	if errors.As(err, &ue) && ue.cause != nil {
		return ue.cause
	}
	return err
}

// LogText is the text to log for err. For an error with a raw cause it is the error's own text, which keeps the
// operation context and any user sentence, followed by the cause in parentheses, so the cause is never lost:
// "resolve profile: No Steam profile matches that custom name. (cause: <Steam's message>)". An error without a cause
// is logged as it is. Paths are reduced to base names by redact.Paths in both parts. The log line is the caller's to
// prefix with its operation. LogText does not scrub keys or SteamID64s; callers that log text which may carry them
// pass the result through steam.RedactSecrets (see app_errors.go logText).
func LogText(err error) string {
	cause := Raw(err)
	if cause == err {
		return redact.Paths(err).Error()
	}
	return fmt.Sprintf("%v (cause: %v)", redact.Paths(err), redact.Paths(cause))
}
