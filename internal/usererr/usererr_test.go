package usererr

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

func TestNewKeepsTextAndIdentity(t *testing.T) {
	sentinel := New("Enter your key first.")
	if sentinel.Error() != "Enter your key first." {
		t.Fatalf("Error() = %q", sentinel.Error())
	}
	wrapped := fmt.Errorf("save: %w", sentinel)
	if !errors.Is(wrapped, sentinel) {
		t.Fatal("errors.Is lost the sentinel through a wrap")
	}
	if Find(wrapped) != sentinel {
		t.Fatal("Find did not return the sentinel value itself")
	}
}

func TestFindReturnsOnlyUserErrors(t *testing.T) {
	if ue := Find(fmt.Errorf("outer: %w", New("Plain message."))); ue == nil || ue.Error() != "Plain message." {
		t.Fatalf("Find(wrapped user error) = %v", ue)
	}
	if ue := Find(errors.New("raw /home/x/db.sqlite: locked")); ue != nil {
		t.Fatalf("Find(raw) = %v, want nil", ue)
	}
	if ue := Find(nil); ue != nil {
		t.Fatalf("Find(nil) = %v, want nil", ue)
	}
}

func TestWrapKeepsCauseForLogOnly(t *testing.T) {
	cause := &fs.PathError{Op: "mkdir", Path: "/home/x/.local/share/app", Err: fs.ErrPermission}
	err := Wrap("The app could not create its folders.", cause)
	if strings.Contains(err.Error(), "/home") || strings.Contains(err.Error(), "permission") {
		t.Fatalf("Error() = %q carries the cause", err.Error())
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatal("errors.Is does not reach the cause through Unwrap")
	}
	if Raw(err) != cause {
		t.Fatalf("Raw(err) = %v, want the cause", Raw(err))
	}
	if got := Raw(fmt.Errorf("init: %w", err)); got != cause {
		t.Fatalf("Raw(wrapped) = %v, want the cause", got)
	}
}

// LogText keeps the outer context and the sentence, then the raw cause, and reduces paths in both.
func TestLogTextKeepsContextAndCause(t *testing.T) {
	cause := errors.New("sql: database is closed")
	err := fmt.Errorf("resolve profile: %w", Wrap("No Steam profile was found for that account.", cause))
	want := "resolve profile: No Steam profile was found for that account. (cause: sql: database is closed)"
	if got := LogText(err); got != want {
		t.Fatalf("LogText = %q, want %q", got, want)
	}
	pathCause := &fs.PathError{Op: "mkdir", Path: "/home/x/.local/share/app", Err: fs.ErrPermission}
	if got := LogText(Wrap("The app could not create its folders.", pathCause)); got !=
		"The app could not create its folders. (cause: mkdir app: permission denied)" {
		t.Fatalf("LogText with a path cause = %q", got)
	}
}

func TestLogTextWithoutCauseIsTheErrorText(t *testing.T) {
	if got := LogText(errors.New("boom")); got != "boom" {
		t.Fatalf("LogText(raw) = %q, want boom", got)
	}
	if got := LogText(New("Plain sentence.")); got != "Plain sentence." {
		t.Fatalf("LogText(sentinel) = %q, want the sentence", got)
	}
}

func TestRawWithoutCauseIsTheError(t *testing.T) {
	sentinel := New("No profile is active.")
	if Raw(sentinel) != sentinel {
		t.Fatal("Raw of a cause-less user error must be the error itself")
	}
	raw := errors.New("boom")
	if Raw(raw) != raw {
		t.Fatal("Raw of a plain error must be the error itself")
	}
}

func TestErrorMarshalsWithoutFields(t *testing.T) {
	// Wails copies json.Marshal of the returned error into the frontend's error cause. The fields are unexported, so
	// the cause carries nothing beyond the message the frontend already has.
	b, err := json.Marshal(New("secret /home/x"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{}" {
		t.Fatalf("json = %s, want {}", b)
	}
}
