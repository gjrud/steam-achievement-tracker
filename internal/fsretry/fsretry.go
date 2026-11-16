// Package fsretry wraps os.Rename, os.Remove and os.RemoveAll with a short retry on Windows.
//
// On Windows a file can be briefly held open by another process (antivirus, the search indexer, or the
// cover server reading an image), and a rename or delete then fails with a sharing or access violation
// that clears within a few milliseconds. The wrappers retry only those errors. Every other error returns on
// the first attempt, exactly like the os function it wraps. Off Windows nothing is classified as transient,
// so every call is a single os call.
package fsretry

import (
	"os"
	"time"
)

const (
	// maxAttempts is the total number of calls on Windows: the first try plus retries.
	maxAttempts = 5
	// retryDelay is the wait between attempts.
	retryDelay = 30 * time.Millisecond
)

// Rename is os.Rename with transient Windows errors retried.
func Rename(oldpath, newpath string) error {
	return run(defaultPolicy(), func() error { return os.Rename(oldpath, newpath) })
}

// Remove is os.Remove with transient Windows errors retried.
func Remove(name string) error {
	return run(defaultPolicy(), func() error { return os.Remove(name) })
}

// RemoveAll is os.RemoveAll with transient Windows errors retried. os.RemoveAll returns nil for a missing
// path, so a retry after a partial delete is safe.
func RemoveAll(path string) error {
	return run(defaultPolicy(), func() error { return os.RemoveAll(path) })
}

// policy controls run. It is a value so tests can inject sleep and classification.
type policy struct {
	// attempts is the maximum total number of op calls. Values below 2 mean one call.
	attempts int
	// delay is passed to sleep before every retry.
	delay time.Duration
	// sleep waits for delay before a retry. time.Sleep in production.
	sleep func(time.Duration)
	// transient reports whether err is worth retrying.
	transient func(error) bool
}

func defaultPolicy() policy {
	return policy{
		attempts:  maxAttempts,
		delay:     retryDelay,
		sleep:     time.Sleep,
		transient: isTransient,
	}
}

// run calls op once. A transient error is followed by a delay and another call, up to p.attempts calls in
// total. Any other error returns immediately. It returns the error of the last call, or nil on success.
func run(p policy, op func() error) error {
	for call := 1; ; call++ {
		err := op()
		if err == nil || !p.transient(err) || call >= p.attempts {
			return err
		}
		p.sleep(p.delay)
	}
}
