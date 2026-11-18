// Package redact removes filesystem paths from error text before it reaches logs or the UI.
package redact

import (
	"errors"
	"io/fs"
	"os"
	"strings"
)

// redactedError is an error whose message has its filesystem paths reduced to base names. The original error stays
// in the chain, so errors.Is keeps working.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// Paths returns err with the paths of a *fs.PathError or *os.LinkError in its chain reduced to their base
// file names, e.g. "open /home/<user>/.cache/.../library_600x900.jpg: ..." becomes "open library_600x900.jpg: ...".
// Paths under the user's profile directory carry the OS user name. Errors without such a path are returned
// unchanged. Use the result for log lines and UI messages only.
func Paths(err error) error {
	if err == nil {
		return nil
	}
	var paths []string
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) && pathErr.Path != "" {
		paths = append(paths, pathErr.Path)
	}
	if linkErr, ok := errors.AsType[*os.LinkError](err); ok {
		paths = append(paths, linkErr.Old, linkErr.New)
	}
	msg := err.Error()
	redacted := false
	for _, p := range paths {
		if p == "" {
			continue
		}
		if strings.Contains(msg, p) {
			msg = strings.ReplaceAll(msg, p, baseName(p))
			redacted = true
		}
	}
	if !redacted {
		return err
	}
	return &redactedError{msg: msg, err: err}
}

// baseName returns the last element of p for both '/' and '\' separators, whatever the OS of the process: a
// Windows path logged on Linux (or the reverse) must not keep its directories.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
