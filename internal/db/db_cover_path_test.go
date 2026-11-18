package db

import (
	"bytes"
	"os"
	"testing"
)

var coverImagesColumns = []string{"image_appid", "kind", "file_name", "source_url", "checked_at", "downloaded_at", "cached"}

// Migration 2 drops the absolute cover path a version 1 database stored (it holds the OS user name), keeps only a
// cached flag, and compacts the files so the old bytes are gone. The pre-migration backup is the one place that
// still holds the path; it is not checked here.
func TestCoverPathMigrationDropsPathAndCompactsFiles(t *testing.T) {
	const oldPath = "/home/john.doe/.cache/steam-achievement-tracker/images/games/42/library_600x900.jpg"
	paths := testPaths(t)
	seedV1Database(t, paths.DB)
	raw := openRawDB(t, paths.DB)
	if _, err := raw.Exec(`INSERT INTO cover_images(image_appid, kind, file_name, path, source_url, checked_at, downloaded_at) VALUES
		(42, 'library', 'library_600x900.jpg', ?, 'https://example.com/42.jpg', '2026-01-02T00:00:00Z', '2026-01-02T00:00:00Z'),
		(43, 'library', 'library_600x900.jpg', NULL, 'https://example.com/43.jpg', '2026-01-02T00:00:00Z', NULL),
		(44, 'library', 'library_600x900.jpg', '  ', 'https://example.com/44.jpg', '2026-01-02T00:00:00Z', NULL)`, oldPath); err != nil {
		t.Fatalf("seed cover rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate() error = %v", err)
	}
	defer conn.Close()

	assertUserVersion(t, conn, 3)
	assertMigration(t, conn, 2)
	assertMigration(t, conn, 3)
	assertColumns(t, conn, "cover_images", coverImagesColumns)
	assertCount(t, conn, `select count(*) from cover_images where image_appid=42 and cached=1 and source_url='https://example.com/42.jpg'`, 1)
	assertCount(t, conn, `select count(*) from cover_images where image_appid=43 and cached=0`, 1)
	// A whitespace-only path is not a stored file: the backfill trims it, so it must not count as cached.
	assertCount(t, conn, `select count(*) from cover_images where image_appid=44 and cached=0`, 1)

	for _, name := range []string{paths.DB, paths.DB + "-wal"} {
		data, err := os.ReadFile(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if bytes.Contains(data, []byte(oldPath)) {
			t.Fatalf("%s still holds the absolute cover path after migration", name)
		}
	}
}
