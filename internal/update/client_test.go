package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

// stubGitHub serves the GitHub release API and asset downloads from memory. Asset URLs point back at the stub, so no
// request leaves the test. assets is the release's asset list; nil means realAssets. notes is the release body; empty
// means "notes". insecureRedirect answers every asset download (not SHA256SUMS) with a 302 to a plain-http copy of it.
type stubGitHub struct {
	tag              string
	withSums         bool
	sumsStatus       int
	latestCode       int
	assets           []string
	notes            string
	insecureRedirect bool
	bodies           map[string][]byte
	srv              *httptest.Server
}

func newStubGitHub(t *testing.T, s *stubGitHub) *httptest.Server {
	t.Helper()
	if s.assets == nil {
		s.assets = realAssets
	}
	s.bodies = map[string][]byte{}
	for _, name := range s.assets {
		if name == assetSHA256SUMS {
			continue
		}
		s.bodies[name] = []byte("bytes of " + name)
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		switch {
		case r.URL.Path == "/repos/"+repository+"/releases/latest":
			if s.latestCode != 0 {
				w.WriteHeader(s.latestCode)
				return
			}
			var assets []map[string]any
			for _, name := range s.assets {
				if name == "SHA256SUMS" && !s.withSums {
					continue
				}
				assets = append(assets, map[string]any{
					"id": len(assets) + 1, "name": name, "content_type": "application/octet-stream",
					"size": len(s.bodies[name]), "url": base + "/dl/" + name,
				})
			}
			notes := s.notes
			if notes == "" {
				notes = "notes"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": s.tag, "name": s.tag, "body": notes, "prerelease": false, "draft": false,
				"published_at": "2026-01-01T00:00:00Z", "html_url": "https://github.com/" + repository + "/releases/tag/" + s.tag,
				"assets": assets,
			})
		case r.URL.Path == "/dl/SHA256SUMS":
			if s.sumsStatus != 0 {
				w.WriteHeader(s.sumsStatus)
				return
			}
			var b strings.Builder
			for _, name := range s.assets {
				if name == assetSHA256SUMS {
					continue
				}
				sum := sha256.Sum256(s.bodies[name])
				fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
			}
			_, _ = w.Write([]byte(b.String()))
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			name := strings.TrimPrefix(r.URL.Path, "/dl/")
			if s.insecureRedirect {
				http.Redirect(w, r, "http://"+r.Host+"/plain/"+name, http.StatusFound)
				return
			}
			body, ok := s.bodies[name]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		case strings.HasPrefix(r.URL.Path, "/plain/"):
			body, ok := s.bodies[strings.TrimPrefix(r.URL.Path, "/plain/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s.srv
}

func providerFor(t *testing.T, base string) *github.Provider {
	t.Helper()
	p, err := github.New(githubConfig(base, newHTTPClient()))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProviderSelectsAppImageAndVerifiesDigest(t *testing.T) {
	stub := &stubGitHub{tag: "v0.3.0", withSums: true}
	srv := newStubGitHub(t, stub)
	p := providerFor(t, srv.URL)

	rel, err := p.Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.2.0", Platform: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel == nil || rel.Version != "0.3.0" || rel.Artifact.Filename != linuxAppImage {
		t.Fatalf("release = %+v", rel)
	}
	if rel.Verification == nil || rel.Verification.DigestAlgo != "sha256" || len(rel.Verification.Digest) != sha256.Size {
		t.Fatalf("verification = %+v", rel.Verification)
	}
	want := sha256.Sum256(stub.bodies[linuxAppImage])
	if !bytes.Equal(rel.Verification.Digest, want[:]) {
		t.Fatal("digest does not match the asset bytes")
	}
	if refusal := installRefusal(rel.Verification, hostEnv{goos: "windows"}); refusal != "" {
		t.Fatalf("installRefusal = %q, want installable", refusal)
	}

	var buf bytes.Buffer
	if err := p.Download(context.Background(), rel, &buf, nil); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), stub.bodies[linuxAppImage]) {
		t.Fatal("downloaded bytes differ from the asset")
	}
}

// TestProviderSelectsWindowsPortable runs both portable names end to end: the release's asset list carries one of them,
// the provider selects it, and its digest is found by exact name in SHA256SUMS and verified.
func TestProviderSelectsWindowsPortable(t *testing.T) {
	cases := []struct {
		name  string
		asset string
		list  []string
	}{
		{"unversioned name (next release, realAssets)", assetPortableUnversioned, realAssets},
		{"versioned name (earlier release)", assetPortable, assetsWithPortable(assetPortable)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubGitHub{tag: "v0.3.0", withSums: true, assets: tc.list}
			srv := newStubGitHub(t, stub)
			rel, err := providerFor(t, srv.URL).Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.2.0", Platform: "windows", Arch: "amd64"})
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if rel == nil || rel.Artifact.Filename != tc.asset {
				t.Fatalf("release = %+v, want filename %q", rel, tc.asset)
			}
			if rel.Verification == nil || rel.Verification.DigestAlgo != "sha256" || len(rel.Verification.Digest) != sha256.Size {
				t.Fatalf("verification = %+v", rel.Verification)
			}
			want := sha256.Sum256(stub.bodies[tc.asset])
			if !bytes.Equal(rel.Verification.Digest, want[:]) {
				t.Fatal("digest does not match the asset bytes")
			}
			if refusal := installRefusal(rel.Verification, hostEnv{goos: "windows"}); refusal != "" {
				t.Fatalf("windows portable refused: %q", refusal)
			}
		})
	}
}

func TestProviderMissingChecksumIsNotifyOnly(t *testing.T) {
	srv := newStubGitHub(t, &stubGitHub{tag: "v0.3.0", withSums: false})
	rel, err := providerFor(t, srv.URL).Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.2.0", Platform: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel == nil || rel.Verification != nil {
		t.Fatalf("want release with nil Verification, got %+v", rel)
	}
	if refusal := installRefusal(rel.Verification, hostEnv{goos: "windows"}); refusal != reasonNoChecksum {
		t.Fatalf("refusal = %q, want no-checksum", refusal)
	}
}

func TestProviderChecksumFetchFailureFailsCheck(t *testing.T) {
	srv := newStubGitHub(t, &stubGitHub{tag: "v0.3.0", withSums: true, sumsStatus: http.StatusInternalServerError})
	rel, err := providerFor(t, srv.URL).Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.2.0", Platform: "linux", Arch: "amd64"})
	if err == nil || rel != nil {
		t.Fatalf("want check error, got rel=%v err=%v", rel, err)
	}
}

func TestProviderNoRelease(t *testing.T) {
	srv := newStubGitHub(t, &stubGitHub{tag: "v0.3.0", withSums: true, latestCode: http.StatusNotFound})
	rel, err := providerFor(t, srv.URL).Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.2.0", Platform: "linux", Arch: "amd64"})
	if err != nil || rel != nil {
		t.Fatalf("404 should read as up to date, got rel=%v err=%v", rel, err)
	}
}

func TestProviderNotNewer(t *testing.T) {
	srv := newStubGitHub(t, &stubGitHub{tag: "v0.2.0", withSums: true})
	rel, err := providerFor(t, srv.URL).Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.2.0", Platform: "linux", Arch: "amd64"})
	if err != nil || rel != nil {
		t.Fatalf("same version should be up to date, got rel=%v err=%v", rel, err)
	}
}

func TestProviderNoAssetForPlatform(t *testing.T) {
	srv := newStubGitHub(t, &stubGitHub{tag: "v0.3.0", withSums: true})
	if _, err := providerFor(t, srv.URL).Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.2.0", Platform: "linux", Arch: "arm64"}); err == nil {
		t.Fatal("want error for an unsupported platform")
	}
}

func TestGitHubConfigIsStableAndVerified(t *testing.T) {
	cfg := githubConfig("", newHTTPClient())
	if cfg.Repository != repository || cfg.Prerelease || cfg.ChecksumAsset != checksumAsset || cfg.AssetMatcher == nil {
		t.Fatalf("githubConfig = %+v", cfg)
	}
	if cfg.Token != "" {
		t.Fatal("no token may ship in the binary")
	}
}

func TestHTTPClientHasNoTotalTimeout(t *testing.T) {
	c := newHTTPClient()
	if c.Timeout != 0 {
		t.Fatalf("Timeout = %v, want 0 (a total timeout would fail large downloads)", c.Timeout)
	}
	lt, ok := c.Transport.(limitedTransport)
	if !ok {
		t.Fatalf("transport = %T, want the size-limited wrapper", c.Transport)
	}
	if lt.limit != maxDownloadBytes {
		t.Fatalf("limit = %d, want maxDownloadBytes %d", lt.limit, maxDownloadBytes)
	}
	tr, ok := lt.base.(*http.Transport)
	if !ok {
		t.Fatalf("base transport = %T", lt.base)
	}
	if tr.ResponseHeaderTimeout != responseHeaderTimeout || tr.TLSHandshakeTimeout != tlsHandshakeTimeout || tr.DialContext == nil {
		t.Fatal("dial, TLS and response-header timeouts must be set")
	}
	if checkTimeout <= 0 || checkTimeout > 2*time.Minute {
		t.Fatalf("checkTimeout = %v", checkTimeout)
	}
}

// A redirect to anything but https is refused. The first request is plain http, as the stubs are, so only the hop
// matters: the client returns the policy's error, not the target's response.
func TestRedirectToHTTPRefused(t *testing.T) {
	target := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("target reached"))
	}))
	start := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/x", http.StatusFound)
	}))
	resp, err := newHTTPClient().Get(start.URL + "/start")
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, errInsecureRedirect) {
		t.Fatalf("err = %v, want errInsecureRedirect", err)
	}
}

// A redirect to https is allowed. The TLS stub's certificate is trusted through its own transport, which the limited
// wrapper keeps as its base; the redirect policy is the client's.
func TestRedirectToHTTPSAllowed(t *testing.T) {
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("secure"))
	}))
	t.Cleanup(tlsSrv.Close)
	start := newServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, tlsSrv.URL+"/x", http.StatusFound)
	}))
	c := newHTTPClient()
	c.Transport = limitedTransport{base: tlsSrv.Client().Transport, limit: maxDownloadBytes}
	resp, err := c.Get(start.URL + "/start")
	if err != nil {
		t.Fatalf("https redirect refused: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "secure" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}

// checkRedirect on synthetic requests. via holds the requests made so far: 9 entries allow the next redirect (the 10th
// request), 10 entries refuse it.
func TestCheckRedirectPolicy(t *testing.T) {
	https := &http.Request{URL: &url.URL{Scheme: "https", Host: "objects.example.invalid", Path: "/a"}}
	http1 := &http.Request{URL: &url.URL{Scheme: "http", Host: "objects.example.invalid", Path: "/a"}}
	hops := func(n int) []*http.Request { return make([]*http.Request, n) }
	cases := []struct {
		name string
		req  *http.Request
		via  []*http.Request
		want error
	}{
		{"one request made: redirect allowed", https, hops(1), nil},
		{"nine requests made: redirect allowed (10th request)", https, hops(9), nil},
		{"ten requests already made: refused", https, hops(10), errTooManyRedirects},
		{"plain http target refused", http1, hops(1), errInsecureRedirect},
		{"ftp target refused", &http.Request{URL: &url.URL{Scheme: "ftp", Host: "x"}}, hops(1), errInsecureRedirect},
	}
	for _, tc := range cases {
		if got := checkRedirect(tc.req, tc.via); got != tc.want {
			t.Errorf("%s: err = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// newServer is an httptest server on plain http running h.
func newServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// fakeRoundTripper returns the response built by resp.
type fakeRoundTripper struct {
	resp func() *http.Response
}

func (f fakeRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return f.resp(), nil
}

// idleCounter is a RoundTripper whose CloseIdleConnections counts its calls.
type idleCounter struct {
	fakeRoundTripper
	closes *int
}

func (c idleCounter) CloseIdleConnections() { *c.closes++ }

// The wrapper must forward CloseIdleConnections to its base, or http.Client.CloseIdleConnections never reaches the
// transport and its idle connections stay open.
func TestLimitedTransportForwardsCloseIdleConnections(t *testing.T) {
	closes := 0
	c := &http.Client{Transport: limitedTransport{base: idleCounter{closes: &closes}, limit: 1024}}
	c.CloseIdleConnections()
	if closes != 1 {
		t.Fatalf("base CloseIdleConnections called %d times, want 1", closes)
	}
}

// trackedBody is a response body that records Close and, when unread is set, fails the test on any Read.
type trackedBody struct {
	r      io.Reader
	t      *testing.T
	unread bool
	closed *bool
}

func (b *trackedBody) Read(p []byte) (int, error) {
	if b.unread {
		b.t.Error("the body was read although its declared length is over the cap")
	}
	return b.r.Read(p)
}

func (b *trackedBody) Close() error {
	*b.closed = true
	return nil
}

// The wrapper caps the body at exactly the limit: a body of exactly the cap reads whole, and one byte more fails with
// errTooLarge. It holds for an unknown length (-1) and for a declared length equal to the cap.
func TestCappedBodyLimit(t *testing.T) {
	const limit = 10
	for _, tc := range []struct {
		name    string
		size    int
		declare int64
		wantErr bool
	}{
		{"exactly the cap, unknown length", limit, -1, false},
		{"exactly the cap, declared", limit, limit, false},
		{"one byte over, unknown length", limit + 1, -1, true},
		{"well over, unknown length", 5 * limit, -1, true},
		{"small body", 3, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closed := false
			rt := limitedTransport{base: fakeRoundTripper{resp: func() *http.Response {
				return &http.Response{StatusCode: 200, ContentLength: tc.declare, Body: &trackedBody{
					r: bytes.NewReader(bytes.Repeat([]byte{'x'}, tc.size)), t: t, closed: &closed,
				}}
			}}, limit: limit}
			req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			got, err := io.ReadAll(resp.Body)
			if tc.wantErr {
				if !errors.Is(err, errTooLarge) {
					t.Fatalf("read err = %v, want errTooLarge", err)
				}
			} else if err != nil || len(got) != tc.size {
				t.Fatalf("read %d bytes, err %v; want %d, nil", len(got), err, tc.size)
			}
			_ = resp.Body.Close()
			if !closed {
				t.Fatal("Close did not reach the underlying body")
			}
		})
	}
}

// A declared length over the cap is refused before any byte is read, and the body is closed.
func TestDeclaredLengthOverCapRefusedUnread(t *testing.T) {
	closed := false
	rt := limitedTransport{base: fakeRoundTripper{resp: func() *http.Response {
		return &http.Response{StatusCode: 200, ContentLength: 11, Body: &trackedBody{
			r: bytes.NewReader(make([]byte, 11)), t: t, unread: true, closed: &closed,
		}}
	}}, limit: 10}
	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	resp, err := rt.RoundTrip(req)
	if !errors.Is(err, errTooLarge) || resp != nil {
		t.Fatalf("resp %v, err %v; want nil and errTooLarge", resp, err)
	}
	if !closed {
		t.Fatal("the refused body was not closed")
	}
}

// Through a real client: a declared over-cap length fails the request, and a chunked over-cap body fails the read
// with errTooLarge. A body of exactly the cap comes through.
func TestLimitedClientOverHTTP(t *testing.T) {
	const limit = 16
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/declared-over":
			_, _ = w.Write(bytes.Repeat([]byte{'x'}, limit+1)) // no flush: the server sets Content-Length
		case "/chunked-over":
			_, _ = w.Write(bytes.Repeat([]byte{'x'}, limit+1))
			w.(http.Flusher).Flush() // forces chunked encoding, so the length is unknown to the client
		case "/exact":
			_, _ = w.Write(bytes.Repeat([]byte{'x'}, limit))
		}
	}))
	t.Cleanup(srv.Close)
	c := newLimitedClient(limit)

	if resp, err := c.Get(srv.URL + "/declared-over"); !errors.Is(err, errTooLarge) {
		if resp != nil {
			_ = resp.Body.Close()
		}
		t.Fatalf("declared over cap: err = %v, want errTooLarge", err)
	}
	resp, err := c.Get(srv.URL + "/chunked-over")
	if err != nil {
		t.Fatalf("chunked over cap: request failed early: %v", err)
	}
	_, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !errors.Is(err, errTooLarge) {
		t.Fatalf("chunked over cap: read err = %v, want errTooLarge", err)
	}
	resp, err = c.Get(srv.URL + "/exact")
	if err != nil {
		t.Fatalf("exact cap: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || len(body) != limit {
		t.Fatalf("exact cap: %d bytes, err %v", len(body), err)
	}
}
