package db

import (
	"fmt"
	"runtime"
	"strings"
)

// sqliteParams are the go-sqlite3 query parameters every connection uses.
const sqliteParams = "_busy_timeout=5000&_foreign_keys=on"

// sqliteDSN returns the go-sqlite3 DSN for the database file at path on the running OS.
func sqliteDSN(path string) string {
	return sqliteDSNFor(path, runtime.GOOS == "windows")
}

// sqliteDSNFor builds the DSN for path with Windows path rules when windows is true.
//
// go-sqlite3 hands a "file:" DSN to sqlite3_open_v2 unchanged, with SQLITE_OPEN_URI set, so SQLite reads the
// query string itself and everything before the first '?' is the path. The path therefore has to be escaped:
// a raw '?' or '#' would end it early, and a raw '%' would be decoded.
func sqliteDSNFor(path string, windows bool) string {
	return sqliteURIFile(path, windows) + "?" + sqliteParams
}

// sqliteURIFile converts a filesystem path to a SQLite "file:" URI, following the conversion steps in
// https://www.sqlite.org/uri.html#uriconv:
//   - on Windows, "\" becomes "/" and a drive-letter path gets a leading "/" ("C:/x" -> "/C:/x");
//   - an absolute path (leading "/", or a UNC path "//server/share" on Windows) uses the "file://" form with an
//     empty authority, so "/C:/x" gives "file:///C:/x" and a UNC path gives "file:////server/share/x";
//   - a relative path uses the "file:" form;
//   - every byte except ASCII letters, digits and "-._~/:" is percent-encoded. That covers '%', '?', '#',
//     spaces, control characters and all non-ASCII UTF-8 bytes.
func sqliteURIFile(path string, windows bool) string {
	p := path
	if windows {
		p = strings.ReplaceAll(p, `\`, "/")
		if hasDriveLetter(p) {
			p = "/" + p
		}
	}
	if strings.HasPrefix(p, "/") {
		return "file://" + escapeURIPath(p)
	}
	return "file:" + escapeURIPath(p)
}

// hasDriveLetter reports whether p starts with "X:/" for an ASCII letter X.
func hasDriveLetter(p string) bool {
	if len(p) < 3 || p[1] != ':' || p[2] != '/' {
		return false
	}
	c := p[0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// escapeURIPath percent-encodes p for use in the path part of a SQLite URI. "/" and ":" are kept so that
// separators and drive letters stay readable.
func escapeURIPath(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
		case c == '-', c == '.', c == '_', c == '~', c == '/', c == ':':
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}
