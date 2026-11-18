package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/keyring"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
	"github.com/gjrud/steam-achievement-tracker/internal/usererr"
)

// steamHTTPStatusErr returns the typed error a Steam call gives for an HTTP status, from a stub server.
func steamHTTPStatusErr(t *testing.T, status int) error {
	t.Helper()
	stub, _ := newSteamStub(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "stub body", status)
	})
	client := steam.New("fixture-steam-key")
	client.BaseURL = stub.URL
	_, err := client.GetPlayerSummary(context.Background(), testSteamID)
	if err == nil {
		t.Fatalf("GetPlayerSummary with HTTP %d: err = nil", status)
	}
	return err
}

// TestUserFacingError pins the mapping from a raw error to the text the frontend gets. For each case the returned
// text must be exactly want, the raw text must be in the log when logRaw is set, and the returned text must not
// carry any of the forbidden fragments.
func TestUserFacingError(t *testing.T) {
	tmpPath := filepath.Join(t.TempDir(), "library_600x900.jpg")
	statusErr := steamHTTPStatusErr(t, http.StatusBadRequest)
	dbErr := errors.New("sql: database is closed")
	sqlErr := fmt.Errorf("sync_runs: %w", errors.New(`SQL logic error: near "insert": syntax error`))

	tests := []struct {
		name string
		err  error
		// same is the error the returned value must be (errors.Is), for sentinels that pass through unchanged.
		same error
		want string
		// logRaw is a fragment of the raw error that must reach the log.
		logRaw string
		// hidden lists fragments that must not appear in the returned text.
		hidden []string
	}{
		{name: "passes through the missing key sentinel", err: keyring.ErrMissing, same: keyring.ErrMissing,
			want: keyring.ErrMissing.Error()},
		{name: "passes through a wrapped missing key", err: fmt.Errorf("steam: %w", keyring.ErrMissing), same: keyring.ErrMissing,
			want: keyring.ErrMissing.Error()},
		{name: "passes through the sync already running sentinel", err: errSyncAlreadyInProgress, same: errSyncAlreadyInProgress,
			want: errSyncAlreadyInProgress.Error()},
		{name: "passes through the shutting down sentinel", err: errShuttingDown, same: errShuttingDown,
			want: errShuttingDown.Error()},
		{name: "passes through the no profile sentinel", err: errNoProfile, same: errNoProfile, want: msgNoProfile},
		{name: "passes through the invalid key message", err: keyring.ErrInvalidKey, same: keyring.ErrInvalidKey,
			want: keyring.ErrInvalidKey.Error()},
		{name: "wrapped no profile sentinel keeps only its sentence", err: fmt.Errorf("activeRepoAndProfile: %w", errNoProfile),
			want: msgNoProfile},
		{name: "keyring backend error", err: &keyring.BackendError{Err: errors.New("secret service locked")},
			want: msgKeyring, logRaw: "secret service locked", hidden: []string{"secret service"}},
		{name: "wrapped keyring backend error", err: fmt.Errorf("store api key: %w", &keyring.BackendError{Err: errors.New("org.freedesktop.secrets: no such object")}),
			want: msgKeyring, logRaw: "org.freedesktop.secrets", hidden: []string{"freedesktop", "org."}},
		{name: "keyring not stored", err: keyring.ErrNotStored, want: msgKeyring, logRaw: "did not keep it"},
		{name: "sqlite closed database", err: dbErr, want: msgGeneric, logRaw: "database is closed", hidden: []string{"sql", "database"}},
		{name: "wrapped sql error keeps no SQL text", err: sqlErr, want: msgGeneric, logRaw: "syntax error", hidden: []string{"SQL", "insert", "sync_runs"}},
		{name: "os path error keeps no path", err: &fs.PathError{Op: "open", Path: tmpPath, Err: fs.ErrPermission},
			want: msgGeneric, logRaw: "permission denied", hidden: []string{tmpPath, filepath.Dir(tmpPath)}},
		{name: "wrapped path error keeps no path", err: fmt.Errorf("remove cached images for appid 10: %w", &fs.PathError{Op: "remove", Path: tmpPath, Err: fs.ErrPermission}),
			want: msgGeneric, logRaw: "remove", hidden: []string{tmpPath}},
		{name: "steam http status error", err: statusErr, want: "Steam could not process the request.", logRaw: "returned 400", hidden: []string{"/IPlayerService", "/ISteamUser", "stub body", "400", "Bad Request"}},
		{name: "wrapped steam http error uses the endpoint sentence", err: fmt.Errorf("resolve profile: %w", statusErr),
			want: "Steam could not process the request.", logRaw: "resolve profile"},
		{name: "usererr with cause shows the sentence and logs the cause", err: usererr.Wrap(msgInitDatabase, errors.New("unable to open database file: /x/app.db")),
			want: msgInitDatabase, logRaw: "unable to open database file", hidden: []string{"/x/app.db"}},
		{name: "nested wrap of a raw error", err: fmt.Errorf("a: %w", fmt.Errorf("b: %w", errors.New("disk I/O error"))),
			want: msgGeneric, logRaw: "disk I/O error", hidden: []string{"disk", "I/O"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			got := userFacingError("Op", tc.err)
			if got == nil {
				t.Fatalf("userFacingError(%v) = nil, want an error", tc.err)
			}
			if got.Error() != tc.want {
				t.Fatalf("userFacingError text = %q, want %q", got.Error(), tc.want)
			}
			if tc.same != nil && got != tc.same {
				t.Fatalf("userFacingError = %v (%T), want the sentinel itself", got, got)
			}
			for _, h := range tc.hidden {
				if strings.Contains(got.Error(), h) {
					t.Fatalf("returned text %q contains %q", got.Error(), h)
				}
			}
			// The error handed to Wails carries no raw cause, sentinels included.
			if ue := usererr.Find(got); ue != nil && ue.Unwrap() != nil {
				t.Fatalf("returned error %v keeps a raw cause", got)
			}
			if tc.logRaw != "" && !strings.Contains(logs.String(), tc.logRaw) {
				t.Fatalf("log = %q, want the raw fragment %q", logs.String(), tc.logRaw)
			}
		})
	}
}

func TestUserFacingErrorNil(t *testing.T) {
	if got := userFacingError("Op", nil); got != nil {
		t.Fatalf("userFacingError(nil) = %v, want nil", got)
	}
}

// A usererr without a cause is a sentence the app wrote for a normal state or an input check, so it is not logged.
// A usererr with a cause, and any other error, is logged.
func TestUserFacingErrorLogsOnlyFailures(t *testing.T) {
	for _, err := range []error{keyring.ErrMissing, errSyncAlreadyInProgress, errShuttingDown, errNoProfile, errSteamIDMissing,
		keyring.ErrInvalidKey, fmt.Errorf("start: %w", errShuttingDown)} {
		logs := captureLog(t)
		userFacingError("Op", err)
		if logs.Len() != 0 {
			t.Fatalf("log for %v = %q, want none", err, logs.String())
		}
	}
	logs := captureLog(t)
	userFacingError("Op", usererr.Wrap(msgNoProfile, errors.New("raw cause")))
	// The log keeps the sentence and the raw cause, so neither the operation context nor the cause is lost.
	if want := "Op: " + msgNoProfile + " (cause: raw cause)"; !strings.Contains(logs.String(), want) {
		t.Fatalf("log = %q, want %q", logs.String(), want)
	}
}

// The start-sync logger follows the same rule as userFacingError.
func TestLogStartSyncErrorSkipsSentinels(t *testing.T) {
	logs := captureLog(t)
	logStartSyncError(errSyncAlreadyInProgress)
	logStartSyncError(nil)
	if logs.Len() != 0 {
		t.Fatalf("log = %q, want none", logs.String())
	}
	logStartSyncError(usererr.Wrap(msgNotReady, errors.New("database is closed")))
	if !strings.Contains(logs.String(), "database is closed") {
		t.Fatalf("log = %q, want the raw cause", logs.String())
	}
}

// The generic sentence of the App and the one steam.UserMessage gives for an unknown error must stay the same,
// because userFacingError falls back to steam.UserMessage.
func TestGenericSentenceMatchesSteam(t *testing.T) {
	if got := steam.UserMessage(errors.New("anything")); got != msgGeneric {
		t.Fatalf("steam.UserMessage(unknown) = %q, want msgGeneric %q", got, msgGeneric)
	}
}

// boundErrorMethods lists every exported App method that returns an error and is bound to the frontend. Adding such a
// method without a row here fails TestBoundMethodsUserFacingErrors, so no new method can skip the conversion.
var boundErrorMethods = []string{
	"GetAppState", "SetLibrarySettings", "RetryAPIKey", "SaveAPIKey", "ValidateProfile", "SaveProfile", "SyncNow",
	"ClearActiveUserData", "MarkGamePreviouslyCompleted", "ToggleGameMissingAchievementsInDLC", "DisableGame", "EnableGame",
}

// lifecycleErrorMethods are Wails service hooks. They return an error but are not bound to the frontend.
var lifecycleErrorMethods = []string{"ServiceStartup", "ServiceShutdown"}

// noErrorAppMethods lists exported App methods that return no error and are allowed on the App. Wails binds every
// exported method, so an entry here is a deliberate exception. None exists today.
var noErrorAppMethods = []string{}

// TestBoundMethodsUserFacingErrors checks, through the real methods, that each bound error-returning method gives a
// user-facing error when its database fails. The database is closed under the App, so every database call fails with
// "sql: database is closed". Each method must return one of its expected sentences, never that raw text, and the raw
// text must be in the log. It also checks that every exported App method is accounted for: an error-returning one in
// boundErrorMethods or lifecycleErrorMethods, and any other one in noErrorAppMethods.
func TestBoundMethodsUserFacingErrors(t *testing.T) {
	errType := reflect.TypeOf((*error)(nil)).Elem()
	typ := reflect.TypeOf(&App{})
	listed := map[string]bool{}
	for _, name := range append(append([]string{}, boundErrorMethods...), lifecycleErrorMethods...) {
		listed[name] = true
	}
	noError := map[string]bool{}
	for _, name := range noErrorAppMethods {
		noError[name] = true
	}
	methods := map[string]bool{}
	for i := 0; i < typ.NumMethod(); i++ {
		m := typ.Method(i)
		methods[m.Name] = true
		mt := m.Type
		returnsError := mt.NumOut() > 0 && mt.Out(mt.NumOut()-1) == errType
		switch {
		case listed[m.Name]:
			if !returnsError {
				t.Errorf("App method %s is listed as an error-returning method but its last result is not error", m.Name)
			}
		case noError[m.Name]:
			if returnsError {
				t.Errorf("App method %s is in noErrorAppMethods but returns an error; list it in boundErrorMethods and route it through callBound", m.Name)
			}
		case returnsError:
			t.Errorf("exported App method %s returns an error but is not in boundErrorMethods; add it and route it through callBound", m.Name)
		default:
			t.Errorf("exported App method %s returns no error and is not in noErrorAppMethods; Wails binds every exported method, so add it to noErrorAppMethods or make it return an error through callBound", m.Name)
		}
	}
	for name := range listed {
		if !methods[name] {
			t.Errorf("boundErrorMethods or lifecycleErrorMethods lists %s, which is not an exported App method", name)
		}
	}
	for name := range noError {
		if !methods[name] {
			t.Errorf("noErrorAppMethods lists %s, which is not an exported App method", name)
		}
	}

	app := newTestApp(t)
	saveTestProfile(t, app)
	if err := app.SaveAPIKey("fixture-steam-key"); err != nil {
		t.Fatal(err)
	}
	stub, _ := newSteamStub(t, forbidSteam)
	setSteamBaseURL(app, stub.URL)
	if err := app.repo.Close(); err != nil {
		t.Fatal(err)
	}

	const dbText = "database is closed"
	steamKeyRejected := "Steam rejected the Web API key. Check that the key is correct and still valid, then replace it in Settings."
	calls := map[string]struct {
		call func() error
		want string
	}{
		"GetAppState":                        {func() error { _, err := app.GetAppState(); return err }, msgGeneric},
		"SetLibrarySettings":                 {func() error { _, err := app.SetLibrarySettings(false, false); return err }, msgGeneric},
		"RetryAPIKey":                        {func() error { _, err := app.RetryAPIKey(); return err }, msgGeneric},
		"SaveAPIKey":                         {func() error { return app.SaveAPIKey("fixture-steam-key") }, ""},
		"ValidateProfile":                    {func() error { _, err := app.ValidateProfile(testSteamID); return err }, steamKeyRejected},
		"SaveProfile":                        {func() error { _, err := app.SaveProfile(ProfilePreview{SteamID64: testSteamID}); return err }, msgGeneric},
		"SyncNow":                            {func() error { _, err := app.SyncNow(); return err }, msgGeneric},
		"ClearActiveUserData":                {func() error { _, err := app.ClearActiveUserData(false, false); return err }, msgGeneric},
		"MarkGamePreviouslyCompleted":        {func() error { _, err := app.MarkGamePreviouslyCompleted(10, 0); return err }, msgGeneric},
		"ToggleGameMissingAchievementsInDLC": {func() error { _, err := app.ToggleGameMissingAchievementsInDLC(10, 0); return err }, msgGeneric},
		"DisableGame":                        {func() error { _, err := app.DisableGame(10, 0); return err }, msgGeneric},
		"EnableGame":                         {func() error { _, err := app.EnableGame(10, 0); return err }, msgGeneric},
	}
	if len(calls) != len(boundErrorMethods) {
		t.Fatalf("calls table has %d rows, boundErrorMethods has %d", len(calls), len(boundErrorMethods))
	}
	for _, name := range boundErrorMethods {
		tc, ok := calls[name]
		if !ok {
			t.Fatalf("no call row for bound method %s", name)
		}
		t.Run(name, func(t *testing.T) {
			logs := captureLog(t)
			err := tc.call()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("%s err = %v, want nil", name, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s err = nil, want %q", name, tc.want)
			}
			if err.Error() != tc.want {
				t.Fatalf("%s err = %q, want %q", name, err.Error(), tc.want)
			}
			if strings.Contains(err.Error(), dbText) || strings.Contains(err.Error(), app.paths.DataDir) {
				t.Fatalf("%s err = %q carries database or path text", name, err.Error())
			}
			if tc.want == msgGeneric && !strings.Contains(logs.String(), dbText) {
				t.Fatalf("%s log = %q, want the raw database error", name, logs.String())
			}
		})
	}
}
