package db

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gjrud/steam-achievement-tracker/internal/appdata"
)

func TestSQLiteURIFile(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		windows bool
		want    string
	}{
		{
			name:    "windows drive letter",
			path:    `C:\Users\me\AppData\Local\steam-achievement-tracker\steam-achievement-tracker.db`,
			windows: true,
			want:    "file:///C:/Users/me/AppData/Local/steam-achievement-tracker/steam-achievement-tracker.db",
		},
		{
			name:    "windows forward slashes and lower-case drive",
			path:    `c:/Users/me/x.db`,
			windows: true,
			want:    "file:///c:/Users/me/x.db",
		},
		{
			name:    "windows space",
			path:    `C:\Users\John Doe\x.db`,
			windows: true,
			want:    "file:///C:/Users/John%20Doe/x.db",
		},
		{
			name:    "windows hash question percent",
			path:    `C:\data\a#b?c%d.db`,
			windows: true,
			want:    "file:///C:/data/a%23b%3Fc%25d.db",
		},
		{
			name:    "windows unicode",
			path:    `C:\Users\Zoë\x.db`,
			windows: true,
			want:    "file:///C:/Users/Zo%C3%AB/x.db",
		},
		{
			name:    "windows UNC",
			path:    `\\server\share\x.db`,
			windows: true,
			want:    "file:////server/share/x.db",
		},
		{
			name:    "windows UNC with special characters",
			path:    `\\server\my share\a#b.db`,
			windows: true,
			want:    "file:////server/my%20share/a%23b.db",
		},
		{
			name:    "linux absolute space hash question percent unicode",
			path:    "/home/u/steam data #1?/100%/ü.db",
			windows: false,
			want:    "file:///home/u/steam%20data%20%231%3F/100%25/%C3%BC.db",
		},
		{
			name:    "linux absolute plain",
			path:    "/home/u/.local/share/steam-achievement-tracker/steam-achievement-tracker.db",
			windows: false,
			want:    "file:///home/u/.local/share/steam-achievement-tracker/steam-achievement-tracker.db",
		},
		{
			name:    "linux backslash is an ordinary byte",
			path:    `/tmp/a\b.db`,
			windows: false,
			want:    "file:///tmp/a%5Cb.db",
		},
		{
			name:    "linux treats drive-looking path as relative with a literal backslash",
			path:    `C:\x.db`,
			windows: false,
			want:    "file:C:%5Cx.db",
		},
		{
			name:    "relative path uses plain file form",
			path:    "data/x y.db",
			windows: false,
			want:    "file:data/x%20y.db",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sqliteURIFile(tc.path, tc.windows); got != tc.want {
				t.Fatalf("sqliteURIFile(%q, %v) = %q, want %q", tc.path, tc.windows, got, tc.want)
			}
		})
	}
}

// TestSQLiteDSNForKeepsQueryParams checks that the escaped path never contains a raw '?' or '#', and that the
// query string keeps the busy timeout and foreign key parameters.
func TestSQLiteDSNForKeepsQueryParams(t *testing.T) {
	for _, tc := range []struct {
		path    string
		windows bool
	}{
		{`C:\a#b?c%d\x.db`, true},
		{"/a#b?c%d/x.db", false},
	} {
		dsn := sqliteDSNFor(tc.path, tc.windows)
		pos := strings.IndexByte(dsn, '?')
		if pos < 0 {
			t.Fatalf("dsn %q has no query string", dsn)
		}
		if strings.ContainsAny(dsn[:pos], "?#") {
			t.Fatalf("dsn path part %q contains a raw ? or #", dsn[:pos])
		}
		query, err := url.ParseQuery(dsn[pos+1:])
		if err != nil {
			t.Fatalf("parse query: %v", err)
		}
		if got := query.Get("_busy_timeout"); got != "5000" {
			t.Fatalf("_busy_timeout = %q, want 5000", got)
		}
		if got := query.Get("_foreign_keys"); got != "on" {
			t.Fatalf("_foreign_keys = %q, want on", got)
		}
	}
}

// TestOpenAndMigrateSpecialCharacterPath opens a database through the real driver at a path with a space, '#',
// '?', '%' and non-ASCII characters, then checks the file was created at that exact path. An unescaped '#' or '?'
// would cut the path and create a different file; an unescaped '%' would be decoded. The backup written next to
// it is opened through sqliteDSN as well.
func TestOpenAndMigrateSpecialCharacterPath(t *testing.T) {
	dirName := "my data #1 %41 ü"
	if runtime.GOOS != "windows" {
		// '?' is not allowed in Windows file names; the pure URI tests cover it there.
		dirName += " ?q"
	}
	dir := filepath.Join(t.TempDir(), dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbName := "steam achievement #1 %41 ü.db"
	if runtime.GOOS != "windows" {
		dbName = "steam achievement #1 ?q %41 ü.db"
	}
	paths := appdata.Paths{
		DataDir: dir,
		DB:      filepath.Join(dir, dbName),
		Backups: filepath.Join(dir, "backups"),
	}

	conn, err := OpenAndMigrate(paths)
	if err != nil {
		t.Fatalf("OpenAndMigrate(%q) error = %v", paths.DB, err)
	}
	defer conn.Close()
	assertUserVersion(t, conn, 14)

	if _, err := os.Stat(paths.DB); err != nil {
		t.Fatalf("database not created at the exact path: %v", err)
	}

	// Backups are read with os.ReadDir, not filepath.Glob: Glob treats '?' and '[' as patterns.
	if err := backupDB(conn, paths); err != nil {
		t.Fatalf("backupDB() error = %v", err)
	}
	entries, err := os.ReadDir(paths.Backups)
	if err != nil {
		t.Fatalf("read backups: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("backup entries = %d, want 1", len(entries))
	}
	backup, err := sql.Open("sqlite3", sqliteDSN(filepath.Join(paths.Backups, entries[0].Name())))
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backup.Close()
	assertUserVersion(t, backup, 14)
}
