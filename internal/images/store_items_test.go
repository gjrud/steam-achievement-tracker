package images

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const storeOKBody = `{"response":{"store_items":[]}}`

// storeScriptStep is one scripted reply to a store-items request.
type storeScriptStep struct {
	status int
	body   string
	err    error
}

// storeScriptTransport replies to request n with steps[n]. Requests past the
// script get 418, which is not retried, so a mutant that retries too often ends
// on a wrong request count instead of looping forever.
type storeScriptTransport struct {
	steps    []storeScriptStep
	requests []string
	onReply  func(n int)
}

func (t *storeScriptTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	n := len(t.requests)
	t.requests = append(t.requests, req.URL.String())
	if t.onReply != nil {
		t.onReply(n)
	}
	if n >= len(t.steps) {
		return storeScriptReply(req, http.StatusTeapot, "script exhausted"), nil
	}
	step := t.steps[n]
	if step.err != nil {
		return nil, step.err
	}
	return storeScriptReply(req, step.status, step.body), nil
}

func storeScriptReply(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

// storeIDsFromURL returns the appids sent in one store-items request, in order.
func storeIDsFromURL(t *testing.T, rawURL string) []int64 {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	var payload struct {
		IDs []struct {
			AppID int64 `json:"appid"`
		} `json:"ids"`
	}
	if err := json.Unmarshal([]byte(u.Query().Get("input_json")), &payload); err != nil {
		t.Fatalf("decode input_json of %q: %v", rawURL, err)
	}
	ids := make([]int64, 0, len(payload.IDs))
	for _, id := range payload.IDs {
		ids = append(ids, id.AppID)
	}
	return ids
}

func consecutiveIDs(from, count int) []int64 {
	ids := make([]int64, 0, count)
	for i := 0; i < count; i++ {
		ids = append(ids, int64(from+i))
	}
	return ids
}

func storeOKSteps(n int) []storeScriptStep {
	steps := make([]storeScriptStep, n)
	for i := range steps {
		steps[i] = storeScriptStep{status: http.StatusOK, body: storeOKBody}
	}
	return steps
}

func TestStoreItemAssetsBatchBoundary(t *testing.T) {
	if storeItemsBatchSize != 100 {
		t.Fatalf("storeItemsBatchSize = %d, want 100", storeItemsBatchSize)
	}
	tests := []struct {
		name      string
		count     int
		wantSizes []int
	}{
		{"one under the batch size", 99, []int{99}},
		{"exactly the batch size", 100, []int{100}},
		{"one over the batch size", 101, []int{100, 1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transport := &storeScriptTransport{steps: storeOKSteps(len(tc.wantSizes))}
			cache, _ := testCache(t, transport)
			if _, err := cache.StoreItemAssets(context.Background(), consecutiveIDs(1, tc.count)); err != nil {
				t.Fatalf("store item assets: %v", err)
			}
			if len(transport.requests) != len(tc.wantSizes) {
				t.Fatalf("requests = %d, want %d", len(transport.requests), len(tc.wantSizes))
			}
			next := int64(1)
			for i, want := range tc.wantSizes {
				got := storeIDsFromURL(t, transport.requests[i])
				if len(got) != want {
					t.Fatalf("request %d carried %d ids, want %d", i, len(got), want)
				}
				for _, id := range got {
					if id != next {
						t.Fatalf("request %d ids out of order: got %d, want %d", i, id, next)
					}
					next++
				}
			}
			if next != int64(tc.count)+1 {
				t.Fatalf("sent ids up to %d, want %d", next-1, tc.count)
			}
		})
	}
}

func TestStoreItemAssetsBatchErrorKeepsPartialResult(t *testing.T) {
	first := `{"response":{"store_items":[{"id":1,"appid":1,"success":1,"type":4,"assets":{"main_capsule":"m/main_capsule.jpg"}}]}}`
	transport := &storeScriptTransport{steps: []storeScriptStep{
		{status: http.StatusOK, body: first},
		{status: http.StatusBadRequest, body: "bad input"},
	}}
	cache, _ := testCache(t, transport)

	assets, err := cache.StoreItemAssets(context.Background(), consecutiveIDs(1, 101))
	if err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("error = %v, want 400 error from second batch", err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests = %d, want 2 (400 is not retried)", len(transport.requests))
	}
	if len(assets) != 1 || assets[1].MainCapsule != "m/main_capsule.jpg" {
		t.Fatalf("partial assets = %+v, want only app 1 from first batch", assets)
	}
}

func TestStoreItemAssetsRetriesRetryableStatusOnce(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			withStoreItemsRetryDelay(t, noStoreItemsRetryDelay)
			transport := &storeScriptTransport{steps: []storeScriptStep{
				{status: status, body: "try later"},
				{status: http.StatusOK, body: storeOKBody},
			}}
			cache, _ := testCache(t, transport)
			if _, err := cache.StoreItemAssets(context.Background(), []int64{dlcAppID}); err != nil {
				t.Fatalf("store item assets after %d: %v", status, err)
			}
			if len(transport.requests) != 2 {
				t.Fatalf("requests = %d, want 2", len(transport.requests))
			}
			if transport.requests[0] != transport.requests[1] {
				t.Fatalf("retry sent a different request:\n%s\n%s", transport.requests[0], transport.requests[1])
			}
		})
	}
}

func TestStoreItemAssetsDoesNotRetryOtherStatuses(t *testing.T) {
	for _, status := range []int{http.StatusMultipleChoices, http.StatusBadRequest, http.StatusNotFound, http.StatusBadGateway, http.StatusGatewayTimeout} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			transport := &storeScriptTransport{steps: []storeScriptStep{
				{status: status, body: "nope"},
				{status: http.StatusOK, body: storeOKBody},
			}}
			cache, _ := testCache(t, transport)
			_, err := cache.StoreItemAssets(context.Background(), []int64{dlcAppID})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprint(status)) {
				t.Fatalf("error = %v, want %d error", err, status)
			}
			if len(transport.requests) != 1 {
				t.Fatalf("requests = %d, want 1", len(transport.requests))
			}
		})
	}
}

func TestStoreItemAssetsStatus299IsSuccess(t *testing.T) {
	transport := &storeScriptTransport{steps: []storeScriptStep{{status: 299, body: storeOKBody}}}
	cache, _ := testCache(t, transport)
	if _, err := cache.StoreItemAssets(context.Background(), []int64{dlcAppID}); err != nil {
		t.Fatalf("status 299: %v", err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(transport.requests))
	}
}

func TestStoreItemAssetsRetriesTransportErrorOnce(t *testing.T) {
	withStoreItemsRetryDelay(t, noStoreItemsRetryDelay)
	transport := &storeScriptTransport{steps: []storeScriptStep{
		{err: errors.New("connection reset by peer")},
		{status: http.StatusOK, body: storeOKBody},
	}}
	cache, _ := testCache(t, transport)
	if _, err := cache.StoreItemAssets(context.Background(), []int64{dlcAppID}); err != nil {
		t.Fatalf("store item assets after transport error: %v", err)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(transport.requests))
	}
}

func TestStoreItemAssetsInvalidJSONIsNotRetried(t *testing.T) {
	transport := &storeScriptTransport{steps: []storeScriptStep{
		{status: http.StatusOK, body: "not json"},
		{status: http.StatusOK, body: storeOKBody},
	}}
	cache, _ := testCache(t, transport)
	if _, err := cache.StoreItemAssets(context.Background(), []int64{dlcAppID}); err == nil {
		t.Fatal("error = nil for invalid JSON with 200")
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(transport.requests))
	}
}

func TestStoreItemAssetsFourthAttemptSucceeds(t *testing.T) {
	ok := `{"response":{"store_items":[{"id":5,"appid":5,"success":1,"type":4,"assets":{"header":"h/header.jpg"}}]}}`
	withStoreItemsRetryDelay(t, noStoreItemsRetryDelay)
	transport := &storeScriptTransport{steps: []storeScriptStep{
		{status: http.StatusServiceUnavailable},
		{status: http.StatusInternalServerError},
		{status: http.StatusTooManyRequests},
		{status: http.StatusOK, body: ok},
	}}
	cache, _ := testCache(t, transport)
	assets, err := cache.StoreItemAssets(context.Background(), []int64{5})
	if err != nil {
		t.Fatalf("store item assets: %v", err)
	}
	if len(transport.requests) != 4 {
		t.Fatalf("requests = %d, want 4", len(transport.requests))
	}
	if assets[5].Header != "h/header.jpg" {
		t.Fatalf("assets = %+v, want header from fourth attempt", assets)
	}
}

func TestStoreItemAssetsGivesUpAfterFourAttempts(t *testing.T) {
	// The fifth scripted reply succeeds; it must never be requested.
	withStoreItemsRetryDelay(t, noStoreItemsRetryDelay)
	transport := &storeScriptTransport{steps: []storeScriptStep{
		{status: http.StatusServiceUnavailable},
		{status: http.StatusServiceUnavailable},
		{status: http.StatusServiceUnavailable},
		{status: http.StatusServiceUnavailable},
		{status: http.StatusOK, body: storeOKBody},
	}}
	cache, _ := testCache(t, transport)
	_, err := cache.StoreItemAssets(context.Background(), []int64{dlcAppID})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("error = %v, want 503 after four attempts", err)
	}
	if len(transport.requests) != 4 {
		t.Fatalf("requests = %d, want exactly 4 (no fifth attempt)", len(transport.requests))
	}
}

func TestStoreItemAssetsCancelDuringBackoffStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Non-zero so the backoff select must take the ctx.Done branch; cancel fires
	// before the wait, so this returns at once rather than sleeping a second.
	withStoreItemsRetryDelay(t, func(int) time.Duration { return time.Second })
	transport := &storeScriptTransport{
		steps: []storeScriptStep{
			{status: http.StatusServiceUnavailable},
			{status: http.StatusOK, body: storeOKBody},
		},
		onReply: func(n int) {
			if n == 0 {
				cancel()
			}
		},
	}
	cache, _ := testCache(t, transport)
	_, err := cache.StoreItemAssets(ctx, []int64{dlcAppID})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %d, want 1 (no retry after cancel)", len(transport.requests))
	}
}

func TestStoreItemsRetryStatusTable(t *testing.T) {
	retry := map[int]bool{
		http.StatusTooManyRequests:     true,
		http.StatusInternalServerError: true,
		http.StatusServiceUnavailable:  true,
		http.StatusOK:                  false,
		http.StatusBadRequest:          false,
		http.StatusNotFound:            false,
		http.StatusBadGateway:          false,
		http.StatusGatewayTimeout:      false,
		http.StatusRequestTimeout:      false,
		http.StatusUnauthorized:        false,
	}
	for status, want := range retry {
		if got := storeItemsRetryStatus(status); got != want {
			t.Fatalf("storeItemsRetryStatus(%d) = %v, want %v", status, got, want)
		}
	}
}

// withStoreItemsRetryDelay swaps storeItemsRetryDelay for one test and restores
// the previous value on cleanup. The package has no t.Parallel, so this is safe.
func withStoreItemsRetryDelay(t *testing.T, delay func(int) time.Duration) {
	t.Helper()
	prev := storeItemsRetryDelay
	storeItemsRetryDelay = delay
	t.Cleanup(func() { storeItemsRetryDelay = prev })
}

func noStoreItemsRetryDelay(int) time.Duration { return 0 }

func TestStoreItemsRetryDelayDefaults(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 300 * time.Millisecond},
		{2, 600 * time.Millisecond},
		{3, 1200 * time.Millisecond},
	}
	for _, tc := range tests {
		if got := storeItemsRetryDelay(tc.attempt); got != tc.want {
			t.Fatalf("storeItemsRetryDelay(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

// TestWaitStoreItemsRetryRealWait is the only test that really sleeps: it covers
// the timer branch of waitStoreItemsRetry with the default 300ms delay.
func TestWaitStoreItemsRetryRealWait(t *testing.T) {
	start := time.Now()
	if err := waitStoreItemsRetry(context.Background(), 1); err != nil {
		t.Fatalf("attempt 1: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("attempt 1 waited %v, want between 300ms and 2s", elapsed)
	}
}

func TestStoreItemsAssetsIDFallbackAndSkips(t *testing.T) {
	const body = `{"response":{"store_items":[
	 {"id":77,"appid":0,"success":1,"type":2,"assets":{"header":" h/header.jpg "}},
	 {"id":9,"appid":9,"success":1,"type":1,"assets":{}},
	 {"id":0,"appid":0,"success":1,"type":1,"assets":{}},
	 {"id":-4,"appid":-4,"success":1,"type":1,"assets":{}},
	 {"id":12,"appid":12,"success":2,"type":1,"assets":{}},
	 {"id":13,"appid":13,"success":0,"type":1,"assets":{}}
	]}}`
	var payload storeItemsResponse
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	out := storeItemsAssets(payload)
	if len(out) != 2 {
		t.Fatalf("assets = %+v, want exactly apps 77 and 9", out)
	}
	fromID := out[77]
	if fromID.AppID != 77 || fromID.Type != 2 || fromID.Header != "h/header.jpg" {
		t.Fatalf("app 77 (appid 0, id fallback) = %+v", fromID)
	}
	if _, ok := out[9]; !ok {
		t.Fatalf("app 9 missing: %+v", out)
	}
}

func TestStoreItemAssetsDropsNonPositiveAndDuplicateIDs(t *testing.T) {
	transport := &storeScriptTransport{steps: storeOKSteps(1)}
	cache, _ := testCache(t, transport)
	if _, err := cache.StoreItemAssets(context.Background(), []int64{0, -3, 5, 5, 0}); err != nil {
		t.Fatal(err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(transport.requests))
	}
	if got := storeIDsFromURL(t, transport.requests[0]); len(got) != 1 || got[0] != 5 {
		t.Fatalf("ids sent = %v, want [5]", got)
	}

	none := &storeScriptTransport{}
	cache, _ = testCache(t, none)
	assets, err := cache.StoreItemAssets(context.Background(), []int64{0, -3})
	if err != nil {
		t.Fatalf("all-invalid ids: %v", err)
	}
	if len(none.requests) != 0 || len(assets) != 0 {
		t.Fatalf("all-invalid ids: requests=%d assets=%+v, want none", len(none.requests), assets)
	}
}
