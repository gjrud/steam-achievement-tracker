//go:build windows

package fsretry

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestIsTransientWindowsErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"access denied", &os.PathError{Op: "remove", Path: "x", Err: syscall.ERROR_ACCESS_DENIED}, true},
		{"sharing violation", &os.LinkError{Op: "rename", Old: "a", New: "b", Err: errorSharingViolation}, true},
		{"lock violation", &os.PathError{Op: "remove", Path: "x", Err: errorLockViolation}, true},
		{"file not found", &os.PathError{Op: "remove", Path: "x", Err: syscall.ERROR_FILE_NOT_FOUND}, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransient(tc.err); got != tc.want {
				t.Fatalf("isTransient() = %v, want %v", got, tc.want)
			}
		})
	}
}
