package integration

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// APM-22: the dashboard showed 2 crashes in 25 sessions as a crash rate of
// 8000.00% (crashes per 1000 sessions, formatted as a fraction). Its crash
// rate is now crashed sessions / sessions, a fraction, counted as the
// session health (APM-9) counts them: every session with a row, crashes of
// the default types, kills by the player left out.
func TestAPM22_DashboardCrashRateIsTheHealthCrashRate(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupAPM9(t, async)
			var events []string
			crash := func(session, version string, offset int64, crashType, exitReason string) string {
				return e.ev("crash", session, version, offset, map[string]interface{}{"crash_type": crashType, "fingerprint": "fp-" + crashType,
					"stack": "", "breadcrumbs": []string{}, "exit_reason": exitReason, "foreground": true})
			}
			// 5.0: the bug's numbers, 25 sessions and 2 crashed.
			for i := 1; i <= 25; i++ {
				events = append(events, e.ev("perf_sample", fmt.Sprintf("p%d", i), "5.0", int64(i)*1000, map[string]interface{}{"fps": 60}))
			}
			events = append(events, crash("p1", "5.0", 30000, "native", "CRASH_NATIVE"), crash("p2", "5.0", 30000, "java", "CRASH"))
			// 5.1: what counts. q1 crashed twice (one crashed session); q2 was
			// force-stopped by the player, q3 had an ANR, q4 an unknown exit
			// (none of them crashed); q5 has no perf sample (a startup and an
			// OOM kill) and is a session that crashed.
			for i := 1; i <= 4; i++ {
				events = append(events, e.ev("perf_sample", fmt.Sprintf("q%d", i), "5.1", int64(i)*1000, map[string]interface{}{"fps": 60}))
			}
			events = append(events,
				crash("q1", "5.1", 30000, "native", "CRASH_NATIVE"), crash("q1", "5.1", 31000, "native", "CRASH_NATIVE"),
				crash("q2", "5.1", 30000, "killed", "USER_REQUESTED"), crash("q3", "5.1", 30000, "anr", "ANR"),
				crash("q4", "5.1", 30000, "unknown", ""),
				e.ev("startup", "q5", "5.1", 1000, map[string]interface{}{"phase1_ms": 100, "phase2_ms": 200, "tti_ms": 300}),
				crash("q5", "5.1", 30000, "oom", "LOW_MEMORY"),
				// A dev build's crashed session: not in the default (release) view.
				e.ev("perf_sample", "d1", "5.0", 1000, map[string]interface{}{"fps": 60, "build_type": "dev"}),
				e.ev("crash", "d1", "5.0", 2000, map[string]interface{}{"build_type": "dev", "crash_type": "native", "fingerprint": "fp", "stack": "", "breadcrumbs": []string{}}),
			)
			e.ingest(t, events)

			var s models.DashboardSummary
			e.get(t, "/api/summary", nil, &s)
			if s.TotalSessions != 30 || s.CrashedSessions != 4 || s.CrashCount != 8 || !near3(s.CrashedSessionRate, 4.0/30) || !near3(s.CrashRate, 8.0/30*1000) {
				t.Errorf("summary: sessions %d crashed %d crashes %d rate %v per 1000 %v, want 30 4 8 %v %v",
					s.TotalSessions, s.CrashedSessions, s.CrashCount, s.CrashedSessionRate, s.CrashRate, 4.0/30, 8.0/30*1000)
			}
			if len(s.TopVersions) != 2 {
				t.Fatalf("top_versions %+v", s.TopVersions)
			}
			v0, v1 := s.TopVersions[0], s.TopVersions[1]
			if v0.Version != "5.0" || v0.SessionCount != 25 || v0.CrashedSessions != 2 || v0.CrashCount != 2 || !near3(v0.CrashedSessionRate, 0.08) || !near3(v0.CrashRate, 80) {
				t.Errorf("5.0 (the bug: 8%%, not 8000%%): %+v", v0)
			}
			if v1.Version != "5.1" || v1.SessionCount != 5 || v1.CrashedSessions != 2 || v1.CrashCount != 6 || !near3(v1.CrashedSessionRate, 0.4) || !near3(v1.CrashRate, 1200) {
				t.Errorf("5.1: %+v", v1)
			}

			// The same numbers as the session health with its defaults.
			var h models.SessionHealthResponse
			e.get(t, "/api/health/sessions", nil, &h)
			byVersion := map[string]models.SessionHealthRow{}
			for _, r := range h.Rows {
				byVersion[r.Value] = r
			}
			for _, v := range s.TopVersions {
				r := byVersion[v.Version]
				if r.Sessions != v.SessionCount || r.CrashedSessions != v.CrashedSessions || !near3(1-r.CrashFreeSessions, v.CrashedSessionRate) {
					t.Errorf("%s: dashboard %+v, health %+v", v.Version, v, r)
				}
			}

			// The dev build alone, and every build.
			e.get(t, "/api/summary", url.Values{"build_type": {"dev"}}, &s)
			if s.TotalSessions != 1 || s.CrashedSessions != 1 || !near3(s.CrashedSessionRate, 1) {
				t.Errorf("dev: %+v", s)
			}
			e.get(t, "/api/summary", url.Values{"build_type": {"all"}}, &s)
			if s.TotalSessions != 31 || s.CrashedSessions != 5 || s.CrashCount != 9 || s.TopVersions[0].Version != "5.0" || s.TopVersions[0].SessionCount != 26 {
				t.Errorf("all builds: %+v", s)
			}
		})
	}
}
