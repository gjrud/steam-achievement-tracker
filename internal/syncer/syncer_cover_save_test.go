package syncer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
	"github.com/gjrud/steam-achievement-tracker/internal/images"
	"github.com/gjrud/steam-achievement-tracker/internal/steam"
)

// blockCoverWrites makes every write to cover_images fail, the way a broken database would.
func blockCoverWrites(t *testing.T, paths appdata.Paths) {
	t.Helper()
	addTriggers(t, paths,
		`create trigger block_cover_insert before insert on cover_images begin select raise(abort, 'cover blocked'); end`,
		`create trigger block_cover_update before update on cover_images begin select raise(abort, 'cover blocked'); end`,
	)
}

// writeStoredCover puts a cover file on disk the way writeCover would, and returns its path.
func writeStoredCover(t *testing.T, paths appdata.Paths, imageAppID int64, fileName, body string) string {
	t.Helper()
	dir := filepath.Join(paths.GameImages, fmt.Sprintf("%d", imageAppID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A newly downloaded cover under a new name is removed when its row cannot be saved, and the save error is returned.
func TestSaveCoverRemovesNewDownloadWhenSaveFails(t *testing.T) {
	const appID = 730
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	svc := &Service{Repo: repo, Images: images.New(paths)}
	path := writeStoredCover(t, paths, appID, images.LibraryFileName, "new-bytes")
	blockCoverWrites(t, paths)
	logs := captureLog(t)

	err := svc.saveCover(appID, images.Result{
		Kind: images.KindLibrary, FileName: images.LibraryFileName, Path: path,
		SourceURL: "https://shared.akamai.steamstatic.com/x", Downloaded: true, Changed: true,
	})
	if err == nil || !strings.Contains(err.Error(), "cover blocked") {
		t.Fatalf("saveCover error = %v, want the SaveCover error", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("cover file stat error = %v, want removed after failed save", statErr)
	}
	if strings.Contains(logs.String(), "remove cover") {
		t.Fatalf("log = %q, want no removal warning when removal succeeds", logs.String())
	}
}

// A download under the stored name overwrote the file the row still points at, so it is kept.
func TestSaveCoverKeepsSameNameDownloadWhenSaveFails(t *testing.T) {
	const appID = 731
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	svc := &Service{Repo: repo, Images: images.New(paths)}
	path := writeStoredCover(t, paths, appID, images.HeaderFileName, "refreshed-bytes")
	blockCoverWrites(t, paths)

	err := svc.saveCover(appID, images.Result{
		Kind: images.KindHeader, FileName: images.HeaderFileName, Path: path,
		Downloaded: true, Changed: false,
	})
	if err == nil {
		t.Fatal("saveCover error = nil, want the SaveCover error")
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "refreshed-bytes" {
		t.Fatalf("cover file = %q err=%v, want kept after failed save", got, readErr)
	}
}

// A kept file (not downloaded in this run) is never removed, even when its row cannot be saved.
func TestSaveCoverKeepsPreviousFileWhenNothingDownloaded(t *testing.T) {
	const appID = 732
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	svc := &Service{Repo: repo, Images: images.New(paths)}
	path := writeStoredCover(t, paths, appID, images.MainFileName, "kept-bytes")
	blockCoverWrites(t, paths)

	err := svc.saveCover(appID, images.Result{
		Kind: images.KindMain, FileName: images.MainFileName, Path: path, Downloaded: false, Changed: true,
	})
	if err == nil {
		t.Fatal("saveCover error = nil, want the SaveCover error")
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != "kept-bytes" {
		t.Fatalf("cover file = %q err=%v, want kept", got, readErr)
	}
}

// Full sync: a cover downloaded for the first time is removed when its row write fails, and the run still succeeds.
func TestFullSyncRemovesNewCoverWhenRowWriteFails(t *testing.T) {
	const appID = 733
	paths := testPaths(t)
	repo := openTestRepository(t, paths)
	profile := testProfile(t, repo)
	fake := newFakeSteamServer(t, []steam.OwnedGame{{AppID: appID, Name: "Cover Cleanup Game", PlaytimeForever: 10, CapsuleFilename: images.LibraryFileName}})
	fake.images[fmt.Sprintf("%d/%s", appID, images.LibraryFileName)] = true
	svc := serviceWithImages(t, repo, fake, paths)
	blockCoverWrites(t, paths)
	logs := captureLog(t)

	requireSyncResult(t, runServiceOK(t, svc, profile), expectedSyncResult{status: "success", total: 1, synced: 1, failed: 0})
	if !strings.Contains(logs.String(), fmt.Sprintf("refresh cover %d:", appID)) {
		t.Fatalf("log = %q, want cover save failure", logs.String())
	}
	stored := filepath.Join(paths.GameImages, fmt.Sprintf("%d", appID), images.LibraryFileName)
	if _, err := os.Stat(stored); !os.IsNotExist(err) {
		t.Fatalf("cover file stat error = %v, want removed after failed row write", err)
	}
	if _, ok, err := repo.GetCover(appID); err != nil || ok {
		t.Fatalf("cover row: ok=%v err=%v, want none after failed write", ok, err)
	}
}
