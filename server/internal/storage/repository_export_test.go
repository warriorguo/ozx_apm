package storage

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

// APM-10: the export's queries bind every value, read one session in the
// range, keep the earliest rows and cover every event table once.
func TestExportQueries_BindEveryValue(t *testing.T) {
	evil := "s' OR 1=1 --"
	q := SessionQuery{SessionID: evil, Start: time.Unix(100, 0), End: time.Unix(200, 0)}
	tables := map[string]bool{}
	where := "WHERE session_id = ? AND timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND timestamp <= fromUnixTimestamp64Milli(toInt64(?))"
	for _, src := range exportSources {
		if tables[src.table] {
			t.Errorf("%s twice", src.table)
		}
		tables[src.table] = true
		sql, args := exportQuery(src, q)
		if strings.Contains(sql, evil) || strings.Count(sql, "?") != len(args) || len(args) != 3 || args[0] != evil ||
			args[1] != int64(100000) || args[2] != int64(200000) {
			t.Errorf("%s: %s %v", src.table, sql, args)
		}
		if !strings.Contains(sql, "SELECT timestamp, seq, game_t, frame, ") || !strings.Contains(sql, where) ||
			!strings.HasSuffix(sql, "ORDER BY timestamp, seq LIMIT "+strconv.Itoa(src.limit+1)) {
			t.Errorf("%s: %s", src.table, sql)
		}
		if src.limit <= 0 || src.limit > ExportCustomEventRows {
			t.Errorf("%s: limit %d", src.table, src.limit)
		}
	}
	for _, et := range EventTables {
		if !tables[et.Name] {
			t.Errorf("%s not exported", et.Name)
		}
	}
	meta, metaArgs := exportMetaQuery(q)
	if strings.Contains(meta, evil) || strings.Count(meta, "?") != len(metaArgs) || strings.Count(meta, where) != len(EventTables) {
		t.Errorf("meta: %s", meta)
	}
	for _, k := range exportMetaKeys {
		if !strings.Contains(meta, "ctx['"+k+"']") {
			t.Errorf("meta lacks %s", k)
		}
	}
	if !strings.Contains(meta, "scene_name AS sc FROM apm_scene_loads") || !strings.Contains(meta, "'' AS sc FROM apm_startups") {
		t.Errorf("meta scene: %s", meta)
	}
}

// One export at a time: a second waits for the first, or gives up with its
// own context.
func TestExportSession_OneAtATime(t *testing.T) {
	exportSlots <- struct{}{}
	defer func() { <-exportSlots }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r := &Repository{}
	if _, _, err := r.ExportSession(ctx, SessionQuery{SessionID: "s"}); err != context.DeadlineExceeded {
		t.Errorf("err %v", err)
	}
}
