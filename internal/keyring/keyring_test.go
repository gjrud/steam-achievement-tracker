package keyring

import (
	"errors"
	"testing"

	zk "github.com/zalando/go-keyring"

	"github.com/gjrud/steam-achievement-tracker/internal/config"
)

func TestGetSteamAPIKeyReturnsStoredSecret(t *testing.T) {
	zk.MockInit()

	if err := zk.Set(config.AppID, config.KeyringUser, "abc123"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := GetSteamAPIKey()
	if err != nil {
		t.Fatalf("GetSteamAPIKey() error = %v, want nil", err)
	}
	if got != "abc123" {
		t.Fatalf("GetSteamAPIKey() = %q, want %q", got, "abc123")
	}
}

func TestGetSteamAPIKeyUsesExpectedServiceAndUser(t *testing.T) {
	zk.MockInit()

	if config.AppID != "steam-achievement-tracker" {
		t.Fatalf("config.AppID = %q, want %q", config.AppID, "steam-achievement-tracker")
	}
	if config.KeyringUser != "steam-web-api-key" {
		t.Fatalf("config.KeyringUser = %q, want %q", config.KeyringUser, "steam-web-api-key")
	}

	// Secret stored under the wrong service or user must not be returned.
	if err := zk.Set("other-service", config.KeyringUser, "wrong-service"); err != nil {
		t.Fatalf("Set other service: %v", err)
	}
	if err := zk.Set(config.AppID, "other-user", "wrong-user"); err != nil {
		t.Fatalf("Set other user: %v", err)
	}

	_, err := GetSteamAPIKey()
	if !errors.Is(err, ErrMissing) {
		t.Fatalf("GetSteamAPIKey() error = %v, want ErrMissing", err)
	}

	// Correct service and user is found.
	if err := zk.Set(config.AppID, config.KeyringUser, "right"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := GetSteamAPIKey()
	if err != nil {
		t.Fatalf("GetSteamAPIKey() error = %v, want nil", err)
	}
	if got != "right" {
		t.Fatalf("GetSteamAPIKey() = %q, want %q", got, "right")
	}
}

func TestGetSteamAPIKeyNotFoundMapsToErrMissing(t *testing.T) {
	zk.MockInit()

	got, err := GetSteamAPIKey()
	if !errors.Is(err, ErrMissing) {
		t.Fatalf("GetSteamAPIKey() error = %v, want ErrMissing", err)
	}
	if got != "" {
		t.Fatalf("GetSteamAPIKey() = %q, want empty", got)
	}
}

func TestGetSteamAPIKeyEmptySecretMapsToErrMissing(t *testing.T) {
	zk.MockInit()

	if err := zk.Set(config.AppID, config.KeyringUser, ""); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := GetSteamAPIKey()
	if !errors.Is(err, ErrMissing) {
		t.Fatalf("GetSteamAPIKey() error = %v, want ErrMissing", err)
	}
	if got != "" {
		t.Fatalf("GetSteamAPIKey() = %q, want empty", got)
	}
}

func TestGetSteamAPIKeyPassesThroughBackendErrors(t *testing.T) {
	backendErr := errors.New("secret service unavailable")
	zk.MockInitWithError(backendErr)

	got, err := GetSteamAPIKey()
	if !errors.Is(err, backendErr) {
		t.Fatalf("GetSteamAPIKey() error = %v, want backend error", err)
	}
	if errors.Is(err, ErrMissing) {
		t.Fatalf("backend error must not map to ErrMissing")
	}
	if got != "" {
		t.Fatalf("GetSteamAPIKey() = %q, want empty", got)
	}
}
