package redact

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
)

func TestPathsReducesPathErrorsToBaseName(t *testing.T) {
	cases := []struct {
		name string
		err  error
		is   error
		want string
		gone []string
	}{
		{
			name: "linux path error",
			err:  &fs.PathError{Op: "open", Path: "/home/john.doe/.cache/x/1/library_600x900.jpg", Err: fs.ErrNotExist},
			is:   fs.ErrNotExist,
			want: "open library_600x900.jpg: file does not exist",
			gone: []string{"john.doe", "/home"},
		},
		{
			name: "windows path error",
			err:  &fs.PathError{Op: "open", Path: `C:\Users\John Doe\AppData\Local\cache\steam-achievement-tracker\images\1\library_600x900.jpg.tmp`, Err: fs.ErrPermission},
			is:   fs.ErrPermission,
			want: "open library_600x900.jpg.tmp: permission denied",
			gone: []string{"John Doe", `C:\`, "AppData"},
		},
		{
			name: "wrapped path error keeps context",
			err:  fmt.Errorf("refresh: %w", &fs.PathError{Op: "mkdir", Path: "/home/john.doe/.cache/x/1", Err: fs.ErrExist}),
			is:   fs.ErrExist,
			want: "refresh: mkdir 1: file already exists",
			gone: []string{"john.doe"},
		},
		{
			name: "link error",
			err:  &os.LinkError{Op: "rename", Old: `C:\Users\John Doe\cache\1\library.tmp`, New: "/home/john.doe/cache/1/library.jpg", Err: fs.ErrNotExist},
			is:   fs.ErrNotExist,
			want: "rename library.tmp library.jpg: file does not exist",
			gone: []string{"John Doe", "john.doe"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Paths(tc.err)
			if got.Error() != tc.want {
				t.Fatalf("Paths() = %q, want %q", got.Error(), tc.want)
			}
			for _, s := range tc.gone {
				if strings.Contains(got.Error(), s) {
					t.Fatalf("Paths() = %q still contains %q", got.Error(), s)
				}
			}
			if !errors.Is(got, tc.is) {
				t.Fatalf("Paths() lost the underlying error %v: %v", tc.is, got)
			}
		})
	}
}

func TestPathsLeavesOtherErrorsUnchanged(t *testing.T) {
	if got := Paths(nil); got != nil {
		t.Fatalf("Paths(nil) = %v, want nil", got)
	}
	plain := errors.New("cover download returned 500 Internal Server Error")
	if got := Paths(plain); got != plain {
		t.Fatalf("Paths(plain) = %v, want the same error", got)
	}
}
