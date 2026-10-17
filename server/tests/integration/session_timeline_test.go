package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// APM-15 (and APM-11's link into it) end to end: one session as the SDK
// sends it, through the real ingest router into a fresh database, read back
// as a time line through the admin API, on both insert paths.
//
// The scenario of the ticket, "the room was only half built": the player
// enters room r-11 (room_enter, then room_ready), then room r-12: room_enter,
// and the event bus swallows the exception a RoomEnteredEvent handler threw,
// so room_ready never comes; the player quits (the session's data ends).

// sessionEvent builds one event of the session in the SDK's wire format; ms
// is the offset from the session start, fields are merged in.
func (e *apm4Env) sessionEvent(typ, session string, ms int64, ctx map[string]string, fields map[string]interface{}) string {
	e.n++
	m := map[string]interface{}{
		"type": typ, "event_id": fmt.Sprintf("tl-%s-%d", session, e.n), "seq": e.n, "timestamp": e.ts + ms,
		"app_version": "2.3", "platform": "Android", "device_model": "SM-A336B", "os_version": "14",
		"session_id": session, "device_id": "dev-" + session[:2], "scene": "Main", "build_type": "release",
	}
	if ctx != nil {
		m["ctx"] = ctx
	}
	for k, v := range fields {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// clock is the SDK's breadcrumb time of ms after the session start.
func (e *apm4Env) clock(ms int64) string {
	return "[" + time.UnixMilli(e.ts+ms).UTC().Format("15:04:05.000") + "] "
}

const busRoomException = "System.InvalidOperationException: Room template not found for roomId=r-12\n" +
	"  at Ozx.Rooms.RoomBuilder.Build (System.String roomId) [0x00000] in /Users/builder/ozx/Assets/Scripts/Rooms/RoomBuilder.cs:88 \n" +
	"  at Ozx.Core.GameEventBus.Publish[T] (T evt) [0x00000] in /Users/builder/ozx/Assets/Scripts/Core/GameEventBus.cs:48 \n"

func (e *apm4Env) halfBuiltRoomSession(t *testing.T, s string) {
	t.Helper()
	level := map[string]string{"levelId": "3"}
	r11 := map[string]string{"levelId": "3", "roomId": "r-11"}
	r12 := map[string]string{"levelId": "3", "roomId": "r-12"}
	var events []string
	events = append(events,
		e.sessionEvent("startup", s, 0, level, map[string]interface{}{"phase1_ms": 900, "phase2_ms": 400, "tti_ms": 1200}),
		e.sessionEvent("scene_load", s, 1000, level, map[string]interface{}{"scene_name": "Main", "load_ms": 350, "activate_ms": 20}),
	)
	for sec := int64(0); sec <= 20; sec++ {
		ctx := level
		switch {
		case sec >= 10:
			ctx = r12
		case sec >= 5:
			ctx = r11
		}
		events = append(events, e.sessionEvent("perf_sample", s, sec*1000+500, ctx, map[string]interface{}{"fps": 55 - sec, "frame_time_ms": 18, "mem_mb": 400 + sec}))
	}
	events = append(events,
		e.sessionEvent("custom_event", s, 5000, r11, map[string]interface{}{"name": "room_enter", "count": 1, "props": map[string]string{"roomId": "r-11"}}),
		e.sessionEvent("custom_event", s, 6000, r11, map[string]interface{}{"name": "room_ready", "count": 1, "props": map[string]string{"roomId": "r-11"}}),
		e.metricJSONAt(t, s, 6000, r11, 640, 700),
		// Room r-12: entered; the bus handler throws in the same millisecond.
		e.sessionEvent("custom_event", s, 10000, r12, map[string]interface{}{"name": "room_enter", "count": 1, "props": map[string]string{"roomId": "r-12"}}),
		e.sessionEvent("exception", s, 10000, r12, map[string]interface{}{
			"fingerprint": "client-room", "log_type": "error", "count": 1, "error_seq": 1, "session_ms": 10000,
			"message":            "[GameEventBus] Error handling event RoomEnteredEvent: " + busRoomException,
			"stack":              "Ozx.Core.GameEventBus:Publish(RoomEnteredEvent)\n",
			"embedded_exception": busRoomException,
			"breadcrumbs": []string{
				e.clock(1000) + "Scene: Main",
				e.clock(5000) + "Event: room_enter roomId=r-11",
				e.clock(6000) + "Event: room_ready roomId=r-11",
				e.clock(9000) + "Warning: Asset not found: anim/arack_red (x3 until " + e.clock(9500)[1:13] + ")",
				e.clock(10000) + "Event: room_enter roomId=r-12",
			}}),
		// The same exception again (the bus retries every frame): a repeat count.
		e.sessionEvent("exception", s, 10500, r12, map[string]interface{}{
			"fingerprint": "client-room", "log_type": "error", "count": 40, "error_seq": 2, "session_ms": 10500,
			"message":            "[GameEventBus] Error handling event RoomEnteredEvent: " + busRoomException,
			"stack":              "Ozx.Core.GameEventBus:Publish(RoomEnteredEvent)\n",
			"embedded_exception": busRoomException}),
		// Calls beyond the SDK's budget, counted for the window.
		e.sessionEvent("custom_event", s, 12000, r12, map[string]interface{}{"name": "enemy_killed", "count": 15, "aggregated": true}),
		// A jank later carries some of the same lines (cut to 200 characters by the SDK).
		e.sessionEvent("jank", s, 15000, r12, map[string]interface{}{"duration_ms": 120, "max_frame_ms": 95, "recent_gc_count": 2,
			"recent_events": []string{
				e.clock(9000) + "Warning: Asset not found: anim/arack_red (x30 until " + e.clock(14000)[1:13] + ")",
				e.clock(10000) + "Event: room_enter roomId=r-12",
				e.clock(14900) + "Error: Pool exhausted for enemy 7",
			}}),
	)
	resp := postIngest(t, e.ingest, events...)
	if resp.Accepted != len(events) || len(resp.Errors) != 0 {
		t.Fatalf("ingest: %+v", resp)
	}
}

// metricJSONAt is a room_switch_ms metric row of the session at ms.
func (e *apm4Env) metricJSONAt(t *testing.T, session string, ms int64, ctx map[string]string, values ...float64) string {
	var m map[string]interface{}
	must(t, json.Unmarshal([]byte(e.metricJSON(session, "2.3", "release", ctx, false, values...)), &m))
	m["timestamp"] = e.ts + ms
	m["device_id"] = "dev-" + session[:2]
	b, _ := json.Marshal(m)
	return string(b)
}

func timelineOf(t *testing.T, e *apm4Env, params url.Values) models.SessionTimeline {
	t.Helper()
	var tl models.SessionTimeline
	getAdmin(t, e.admin, "/api/sessions/timeline", params, &tl)
	return tl
}

// describe renders an item for comparisons: type and its key fields.
func describe(it models.TimelineItem) string {
	switch it.Type {
	case models.TimelineCustomEvent:
		if it.Aggregated {
			return fmt.Sprintf("event %s x%d (aggregated)", it.Name, it.Count)
		}
		return "event " + it.Name + " " + it.Props["roomId"]
	case models.TimelineException:
		return fmt.Sprintf("exception %s x%d seq %d", it.ExceptionType, it.Count, it.ErrorSeq)
	case models.TimelineContext:
		var parts []string
		for _, c := range it.Changes {
			parts = append(parts, c.Key+":"+c.From+"->"+c.To)
		}
		return "context " + strings.Join(parts, ",")
	case models.TimelineBreadcrumb:
		s := "crumb " + it.Kind + " " + it.Text
		if it.Repeats > 0 {
			s += fmt.Sprintf(" x%d", it.Repeats)
		}
		return s
	case models.TimelineSceneLoad:
		return "scene_load " + it.SceneName
	case models.TimelineCustomMetric:
		return fmt.Sprintf("metric %s x%d", it.Name, it.Count)
	}
	return it.Type
}

func TestSessionTimeline_RoomOnlyHalfBuilt(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupAPM4(t, async)
			e.ts = time.Now().Add(-2 * time.Hour).Truncate(time.Second).UnixMilli()
			e.halfBuiltRoomSession(t, "S1-half")
			// Another session of the same device, and one of another device.
			resp := postIngest(t, e.ingest,
				e.sessionEvent("custom_event", "S1-next", 60000, nil, map[string]interface{}{"name": "room_enter", "count": 1}),
				e.sessionEvent("custom_event", "S9-other", 60000, nil, map[string]interface{}{"name": "room_enter", "count": 1}),
			)
			if resp.Accepted != 2 {
				t.Fatalf("ingest: %+v", resp)
			}

			// From the exception detail page: the occurrence carries the session,
			// its device and its time.
			groups := groupsByType(t, e, url.Values{"start_time": {time.UnixMilli(e.ts - time.Hour.Milliseconds()).UTC().Format(time.RFC3339)}})
			room := groups["System.InvalidOperationException"]
			if room.Count != 41 || room.FirstErrorSessions != 1 || room.SessionsWithSeq != 1 {
				t.Fatalf("group: %+v (all %+v)", room, groups)
			}
			var occ models.ExceptionOccurrencesResponse
			getAdmin(t, e.admin, "/api/exceptions/occurrences", url.Values{"fingerprint": {room.Fingerprint}, "page_size": {"1"}, "page": {"2"}}, &occ)
			if occ.TotalCount != 2 || len(occ.Occurrences) != 1 {
				t.Fatalf("occurrences: %+v", occ)
			}
			first := occ.Occurrences[0]
			if first.SessionID != "S1-half" || first.DeviceID != "dev-S1" || first.ErrorSeq != 1 || first.SessionMs != 10000 || len(first.Breadcrumbs) != 5 {
				t.Fatalf("first occurrence: %+v", first)
			}

			// The link: session id and the occurrence's time.
			at := first.Timestamp.UTC().Format(time.RFC3339Nano)
			tl := timelineOf(t, e, url.Values{"session_id": {first.SessionID}, "at": {at}, "perf": {"1"}})
			if !tl.Found || tl.Session.DeviceID != "dev-S1" || tl.Session.AppVersion != "2.3" || tl.Session.Rows != 21+2+3+1+2+1+1 {
				t.Fatalf("session: %+v", tl.Session)
			}
			if tl.Session.StartedAt == nil || tl.Session.StartedAt.UnixMilli() != e.ts {
				t.Errorf("session start: %v, want %v", tl.Session.StartedAt, time.UnixMilli(e.ts).UTC())
			}
			if len(tl.Truncated) != 0 {
				t.Errorf("truncated: %v", tl.Truncated)
			}

			var got []string
			for _, it := range tl.Items {
				got = append(got, describe(it))
			}
			want := []string{
				"context levelId:->3",
				"startup",
				"scene_load Main",
				"context roomId:->r-11",
				"event room_enter r-11",
				"event room_ready r-11",
				"metric room_switch_ms x2",
				"crumb warning Asset not found: anim/arack_red x30",
				"context roomId:r-11->r-12",
				"event room_enter r-12",
				"exception System.InvalidOperationException x1 seq 1",
				"exception System.InvalidOperationException x40 seq 2",
				"event enemy_killed x15 (aggregated)",
				"crumb error Pool exhausted for enemy 7",
				"jank",
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("time line:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			// room_ready of r-12 never came: nothing after the exception but the
			// aggregated count, a log line and the jank; the session ends at its
			// last perf sample (20.5 s).
			if tl.Session.LastSeen.UnixMilli() != e.ts+20500 {
				t.Errorf("last seen %v", tl.Session.LastSeen)
			}
			// 2 lines carried by both the exception and the jank, and 4 lines
			// standing for rows on the time line (the scene load, 3 custom events).
			if tl.DuplicateBreadcrumbs != 6 {
				t.Errorf("duplicate breadcrumbs: %d", tl.DuplicateBreadcrumbs)
			}
			for _, it := range tl.Items {
				if it.Type == models.TimelineBreadcrumb && it.Kind == models.CrumbWarning {
					if it.RepeatUntil == nil || it.RepeatUntil.UnixMilli() != e.ts+14000 || it.Time.UnixMilli() != e.ts+9000 {
						t.Errorf("warning: %+v", it)
					}
				}
				if it.Type == models.TimelineException && (it.ThrowSite == "" || it.Context["roomId"] != "r-12") {
					t.Errorf("exception item: %+v", it)
				}
			}
			if len(tl.Perf) == 0 || tl.PerfIntervalSeconds != 1 || tl.Perf[0].Samples != 1 {
				t.Errorf("perf: %d points, interval %d", len(tl.Perf), tl.PerfIntervalSeconds)
			}
			if tl.Counts[models.TimelineCustomEvent] != 4 || tl.Counts[models.TimelineBreadcrumb] != 2 || tl.Counts[models.TimelineContext] != 3 {
				t.Errorf("counts: %v", tl.Counts)
			}

			// Without at and perf: the same items, no perf track.
			tl2 := timelineOf(t, e, url.Values{"session_id": {"S1-half"}})
			if len(tl2.Items) != len(tl.Items) || len(tl2.Perf) != 0 {
				t.Errorf("without at: %d items, %d perf", len(tl2.Items), len(tl2.Perf))
			}

			// The device's sessions.
			var ds models.DeviceSessionsResponse
			getAdmin(t, e.admin, "/api/sessions", url.Values{"device_id": {"dev-S1"}}, &ds)
			if len(ds.Sessions) != 2 || ds.Sessions[0].SessionID != "S1-next" || ds.Sessions[1].SessionID != "S1-half" ||
				ds.Sessions[1].Exceptions != 41 || ds.Sessions[1].Rows != tl.Session.Rows {
				t.Errorf("device sessions: %+v", ds)
			}

			// An unknown session: found false, empty lists.
			tl3 := timelineOf(t, e, url.Values{"session_id": {"no-such-session"}})
			if tl3.Found || len(tl3.Items) != 0 || tl3.Items == nil {
				t.Errorf("unknown session: %+v", tl3)
			}
			// Bad parameters.
			for _, p := range []url.Values{{}, {"session_id": {"S1-half"}, "at": {"yesterday"}},
				{"session_id": {"S1-half"}, "start_time": {"2026-01-01T00:00:00Z"}, "end_time": {"2026-03-01T00:00:00Z"}}} {
				if code := adminStatus(t, e.admin, "/api/sessions/timeline", p); code != http.StatusBadRequest {
					t.Errorf("%v: %d", p, code)
				}
			}
		})
	}
}

// Rows of SDKs before APM-3/4/5/6 (no context, breadcrumbs, error_seq):
// the time line and the detail endpoints work and show them as they are.
func TestSessionTimeline_LegacyRows(t *testing.T) {
	e := setupAPM4(t, false)
	session := seedLegacyRows(t, e.client)
	ts := time.Now().Add(-2 * time.Hour)
	tl := timelineOf(t, e, url.Values{"session_id": {session}, "at": {ts.UTC().Format(time.RFC3339Nano)}, "perf": {"1"}})
	if !tl.Found || len(tl.Items) != 7 || tl.Session.StartedAt != nil || len(tl.Perf) != 1 {
		t.Fatalf("legacy time line: %+v", tl)
	}
	types := map[string]int{}
	for _, it := range tl.Items {
		types[it.Type]++
	}
	// one row of each table; the crash's and the jank's lines have no time
	if types["exception"] != 1 || types["crash"] != 1 || types["jank"] != 1 || types["startup"] != 1 || types["scene_load"] != 1 || types["breadcrumb"] != 2 {
		t.Errorf("types: %v", types)
	}

	var occ models.ExceptionOccurrencesResponse
	getAdmin(t, e.admin, "/api/exceptions/occurrences", url.Values{"fingerprint": {"F"}}, &occ)
	if occ.TotalCount != 1 || occ.Occurrences[0].ErrorSeq != 0 || len(occ.Occurrences[0].Context) != 0 || occ.Occurrences[0].Breadcrumbs == nil {
		t.Errorf("legacy occurrences: %+v", occ)
	}
	var detail models.ExceptionDetail
	getAdmin(t, e.admin, "/api/exceptions/detail", url.Values{"fingerprint": {"F"}}, &detail)
	if detail.SessionsWithSeq != 0 || len(detail.Trend.Points) != 1 || detail.Trend.Points[0].Count != 3 {
		t.Errorf("legacy detail: %+v", detail)
	}
	var dist models.ExceptionDistribution
	getAdmin(t, e.admin, "/api/exceptions/distribution", url.Values{"fingerprint": {"F"}, "key": {"roomId"}}, &dist)
	if dist.Total != 3 || len(dist.Rows) != 1 || dist.Rows[0].Value != "" || dist.Merged != 0 {
		t.Errorf("legacy distribution: %+v", dist)
	}
}
