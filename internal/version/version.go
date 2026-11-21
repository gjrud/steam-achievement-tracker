// Package version holds the app version stamped at link time and the rule that turns the stamp into a version the
// updater may use. The stamp is written by -ldflags in the production build tasks (build/linux, build/windows):
//
//	-X github.com/gjrud/steam-achievement-tracker/internal/version.tag=sat-version:X.Y.Z;
//
// tag has no initializer on purpose: an unstamped build (dev, Docker, untagged local) keeps it empty and the updater
// stays unavailable.
package version

import (
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

// tag is the stamped marker. Set only by -X; see the package comment.
var tag string

const (
	stampPrefix = "sat-version:"
	stampSuffix = ";"
)

// fullVersion is X.Y.Z with an optional -pre part. semver.IsValid alone also accepts shorthand such as "1" or "1.2",
// so the shape is checked first.
var fullVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

// Current returns the stamped version without a "v" prefix, and whether the stamp is usable.
func Current() (string, bool) {
	return ParseStamp(tag)
}

// ParseStamp turns a stamp such as "sat-version:0.3.0;" into "0.3.0". It returns false for an empty, unlabeled,
// unterminated, partial, "v"-prefixed or otherwise invalid stamp.
func ParseStamp(stamp string) (string, bool) {
	if !strings.HasPrefix(stamp, stampPrefix) || !strings.HasSuffix(stamp, stampSuffix) {
		return "", false
	}
	return Valid(stamp[len(stampPrefix) : len(stamp)-len(stampSuffix)])
}

// Valid reports whether v is a full X.Y.Z[-pre] version without a "v" prefix. It returns v when it is valid.
func Valid(v string) (string, bool) {
	if !fullVersion.MatchString(v) || !semver.IsValid("v"+v) {
		return "", false
	}
	return v, true
}

// IsPrerelease reports whether a valid version v carries a -pre part.
func IsPrerelease(v string) bool {
	return strings.Contains(v, "-")
}
