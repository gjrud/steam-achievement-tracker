package fsretry

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var (
	errTransient = errors.New("sharing violation")
	errPermanent = errors.New("disk on fire")
)

// recordingPolicy returns a policy whose sleep only records the requested delays and whose transient check
// accepts errTransient.
func recordingPolicy() (policy, *[]time.Duration) {
	var sleeps []time.Duration
	return policy{
		attempts:  maxAttempts,
		delay:     retryDelay,
		sleep:     func(d time.Duration) { sleeps = append(sleeps, d) },
		transient: func(err error) bool { return errors.Is(err, errTransient) },
	}, &sleeps
}

// failFirst returns an op that fails with err for the first n calls and then succeeds. calls counts every call.
func failFirst(n int, err error, calls *int) func() error {
	return func() error {
		*calls++
		if *calls <= n {
			return err
		}
		return nil
	}
}

func TestRunSucceedsAfterTransientFailures(t *testing.T) {
	p, sleeps := recordingPolicy()
	calls := 0
	if err := run(p, failFirst(3, errTransient, &calls)); err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}
	if calls != 4 {
		t.Fatalf("op calls = %d, want 4 (3 failures + 1 success)", calls)
	}
	if len(*sleeps) != 3 {
		t.Fatalf("sleeps = %v, want 3 waits", *sleeps)
	}
	for _, d := range *sleeps {
		if d != retryDelay {
			t.Fatalf("wait = %v, want %v", d, retryDelay)
		}
	}
}

func TestRunGivesUpAfterMaxAttempts(t *testing.T) {
	p, sleeps := recordingPolicy()
	calls := 0
	err := run(p, func() error {
		calls++
		return errTransient
	})
	if !errors.Is(err, errTransient) {
		t.Fatalf("run() error = %v, want the last transient error", err)
	}
	if calls != maxAttempts {
		t.Fatalf("op calls = %d, want %d", calls, maxAttempts)
	}
	if len(*sleeps) != maxAttempts-1 {
		t.Fatalf("sleeps = %d, want %d", len(*sleeps), maxAttempts-1)
	}
}

// A non-transient error is returned after one call, with no wait, even when retries would otherwise run.
func TestRunDoesNotRetryNonTransientError(t *testing.T) {
	p, sleeps := recordingPolicy()
	calls := 0
	err := run(p, func() error {
		calls++
		return errPermanent
	})
	if !errors.Is(err, errPermanent) {
		t.Fatalf("run() error = %v, want %v", err, errPermanent)
	}
	if calls != 1 {
		t.Fatalf("op calls = %d, want 1", calls)
	}
	if len(*sleeps) != 0 {
		t.Fatalf("sleeps = %v, want none", *sleeps)
	}
}

func TestRunSuccessFirstTryNoWait(t *testing.T) {
	p, sleeps := recordingPolicy()
	calls := 0
	if err := run(p, failFirst(0, nil, &calls)); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if calls != 1 || len(*sleeps) != 0 {
		t.Fatalf("calls = %d, sleeps = %v; want 1 call and no wait", calls, *sleeps)
	}
}

func TestRunAttemptsBelowTwoMeansSingleCall(t *testing.T) {
	p, _ := recordingPolicy()
	p.attempts = 0
	calls := 0
	err := run(p, func() error {
		calls++
		return errTransient
	})
	if !errors.Is(err, errTransient) || calls != 1 {
		t.Fatalf("calls = %d, err = %v; want 1 call and the error", calls, err)
	}
}

func TestRenameRemoveRemoveAllMatchOSOnSuccess(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.tmp")
	dst := filepath.Join(dir, "a.jpg")
	if err := os.WriteFile(src, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Rename(src, dst); err != nil {
		t.Fatalf("Rename() error = %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}
	if err := Remove(dst); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(filepath.Join(sub, "deep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := RemoveAll(sub); err != nil {
		t.Fatalf("RemoveAll() error = %v", err)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Fatalf("sub still present: %v", err)
	}
}

func TestMissingPathErrorsMatchOS(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	if err := Remove(missing); !os.IsNotExist(err) {
		t.Fatalf("Remove(missing) error = %v, want not-exist", err)
	}
	if err := Rename(missing, filepath.Join(dir, "dst")); !os.IsNotExist(err) {
		t.Fatalf("Rename(missing) error = %v, want not-exist", err)
	}
	if err := RemoveAll(missing); err != nil {
		t.Fatalf("RemoveAll(missing) error = %v, want nil", err)
	}
}
