// Package appdata resolves the directories the app stores its data in, following the XDG base directory
// layout on every OS (via github.com/adrg/xdg). On Linux that is ~/.local/share, ~/.cache and ~/.local/state.
// On Windows it is the Known Folder %LocalAppData%: data and state live in %LocalAppData%\steam-achievement-tracker
// and the cache in %LocalAppData%\cache\steam-achievement-tracker. The XDG_DATA_HOME, XDG_CACHE_HOME and
// XDG_STATE_HOME environment variables override these base directories on every OS.
package appdata

import (
	"os"
	"path/filepath"

	"github.com/adrg/xdg"

	"github.com/gjrud/steam-achievement-tracker/internal/config"
)

// Paths are the absolute locations of the app's files. DataDir, CacheDir and StateDir are the per-app roots under
// the XDG data, cache and state base directories; the other fields are derived from them.
type Paths struct {
	DataDir    string `json:"dataDir"`
	CacheDir   string `json:"cacheDir"`
	StateDir   string `json:"stateDir"`
	DB         string `json:"db"`
	Backups    string `json:"backups"`
	GameImages string `json:"gameImages"`
	Logs       string `json:"logs"`
	LogFile    string `json:"logFile"`
}

// Resolve returns the app's paths under the current XDG base directories. Call xdg.Reload after changing the XDG_*
// environment variables; Resolve does not reload on its own. It does not touch the file system; use Ensure for that.
func Resolve() Paths {
	dataDir := filepath.Join(xdg.DataHome, config.AppDirName)
	cacheDir := filepath.Join(xdg.CacheHome, config.AppDirName)
	stateDir := filepath.Join(xdg.StateHome, config.AppDirName)
	logs := filepath.Join(stateDir, "logs")
	return Paths{
		DataDir:    dataDir,
		CacheDir:   cacheDir,
		StateDir:   stateDir,
		DB:         filepath.Join(dataDir, config.DBFileName),
		Backups:    filepath.Join(dataDir, "backups"),
		GameImages: filepath.Join(cacheDir, "images", "games"),
		Logs:       logs,
		LogFile:    filepath.Join(logs, "app.log"),
	}
}

// Ensure creates the app's directories and restricts each to owner access (0o700).
func Ensure(paths Paths) error {
	for _, dir := range []string{paths.DataDir, paths.Backups, paths.CacheDir, paths.GameImages, paths.StateDir, paths.Logs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}
