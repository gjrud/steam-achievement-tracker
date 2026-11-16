package keyring

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	zk "github.com/zalando/go-keyring"

	"github.com/gjrud/steam-achievement-tracker/internal/config"
)

var ErrMissing = errors.New("steam web api key missing from system keyring")

// ErrInvalidKey is returned by SetSteamAPIKey for a key that is empty after trimming or that contains spaces,
// control characters, or format characters. The message is constant and never includes the input.
var ErrInvalidKey = errors.New("steam web api key must not be empty or contain spaces, control or invisible characters")

// ErrNotStored is returned by SetSteamAPIKey when the read-back does not return the key that was just written. The
// usual cause is a dismissed unlock or create prompt, which go-keyring ignores silently; a provider that keeps a
// duplicate item (Get returns the first match) can also cause it. The message is constant and never includes the key.
var ErrNotStored = errors.New("steam web api key was not stored: the system keyring did not keep it (a dismissed prompt, or a duplicate item left by another provider)")

// zkSet and zkGet are the go-keyring calls SetSteamAPIKey makes. Tests swap them to simulate a silent non-store
// or a failing read-back; production uses go-keyring directly.
// zkDelete is the go-keyring call DeleteSteamAPIKey makes. Tests swap it to simulate a failing backend.
var (
	zkSet    = zk.Set
	zkGet    = zk.Get
	zkDelete = zk.Delete
)

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

// SetSteamAPIKey stores the Steam Web API key in the system keyring (Secret Service on Linux, Credential Manager on
// Windows) under the same service and user as GetSteamAPIKey, replacing any value already there. The key is trimmed of surrounding whitespace first; the
// trimmed key is stored and returned. It is then read back, and any mismatch is ErrNotStored. Nothing is logged,
// and no error message contains the key.
func SetSteamAPIKey(key string) (string, error) {
	trimmed := strings.TrimSpace(key)
	if trimmed == "" || strings.IndexFunc(trimmed, isInvalidKeyRune) >= 0 {
		return "", ErrInvalidKey
	}
	if err := zkSet(config.AppID, config.KeyringUser, trimmed); err != nil {
		return "", fmt.Errorf("store api key: %w", err)
	}
	got, err := zkGet(config.AppID, config.KeyringUser)
	if err != nil {
		if errors.Is(err, zk.ErrNotFound) {
			return "", ErrNotStored
		}
		return "", fmt.Errorf("read back api key: %w", err)
	}
	if got != trimmed {
		return "", ErrNotStored
	}
	return trimmed, nil
}

// DeleteSteamAPIKey removes the Steam Web API key item from the system keyring. An absent item is success, so the
// call is idempotent. Any other backend error is wrapped and never includes the key.
func DeleteSteamAPIKey() error {
	if err := zkDelete(config.AppID, config.KeyringUser); err != nil && !errors.Is(err, zk.ErrNotFound) {
		return fmt.Errorf("delete api key: %w", err)
	}
	return nil
}

// isInvalidKeyRune reports whether r may not appear inside a key: any whitespace, control, or format character
// (for example U+200B zero-width space or U+FEFF byte order mark, which TrimSpace does not strip).
func isInvalidKeyRune(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
}
