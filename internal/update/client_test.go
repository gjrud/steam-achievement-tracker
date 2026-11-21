package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/github"
)

// stubGitHub serves the GitHub release API and asset downloads from memory. Asset URLs point back at the stub, so no
// request leaves the test.
type stubGitHub struct {
	tag        string
	withSums   bool
	sumsStatus int
	latestCode int
	bodies     map[string][]byte
	srv        *httptest.Server
}

func newStubGitHub(t *testing.T, s *stubGitHub) *httptest.Server {
	t.Helper()
	s.bodies = map[string][]byte{}
	for _, name := range realAssets {
		if name == "SHA256SUMS" {
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
			for _, name := range realAssets {
				if name == "SHA256SUMS" && !s.withSums {
					continue
				}
				assets = append(assets, map[string]any{
					"id": len(assets) + 1, "name": name, "content_type": "application/octet-stream",
					"size": len(s.bodies[name]), "url": base + "/dl/" + name,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": s.tag, "name": s.tag, "body": "notes", "prerelease": false, "draft": false,
				"published_at": "2026-01-01T00:00:00Z", "html_url": "https://github.com/" + repository + "/releases/tag/" + s.tag,
				"assets": assets,
			})
		case r.URL.Path == "/dl/SHA256SUMS":
			if s.sumsStatus != 0 {
				w.WriteHeader(s.sumsStatus)
				return
			}
			var b strings.Builder
			for _, name := range realAssets {
				if name == "SHA256SUMS" {
					continue
				}
				sum := sha256.Sum256(s.bodies[name])
				fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
			}
			_, _ = w.Write([]byte(b.String()))
		case strings.HasPrefix(r.URL.Path, "/dl/"):
			body, ok := s.bodies[strings.TrimPrefix(r.URL.Path, "/dl/")]
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

func TestProviderSelectsWindowsPortable(t *testing.T) {
	stub := &stubGitHub{tag: "v0.3.0", withSums: true}
	srv := newStubGitHub(t, stub)
	rel, err := providerFor(t, srv.URL).Check(context.Background(), updater.CheckRequest{CurrentVersion: "0.2.0", Platform: "windows", Arch: "amd64"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel == nil || !strings.HasSuffix(rel.Artifact.Filename, windowsPortableSuffix) {
		t.Fatalf("release = %+v", rel)
	}
	if refusal := installRefusal(rel.Verification, hostEnv{goos: "windows"}); refusal != "" {
		t.Fatalf("windows portable refused: %q", refusal)
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
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T", c.Transport)
	}
	if tr.ResponseHeaderTimeout != responseHeaderTimeout || tr.TLSHandshakeTimeout != tlsHandshakeTimeout || tr.DialContext == nil {
		t.Fatal("dial, TLS and response-header timeouts must be set")
	}
	if checkTimeout <= 0 || checkTimeout > 2*time.Minute {
		t.Fatalf("checkTimeout = %v", checkTimeout)
	}
}
