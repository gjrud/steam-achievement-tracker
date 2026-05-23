package appdata

import (
	"os"
	"path/filepath"

	"github.com/gjrud/steam-achievement-tracker/internal/config"
)

type Paths struct {
	Root       string `json:"root"`
	DB         string `json:"db"`
	Backups    string `json:"backups"`
	Cache      string `json:"cache"`
	GameImages string `json:"gameImages"`
	Logs       string `json:"logs"`
	LogFile    string `json:"logFile"`
}

func Resolve() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	root := filepath.Join(home, config.DataDirName)
	return Paths{
		Root:       root,
		DB:         filepath.Join(root, config.DBFileName),
		Backups:    filepath.Join(root, "backups"),
		Cache:      filepath.Join(root, "cache"),
		GameImages: filepath.Join(root, "cache", "images", "games"),
		Logs:       filepath.Join(root, "logs"),
		LogFile:    filepath.Join(root, "logs", "app.log"),
	}, nil
}

func Ensure(paths Paths) error {
	for _, dir := range []string{paths.Root, paths.Backups, paths.Cache, paths.GameImages, paths.Logs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}
