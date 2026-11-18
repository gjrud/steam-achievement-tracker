package steam

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/usererr"
)

// userKeySentinel stands in for a Steam Web API key. It must never appear in a user message.
const userKeySentinel = "SENTINEL-KEY-0123456789abcdef"

// forbiddenUserText lists substrings that must never reach a user message: endpoint paths, status lines, HTML,
// eresult and decoder wording, URLs and the key.
var forbiddenUserText = []string{
	"Steam API /", "<html", "HTTP/", "://", "/ISteam", "/IPlayer", "eresult", "x-eresult", "decode",
	"Forbidden", "Retrying", "Access is denied", "key=", userKeySentinel,
}

func requireUserSafe(t *testing.T, msg string) {
	t.Helper()
	for _, bad := range forbiddenUserText {
		if strings.Contains(msg, bad) {
			t.Fatalf("user message %q contains %q", msg, bad)
		}
	}
}

// forbiddenHTML is the 403 body Steam's edge returns. It echoes the key parameter, so the body carries the sentinel.
const forbiddenHTML = `<html><head><title>Forbidden</title></head><body><h1>Forbidden</h1>Access is denied. Retrying will not help. Please verify your <pre>key=` + userKeySentinel + `</pre> parameter.</body></html>`

// Endpoint paths as the client sends them (see client.go).
const (
	pathResolveVanity    = "/ISteamUser/ResolveVanityURL/v1/"
	pathPlayerSummaries  = "/ISteamUser/GetPlayerSummaries/v2/"
	pathOwnedGames       = "/IPlayerService/GetOwnedGames/v0001/"
	pathUserAchievements = "/IPlayerService/GetUserAchievements/v1/"
	pathLegacyPlayer     = "/ISteamUserStats/GetPlayerAchievements/v1/"
	pathGameAchievements = "/IPlayerService/GetGameAchievements/v1/"
	pathSchemaForGame    = "/ISteamUserStats/GetSchemaForGame/v2/"
	pathGlobalPercent    = "/ISteamUserStats/GetGlobalAchievementPercentagesForApp/v0002/"
	pathUnknown          = "/ISteamUser/SomethingNew/v1/"
)

func pathStatusErr(path string, code int, body string) *HTTPStatusError {
	e := &HTTPStatusError{Path: path, Code: code, Status: fmt.Sprintf("%d %s", code, http.StatusText(code))}
	if body != "" {
		e.body = body
		e.hasBody = true
	}
	return e
}

// realURLError returns a *url.Error from a real request to a closed server, so it carries the same shape as a
// transport failure from the client.
func realURLError(t *testing.T) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := server.URL
	server.Close()
	_, err := http.Get(addr + "/ISteamUser/GetPlayerSummaries/v2/?key=" + userKeySentinel)
	if err == nil {
		t.Fatal("request to closed server succeeded")
	}
	return err
}

func TestUserMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", err: nil, want: ""},

		// HTTP statuses, by code, on an endpoint that uses the key for its answer.
		{name: "http 400", err: pathStatusErr(pathPlayerSummaries, 400, ""), want: userMsgBadRequest},
		{name: "http 404", err: pathStatusErr(pathPlayerSummaries, 404, "try later"), want: userMsgNoData},
		{name: "http 429", err: pathStatusErr(pathPlayerSummaries, 429, ""), want: userMsgRateLimited},
		{name: "http 500", err: pathStatusErr(pathPlayerSummaries, 500, ""), want: userMsgUnavailable},
		{name: "http 502", err: pathStatusErr(pathPlayerSummaries, 502, ""), want: userMsgUnavailable},
		{name: "http 503", err: pathStatusErr(pathPlayerSummaries, 503, ""), want: userMsgUnavailable},
		{name: "http 504", err: pathStatusErr(pathPlayerSummaries, 504, ""), want: userMsgUnavailable},
		{name: "http 418 other", err: pathStatusErr(pathPlayerSummaries, 418, ""), want: "Steam returned an unexpected response."},
		{name: "http 302 other", err: pathStatusErr(pathPlayerSummaries, 302, ""), want: "Steam returned an unexpected response."},

		// 401 and 403 by endpoint (see the per-path table below for the full set).
		{name: "403 profile lookup is key rejected", err: pathStatusErr(pathPlayerSummaries, 403, forbiddenHTML), want: userMsgKeyRejected},
		{name: "403 key-check body on achievements is key rejected", err: pathStatusErr(pathUserAchievements, 403, forbiddenHTML), want: userMsgKeyRejected},

		// Typed errors wrapped by a caller keep only the friendly sentence.
		{name: "wrapped http 404", err: fmt.Errorf("achievement schema: %w", pathStatusErr(pathSchemaForGame, 404, "")), want: userMsgNoData},
		{name: "doubly wrapped http 403", err: fmt.Errorf("user achievements: %w", fmt.Errorf("achievement schema: %w", pathStatusErr(pathUserAchievements, 403, forbiddenHTML))), want: userMsgKeyRejected},
		{name: "eresult", err: &EResultError{Path: pathGameAchievements, Code: 2}, want: userMsgEResult},
		{name: "wrapped eresult", err: fmt.Errorf("achievement schema: %w", &EResultError{Path: "/x", Code: 15}), want: userMsgEResult},

		// Transport failures.
		{name: "real url.Error", err: realURLError(t), want: userMsgUnreachable},
		{name: "url.Error with key in URL", err: &url.Error{Op: "Get", URL: "https://api.steampowered.com/x?key=" + userKeySentinel, Err: errors.New("connection reset")}, want: userMsgUnreachable},
		{name: "wrapped url.Error", err: fmt.Errorf("achievement schema: %w", &url.Error{Op: "Get", URL: "https://api.steampowered.com/", Err: errors.New("EOF")}), want: userMsgUnreachable},
		{name: "context canceled", err: context.Canceled, want: userMsgCanceled},
		{name: "wrapped context canceled", err: fmt.Errorf("achievement schema: %w", context.Canceled), want: userMsgCanceled},
		{name: "url.Error wrapping context canceled", err: &url.Error{Op: "Get", URL: "https://api.steampowered.com/?key=" + userKeySentinel, Err: context.Canceled}, want: userMsgCanceled},
		{name: "deadline exceeded", err: context.DeadlineExceeded, want: userMsgUnreachable},
		{name: "net.OpError", err: &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}, want: userMsgUnreachable},
		{name: "body read failure", err: &bodyReadError{err: io.ErrUnexpectedEOF}, want: userMsgUnreachable},
		{name: "wrapped body read failure", err: fmt.Errorf("user achievements: %w", &bodyReadError{err: io.ErrUnexpectedEOF}), want: userMsgUnreachable},

		// Responses the app cannot read.
		{name: "decode error", err: decodeJSONBody(pathPlayerSummaries, []byte("{"), &struct{}{}), want: userMsgUnreadable},
		{name: "decode type error", err: decodeJSONBody(pathOwnedGames, []byte(`{"response":{"game_count":"x"}}`), &ownedGamesResponse{}), want: userMsgUnreadable},
		{name: "wrapped decode error", err: fmt.Errorf("global percentages: %w", decodeJSONBody(pathGlobalPercent, []byte("{"), &struct{}{})), want: userMsgUnreadable},
		{name: "invalid x-eresult", err: badEResultErr(t, "abc"), want: userMsgUnreadable},
		{name: "wrapped invalid x-eresult", err: fmt.Errorf("achievement schema: %w", badEResultErr(t, "abc")), want: userMsgUnreadable},

		// User-facing text built by the app or already redacted passes through, without any wrapping prefix.
		{name: "profile input required", err: usererr.New("Enter a Steam profile URL, SteamID64 or custom name."), want: "Enter a Steam profile URL, SteamID64 or custom name."},
		{name: "profile not found", err: usererr.New("No Steam profile was found for that account."), want: "No Steam profile was found for that account."},
		{name: "vanity no match", err: errNoVanityMatch, want: "No Steam profile matches that custom name."},
		{name: "api key as input", err: errAPIKeyAsProfileInput, want: errAPIKeyAsProfileInput.Error()},
		{name: "profile url shape", err: usererr.New("Enter a profile link like steamcommunity.com/profiles/<number> or steamcommunity.com/id/<name>."), want: "Enter a profile link like steamcommunity.com/profiles/<number> or steamcommunity.com/id/<name>."},
		{name: "Steam did not return achievements for this game. Try again later.", err: usererr.New("Steam did not return achievements for this game. Try again later."), want: "Steam did not return achievements for this game. Try again later."},
		{name: "owned games unavailable", err: usererr.New("Steam did not return your game list. Your profile's game details may be private."), want: "Steam did not return your game list. Your profile's game details may be private."},
		{name: "steam message text", err: usererr.New("The specified profile could not be found."), want: "The specified profile could not be found."},
		// A user error keeps its sentence even when its cause is a typed Steam error (the cause is for the log only).
		{name: "user error with http cause keeps its sentence", err: usererr.Wrap("Steam did not provide achievements for this game.", pathStatusErr(pathUserAchievements, 403, "")), want: "Steam did not provide achievements for this game."},
		{name: "wrapped user error drops prefix", err: fmt.Errorf("achievement schema: %w", usererr.New("Steam returned an incomplete game list. Try again later.")), want: "Steam returned an incomplete game list. Try again later."},

		// Anything else is generic: no raw text, whatever it says.
		{name: "untyped endpoint text", err: errors.New("Steam API /ISteamUser/ResolveVanityURL/v1/ returned 403 Forbidden"), want: userMsgGeneric},
		{name: "untyped html text", err: errors.New("<html><body>Forbidden</body></html>"), want: userMsgGeneric},
		{name: "untyped status line", err: errors.New("HTTP/2.0 500 Internal Server Error"), want: userMsgGeneric},
		{name: "untyped url", err: errors.New("get https://api.steampowered.com/x?key=" + userKeySentinel), want: userMsgGeneric},
		{name: "wrapped untyped error", err: fmt.Errorf("finish: %w", errors.New("database is locked")), want: userMsgGeneric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UserMessage(tc.err)
			if got != tc.want {
				t.Fatalf("UserMessage(%v) = %q, want %q", tc.err, got, tc.want)
			}
			if tc.err != nil {
				requireUserSafe(t, got)
			}
		})
	}
}

// TestUserMessageByEndpoint pins the 401 and 403 sentence per endpoint. A 403 on a per-game endpoint means the
// profile or the game did not share the data, not a bad key, so the sentence must not blame the key there.
func TestUserMessageByEndpoint(t *testing.T) {
	cases := []struct {
		name string
		path string
		code int
		want string
	}{
		{name: "resolve vanity 401", path: pathResolveVanity, code: 401, want: userMsgKeyRejected},
		{name: "resolve vanity 403", path: pathResolveVanity, code: 403, want: userMsgKeyRejected},
		{name: "player summaries 401", path: pathPlayerSummaries, code: 401, want: userMsgKeyRejected},
		{name: "player summaries 403", path: pathPlayerSummaries, code: 403, want: userMsgKeyRejected},
		{name: "owned games 401", path: pathOwnedGames, code: 401, want: userMsgKeyRejected},
		{name: "owned games 403", path: pathOwnedGames, code: 403, want: userMsgKeyRejected},
		{name: "user achievements 401", path: pathUserAchievements, code: 401, want: userMsgKeyRejected},
		{name: "user achievements 403", path: pathUserAchievements, code: 403, want: userMsgPrivate},
		{name: "legacy player achievements 401", path: pathLegacyPlayer, code: 401, want: userMsgKeyRejected},
		{name: "legacy player achievements 403", path: pathLegacyPlayer, code: 403, want: userMsgPrivate},
		{name: "game achievements 401", path: pathGameAchievements, code: 401, want: userMsgKeyRejected},
		{name: "game achievements 403", path: pathGameAchievements, code: 403, want: userMsgRefused},
		{name: "schema for game 401", path: pathSchemaForGame, code: 401, want: userMsgKeyRejected},
		{name: "schema for game 403", path: pathSchemaForGame, code: 403, want: userMsgRefused},
		{name: "global percentages 401", path: pathGlobalPercent, code: 401, want: userMsgRefused},
		{name: "global percentages 403", path: pathGlobalPercent, code: 403, want: userMsgRefused},
		{name: "unknown path 401", path: pathUnknown, code: 401, want: userMsgKeyRejected},
		{name: "unknown path 403", path: pathUnknown, code: 403, want: userMsgRefused},
		{name: "empty path 403", path: "", code: 403, want: userMsgRefused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UserMessage(pathStatusErr(tc.path, tc.code, ""))
			if got != tc.want {
				t.Fatalf("UserMessage(%s %d) = %q, want %q", tc.path, tc.code, got, tc.want)
			}
			requireUserSafe(t, got)
		})
	}
}

// The key-check body wins on any endpoint, but only for a 403 with both markers. A 403 with some other body keeps
// the endpoint's own sentence.
func TestUserMessageKeyCheckBody(t *testing.T) {
	cases := []struct {
		name string
		err  *HTTPStatusError
		want string
	}{
		{name: "key-check body on owned games", err: pathStatusErr(pathOwnedGames, 403, forbiddenHTML), want: userMsgKeyRejected},
		{name: "key-check body on game achievements", err: pathStatusErr(pathGameAchievements, 403, forbiddenHTML), want: userMsgKeyRejected},
		// 403 with the key-check body is key rejected by the body rule, even on an endpoint that never means a key problem.
		{name: "403 key-check body on global percentages is key rejected", err: pathStatusErr(pathGlobalPercent, 403, forbiddenHTML), want: userMsgKeyRejected},
		{name: "verify your without key= is not key check", err: pathStatusErr(pathUserAchievements, 403, "Please verify your account."), want: userMsgPrivate},
		{name: "key= without verify your is not key check", err: pathStatusErr(pathUserAchievements, 403, "bad key=REDACTED"), want: userMsgPrivate},
		// 401 with the key-check body is not the body rule (it is 403 only). The endpoint rule decides: on game
		// achievements a non-403 means a key problem.
		{name: "401 key-check body on game achievements is key rejected by endpoint", err: pathStatusErr(pathGameAchievements, 401, forbiddenHTML), want: userMsgKeyRejected},
		// Same body on global percentages: the endpoint rule says refused, so only a 403 can use the body rule.
		{name: "401 key-check body on global percentages is refused, not body rule", err: pathStatusErr(pathGlobalPercent, 401, forbiddenHTML), want: userMsgRefused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := UserMessage(tc.err)
			if got != tc.want {
				t.Fatalf("UserMessage = %q, want %q", got, tc.want)
			}
			requireUserSafe(t, got)
		})
	}
}

// badEResultErr returns the error parseEResult gives for a header that is not an integer.
func badEResultErr(t *testing.T, header string) error {
	t.Helper()
	_, err := parseEResult(pathGameAchievements, header)
	if err == nil {
		t.Fatalf("parseEResult(%q) error = nil", header)
	}
	return err
}

// The 403 example from a real request: the client's error is mapped with the key-rejected sentence, and neither the
// HTML body nor the key reaches the message, though the raw error keeps the body text for the log.
func TestUserMessageRealForbiddenResponse(t *testing.T) {
	client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(forbiddenHTML))
	})
	defer closeServer()
	client.APIKey = userKeySentinel

	_, err := client.ResolveProfileInput(context.Background(), "vanity-name")
	if err == nil {
		t.Fatal("err = nil, want 403")
	}
	if !strings.Contains(err.Error(), "returned 403 Forbidden") {
		t.Fatalf("raw error = %q, want the status text kept for the log", err.Error())
	}
	got := UserMessage(err)
	if got != userMsgKeyRejected {
		t.Fatalf("UserMessage = %q, want %q", got, userMsgKeyRejected)
	}
	requireUserSafe(t, got)
}

// A real client call against a closed server gives a *url.Error, mapped to the connection sentence.
func TestUserMessageRealTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	client := &Client{APIKey: userKeySentinel, BaseURL: server.URL, HTTPClient: server.Client()}
	client.SetRetryBackoff(func(int) time.Duration { return 0 })
	server.Close()

	_, err := client.GetPlayerSummary(context.Background(), testSteamID)
	if err == nil {
		t.Fatal("err = nil, want transport failure")
	}
	got := UserMessage(err)
	if got != userMsgUnreachable {
		t.Fatalf("UserMessage = %q, want %q", got, userMsgUnreachable)
	}
	requireUserSafe(t, got)
}

// Every sentence the mapper can return is free of raw Steam output, so no branch can leak an endpoint, a body or a key.
func TestUserMessageMappedSentencesAreSafe(t *testing.T) {
	for _, msg := range []string{
		userMsgKeyRejected, userMsgPrivate, userMsgRefused, userMsgBadRequest, userMsgNoData, userMsgRateLimited,
		userMsgUnavailable, userMsgEResult, userMsgUnreachable, userMsgCanceled, userMsgUnreadable, userMsgGeneric,
		httpStatusMessage(pathStatusErr(pathOwnedGames, 418, "")),
	} {
		requireUserSafe(t, msg)
	}
}

// TestUserMessageSentencesListsEveryConstant reads usermessage.go and checks that UserMessageSentences holds the value
// of every userMsg constant, so a new sentence cannot be left out of the list that the db tests pin stored text to.
func TestUserMessageSentencesListsEveryConstant(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "usermessage.go", nil, 0)
	if err != nil {
		t.Fatalf("parse usermessage.go: %v", err)
	}
	declared := map[string]bool{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "userMsg") {
					continue
				}
				if i >= len(vs.Values) {
					t.Fatalf("constant %s has no value", name.Name)
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok {
					t.Fatalf("constant %s is not a string literal", name.Name)
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("constant %s: %v", name.Name, err)
				}
				declared[text] = true
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("found no userMsg constants in usermessage.go")
	}
	listed := map[string]bool{}
	for _, s := range UserMessageSentences() {
		listed[s] = true
	}
	for s := range declared {
		if !listed[s] {
			t.Errorf("userMsg sentence %q is not in UserMessageSentences", s)
		}
	}
	for s := range listed {
		if !declared[s] {
			t.Errorf("UserMessageSentences lists %q, which is not a userMsg constant", s)
		}
	}
}
