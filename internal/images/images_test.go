package images

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
)

type statusTransport struct {
	status int
	count  atomic.Int32
}

type coverTransport struct {
	contentType   string
	body          string
	contentLength int64
}

func (t *statusTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.count.Add(1)
	return &http.Response{
		StatusCode: t.status,
		Status:     fmt.Sprintf("%d %s", t.status, http.StatusText(t.status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func (t coverTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Header:        http.Header{"Content-Type": {t.contentType}},
		Body:          io.NopCloser(strings.NewReader(t.body)),
		ContentLength: t.contentLength,
		Request:       req,
	}, nil
}

func TestRefreshCoverMissingCoverRetriesEachSync(t *testing.T) {
	transport := &statusTransport{status: http.StatusNotFound}
	cache, imagesDir := testCache(t, transport)
	appID := int64(123)

	res, err := cache.RefreshCover(context.Background(), appID, "")
	if err != nil {
		t.Fatalf("refresh missing cover: %v", err)
	}
	if res.Path != "" {
		t.Fatalf("missing cover path = %q, want empty", res.Path)
	}
	coverPath := filepath.Join(imagesDir, "123", coverFileName)
	if _, err := os.Stat(coverPath); !os.IsNotExist(err) {
		t.Fatalf("cover file stat error = %v, want not exist", err)
	}
	afterFirst := transport.count.Load()

	res, err = cache.RefreshCover(context.Background(), appID, "")
	if err != nil {
		t.Fatalf("refresh missing cover second time: %v", err)
	}
	if res.Path != "" {
		t.Fatalf("second missing cover path = %q, want empty", res.Path)
	}
	if got := transport.count.Load(); got <= afterFirst {
		t.Fatalf("request count after second refresh = %d, want > %d", got, afterFirst)
	}
}

func TestRefreshCoverStaleExistingFailureKeepsAndDefers(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			transport := &statusTransport{status: status}
			cache, imagesDir := testCache(t, transport)
			appID := int64(456)
			coverPath := filepath.Join(imagesDir, "456", coverFileName)
			if err := os.MkdirAll(filepath.Dir(coverPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(coverPath, []byte("old-cover"), 0o644); err != nil {
				t.Fatal(err)
			}
			stale := time.Now().Add(-31 * 24 * time.Hour)
			if err := os.Chtimes(coverPath, stale, stale); err != nil {
				t.Fatal(err)
			}

			res, err := cache.RefreshCover(context.Background(), appID, "")
			if err != nil {
				t.Fatalf("refresh stale existing cover: %v", err)
			}
			if res.Path != coverPath {
				t.Fatalf("cover path = %q, want %q", res.Path, coverPath)
			}
			if res.Downloaded {
				t.Fatal("downloaded = true, want false")
			}
			contents, err := os.ReadFile(coverPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(contents) != "old-cover" {
				t.Fatalf("cover contents = %q, want old-cover", contents)
			}
			info, err := os.Stat(coverPath)
			if err != nil {
				t.Fatal(err)
			}
			if !info.ModTime().After(stale) {
				t.Fatalf("cover modtime = %s, want after stale time %s", info.ModTime(), stale)
			}
			afterFirst := transport.count.Load()
			if afterFirst == 0 {
				t.Fatal("first refresh made no requests")
			}

			_, err = cache.RefreshCover(context.Background(), appID, "")
			if err != nil {
				t.Fatalf("refresh deferred cover: %v", err)
			}
			if got := transport.count.Load(); got != afterFirst {
				t.Fatalf("request count after deferred refresh = %d, want %d", got, afterFirst)
			}
		})
	}
}

func TestRefreshCoverRejectsUnsupportedContentType(t *testing.T) {
	cache, imagesDir := testCache(t, coverTransport{contentType: "text/plain", body: "not an image", contentLength: int64(len("not an image"))})
	appID := int64(789)

	_, err := cache.RefreshCover(context.Background(), appID, "")
	if err == nil {
		t.Fatal("refresh cover error = nil, want unsupported content type error")
	}
	coverPath := filepath.Join(imagesDir, "789", coverFileName)
	if _, err := os.Stat(coverPath); !os.IsNotExist(err) {
		t.Fatalf("cover file stat error = %v, want not exist", err)
	}
}

func TestRefreshCoverLimitsDownloadedBytes(t *testing.T) {
	cache, imagesDir := testCache(t, coverTransport{contentType: "image/jpeg", body: strings.Repeat("x", maxCoverBytes+1), contentLength: -1})
	appID := int64(790)

	_, err := cache.RefreshCover(context.Background(), appID, "")
	if err == nil {
		t.Fatal("refresh cover error = nil, want max bytes error")
	}
	coverPath := filepath.Join(imagesDir, "790", coverFileName)
	if _, err := os.Stat(coverPath); !os.IsNotExist(err) {
		t.Fatalf("cover file stat error = %v, want not exist", err)
	}
}

func testCache(t *testing.T, transport http.RoundTripper) (*Cache, string) {
	t.Helper()
	root := t.TempDir()
	imagesDir := filepath.Join(root, "cache", "images", "games")
	return &Cache{
		Paths: appdata.Paths{GameImages: imagesDir},
		HTTPClient: &http.Client{
			Transport: transport,
		},
	}, imagesDir
}
