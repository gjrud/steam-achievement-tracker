package update

import (
	"path/filepath"
	"strings"
)

// stagingPrefix is the name prefix the framework gives every staging directory (os.MkdirTemp "wails-update-*").
const stagingPrefix = "wails-update-"

// stagingDirFor returns the staging directory that holds the staged payload at path, or "" when path is empty or its
// directory is not a framework staging directory. Removal is limited to that directory name, so nothing else next to
// the executable can be deleted through this path.
func stagingDirFor(path string) string {
	if path == "" {
		return ""
	}
	dir := filepath.Dir(path)
	if !strings.HasPrefix(filepath.Base(dir), stagingPrefix) {
		return ""
	}
	return dir
}
