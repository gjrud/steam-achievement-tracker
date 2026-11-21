package update

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/usererr"
)

func TestCategorize(t *testing.T) {
	raw := errors.New(`updater: staging dir: mkdir /home/user/app: permission denied`)
	perm := fmt.Errorf("updater: staging dir: %w", &fs.PathError{Op: "mkdir", Path: "/opt/app", Err: fs.ErrPermission})
	cases := []struct {
		name      string
		st        step
		cancelled bool
		err       error
		want      category
	}{
		{"check failure", stepCheck, false, raw, catCheckFailed},
		{"check cancelled wins", stepCheck, true, raw, catCancelled},
		{"download permission", stepDownload, false, perm, catNotWritable},
		{"download generic", stepDownload, false, raw, catDownloadFailed},
		{"download cancelled beats permission", stepDownload, true, perm, catCancelled},
		{"download cancelled beats generic", stepDownload, true, raw, catCancelled},
		{"restart failure", stepRestart, false, raw, catRestartFailed},
		{"restart ignores cancel flag", stepRestart, true, raw, catRestartFailed},
		{"unknown step", step(99), false, raw, catOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := categorize(tc.st, tc.cancelled, tc.err); got != tc.want {
				t.Fatalf("categorize = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCategoryMessagesAreCauseless(t *testing.T) {
	cats := []category{catCheckFailed, catNotWritable, catDownloadFailed, catRestartFailed, catOther}
	for _, c := range cats {
		msg := c.message()
		if msg == "" {
			t.Fatalf("%s has no sentence", c)
		}
		if strings.ContainsAny(msg, `/\`) || strings.Contains(strings.ToLower(msg), "updater") {
			t.Fatalf("%s sentence leaks detail: %q", c, msg)
		}
		err := usererr.New(msg)
		if usererr.Raw(err) != err {
			t.Fatalf("%s: usererr error has a cause", c)
		}
	}
	if catCancelled.message() != "" {
		t.Fatal("a cancel must have no sentence")
	}
}
