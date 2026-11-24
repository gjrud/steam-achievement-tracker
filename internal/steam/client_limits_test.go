package steam

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

const playerSummariesPath = "/ISteamUser/GetPlayerSummaries/v2/"

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "absent", value: "", want: 0},
		{name: "zero seconds", value: "0", want: 0},
		{name: "seconds", value: "2", want: 2 * time.Second},
		{name: "seconds with spaces", value: " 5 ", want: 5 * time.Second},
		{name: "seconds at cap", value: "60", want: 60 * time.Second},
		{name: "seconds over cap", value: "3600", want: 60 * time.Second},
		{name: "seconds overflowing int64", value: "99999999999999999999", want: 60 * time.Second},
		{name: "negative seconds", value: "-1", want: 0},
		{name: "fractional seconds", value: "1.5", want: 0},
		{name: "garbage", value: "soon", want: 0},
		{name: "http-date in 10 seconds", value: now.Add(10 * time.Second).Format(http.TimeFormat), want: 10 * time.Second},
		{name: "http-date in the past", value: now.Add(-time.Minute).Format(http.TimeFormat), want: 0},
		{name: "http-date far ahead is capped", value: now.Add(time.Hour).Format(http.TimeFormat), want: 60 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseRetryAfter(tc.value, now); got != tc.want {
				t.Fatalf("parseRetryAfter(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// retryAfterClient answers the first len(headers) requests with status and the matching Retry-After header
// ("" sends none), then a one-player body. Waits go to the recorded sleeps list, not to a timer.
func retryAfterClient(t *testing.T, status int, headers ...string) (*Client, *atomic.Int32, *[]time.Duration, func()) {
	t.Helper()
	var attempts atomic.Int32
	client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != playerSummariesPath {
			http.NotFound(w, r)
			return
		}
		n := int(attempts.Add(1))
		if n <= len(headers) {
			if headers[n-1] != "" {
				w.Header().Set("Retry-After", headers[n-1])
			}
			http.Error(w, "slow down", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":{"players":[{"steamid":"76561198000000001","personaname":"Gabe"}]}}`))
	})
	var sleeps []time.Duration
	client.retrySleep = func(_ context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return nil
	}
	return client, &attempts, &sleeps, closeServer
}

func TestRetryAfterOn429ReplacesBackoff(t *testing.T) {
	client, attempts, sleeps, closeServer := retryAfterClient(t, http.StatusTooManyRequests, "2", "2")
	defer closeServer()
	client.SetRetryBackoff(func(int) time.Duration {
		t.Error("backoff used although Retry-After was sent")
		return 0
	})

	if _, err := client.GetPlayerSummary(context.Background(), testSteamID); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 || fmt.Sprint(*sleeps) != "[2s 2s]" {
		t.Fatalf("attempts=%d sleeps=%v, want 3 attempts and waits [2s 2s]", attempts.Load(), *sleeps)
	}
}

func TestRetryAfterOn429IsCapped(t *testing.T) {
	client, _, sleeps, closeServer := retryAfterClient(t, http.StatusTooManyRequests, "3600")
	defer closeServer()

	if _, err := client.GetPlayerSummary(context.Background(), testSteamID); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(*sleeps) != "[1m0s]" {
		t.Fatalf("sleeps = %v, want one capped wait of 1m0s", *sleeps)
	}
}

// TestRetryAfterTotalBudgetPerCall: the 60 s Retry-After budget belongs to one call. Three 429s asking for 60 s
// wait 60 s in all, then the backoff (attempt 2 = 500ms, attempt 3 = 750ms with the hook below). A partly spent
// budget cuts the next wait to what is left.
func TestRetryAfterTotalBudgetPerCall(t *testing.T) {
	tests := []struct {
		name    string
		headers []string
		want    string
	}{
		{name: "three full waits exhaust the budget", headers: []string{"60", "60", "60"}, want: "[1m0s 500ms 750ms]"},
		{name: "second wait is cut to the rest", headers: []string{"40", "40", "40"}, want: "[40s 20s 750ms]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, attempts, sleeps, closeServer := retryAfterClient(t, http.StatusTooManyRequests, tc.headers...)
			defer closeServer()
			client.SetRetryBackoff(func(attempt int) time.Duration { return time.Duration(attempt) * 250 * time.Millisecond })

			if _, err := client.GetPlayerSummary(context.Background(), testSteamID); err != nil {
				t.Fatal(err)
			}
			if attempts.Load() != 4 || fmt.Sprint(*sleeps) != tc.want {
				t.Fatalf("attempts=%d sleeps=%v, want 4 attempts and waits %s", attempts.Load(), *sleeps, tc.want)
			}
		})
	}
}

func TestRetryAfterAbsentOrInvalidUsesBackoff(t *testing.T) {
	for _, header := range []string{"", "soon", "0"} {
		t.Run("header "+header, func(t *testing.T) {
			client, _, sleeps, closeServer := retryAfterClient(t, http.StatusTooManyRequests, header)
			defer closeServer()
			client.SetRetryBackoff(func(attempt int) time.Duration { return time.Duration(attempt) * 250 * time.Millisecond })

			if _, err := client.GetPlayerSummary(context.Background(), testSteamID); err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(*sleeps) != "[250ms]" {
				t.Fatalf("sleeps = %v, want the backoff for attempt 1 (250ms)", *sleeps)
			}
		})
	}
}

func TestRetryAfterIgnoredForNon429(t *testing.T) {
	client, _, sleeps, closeServer := retryAfterClient(t, http.StatusServiceUnavailable, "30")
	defer closeServer()
	client.SetRetryBackoff(func(int) time.Duration { return 5 * time.Millisecond })

	if _, err := client.GetPlayerSummary(context.Background(), testSteamID); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(*sleeps) != "[5ms]" {
		t.Fatalf("sleeps = %v, want the backoff only for a 503", *sleeps)
	}
}

// TestRetryAfterWaitHonorsContext: the hook cancels the context when the 60 s wait starts, then runs the production
// sleep, which returns ctx.Err() at once. The timing does not depend on the scheduler.
func TestRetryAfterWaitHonorsContext(t *testing.T) {
	client, attempts, sleeps, closeServer := retryAfterClient(t, http.StatusTooManyRequests, "60", "60")
	defer closeServer()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sleepErr error
	client.retrySleep = func(ctx context.Context, d time.Duration) error {
		*sleeps = append(*sleeps, d)
		cancel()
		sleepErr = sleepContext(ctx, d)
		return sleepErr
	}

	_, err := client.GetPlayerSummary(ctx, testSteamID)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !errors.Is(sleepErr, context.Canceled) {
		t.Fatalf("sleepContext returned %v, want context.Canceled", sleepErr)
	}
	if attempts.Load() != 1 || fmt.Sprint(*sleeps) != "[1m0s]" {
		t.Fatalf("attempts=%d sleeps=%v, want 1 attempt and one 1m0s wait cut short by cancel", attempts.Load(), *sleeps)
	}
}

// failingBody yields r, then err in place of io.EOF, so a body read fails after a partial answer.
type failingBody struct {
	r   io.Reader
	err error
}

func (b *failingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if errors.Is(err, io.EOF) {
		return n, b.err
	}
	return n, err
}

func (b *failingBody) Close() error { return nil }

const fullPlayerBody = `{"response":{"players":[{"steamid":"76561198000000001","personaname":"Gabe"}]}}`

// bodyFailureClient answers each request from next: a body that fails when readErr is set, else the full player body.
func bodyFailureClient(t *testing.T, readErr func(attempt int32) error) (*Client, *atomic.Int32) {
	t.Helper()
	var attempts atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		n := attempts.Add(1)
		body := io.Reader(strings.NewReader(fullPlayerBody))
		if err := readErr(n); err != nil {
			body = &failingBody{r: strings.NewReader(fullPlayerBody[:20]), err: err}
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: http.Header{}, Body: io.NopCloser(body)}, nil
	})}
	client := &Client{APIKey: "test-key", BaseURL: "http://steam.invalid", HTTPClient: httpClient}
	client.SetRetryBackoff(func(int) time.Duration { return 0 })
	return client, &attempts
}

func TestBodyReadErrorsAreRetried(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF},
		{name: "connection reset", err: syscall.ECONNRESET},
		{name: "wrapped connection reset", err: fmt.Errorf("read body: %w", syscall.ECONNRESET)},
		// A body read that times out (the 30 s HTTP client timeout) while the request context is live is transient.
		{name: "deadline exceeded with live context", err: context.DeadlineExceeded},
		{name: "canceled error with live context", err: context.Canceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, attempts := bodyFailureClient(t, func(n int32) error {
				if n == 1 {
					return tc.err
				}
				return nil
			})
			player, err := client.GetPlayerSummary(context.Background(), testSteamID)
			if err != nil {
				t.Fatalf("err = %v, want success on the retry", err)
			}
			if attempts.Load() != 2 || player.DisplayName != "Gabe" {
				t.Fatalf("attempts=%d player=%#v, want 2 attempts and Gabe", attempts.Load(), player)
			}
		})
	}
}

// TestBodyReadErrorWithDoneContextIsNotRetried: once the request context is done, a failed body read is final,
// whatever its error value. The read cancels the context first, as a caller that gives up mid-body would.
func TestBodyReadErrorWithDoneContextIsNotRetried(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client, attempts := bodyFailureClient(t, func(int32) error {
				cancel()
				return cause
			})
			_, err := client.GetPlayerSummary(ctx, testSteamID)
			if !errors.Is(err, cause) {
				t.Fatalf("err = %v, want %v in the chain", err, cause)
			}
			if attempts.Load() != 1 {
				t.Fatalf("attempts = %d, want 1 (no retry once the context is done)", attempts.Load())
			}
		})
	}
}

func TestBodyReadErrorRedactsIdentifiersAndKeepsChain(t *testing.T) {
	sentinel := errors.New("read https://steam.invalid/x?key=test-key&steamid=76561198000000001: reset")
	client, attempts := bodyFailureClient(t, func(int32) error { return sentinel })
	_, err := client.GetPlayerSummary(context.Background(), testSteamID)
	if err == nil {
		t.Fatal("err = nil, want body read error after retries")
	}
	text := err.Error()
	if strings.Contains(text, "test-key") || strings.Contains(text, "76561198000000001") {
		t.Fatalf("error leaks key or SteamID64: %q", text)
	}
	if !strings.Contains(text, "key=REDACTED") {
		t.Fatalf("error = %q, want redacted key kept", text)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("err chain lost the read error: %v", err)
	}
	if attempts.Load() != 4 {
		t.Fatalf("attempts = %d, want 4", attempts.Load())
	}
}

// oversizeClient serves body for the player summaries path and counts requests.
func oversizeClient(t *testing.T, body string) (*Client, *atomic.Int32, func()) {
	t.Helper()
	var attempts atomic.Int32
	client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != playerSummariesPath {
			http.NotFound(w, r)
			return
		}
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	return client, &attempts, closeServer
}

func TestOversizeBodyIsUnreadableAndNotShown(t *testing.T) {
	const marker = "BODY-MARKER-SHOULD-NOT-APPEAR"
	client, attempts, closeServer := oversizeClient(t, marker+strings.Repeat("a", maxResponseBytes))
	defer closeServer()

	_, err := client.GetPlayerSummary(context.Background(), testSteamID)
	if err == nil {
		t.Fatal("err = nil, want oversize error")
	}
	if got := UserMessage(err); got != userMsgUnreadable {
		t.Fatalf("UserMessage = %q, want %q", got, userMsgUnreadable)
	}
	if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "aaaa") {
		t.Fatalf("error carries body text: %q", err.Error())
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1 (oversize is not retried)", attempts.Load())
	}
}

// TestErrorStatusWithOversizeBodyKeepsStatusError: an error status never fails for its body size, and the 2xx cap
// does not apply to it. The cap here is 64 bytes, far under the body, yet the answer stays an HTTPStatusError that
// maps as its status does, and the log text is the first errorBodyLogLimit bytes.
func TestErrorStatusWithOversizeBodyKeepsStatusError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{name: "401", status: http.StatusUnauthorized, want: userMsgKeyRejected},
		{name: "403", status: http.StatusForbidden, want: userMsgKeyRejected},
		{name: "404", status: http.StatusNotFound, want: userMsgNoData},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Spaces between the x's: a body of one unbroken token run loses its trailing run at the read window.
			body := strings.Repeat("x ", 2*errorBodyLogLimit)
			client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != playerSummariesPath {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(body))
			})
			defer closeServer()

			var payload struct{}
			_, err := client.fetchJSON(context.Background(), playerSummariesPath, nil, &payload, fetchOptions{maxBytes: 64})
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) || statusErr.Code != tc.status {
				t.Fatalf("err = %v, want HTTPStatusError with code %d", err, tc.status)
			}
			if got := UserMessage(err); got != tc.want {
				t.Fatalf("UserMessage = %q, want %q", got, tc.want)
			}
			if statusErr.body != strings.Repeat("x ", errorBodyLogLimit/2) {
				t.Fatalf("log body is %d bytes, want exactly the first %d bytes", len(statusErr.body), errorBodyLogLimit)
			}
		})
	}
}

// TestErrorBodyRedactsSecretsAcrossLogCut: a secret that starts before the log cut and ends after it is removed
// whole, so no part of it reaches the log text. Each case puts the secret so that it straddles errorBodyLogLimit.
func TestErrorBodyRedactsSecretsAcrossLogCut(t *testing.T) {
	const (
		straddleKey = "0123456789abcdef0123456789abcdef"
		straddleID  = "76561198000000001"
		limit       = errorBodyLogLimit
	)
	tests := []struct {
		name   string
		key    string
		prefix string
		secret string
	}{
		{name: "api key glued to text before the cut", key: straddleKey, prefix: strings.Repeat("x", limit-5), secret: straddleKey},
		{name: "bare SteamID64 across the cut", key: "test-key", prefix: strings.Repeat("-", limit-5), secret: straddleID},
		{name: "steamid query value across the cut", key: "test-key", prefix: strings.Repeat("x", limit-13) + "?steamid=", secret: straddleID},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// A space after the secret: a bare SteamID64 glued to a letter is not matched (see steamID64Text).
			body := tc.prefix + tc.secret + " " + strings.Repeat("y", 64)
			client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != playerSummariesPath {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(body))
			})
			defer closeServer()
			client.APIKey = tc.key

			var payload struct{}
			_, err := client.fetchJSON(context.Background(), playerSummariesPath, nil, &payload, fetchOptions{maxBytes: 64})
			var statusErr *HTTPStatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("err = %v, want HTTPStatusError", err)
			}
			text := statusErr.body
			if len(text) > limit {
				t.Fatalf("log body is %d bytes, want at most %d", len(text), limit)
			}
			// The cut may land inside REDACTED, so only the secret's own bytes are checked, at both ends.
			for _, part := range []string{tc.secret[:4], tc.secret[len(tc.secret)-4:]} {
				if strings.Contains(text, part) {
					t.Fatalf("log body keeps part of the secret %q: %q", part, text)
				}
			}
		})
	}
}

// Secrets for the window-edge tests: a 32-hex key and a 17-digit SteamID64, each with distinct 4-byte ends.
const (
	cutKey = "0123456789abcdef0123456789abcdef"
	cutID  = "76561198000000001"
)

// errorBodyClient answers every request with status and body through a stub transport, so a test sets the exact
// bytes of the error body. The key is apiKey.
func errorBodyClient(apiKey string, status int, body string) *Client {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(body)),
		}, nil
	})}
	client := &Client{APIKey: apiKey, BaseURL: "http://steam.invalid", HTTPClient: httpClient}
	client.SetRetryBackoff(func(int) time.Duration { return 0 })
	return client
}

// errorLogText returns the log text of the client's error answer, which must be an HTTPStatusError.
func errorLogText(t *testing.T, client *Client) string {
	t.Helper()
	var payload struct{}
	_, err := client.fetchJSON(context.Background(), playerSummariesPath, nil, &payload, fetchOptions{maxBytes: 64})
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %v, want HTTPStatusError", err)
	}
	return statusErr.body
}

// secretEnd returns the first or last 4 bytes of secret that text still contains, or "" when neither is there.
func secretEnd(text, secret string) string {
	for _, part := range []string{secret[:4], secret[len(secret)-4:]} {
		if strings.Contains(text, part) {
			return part
		}
	}
	return ""
}

// TestErrorBodyLeadingSpaceDoesNotExposeCutKey: the body is trimmed after the read window, so leading spaces move a
// key the window cuts into the log text. The window ends 20 bytes into the key.
func TestErrorBodyLeadingSpaceDoesNotExposeCutKey(t *testing.T) {
	w := (&Client{APIKey: cutKey}).errorBodyWindow()
	for _, lead := range []int{33, 100, 200} {
		body := strings.Repeat(" ", lead) + strings.Repeat(".", w-20-lead) + cutKey + strings.Repeat(".", 64)
		text := errorLogText(t, errorBodyClient(cutKey, http.StatusForbidden, body))
		if part := secretEnd(text, cutKey); part != "" {
			t.Fatalf("%d leading spaces: log text keeps %q of the key: %q", lead, part, text)
		}
	}
}

// TestErrorBodyEarlierRedactionsDoNotExposeCutKey: each whole key is redacted to REDACTED, which is shorter, so
// earlier keys move a key that the window cuts to another offset. The cut key ends 20 bytes before the window end.
func TestErrorBodyEarlierRedactionsDoNotExposeCutKey(t *testing.T) {
	w := (&Client{APIKey: cutKey}).errorBodyWindow()
	head := cutKey + " " + cutKey + " "
	body := head + strings.Repeat(".", w-20-len(head)) + cutKey + strings.Repeat(".", 64)
	text := errorLogText(t, errorBodyClient(cutKey, http.StatusForbidden, body))
	if got := strings.Count(text, "REDACTED"); got != 2 {
		t.Fatalf("log text has %d REDACTED, want the 2 earlier keys: %q", got, text)
	}
	if part := secretEnd(text, cutKey); part != "" {
		t.Fatalf("log text keeps %q of the cut key: %q", part, text)
	}
}

// TestErrorBodyCutAtWindowEdgeSweep: each secret starts at every offset from W-40 to W+1, so the window ends before,
// inside and after it. The body starts with 100 spaces, which the trim removes, so a cut secret falls within the
// 4 KiB log cut and would show if it were not dropped. The padding is '|', which no test key or token set holds, so
// the token strip stops at the padding and the text keeps it. A '.' padding would be lost for a key that has '.' in
// it, and the text would be empty, so the sweep would prove nothing about the text around the secret.
func TestErrorBodyCutAtWindowEdgeSweep(t *testing.T) {
	const (
		lead    = 100
		padding = "|"
	)
	tests := []struct {
		name   string
		apiKey string
		prefix string
		secret string
	}{
		{name: "api key", apiKey: cutKey, secret: cutKey},
		{name: "bare SteamID64", apiKey: "test-key", secret: cutID},
		{name: "steamid query value", apiKey: "test-key", prefix: "?steamid=", secret: cutID},
		{name: "dotted api key", apiKey: "ABCD1234.EFGH5678.IJKL9012", secret: "ABCD1234.EFGH5678.IJKL9012"},
		{name: "tilde api key", apiKey: "abcd~efgh~ijkl~mnop", secret: "abcd~efgh~ijkl~mnop"},
		{name: "api key with separators", apiKey: `key:with/slash=and"q`, secret: `key:with/slash=and"q`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := (&Client{APIKey: tc.apiKey}).errorBodyWindow()
			for start := w - 40; start <= w+1; start++ {
				body := strings.Repeat(" ", lead) +
					strings.Repeat(padding, start-lead-len(tc.prefix)) + tc.prefix + tc.secret + strings.Repeat(padding, 64)
				text := errorLogText(t, errorBodyClient(tc.apiKey, http.StatusForbidden, body))
				if !strings.Contains(text, padding) {
					t.Fatalf("secret at offset %d: log text lost its padding, so the text is empty around the secret: %q", start, text)
				}
				if part := secretEnd(text, tc.secret); part != "" {
					t.Fatalf("secret at offset %d: log text keeps %q: %q", start, part, text)
				}
			}
		})
	}
}

// TestErrorBodyCutKeepsUTF8Valid: a multi-byte rune cut by the 4 KiB log cut, or by the read window, leaves valid
// UTF-8 in the log text. The window case has keys ahead so the text stays under the log cut, and the rune is dropped
// at the window end.
func TestErrorBodyCutKeepsUTF8Valid(t *testing.T) {
	w := (&Client{APIKey: cutKey}).errorBodyWindow()
	for _, r := range []string{"é", "€", "😀"} {
		for off := errorBodyLogLimit - 4; off <= errorBodyLogLimit; off++ {
			body := strings.Repeat(".", off) + r + strings.Repeat(".", 64)
			text := errorLogText(t, errorBodyClient(cutKey, http.StatusForbidden, body))
			if !utf8.ValidString(text) || len(text) > errorBodyLogLimit {
				t.Fatalf("rune %q at %d: log text is %d bytes, valid UTF-8 %v", r, off, len(text), utf8.ValidString(text))
			}
		}
		head := strings.Repeat(cutKey+" ", 100)
		for start := w - 4; start <= w; start++ {
			body := head + strings.Repeat(".", start-len(head)) + r + strings.Repeat(".", 64)
			text := errorLogText(t, errorBodyClient(cutKey, http.StatusForbidden, body))
			if !utf8.ValidString(text) || len(text) > errorBodyLogLimit {
				t.Fatalf("rune %q at window offset %d: log text is %d bytes, valid UTF-8 %v", r, start, len(text), utf8.ValidString(text))
			}
		}
	}
}

// TestErrorBodyNonASCIIKeyKeepsUTF8Valid: a key of multi-byte runes puts its continuation bytes in the token set. A
// rune at the window end can then lose its last byte to the token strip and keep its lead, which must not reach the
// log text. Ten whole keys ahead are redacted to a shorter text, so the text stays under the log cut and the rune is
// still in it.
func TestErrorBodyNonASCIIKeyKeepsUTF8Valid(t *testing.T) {
	key := strings.Repeat("ж", 20)
	w := (&Client{APIKey: key}).errorBodyWindow()
	head := strings.Repeat(key+" ", 10)
	for _, r := range []string{"Ѷ", "ж", "€", "😀"} {
		for start := w - 4; start <= w+1; start++ {
			body := head + strings.Repeat(".", start-len(head)) + r + strings.Repeat(".", 64)
			text := errorLogText(t, errorBodyClient(key, http.StatusForbidden, body))
			if !utf8.ValidString(text) || len(text) > errorBodyLogLimit {
				t.Fatalf("rune %q at window offset %d: log text is %d bytes, valid UTF-8 %v: %q",
					r, start, len(text), utf8.ValidString(text), text)
			}
		}
	}
}

// TestErrorBodyShortBodyIsNotTrimmed: a body shorter than the read window was read whole, so its trailing token run
// stays. Only a body that reaches the window loses that run.
func TestErrorBodyShortBodyIsNotTrimmed(t *testing.T) {
	w := (&Client{APIKey: "test-key"}).errorBodyWindow()
	long := errorLogText(t, errorBodyClient("test-key", http.StatusForbidden, strings.Repeat("x", w-1)))
	if long != strings.Repeat("x", errorBodyLogLimit) {
		t.Fatalf("body one byte under the window: log text is %d bytes, want the first %d x's", len(long), errorBodyLogLimit)
	}
	const short = "Forbidden: token-abc123"
	if got := errorLogText(t, errorBodyClient("test-key", http.StatusForbidden, short)); got != short {
		t.Fatalf("short body: log text = %q, want %q", got, short)
	}
}

func TestErrorBodyWindow(t *testing.T) {
	for _, key := range []string{"", "test-key", cutKey} {
		want := errorBodyLogLimit + len(key) + 32
		if got := (&Client{APIKey: key}).errorBodyWindow(); got != want {
			t.Fatalf("errorBodyWindow with key length %d = %d, want %d", len(key), got, want)
		}
	}
}

func TestCutBytes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{name: "shorter than n", in: "abc", n: 5, want: "abc"},
		{name: "equal to n", in: "abc", n: 3, want: "abc"},
		{name: "plain cut", in: "abc", n: 2, want: "ab"},
		{name: "zero", in: "abc", n: 0, want: ""},
		{name: "cut inside a rune steps back", in: "aé", n: 2, want: "a"},
		{name: "cut after a whole rune", in: "aé", n: 3, want: "aé"},
		{name: "only a partial rune fits", in: "é", n: 1, want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cutBytes(tc.in, tc.n); got != tc.want {
				t.Fatalf("cutBytes(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

func TestTornTail(t *testing.T) {
	tests := []struct {
		name   string
		apiKey string
		body   string
		limit  int64 // 0 means len(body): the body reaches the window
		want   string
	}{
		{name: "shorter than the limit is kept", body: "abc-123", limit: 100, want: "abc-123"},
		{name: "cut token run dropped", body: "ab " + cutKey[:9], want: "ab "},
		{name: "separator kept, value run dropped", body: "k=v&steamid=7656", want: "k=v&steamid="},
		{name: "percent escape dropped", body: "a %2", want: "a "},
		{name: "plus and JSON escape dropped", body: `x. +\u00`, want: "x. "},
		{name: "dot is not a token byte", body: "ab.cd", want: "ab."},
		{name: "equals is not a token byte", body: "key=", want: "key="},
		{name: "whole run dropped", body: "abcd", want: ""},
		{name: "partial rune dropped", body: "x.\xe2\x82", want: "x."},
		{name: "partial rune then token run", body: "ab\xe2\x82", want: ""},
		{name: "whole rune kept", body: "x€", want: "x€"},
		{name: "empty", body: "", want: ""},
		{name: "hyphen and underscore reach the window", body: "a b-c_d", want: "a "},
		{name: "dotted key run dropped", apiKey: "ABCD1234.EFGH5678.IJKL9012", body: "x ABCD1234.EFGH", want: "x "},
		{name: "tilde key run dropped", apiKey: "abcd~efgh~ijkl", body: "x abcd~efgh~", want: "x "},
		{name: "key separators dropped", apiKey: `key:with/slash=and"q`, body: "<pre>key=key:with/", want: "<pre>"},
		{name: "key bytes stop at a non-key byte", apiKey: "ab.c", body: "x=ab.", want: "x="},
		{name: "key does not add colon", apiKey: "ab.c", body: "x.y:", want: "x.y:"},
		{name: "rune cut again after the token strip", apiKey: "ж", body: "x Ѷ", want: "x "},
		{name: "key rune at end stripped whole", apiKey: "ж", body: "x ж", want: "x "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			limit := tc.limit
			if limit == 0 {
				limit = int64(len(tc.body))
			}
			client := &Client{APIKey: tc.apiKey}
			if got := string(client.tornTail([]byte(tc.body), limit)); got != tc.want {
				t.Fatalf("tornTail(%q, %d) with key %q = %q, want %q", tc.body, limit, tc.apiKey, got, tc.want)
			}
		})
	}
}

// TestSecretTokenBytesAddsOnlyKeyBytes: the set is isSecretTokenByte plus the raw bytes of the key, and nothing else.
// The query-escaped and path-escaped forms of the key must add no byte: each of their bytes is in the set. A hex key
// adds nothing, and an empty key adds nothing.
func TestSecretTokenBytesAddsOnlyKeyBytes(t *testing.T) {
	for _, key := range []string{"", cutKey} {
		set := (&Client{APIKey: key}).secretTokenBytes()
		for b := 0; b < 256; b++ {
			if want := isSecretTokenByte(byte(b)); set[b] != want {
				t.Fatalf("key %q: byte %q in set = %v, want %v", key, byte(b), set[b], want)
			}
		}
	}
	const punct = `k:/="`
	set := (&Client{APIKey: punct}).secretTokenBytes()
	for b := 0; b < 256; b++ {
		want := isSecretTokenByte(byte(b)) || strings.IndexByte(punct, byte(b)) >= 0
		if set[b] != want {
			t.Fatalf("key %q: byte %q in set = %v, want %v", punct, byte(b), set[b], want)
		}
	}
	for _, form := range []string{url.QueryEscape(punct), url.PathEscape(punct), `k:/="%3A%2F%3D%22`} {
		for _, b := range []byte(form) {
			if !set[b] {
				t.Fatalf("byte %q of escaped form %q is not in the set", b, form)
			}
		}
	}
	if set['.'] {
		t.Fatalf("'.' is in the set for a key without it")
	}
}

// TestFetchOptionsZeroMaxBytesUsesDefaultCap: a maxBytes of 0 (or less) reads with the 4 MiB default, not with no cap
// or a zero cap.
func TestFetchOptionsZeroMaxBytesUsesDefaultCap(t *testing.T) {
	small := `{"response":{"players":[]}}`
	client, _, closeServer := oversizeClient(t, small)
	defer closeServer()
	for _, limit := range []int64{0, -1} {
		var payload struct{}
		if _, err := client.fetchJSON(context.Background(), playerSummariesPath, nil, &payload, fetchOptions{maxBytes: limit}); err != nil {
			t.Fatalf("maxBytes %d: small body err = %v, want success", limit, err)
		}
	}

	big, _, closeBig := oversizeClient(t, small+strings.Repeat(" ", maxResponseBytes))
	defer closeBig()
	var payload struct{}
	_, err := big.fetchJSON(context.Background(), playerSummariesPath, nil, &payload, fetchOptions{maxBytes: 0})
	if got := UserMessage(err); got != userMsgUnreadable {
		t.Fatalf("maxBytes 0: body over the default cap: UserMessage = %q, want %q (err %v)", got, userMsgUnreadable, err)
	}
}

func TestBodyExactlyAtCapIsAccepted(t *testing.T) {
	// Valid JSON padded with spaces to exactly the cap: it decodes, and the player list is empty.
	pad := maxResponseBytes - len(`{"response":{"players":[]}}`)
	client, _, closeServer := oversizeClient(t, `{"response":{"players":[]}}`+strings.Repeat(" ", pad))
	defer closeServer()

	_, err := client.GetPlayerSummary(context.Background(), testSteamID)
	if got := UserMessage(err); got != "No Steam profile was found for that account." {
		t.Fatalf("err = %v (%q), want the empty-players sentence, not an oversize error", err, got)
	}
}

func TestBodyLimitBoundaryWithSmallCap(t *testing.T) {
	const limit = 64
	body := `{"response":{"players":[]}}`
	tests := []struct {
		name    string
		size    int
		wantErr bool
	}{
		{name: "one under the cap", size: limit - 1},
		{name: "exactly at the cap", size: limit},
		{name: "one over the cap", size: limit + 1, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			padded := body + strings.Repeat(" ", tc.size-len(body))
			client, _, closeServer := oversizeClient(t, padded)
			defer closeServer()

			var payload struct{}
			_, err := client.fetchJSON(context.Background(), playerSummariesPath, nil, &payload, fetchOptions{maxBytes: limit})
			if tc.wantErr != (err != nil) {
				t.Fatalf("size %d with cap %d: err = %v, want error %v", tc.size, limit, err, tc.wantErr)
			}
		})
	}
}

// TestOwnedGamesLargeLibraryDecodes uses a 10k-game library whose body is over 4 MiB: it decodes only because
// GetOwnedGames has the larger cap.
func TestOwnedGamesLargeLibraryDecodes(t *testing.T) {
	const games = 10000
	name := strings.Repeat("N", 500)
	var b strings.Builder
	b.WriteString(`{"response":{"game_count":10000,"games":[`)
	for i := range games {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"appid":%d,"name":"%s","playtime_forever":%d}`, i+1, name, i)
	}
	b.WriteString(`]}}`)
	body := b.String()
	if len(body) <= maxResponseBytes {
		t.Fatalf("fixture is %d bytes, want over the 4 MiB default cap", len(body))
	}

	var hits atomic.Int32
	client, closeServer := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/IPlayerService/GetOwnedGames/v0001/" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	defer closeServer()

	owned, err := client.GetOwnedGames(context.Background(), testSteamID)
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != games || owned[0].AppID != 1 || owned[games-1].AppID != games {
		t.Fatalf("decoded %d games, want %d in order", len(owned), games)
	}
	if hits.Load() != 3 {
		t.Fatalf("requests = %d, want 3 (full, purchased, vetted)", hits.Load())
	}
}
