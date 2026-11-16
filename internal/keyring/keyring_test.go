package keyring

import (
	"errors"
	"strings"
	"testing"

	zk "github.com/zalando/go-keyring"

	"github.com/gjrud/steam-achievement-tracker/internal/config"
)

// The zkSet/zkGet seams are package vars: tests that swap them restore the originals with t.Cleanup and must not use
// t.Parallel.
func swapZKSet(t *testing.T, fn func(service, user, pass string) error) {
	t.Helper()
	prev := zkSet
	zkSet = fn
	t.Cleanup(func() { zkSet = prev })
}

func swapZKGet(t *testing.T, fn func(service, user string) (string, error)) {
	t.Helper()
	prev := zkGet
	zkGet = fn
	t.Cleanup(func() { zkGet = prev })
}

func swapZKDelete(t *testing.T, fn func(service, user string) error) {
	t.Helper()
	prev := zkDelete
	zkDelete = fn
	t.Cleanup(func() { zkDelete = prev })
}

func TestDeleteSteamAPIKeyRemovesStoredKey(t *testing.T) {
	zk.MockInit()
	if err := zk.Set(config.AppID, config.KeyringUser, "abc123"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if err := DeleteSteamAPIKey(); err != nil {
		t.Fatalf("DeleteSteamAPIKey() error = %v, want nil", err)
	}
	got, err := GetSteamAPIKey()
	if !errors.Is(err, ErrMissing) {
		t.Fatalf("GetSteamAPIKey() after delete = %q, %v; want ErrMissing", got, err)
	}
}

func TestDeleteSteamAPIKeyWhenAbsentIsSuccess(t *testing.T) {
	zk.MockInit()

	if err := DeleteSteamAPIKey(); err != nil {
		t.Fatalf("DeleteSteamAPIKey() with no item error = %v, want nil", err)
	}
	if err := DeleteSteamAPIKey(); err != nil {
		t.Fatalf("second DeleteSteamAPIKey() error = %v, want nil", err)
	}
}

func TestDeleteSteamAPIKeyPassesThroughBackendErrors(t *testing.T) {
	backendErr := errors.New("secret service unavailable")
	swapZKDelete(t, func(service, user string) error { return backendErr })

	err := DeleteSteamAPIKey()
	if !errors.Is(err, backendErr) {
		t.Fatalf("DeleteSteamAPIKey() error = %v, want wrapped backend error", err)
	}
	if errors.Is(err, ErrMissing) {
		t.Fatalf("backend error must not map to ErrMissing: %v", err)
	}
}

func TestDeleteSteamAPIKeyErrorNeverContainsKey(t *testing.T) {
	const sentinel = "SENTINEL"
	zk.MockInit()
	if err := zk.Set(config.AppID, config.KeyringUser, sentinel+"-key"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	swapZKDelete(t, func(service, user string) error { return errors.New("delete refused") })

	err := DeleteSteamAPIKey()
	if err == nil {
		t.Fatal("DeleteSteamAPIKey() error = nil, want an error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error text %q contains the key", err.Error())
	}
}

func TestSetSteamAPIKeyRoundTripStoresTrimmedKey(t *testing.T) {
	zk.MockInit()

	got, err := SetSteamAPIKey("  abc123\t\n")
	if err != nil {
		t.Fatalf("SetSteamAPIKey() error = %v, want nil", err)
	}
	if got != "abc123" {
		t.Fatalf("SetSteamAPIKey() = %q, want trimmed %q", got, "abc123")
	}
	stored, err := zk.Get(config.AppID, config.KeyringUser)
	if err != nil || stored != "abc123" {
		t.Fatalf("stored value = %q, %v; want %q", stored, err, "abc123")
	}
	read, err := GetSteamAPIKey()
	if err != nil || read != "abc123" {
		t.Fatalf("GetSteamAPIKey() = %q, %v; want the saved key", read, err)
	}
}

func TestSetSteamAPIKeyOverwritesExistingValue(t *testing.T) {
	zk.MockInit()
	if err := zk.Set(config.AppID, config.KeyringUser, "old-key"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if _, err := SetSteamAPIKey("new-key"); err != nil {
		t.Fatalf("SetSteamAPIKey() error = %v, want nil", err)
	}
	stored, err := zk.Get(config.AppID, config.KeyringUser)
	if err != nil || stored != "new-key" {
		t.Fatalf("stored value = %q, %v; want %q", stored, err, "new-key")
	}
}

func TestSetSteamAPIKeyRejectsInvalidInputWithoutStoring(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"spaces only", "   "},
		{"internal space", "abc def"},
		{"internal tab", "abc\tdef"},
		{"internal no-break space", "abc\u00a0def"},
		{"control character", "abc\x01def"},
		{"internal newline", "abc\ndef"},
		{"internal zero-width space", "abc\u200bdef"},
		{"leading byte order mark", "\ufeffabc123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zk.MockInit()
			if err := zk.Set(config.AppID, config.KeyringUser, "keep-me"); err != nil {
				t.Fatalf("Set: %v", err)
			}

			got, err := SetSteamAPIKey(tt.input)
			if !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("SetSteamAPIKey(%q) error = %v, want ErrInvalidKey", tt.input, err)
			}
			if got != "" {
				t.Fatalf("SetSteamAPIKey(%q) = %q, want empty", tt.input, got)
			}
			stored, err := zk.Get(config.AppID, config.KeyringUser)
			if err != nil || stored != "keep-me" {
				t.Fatalf("stored value = %q, %v; want the earlier value untouched", stored, err)
			}
		})
	}
}

func TestSetSteamAPIKeyPassesThroughBackendErrors(t *testing.T) {
	backendErr := errors.New("secret service unavailable")
	zk.MockInitWithError(backendErr)

	got, err := SetSteamAPIKey("abc123")
	if !errors.Is(err, backendErr) {
		t.Fatalf("SetSteamAPIKey() error = %v, want wrapped backend error", err)
	}
	if errors.Is(err, ErrNotStored) || errors.Is(err, ErrInvalidKey) {
		t.Fatalf("backend error must not map to ErrNotStored or ErrInvalidKey: %v", err)
	}
	if got != "" {
		t.Fatalf("SetSteamAPIKey() = %q, want empty", got)
	}
}

func TestSetSteamAPIKeyReadBackErrorSurfacesItself(t *testing.T) {
	zk.MockInit()
	readErr := errors.New("read back failed")
	swapZKGet(t, func(service, user string) (string, error) { return "", readErr })

	_, err := SetSteamAPIKey("abc123")
	if !errors.Is(err, readErr) {
		t.Fatalf("SetSteamAPIKey() error = %v, want wrapped read-back error", err)
	}
	if errors.Is(err, ErrNotStored) {
		t.Fatalf("read-back error must not map to ErrNotStored: %v", err)
	}
}

func TestSetSteamAPIKeyNotStoredWhenReadBackFindsNothing(t *testing.T) {
	zk.MockInit()
	// The write is silently dropped (a dismissed create prompt): nothing is stored.
	swapZKSet(t, func(service, user, pass string) error { return nil })

	got, err := SetSteamAPIKey("abc123")
	if !errors.Is(err, ErrNotStored) {
		t.Fatalf("SetSteamAPIKey() error = %v, want ErrNotStored", err)
	}
	if got != "" {
		t.Fatalf("SetSteamAPIKey() = %q, want empty", got)
	}
}

func TestSetSteamAPIKeyNotStoredWhenReadBackDiffers(t *testing.T) {
	zk.MockInit()
	if err := zk.Set(config.AppID, config.KeyringUser, "other-key"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// The write is silently dropped, so the pre-seeded value is what the read-back sees.
	swapZKSet(t, func(service, user, pass string) error { return nil })

	got, err := SetSteamAPIKey("abc123")
	if !errors.Is(err, ErrNotStored) {
		t.Fatalf("SetSteamAPIKey() error = %v, want ErrNotStored", err)
	}
	if got != "" {
		t.Fatalf("SetSteamAPIKey() = %q, want empty", got)
	}
}

// Every error path must keep the key out of the error text. Each path gets its own input so that it is reached:
// padding is trimmed before the store, so the assertion uses the bare sentinel. The empty input has nothing to
// assert.
func TestSetSteamAPIKeyErrorsNeverContainTheKey(t *testing.T) {
	const sentinel = "SENTINEL"
	backendErr := errors.New("boom")
	tests := []struct {
		name  string
		input string
		setup func(t *testing.T)
	}{
		{
			name:  "internal whitespace",
			input: "SENTINEL key",
			setup: func(t *testing.T) { zk.MockInit() },
		},
		{
			name:  "control character",
			input: "SENTINEL\x01key",
			setup: func(t *testing.T) { zk.MockInit() },
		},
		{
			name:  "format character",
			input: "SENTINEL\u200bkey",
			setup: func(t *testing.T) { zk.MockInit() },
		},
		{
			name:  "set error",
			input: "  SENTINELkey  ",
			setup: func(t *testing.T) { zk.MockInitWithError(backendErr) },
		},
		{
			name:  "read-back error",
			input: "  SENTINELkey  ",
			setup: func(t *testing.T) {
				zk.MockInit()
				swapZKGet(t, func(service, user string) (string, error) { return "", backendErr })
			},
		},
		{
			name:  "mismatch",
			input: "  SENTINELkey  ",
			setup: func(t *testing.T) {
				zk.MockInit()
				swapZKSet(t, func(service, user, pass string) error { return nil })
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			_, err := SetSteamAPIKey(tt.input)
			if err == nil {
				t.Fatal("SetSteamAPIKey() error = nil, want an error")
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Fatalf("error text %q contains the key", err.Error())
			}
		})
	}
}

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
