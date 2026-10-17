package storage

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// The time line's concurrent queries must not run on a context with a
// deadline (clickhouse-go 2.17 then writes max_execution_time into the
// settings map of a clickhouse.Context from each query: a fatal concurrent
// map write), yet they stop with the request or after the timeout.
func TestParallelQueryContext(t *testing.T) {
	withSettings := clickhouse.Context(context.Background(), clickhouse.WithSettings(clickhouse.Settings{"log_comment": "x"}))
	parent, cancelParent := context.WithTimeout(withSettings, time.Hour)
	ctx, cancel := parallelQueryContext(parent, time.Hour)
	if _, ok := ctx.Deadline(); ok {
		t.Error("deadline inherited")
	}
	// What each concurrent query does with its context (run with -race).
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = clickhouse.Context(ctx) }()
	}
	wg.Wait()
	if ctx.Err() != nil {
		t.Error("cancelled early")
	}
	cancelParent()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Error("not cancelled with the parent")
	}
	cancel()

	ctx, cancel = parallelQueryContext(context.Background(), 10*time.Millisecond)
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Error("not cancelled after the timeout")
	}
}

func TestSessionQueries_BindEveryValue(t *testing.T) {
	evil := "s' OR 1=1 --"
	q := SessionQuery{SessionID: evil, Start: time.Unix(100, 0), End: time.Unix(200, 0)}
	info, infoArgs := sessionInfoQuery(q)
	changes, changeArgs := contextChangesQuery(q, 10)
	for name, c := range map[string]struct {
		sql  string
		args []interface{}
	}{"info": {info, infoArgs}, "changes": {changes, changeArgs}} {
		if strings.Contains(c.sql, evil) {
			t.Errorf("%s: session id in the SQL text", name)
		}
		if n := strings.Count(c.sql, "?"); n != len(c.args) {
			t.Errorf("%s: %d placeholders, %d args", name, n, len(c.args))
		}
		// Every table part is limited to the session and the range.
		if n := strings.Count(c.sql, "WHERE session_id = ? AND timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND timestamp <= fromUnixTimestamp64Milli(toInt64(?))"); n != len(EventTables) {
			t.Errorf("%s: %d parts limited to the session, want %d", name, n, len(EventTables))
		}
		if c.args[0] != evil || c.args[1] != int64(100000) || c.args[2] != int64(200000) {
			t.Errorf("%s: args %v", name, c.args[:3])
		}
	}
	// Merged counts never decide the context; the sort carries no maps.
	if strings.Contains(changes, ", ctx,") || strings.Contains(changes, "SELECT timestamp, src, ctx") {
		t.Errorf("the window carries the context maps: %s", changes)
	}
	byHash, byHashArgs := contextsByHashQuery(q, []uint64{1, 2})
	if strings.Count(byHash, "?") != len(byHashArgs) || byHashArgs[len(byHashArgs)-1].([]uint64)[1] != 2 {
		t.Errorf("by hash: %s %v", byHash, byHashArgs)
	}
	if strings.Count(changes, "AND ctx_merged = 0") != 3 || !strings.Contains(changes, "LIMIT 11") {
		t.Errorf("changes: %s", changes)
	}
	// scene_loads has no os_version; only exceptions have session_ms.
	if !strings.Contains(info, "'' AS os_version, build_type, toUInt64(0) AS session_ms FROM apm_scene_loads") ||
		!strings.Contains(info, "session_ms AS session_ms FROM apm_exceptions") {
		t.Errorf("info: %s", info)
	}
}

func TestSessionOrder(t *testing.T) {
	if o, args := sessionOrder(nil); o != "ORDER BY timestamp, seq" || args != nil {
		t.Errorf("%s %v", o, args)
	}
	at := time.UnixMilli(1234567)
	o, args := sessionOrder(&at)
	if strings.Count(o, "?") != 1 || len(args) != 1 || args[0] != int64(1234567) {
		t.Errorf("%s %v", o, args)
	}
}

func TestSessionSources_CoverTheEventTables(t *testing.T) {
	tables := map[string]bool{}
	for _, s := range sessionSources {
		tables[s.table] = true
		if strings.Contains(s.columns, "?") || s.limit <= 0 {
			t.Errorf("%s: %s", s.table, s.columns)
		}
	}
	// Perf samples are the optional track, not items.
	for _, et := range EventTables {
		if et != TablePerfSamples && !tables[et.Name] {
			t.Errorf("%s is not on the time line", et.Name)
		}
	}
	if len(ctxSources) != len(EventTables) {
		t.Errorf("context changes read %d tables, want %d", len(ctxSources), len(EventTables))
	}
}

func TestPerfAndTrendIntervals(t *testing.T) {
	base := time.Unix(0, 0)
	cases := []struct {
		span time.Duration
		perf int
	}{{0, 1}, {3 * time.Minute, 1}, {10 * time.Minute, 5}, {time.Hour, 30}, {3 * time.Hour, 60}, {19 * time.Hour, 300}, {20 * time.Hour, 900}, {30 * 24 * time.Hour, 21600}}
	for _, c := range cases {
		if got := PerfInterval(base, base.Add(c.span)); got != c.perf {
			t.Errorf("perf %v: %d, want %d", c.span, got, c.perf)
		}
		if n := c.span.Seconds() / float64(PerfInterval(base, base.Add(c.span))); n >= MaxPerfPoints && c.span < 7*24*time.Hour {
			t.Errorf("perf %v: %v buckets", c.span, n)
		}
	}
	trend := []struct {
		span time.Duration
		want int
	}{{time.Hour, 60}, {6 * time.Hour, 300}, {24 * time.Hour, 900}, {7 * 24 * time.Hour, 3600}, {30 * 24 * time.Hour, 6 * 3600}, {365 * 24 * time.Hour, 86400}}
	for _, c := range trend {
		if got := TrendInterval(base, base.Add(c.span)); got != c.want {
			t.Errorf("trend %v: %d, want %d", c.span, got, c.want)
		}
	}
}

func TestExceptionSorts(t *testing.T) {
	for _, s := range []string{"", "count", "sessions", "first_seen", "last_seen"} {
		if !ValidExceptionSort(s) {
			t.Errorf("%q", s)
		}
	}
	for _, s := range []string{"cnt DESC", "count; DROP TABLE x", "COUNT"} {
		if ValidExceptionSort(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

// At most cap(timelineSlots) time line queries run at once, whatever the
// number of requests; a waiting query gives up when its context ends.
func TestTimelineSlotsBoundConcurrency(t *testing.T) {
	var running, peak int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = withTimelineSlot(context.Background(), func() error {
				n := atomic.AddInt32(&running, 1)
				for {
					p := atomic.LoadInt32(&peak)
					if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
						break
					}
				}
				time.Sleep(5 * time.Millisecond)
				atomic.AddInt32(&running, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	if peak < 1 || int(peak) > cap(timelineSlots) {
		t.Fatalf("peak concurrency %d, want 1..%d", peak, cap(timelineSlots))
	}

	for i := 0; i < cap(timelineSlots); i++ {
		timelineSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(timelineSlots); i++ {
			<-timelineSlots
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	called := false
	err := withTimelineSlot(ctx, func() error { called = true; return nil })
	if !errors.Is(err, context.DeadlineExceeded) || called {
		t.Fatalf("err %v, called %v; want deadline exceeded and not called", err, called)
	}
}
