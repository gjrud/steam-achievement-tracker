package keyring

import (
	"errors"

	zk "github.com/zalando/go-keyring"

	"github.com/gjrud/steam-achievement-tracker/internal/config"
)

var ErrMissing = errors.New("steam web api key missing from Secret Service")

func GetSteamAPIKey() (string, error) {
	secret, err := zk.Get(config.AppID, config.KeyringUser)
	if err != nil {
		if errors.Is(err, zk.ErrNotFound) {
			return "", ErrMissing
		}
		return "", err
	}
	if secret == "" {
		return "", ErrMissing
	}
	return secret, nil
}
