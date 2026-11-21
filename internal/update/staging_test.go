package update

import (
	"path/filepath"
	"testing"
)

func TestStagingDirFor(t *testing.T) {
	staged := filepath.Join("bin", "wails-update-123", "steam-achievement-tracker")
	cases := []struct {
		name string
		path string
		want string
	}{
		{"staged payload", staged, filepath.Join("bin", "wails-update-123")},
		{"empty path", "", ""},
		{"not a staging dir", filepath.Join("bin", "other", "sat"), ""},
		{"payload directly in bin", filepath.Join("bin", "wails-update-1"), ""},
		{"prefix must match the base name", filepath.Join("bin", "x-wails-update-1", "sat"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stagingDirFor(tc.path); got != tc.want {
				t.Fatalf("stagingDirFor(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
