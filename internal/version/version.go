// Package version reports the running Recall release.
package version

import (
	"regexp"
	"strconv"
	"strings"
)

// Version is the release version without its "v" prefix. Release builds set it
// with -ldflags "-X recall/internal/version.Version=<release>"; source and
// local builds report "dev".
var Version = "dev"

// The same grammar scripts/release-macos.py accepts for tags (after "v"):
// MAJOR.MINOR.PATCH, optional SemVer prerelease, no build metadata.
var grammar = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-((?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*))?$`)

// SemVer is a parsed release version.
type SemVer struct {
	Major, Minor, Patch uint64
	Prerelease          []string
}

// Parse accepts a release version without "v".
func Parse(s string) (SemVer, bool) {
	m := grammar.FindStringSubmatch(s)
	if m == nil {
		return SemVer{}, false
	}
	var v SemVer
	for i, target := range []*uint64{&v.Major, &v.Minor, &v.Patch} {
		n, err := strconv.ParseUint(m[i+1], 10, 64)
		if err != nil {
			return SemVer{}, false
		}
		*target = n
	}
	if m[4] != "" {
		v.Prerelease = strings.Split(m[4], ".")
	}
	return v, true
}

// Stable reports whether v has no prerelease identifiers.
func (v SemVer) Stable() bool { return len(v.Prerelease) == 0 }

// Development reports whether the running build is not a versioned release.
func Development() bool {
	_, ok := Parse(Version)
	return !ok
}

// Compare returns -1, 0 or 1 by SemVer 2.0 precedence.
func Compare(a, b SemVer) int {
	for _, pair := range [][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.Stable() && b.Stable():
		return 0
	case a.Stable():
		return 1
	case b.Stable():
		return -1
	}
	for i := 0; i < len(a.Prerelease) && i < len(b.Prerelease); i++ {
		if c := compareIdentifier(a.Prerelease[i], b.Prerelease[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(a.Prerelease) < len(b.Prerelease):
		return -1
	case len(a.Prerelease) > len(b.Prerelease):
		return 1
	}
	return 0
}

func numeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Numeric identifiers have no leading zeros (enforced by grammar), so length
// then lexical order is numeric order without overflow.
func compareIdentifier(a, b string) int {
	an, bn := numeric(a), numeric(b)
	switch {
	case an && bn:
		if len(a) != len(b) {
			if len(a) < len(b) {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}
