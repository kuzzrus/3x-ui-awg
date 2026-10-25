// Package version compares 3x-ui release versions ("v3.8.0" or "3.8.0").
package version

import (
	"regexp"
	"strconv"
	"strings"
)

// Compare returns -1, 0 or 1 as a is older than, equal to or newer than b, and
// false when either side is not a plain MAJOR.MINOR.PATCH version or this
// fork's MAJOR.MINOR.PATCH-awg.N release of one.
func Compare(a, b string) (int, bool) {
	aParts, okA := parse(a)
	bParts, okB := parse(b)
	if !okA || !okB {
		return 0, false
	}
	for i := range len(aParts) {
		if aParts[i] > bParts[i] {
			return 1, true
		}
		if aParts[i] < bParts[i] {
			return -1, true
		}
	}
	return 0, true
}

// Normalize strips surrounding space and a leading "v" from a version tag.
func Normalize(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// partsPattern matches a plain "X.Y.Z" tag as well as this fork's own
// "X.Y.Z-awg.N" release tags (see internal/config/version). The awg build
// number is a 4th, lower-priority component that defaults to 0 for a plain
// upstream-style tag, so e.g. "3.5.0-awg.2" > "3.5.0-awg.1" > "3.5.0".
var partsPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:-awg\.(\d+))?$`)

func parse(v string) ([4]int, bool) {
	var result [4]int
	matches := partsPattern.FindStringSubmatch(Normalize(v))
	if matches == nil {
		return result, false
	}
	for i, part := range matches[1:] {
		if part == "" {
			continue // awg build number omitted: defaults to 0
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return result, false
		}
		result[i] = n
	}
	return result, true
}
