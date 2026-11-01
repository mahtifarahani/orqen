// Package semver parses and compares the dotted versions printed by command-line tools.
package semver

import (
	"regexp"
	"strconv"
)

type Version struct{ Major, Minor, Patch int }

var pattern = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// Find extracts the first x.y or x.y.z from text such as "codex-cli 0.159.2" or "v1.5.0".
func Find(s string) (Version, bool) {
	m := pattern.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
	return Version{n(1), n(2), n(3)}, true
}
func (v Version) String() string {
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
}

// Compare returns -1, 0 or 1.
func (v Version) Compare(o Version) int {
	for _, p := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// AtLeast reports whether v >= min.
func (v Version) AtLeast(min Version) bool { return v.Compare(min) >= 0 }

// InWindow reports whether lo <= v < hi.
func (v Version) InWindow(lo, hi Version) bool { return v.AtLeast(lo) && v.Compare(hi) < 0 }
