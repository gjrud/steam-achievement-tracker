package update

import (
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

// startupDelay keeps the background check off the critical path of app start. A variable so tests can set it large.
var startupDelay = 5 * time.Second

// newHTTPClient returns the client for the GitHub provider and the asset download.
func newHTTPClient() *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   tlsHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{Transport: tr}
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
