package update

import (
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

// Timeouts. The HTTP client has no total timeout: a large download on a slow link must not fail, and Cancel or app
// shutdown ends it. Dial, TLS and response-header waits are bounded.
const (
	dialTimeout           = 15 * time.Second
	tlsHandshakeTimeout   = 15 * time.Second
	responseHeaderTimeout = 30 * time.Second
	// checkTimeout bounds one check (up to two API requests). An install's re-check uses it too.
	checkTimeout = 60 * time.Second
)

// maxDownloadBytes caps every response body the client returns: the release JSON, SHA256SUMS and the asset. A body
// whose declared length is over it is refused before it is read; a body that grows past it fails the read. Exactly
// the cap is allowed.
const maxDownloadBytes = 512 << 20

// maxRedirects is the request limit of one chain: checkRedirect refuses a redirect once this many requests were made,
// the original request included. That is 10 requests, i.e. 9 redirects, as in the framework's default client.
const maxRedirects = 10

var (
	// errTooLarge is a response body over the size cap. The framework reports it as a download error.
	errTooLarge = errors.New("update: response is larger than the size limit")
	// errInsecureRedirect is a redirect to anything but https.
	errInsecureRedirect = errors.New("update: redirect to a non-https address refused")
	// errTooManyRedirects is a redirect chain longer than maxRedirects.
	errTooManyRedirects = errors.New("update: too many redirects")
)

// startupDelay keeps the background check off the critical path of app start. A variable so tests can set it large.
var startupDelay = 5 * time.Second

// newHTTPClient returns the client for the GitHub provider and the asset download.
func newHTTPClient() *http.Client {
	return newLimitedClient(maxDownloadBytes)
}

// newLimitedClient is newHTTPClient with the response size cap given. Tests use a small cap; production uses
// maxDownloadBytes.
func newLimitedClient(limit int64) *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport:     limitedTransport{base: tr, limit: limit},
		CheckRedirect: checkRedirect,
	}
}

// checkRedirect is the client's redirect policy: every redirect target must be https, and a chain stops once
// maxRedirects requests were made (via holds the requests made so far; 10 entries refuse the next redirect, 9 allow
// it). The policy is the client's, so it applies to every request. For SHA256SUMS and the asset, the provider's
// wrapper first strips Authorization on a host change, then calls it. It only affects redirects; the first request
// may be any scheme.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return errTooManyRedirects
	}
	if req.URL.Scheme != "https" {
		return errInsecureRedirect
	}
	return nil
}

// limitedTransport caps the body of every response from base at limit bytes.
type limitedTransport struct {
	base  http.RoundTripper
	limit int64
}

// RoundTrip refuses a response whose declared length is over the cap, closing its body unread. Otherwise it wraps the
// body so that reading past the cap fails.
func (t limitedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > t.limit {
		_ = resp.Body.Close()
		return nil, errTooLarge
	}
	resp.Body = &cappedBody{ReadCloser: resp.Body, limit: t.limit}
	return resp, nil
}

// CloseIdleConnections forwards to base, so http.Client.CloseIdleConnections still reaches the transport.
func (t limitedTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// cappedBody reads at most limit bytes. It reads one byte past the limit to tell exactly limit (fine) from more (an
// error). Close is the underlying body's Close.
type cappedBody struct {
	io.ReadCloser
	limit int64
	read  int64
}

func (b *cappedBody) Read(p []byte) (int, error) {
	if b.read > b.limit {
		return 0, errTooLarge
	}
	if room := b.limit + 1 - b.read; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := b.ReadCloser.Read(p)
	b.read += int64(n)
	if b.read > b.limit {
		return 0, errTooLarge
	}
	return n, err
}

// githubConfig is the provider configuration: stable releases only, SHA256SUMS verification, our matcher and client.
// baseURL is empty in production; tests point it at httptest.
func githubConfig(baseURL string, client *http.Client) github.Config {
	return github.Config{
		Repository:    repository,
		Prerelease:    false,
		BaseURL:       baseURL,
		AssetMatcher:  assetMatcher,
		ChecksumAsset: checksumAsset,
		HTTPClient:    client,
	}
}

// updaterConfig is the framework configuration. There is no CheckInterval (no timer, no automatic download), no
// PublicKey (the release has no signature) and no window: the UI is ours.
func updaterConfig(currentVersion string, p updater.Provider) updater.Config {
	return updater.Config{
		CurrentVersion: currentVersion,
		Providers:      []updater.Provider{p},
		Window:         updater.WindowNone,
	}
}
