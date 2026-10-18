package alert

import (
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"1.4.2", "1.4.2", 0, true},
		{"1.4.10", "1.4.9", 1, true},
		{"1.10", "1.9.9", 1, true},
		{"1.4", "1.4.0", 0, true},
		{"2.0.0", "10.0.0", -1, true},
		{"v1.3", "1.3.0", 0, true},
		{"V2", "1.9", 1, true},
		{"2.0.0-beta.1", "2.0.0", -1, true},
		{"2.0.0-beta.2", "2.0.0-beta.10", -1, true},
		{"2.0.0-alpha", "2.0.0-beta", -1, true},
		{"2.0.0-rc.1", "2.0.0-1", 1, true}, // numeric identifiers sort first
		{"2.0.0rc1", "2.0.0", -1, true},
		{"2.0.0+57", "2.0.0+58", 0, true},
		{"apmtest-2.0.0", "apmtest-1.9.0", 1, true},
		{"APMTEST-2.0.0", "apmtest-2.0.0", 0, true},
		{"apmtest-2.0.0", "2.0.0", 0, false},
		{"release", "1.0", 0, false},
		{"", "1.0", 0, false},
		{"1.0 (123)", "1.0 (124)", -1, true},
		{"99999999999999999999.0", "1.0", 1, true},
	}
	for _, c := range cases {
		got, ok := CompareVersions(c.a, c.b)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("CompareVersions(%q, %q) = %d, %v; want %d, %v", c.a, c.b, got, ok, c.want, c.ok)
		}
		if ok {
			if rev, _ := CompareVersions(c.b, c.a); rev != -got {
				t.Errorf("not antisymmetric: %q %q", c.a, c.b)
			}
		}
	}
}

func TestVersionOrder_FallsBackToFirstSeen(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	o := VersionOrder{FirstSeen: map[string]time.Time{
		"apmtest-2.0.0": now.Add(-48 * time.Hour),
		"2.0.0":         now.Add(-24 * time.Hour),
	}}
	if c := o.Compare("2.0.0", "apmtest-2.0.0", now, now); c != 1 {
		t.Errorf("first seen later must be newer: %d", c)
	}
	if c := o.Compare("1.0.0", "2.0.0", now, now); c != -1 {
		t.Errorf("names win when comparable: %d", c)
	}
	// Unknown first seen: the caller's time.
	if c := o.Compare("nightly", "apmtest-2.0.0", now, now); c != 1 {
		t.Errorf("unknown version at now: %d", c)
	}
	if n := o.Newest([]string{"1.9.0", "2.0.0", "apmtest-2.0.0", "1.10.0"}, now); n != "2.0.0" {
		t.Errorf("newest %q", n)
	}
	if n := o.Newest(nil, now); n != "" {
		t.Errorf("newest of none %q", n)
	}
}
