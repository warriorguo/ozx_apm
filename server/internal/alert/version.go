package alert

import (
	"strconv"
	"strings"
	"time"
)

// Version order (APM-8). app_version is whatever the game sets
// (Application.version): "1.4.2", "2.0.0-beta.1", "apmtest-2.0.0", "v1.3".
//
// A version name is read as: a prefix (everything before the first digit,
// lower-cased; "v" alone counts as no prefix), dot-separated numbers
// ("1.4.2"; missing numbers are 0, so "1.4" = "1.4.0"), then an optional
// pre-release ("-beta.1", or any other text right after the numbers such as
// "rc1") and build metadata ("+57", ignored). Two names are comparable when
// both have numbers and the same prefix; they are then ordered like semantic
// versions: by the numbers, and a pre-release before its release
// ("2.0.0-beta.1" < "2.0.0"), pre-releases by their dot-separated parts
// (numbers numerically, text lexically).
//
// Names that are not comparable (no digits, different prefixes such as
// "apmtest-2.0.0" vs "2.0.0") fall back to time: the version first seen
// later (earliest startup in the retained data) is the newer one.

type parsedVersion struct {
	prefix string
	nums   []int
	pre    []string
	ok     bool
}

func parseVersion(s string) parsedVersion {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, "0123456789")
	if i < 0 {
		return parsedVersion{}
	}
	prefix := strings.ToLower(s[:i])
	if prefix == "v" {
		prefix = ""
	}
	p := parsedVersion{prefix: prefix, ok: true}
	rest := s[i:]
	// Numbers and dots.
	j := 0
	for j < len(rest) && (rest[j] == '.' || (rest[j] >= '0' && rest[j] <= '9')) {
		j++
	}
	for _, part := range strings.Split(strings.Trim(rest[:j], "."), ".") {
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			n = int(^uint(0) >> 1) // absurdly long: treat as the largest
		}
		p.nums = append(p.nums, n)
	}
	suffix := rest[j:]
	if k := strings.IndexByte(suffix, '+'); k >= 0 {
		suffix = suffix[:k] // build metadata
	}
	suffix = strings.TrimLeft(suffix, "-_ ")
	if suffix != "" {
		p.pre = strings.Split(strings.ToLower(suffix), ".")
	}
	return p
}

func comparePre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1 // a release is newer than its pre-releases
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		na, ea := strconv.Atoi(a[i])
		nb, eb := strconv.Atoi(b[i])
		switch {
		case ea == nil && eb == nil:
			if na != nb {
				return cmpInt(na, nb)
			}
		case ea == nil:
			return -1 // numeric identifiers sort before text (semver)
		case eb == nil:
			return 1
		default:
			if c := strings.Compare(a[i], b[i]); c != 0 {
				return c
			}
		}
	}
	return cmpInt(len(a), len(b))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// CompareVersions orders two version names: -1, 0 or 1, and false when the
// names cannot be compared (see above; the caller then falls back to time).
func CompareVersions(a, b string) (int, bool) {
	if a == b {
		return 0, true
	}
	pa, pb := parseVersion(a), parseVersion(b)
	if !pa.ok || !pb.ok || pa.prefix != pb.prefix {
		return 0, false
	}
	n := len(pa.nums)
	if len(pb.nums) > n {
		n = len(pb.nums)
	}
	for i := 0; i < n; i++ {
		var x, y int
		if i < len(pa.nums) {
			x = pa.nums[i]
		}
		if i < len(pb.nums) {
			y = pb.nums[i]
		}
		if x != y {
			return cmpInt(x, y), true
		}
	}
	return comparePre(pa.pre, pb.pre), true
}

// VersionOrder orders versions by name, falling back to when each was first
// seen.
type VersionOrder struct {
	// FirstSeen: the earliest time each version was seen (may be partial).
	FirstSeen map[string]time.Time
}

// Compare returns -1, 0 or 1. Names that cannot be compared are ordered by
// first-seen time; a version without one counts as seen at unknown (the
// caller passes the time to use).
func (o VersionOrder) Compare(a, b string, unknownA, unknownB time.Time) int {
	if c, ok := CompareVersions(a, b); ok {
		return c
	}
	ta, ok := o.FirstSeen[a]
	if !ok {
		ta = unknownA
	}
	tb, ok := o.FirstSeen[b]
	if !ok {
		tb = unknownB
	}
	switch {
	case ta.Before(tb):
		return -1
	case ta.After(tb):
		return 1
	}
	return strings.Compare(a, b)
}

// Newest returns the newest of the versions ("" for none). unknown is the
// first-seen time used for a version the order has no time for.
func (o VersionOrder) Newest(versions []string, unknown time.Time) string {
	newest := ""
	for i, v := range versions {
		if i == 0 || o.Compare(v, newest, unknown, unknown) > 0 {
			newest = v
		}
	}
	return newest
}
