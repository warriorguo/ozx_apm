// Package timeline builds a session's time line (APM-15) from the rows the
// storage layer read: it turns the breadcrumb lines that exceptions, crashes
// and janks carry into items with a full time, drops the lines that appear
// several times or duplicate a row that is itself on the time line, computes
// context changes and orders everything. Pure functions, no I/O.
package timeline

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

const dayMs = 24 * 60 * 60 * 1000

// clockMs parses "HH:mm:ss.fff" into milliseconds of the day.
func clockMs(s string) (int64, bool) {
	if len(s) != 12 || s[2] != ':' || s[5] != ':' || s[8] != '.' {
		return 0, false
	}
	num := func(t string) (int64, bool) {
		var n int64
		for i := 0; i < len(t); i++ {
			if t[i] < '0' || t[i] > '9' {
				return 0, false
			}
			n = n*10 + int64(t[i]-'0')
		}
		return n, true
	}
	h, ok1 := num(s[0:2])
	m, ok2 := num(s[3:5])
	sec, ok3 := num(s[6:8])
	f, ok4 := num(s[9:12])
	if !ok1 || !ok2 || !ok3 || !ok4 || h > 23 || m > 59 || sec > 59 {
		return 0, false
	}
	return ((h*60+m)*60+sec)*1000 + f, true
}

// clockTolerance: a breadcrumb time-of-day up to this much after the
// carrying row's time is taken as the same day (the row's time and the
// line's come from the same device clock, but the row's time may be the
// first-seen time of a count).
const clockTolerance = 2000

// resolveClock returns the latest instant at or before ref (+ tolerance)
// whose UTC time of day is ms.
func resolveClock(ms int64, ref time.Time) time.Time {
	refMs := ref.UnixMilli()
	dayStart := refMs - ((refMs%dayMs)+dayMs)%dayMs
	t := dayStart + ms
	if t > refMs+clockTolerance {
		t -= dayMs
	}
	return time.UnixMilli(t).UTC()
}

// ParseBreadcrumb turns one line as the SDK formats it,
// "[HH:mm:ss.fff] Kind: text", into an item. carrier is the time of the row
// that carried the line; the line's date is resolved from it (the line is
// at or before it). A line without a time gets the carrier's time and
// TimeUnknown. source is the type of the carrying row.
func ParseBreadcrumb(line string, carrier time.Time, source string) models.TimelineItem {
	it := models.TimelineItem{Type: models.TimelineBreadcrumb, Source: source, Time: carrier.UTC()}
	text := line
	if len(line) >= 15 && line[0] == '[' && line[13] == ']' {
		if ms, ok := clockMs(line[1:13]); ok {
			it.Time = resolveClock(ms, carrier)
			text = strings.TrimPrefix(line[14:], " ")
		} else {
			it.TimeUnknown = true
		}
	} else {
		it.TimeUnknown = true
	}
	it.Kind, it.Text = models.CrumbText, text
	for _, p := range crumbPrefixes {
		if strings.HasPrefix(text, p.prefix) {
			it.Kind, it.Text = p.kind, text[len(p.prefix):]
			break
		}
	}
	if it.Kind == models.CrumbWarning || it.Kind == models.CrumbError || it.Kind == models.CrumbAssert {
		if body, n, until, ok := splitRepeat(it.Text); ok {
			it.Text, it.Repeats = body, n
			// The last one is at or after the first, within a day.
			first := it.Time.UnixMilli()
			delta := ((until-((first%dayMs)+dayMs)%dayMs)%dayMs + dayMs) % dayMs
			u := time.UnixMilli(first + delta).UTC()
			it.RepeatUntil = &u
		}
	}
	return it
}

var crumbPrefixes = []struct{ prefix, kind string }{
	{"Event: ", models.CrumbEvent},
	{"Scene: ", models.CrumbScene},
	{"Warning: ", models.CrumbWarning},
	{"Error: ", models.CrumbError},
	{"Assert: ", models.CrumbAssert},
	{"Exception: ", models.CrumbException},
}

// splitRepeat splits a log line counted by the SDK, "text (x37 until
// 12:00:05.120)", into the text, the count and the clock of the last one.
func splitRepeat(s string) (string, int64, int64, bool) {
	const tail = len(" until 00:00:00.000)")
	if !strings.HasSuffix(s, ")") || len(s) < tail+4 {
		return s, 0, 0, false
	}
	until, ok := clockMs(s[len(s)-13 : len(s)-1])
	if !ok || s[len(s)-tail:len(s)-13] != " until " {
		return s, 0, 0, false
	}
	head := s[:len(s)-tail]
	i := strings.LastIndex(head, " (x")
	if i < 0 {
		return s, 0, 0, false
	}
	n, err := strconv.ParseInt(head[i+3:], 10, 64)
	if err != nil || n < 2 {
		return s, 0, 0, false
	}
	return head[:i], n, until, true
}

// dedupeKeyRunes: lines are compared on this many characters, because the
// same line is cut to different lengths by different carriers (exceptions
// keep 200/300 characters, janks 200).
const dedupeKeyRunes = 160

func crumbKey(it *models.TimelineItem) string {
	text := it.Text
	if utf8.RuneCountInString(text) > dedupeKeyRunes {
		n := 0
		for i := range text {
			if n == dedupeKeyRunes {
				text = text[:i]
				break
			}
			n++
		}
	}
	return strconv.FormatInt(it.Time.UnixMilli(), 10) + "\x00" + it.Kind + "\x00" + text
}

// DedupeBreadcrumbs keeps one item per line: the same line (time, kind and
// text) carried by several rows is kept once, with the highest repeat count
// and the longest text. Lines without a time are kept as they are. Returns
// the kept items and how many were dropped.
func DedupeBreadcrumbs(items []models.TimelineItem) ([]models.TimelineItem, int) {
	out := make([]models.TimelineItem, 0, len(items))
	index := map[string]int{}
	dropped := 0
	for _, it := range items {
		if it.TimeUnknown {
			out = append(out, it)
			continue
		}
		k := crumbKey(&it)
		if i, ok := index[k]; ok {
			dropped++
			kept := &out[i]
			if it.Repeats > kept.Repeats || (it.Repeats == kept.Repeats && len(it.Text) > len(kept.Text)) {
				out[i] = it
			}
			continue
		}
		index[k] = len(out)
		out = append(out, it)
	}
	return out, dropped
}

// coverTolerance is how far apart in time a breadcrumb and the row it
// duplicates may be: the SDK records a custom event's breadcrumb and the
// event, or an exception's, with the same clock reading; a scene's
// breadcrumb and its scene_load event come from different code.
func coverTolerance(kind string) time.Duration {
	if kind == models.CrumbScene {
		return 5 * time.Second
	}
	return time.Second
}

// covers reports whether row is the event the breadcrumb was recorded for.
func covers(crumb, row *models.TimelineItem) bool {
	d := crumb.Time.Sub(row.Time)
	if d < 0 {
		d = -d
	}
	if d > coverTolerance(crumb.Kind) {
		return false
	}
	switch crumb.Kind {
	case models.CrumbEvent:
		name, _, _ := strings.Cut(crumb.Text, " ")
		return row.Type == models.TimelineCustomEvent && !row.Aggregated && row.Name == name
	case models.CrumbScene:
		return row.Type == models.TimelineSceneLoad && row.SceneName == crumb.Text
	case models.CrumbException:
		if row.Type != models.TimelineException {
			return false
		}
		first, _, _ := strings.Cut(row.Message, "\n")
		first = strings.TrimSpace(first)
		c := strings.TrimSpace(crumb.Text)
		return c != "" && (strings.HasPrefix(first, c) || strings.HasPrefix(c, first) && first != "")
	}
	return false
}

// DropCovered removes the breadcrumbs that duplicate a row on the time line
// (an individually sent custom event, an exception, a scene load), so each
// thing that happened shows once. Returns the kept breadcrumbs and how many
// were dropped.
func DropCovered(crumbs, rows []models.TimelineItem) ([]models.TimelineItem, int) {
	byTime := make([]*models.TimelineItem, 0, len(rows))
	for i := range rows {
		switch rows[i].Type {
		case models.TimelineCustomEvent, models.TimelineSceneLoad, models.TimelineException:
			byTime = append(byTime, &rows[i])
		}
	}
	sort.SliceStable(byTime, func(i, j int) bool { return byTime[i].Time.Before(byTime[j].Time) })
	out := crumbs[:0:0]
	dropped := 0
	for i := range crumbs {
		c := &crumbs[i]
		covered := false
		if !c.TimeUnknown && (c.Kind == models.CrumbEvent || c.Kind == models.CrumbScene || c.Kind == models.CrumbException) {
			lo := c.Time.Add(-coverTolerance(c.Kind))
			k := sort.Search(len(byTime), func(j int) bool { return !byTime[j].Time.Before(lo) })
			for ; k < len(byTime) && !byTime[k].Time.After(c.Time.Add(coverTolerance(c.Kind))); k++ {
				if covers(c, byTime[k]) {
					covered = true
					break
				}
			}
		}
		if covered {
			dropped++
			continue
		}
		out = append(out, *c)
	}
	return out, dropped
}

// Diff returns the changes from prev to cur, by key.
func Diff(prev, cur map[string]string) []models.ContextChange {
	var changes []models.ContextChange
	for k, v := range cur {
		if old, ok := prev[k]; !ok || old != v {
			changes = append(changes, models.ContextChange{Key: k, From: prev[k], To: v})
		}
	}
	for k, old := range prev {
		if _, ok := cur[k]; !ok {
			changes = append(changes, models.ContextChange{Key: k, From: old, To: ""})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Key < changes[j].Key })
	return changes
}

// rank orders items of the same millisecond: a context change first (it was
// in effect when the row was recorded), then breadcrumbs (they happened
// before the rows that carry them), then rows by sequence number.
func rank(t string) int {
	switch t {
	case models.TimelineContext:
		return 0
	case models.TimelineBreadcrumb:
		return 1
	}
	return 2
}

// Sort orders the items by time, then rank, then the SDK's sequence number.
func Sort(items []models.TimelineItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := &items[i], &items[j]
		if !a.Time.Equal(b.Time) {
			return a.Time.Before(b.Time)
		}
		if ra, rb := rank(a.Type), rank(b.Type); ra != rb {
			return ra < rb
		}
		return a.Seq < b.Seq
	})
}

// Nearest keeps at most n items: the n nearest to at, or the n earliest
// when at is nil. The result is in time order; the bool reports a cut.
func Nearest(items []models.TimelineItem, n int, at *time.Time) ([]models.TimelineItem, bool) {
	if len(items) <= n {
		return items, false
	}
	cp := append([]models.TimelineItem(nil), items...)
	if at == nil {
		Sort(cp)
	} else {
		dist := func(t time.Time) time.Duration {
			d := t.Sub(*at)
			if d < 0 {
				return -d
			}
			return d
		}
		sort.SliceStable(cp, func(i, j int) bool { return dist(cp[i].Time) < dist(cp[j].Time) })
	}
	cp = cp[:n]
	Sort(cp)
	return cp, true
}
