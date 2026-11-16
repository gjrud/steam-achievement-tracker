//go:build windows

package fsretry

import (
	"errors"
	"syscall"
)

// Win32 codes that the syscall package does not export. Values from winerror.h.
const (
	errorSharingViolation syscall.Errno = 32
	errorLockViolation    syscall.Errno = 33
)

// isTransient reports whether err is a Windows sharing, lock or access error that can clear when another
// process closes the file. A missing file (ERROR_FILE_NOT_FOUND) is not transient.
func isTransient(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	// ACCESS_DENIED is retried on purpose: a file held open by the cover server or the webview reports it on
	// rename and remove. The cost is about 120ms (4 waits of 30ms) for a file that is really read-only.
	case syscall.ERROR_ACCESS_DENIED, errorSharingViolation, errorLockViolation:
		return true
	default:
		return false
	}
}
