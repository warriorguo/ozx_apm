package integration

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api"
	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/tests/integration/testutil"
)

// APM-20: the admin API returned 0 for every count because count()/uniq()
// (UInt64) were scanned into int64 and the scan error was ignored. These tests
// seed a small, hand-computed data set into a fresh database and check every
// number the admin API returns against it, through the real HTTP handlers.

const adminTestDB = "ozx_apm_admin_test"

func setupAdminTest(t *testing.T) (*storage.ClickHouseClient, http.Handler) {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	cfg := testutil.TestClickHouseConfig()
	cfg.Database = adminTestDB
	admin, err := storage.NewClickHouseClient(testutil.TestClickHouseConfig(), zap.NewNop())
	if err != nil {
		t.Skipf("ClickHouse not available: %v", err)
	}
	if err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+adminTestDB); err != nil {
		t.Fatal(err)
	}
	admin.Close()
	client, err := testutil.SetupTestDatabase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	repo := storage.NewRepository(client, zap.NewNop())
	router := api.NewAdminRouter(&config.Config{}, repo, zap.NewNop())
	return client, router
}

// seedAdminData writes the data set the expectations below are computed from.
// All rows are 30 minutes old, except one crash two days old (outside the range).
func seedAdminData(t *testing.T, client *storage.ClickHouseClient) {
	t.Helper()
	ctx := context.Background()
	repo := storage.NewRepository(client, zap.NewNop())
	ts := time.Now().Add(-30 * time.Minute).Truncate(time.Second)
	old := time.Now().Add(-48 * time.Hour)

	perf := func(id, version, platform, session, scene string, fps, frame float32) models.PerfSample {
		return models.PerfSample{Timestamp: ts, EventID: id, AppVersion: version, Platform: platform, SessionID: session, DeviceID: "d-" + session, Scene: scene, FPS: fps, FrameTimeMs: frame}
	}
	must(t, repo.InsertPerfSamples(ctx, []models.PerfSample{
		perf("p1", "1.0", "Android", "s1", "A", 30, 33),
		perf("p2", "1.0", "Android", "s1", "A", 60, 16),
		perf("p3", "1.0", "Android", "s2", "A", 50, 20),
		perf("p4", "1.1", "iOS", "s3", "B", 20, 50),
	}))
	crash := func(id, version, platform, session, fp, device string, at time.Time) models.Crash {
		return models.Crash{Timestamp: at, EventID: id, AppVersion: version, Platform: platform, SessionID: session, DeviceID: "d-" + session, DeviceModel: device, OSVersion: "os", Scene: "A", CrashType: "native", Fingerprint: fp, Stack: "stack-" + fp, Breadcrumbs: []string{"b1"}}
	}
	must(t, repo.InsertCrashes(ctx, []models.Crash{
		crash("c1", "1.0", "Android", "s1", "C1", "Pixel", ts),
		crash("c2", "1.0", "Android", "s2", "C1", "Pixel", ts),
		crash("c3", "1.1", "iOS", "s3", "C2", "iPhone", ts),
		crash("c-old", "1.0", "Android", "s1", "C1", "Pixel", old),
	}))
	exc := func(id, platform, session, fp string, count uint32) models.Exception {
		return models.Exception{Timestamp: ts, EventID: id, AppVersion: "1.0", Platform: platform, SessionID: session, DeviceID: "d-" + session, Fingerprint: fp, Message: "msg-" + fp, Count: count}
	}
	must(t, repo.InsertExceptions(ctx, []models.Exception{
		exc("e1", "Android", "s1", "E1", 5),
		exc("e2", "Android", "s2", "E1", 2),
		exc("e3", "iOS", "s3", "E2", 1),
	}))
	must(t, repo.InsertJanks(ctx, []models.Jank{
		{Timestamp: ts, EventID: "j1", AppVersion: "1.0", Platform: "Android", SessionID: "s1", Scene: "A", DurationMs: 80},
		{Timestamp: ts, EventID: "j2", AppVersion: "1.1", Platform: "iOS", SessionID: "s3", Scene: "B", DurationMs: 60},
	}))
	must(t, repo.InsertStartups(ctx, []models.Startup{
		{Timestamp: ts, EventID: "st1", AppVersion: "1.0", Platform: "Android", SessionID: "s1", Phase1Ms: 1000, Phase2Ms: 500, TTIMs: 500},
		{Timestamp: ts, EventID: "st2", AppVersion: "1.0", Platform: "Android", SessionID: "s2", Phase1Ms: 500, Phase2Ms: 500, TTIMs: 500},
	}))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func getAdmin(t *testing.T, router http.Handler, path string, params url.Values, out interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path+"?"+params.Encode(), nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("GET %s: decode %q: %v", path, w.Body.String(), err)
	}
}

func lastHour() url.Values {
	now := time.Now()
	return url.Values{
		"start_time": {now.Add(-time.Hour).Format(time.RFC3339)},
		"end_time":   {now.Add(time.Minute).Format(time.RFC3339)},
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestAdminSummary_ReturnsRealCounts(t *testing.T) {
	client, router := setupAdminTest(t)
	seedAdminData(t, client)

	var s models.DashboardSummary
	getAdmin(t, router, "/api/summary", lastHour(), &s)
	if s.TotalSessions != 3 || s.TotalEvents != 4 || s.CrashCount != 3 || s.ExceptionCount != 8 || s.JankCount != 2 {
		t.Errorf("counts: sessions %d events %d crashes %d exceptions %d janks %d, want 3 4 3 8 2",
			s.TotalSessions, s.TotalEvents, s.CrashCount, s.ExceptionCount, s.JankCount)
	}
	if !near(s.AvgFPS, 40) || !near(s.AvgStartupMs, 1750) || !near(s.CrashRate, 1000) {
		t.Errorf("avg_fps %v avg_startup_ms %v crash_rate %v, want 40 1750 1000", s.AvgFPS, s.AvgStartupMs, s.CrashRate)
	}
	// APM-22: sessions and crashed sessions as the session health counts
	// them (any row; crashes of the default types). In range: s1, s2 (1.0,
	// Android) and s3 (1.1, iOS); each has a native crash (c-old is outside
	// the range), so 3 of 3 sessions crashed (rate 1), 3 crash reports
	// (crash_rate 1000 per 1000 sessions).
	if s.CrashedSessions != 3 || !near(s.CrashedSessionRate, 1) {
		t.Errorf("crashed_sessions %d crashed_session_rate %v, want 3 1", s.CrashedSessions, s.CrashedSessionRate)
	}
	// Per version, a session counts under every version it has rows of (as
	// on the Health page): s3's exception e3 is a 1.0 row (exc() always
	// writes 1.0), so 1.0 has s1, s2 and s3 = 3 sessions, of which s1 and
	// s2 crashed in 1.0 (s3's crash is a 1.1 row): 2/3, 2 crash reports,
	// 666.7 per 1000. 1.1 has s3 alone, crashed: 1/1. The totals above are
	// distinct sessions (3), not the sum of the versions (4).
	if len(s.TopVersions) != 2 {
		t.Fatalf("top_versions %+v", s.TopVersions)
	}
	if v := s.TopVersions[0]; v.Version != "1.0" || v.SessionCount != 3 || v.CrashCount != 2 || v.CrashedSessions != 2 ||
		!near(v.CrashedSessionRate, 2.0/3) || !near(v.CrashRate, 2.0/3*1000) {
		t.Errorf("1.0: %+v, want 3 sessions, 2 crashes, 2 crashed, 2/3, 666.7", v)
	}
	if v := s.TopVersions[1]; v.Version != "1.1" || v.SessionCount != 1 || v.CrashCount != 1 || v.CrashedSessions != 1 ||
		!near(v.CrashedSessionRate, 1) || !near(v.CrashRate, 1000) {
		t.Errorf("1.1: %+v, want 1 session, 1 crash, 1 crashed, 1, 1000", v)
	}
	if len(s.TopPlatforms) != 2 || s.TopPlatforms[0].Platform != "Android" || s.TopPlatforms[0].SessionCount != 2 ||
		!near(s.TopPlatforms[0].AvgFPS, (30+60+50)/3.0) || !near(s.TopPlatforms[1].AvgFPS, 20) {
		t.Errorf("top_platforms %+v", s.TopPlatforms)
	}

	params := lastHour()
	params.Set("platform", "iOS")
	getAdmin(t, router, "/api/summary", params, &s)
	// iOS: s3 alone (its perf sample, crash and jank are 1.1 rows, its
	// exception a 1.0 row): 1 session, crashed, 1 crash report. Per version
	// it is under both: 1.0 (exception only, not crashed) and 1.1 (crashed);
	// tied at 1 session, by version name.
	if s.TotalSessions != 1 || s.CrashCount != 1 || s.CrashedSessions != 1 || !near(s.CrashedSessionRate, 1) || !near(s.CrashRate, 1000) ||
		s.ExceptionCount != 1 || s.JankCount != 1 || !near(s.AvgFPS, 20) {
		t.Errorf("iOS: %+v", s)
	}
	if len(s.TopVersions) != 2 || s.TopVersions[0].Version != "1.0" || s.TopVersions[0].SessionCount != 1 || s.TopVersions[0].CrashedSessions != 0 ||
		s.TopVersions[0].CrashCount != 0 || s.TopVersions[1].Version != "1.1" || s.TopVersions[1].CrashedSessions != 1 || s.TopVersions[1].CrashCount != 1 {
		t.Errorf("iOS top_versions %+v", s.TopVersions)
	}
}

// An empty range must give zeros, not an error: avg() and quantile() of no
// rows are NaN in ClickHouse, which encoding/json refuses to encode.
func TestAdminAPI_EmptyRangeGivesZeros(t *testing.T) {
	client, router := setupAdminTest(t)
	seedAdminData(t, client)
	empty := url.Values{
		"start_time": {time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339)},
		"end_time":   {time.Now().Add(-9 * 24 * time.Hour).Format(time.RFC3339)},
	}

	var s models.DashboardSummary
	getAdmin(t, router, "/api/summary", empty, &s)
	if s.TotalSessions != 0 || s.AvgFPS != 0 || s.AvgStartupMs != 0 || s.CrashCount != 0 || s.CrashedSessions != 0 || s.CrashedSessionRate != 0 ||
		s.CrashRate != 0 || s.TopVersions == nil || len(s.TopVersions) != 0 {
		t.Errorf("summary: %+v", s)
	}
	for _, metric := range []string{"fps", "frame_time", "startup"} {
		p := url.Values{"metric": {metric}, "start_time": empty["start_time"], "end_time": empty["end_time"]}
		var d models.DistributionResponse
		getAdmin(t, router, "/api/distribution", p, &d)
		if d.P50 != 0 || d.P99 != 0 || len(d.Buckets) != 5 {
			t.Errorf("distribution %s: %+v", metric, d)
		}
	}
	var c models.CrashListResponse
	getAdmin(t, router, "/api/crashes", empty, &c)
	if c.TotalCount != 0 || len(c.Crashes) != 0 {
		t.Errorf("crashes: %+v", c)
	}
}

func TestAdminTimeSeries_ReturnsRealValues(t *testing.T) {
	client, router := setupAdminTest(t)
	seedAdminData(t, client)

	want := map[string]float64{
		"fps": 40, "frame_time": (33 + 16 + 20 + 50) / 4.0, "crashes": 3, "exceptions": 8,
		"janks": 2, "sessions": 3, "startup": 1750,
	}
	for metric, v := range want {
		p := lastHour()
		p.Set("metric", metric)
		var resp models.TimeSeriesResponse
		getAdmin(t, router, "/api/timeseries", p, &resp)
		// All rows share one timestamp, so they fall into one 5-minute bucket.
		if len(resp.Data) != 1 || !near(resp.Data[0].Value, v) {
			t.Errorf("%s: %+v, want one point = %v", metric, resp.Data, v)
		}
	}
}

func TestAdminDistribution_ReturnsRealValues(t *testing.T) {
	client, router := setupAdminTest(t)
	seedAdminData(t, client)

	p := lastHour()
	p.Set("metric", "fps")
	var d models.DistributionResponse
	getAdmin(t, router, "/api/distribution", p, &d)
	// fps 20, 30, 50, 60 -> buckets 15-30: 1, 30-45: 1, 45-60: 1, 60+: 1
	counts := map[string]int64{}
	for _, b := range d.Buckets {
		counts[b.Bucket] = b.Count
	}
	if counts["0-15"] != 0 || counts["15-30"] != 1 || counts["30-45"] != 1 || counts["45-60"] != 1 || counts["60+"] != 1 {
		t.Errorf("fps buckets %+v", d.Buckets)
	}
	if d.P50 < 20 || d.P50 > 60 || d.P99 < d.P50 {
		t.Errorf("fps percentiles %+v", d)
	}

	p = lastHour()
	p.Set("metric", "startup")
	getAdmin(t, router, "/api/distribution", p, &d)
	counts = map[string]int64{}
	for _, b := range d.Buckets {
		counts[b.Bucket] = b.Count
	}
	if counts["1000-2000"] != 1 || counts["2000-3000"] != 1 {
		t.Errorf("startup buckets %+v", d.Buckets)
	}

	// apm_startups has no scene column: a scene filter must not break it.
	p.Set("scene", "A")
	getAdmin(t, router, "/api/distribution", p, &d)
}

func TestAdminCrashesAndExceptions_ReturnRealCounts(t *testing.T) {
	client, router := setupAdminTest(t)
	seedAdminData(t, client)

	var c models.CrashListResponse
	getAdmin(t, router, "/api/crashes", lastHour(), &c)
	if c.TotalCount != 2 || len(c.Crashes) != 2 {
		t.Fatalf("crashes: %+v", c)
	}
	top := c.Crashes[0]
	if top.Fingerprint != "C1" || top.Count != 2 || top.SessionCount != 2 || len(top.AffectedVersions) != 1 || len(top.TopDevices) != 1 {
		t.Errorf("top crash group %+v", top)
	}

	p := lastHour()
	p.Set("fingerprint", "C1")
	var d models.CrashDetail
	getAdmin(t, router, "/api/crashes/detail", p, &d)
	if d.Count != 2 || d.SessionCount != 2 || len(d.Occurrences) != 2 ||
		len(d.VersionDist) != 1 || d.VersionDist[0].Count != 2 || len(d.DeviceDist) != 1 || d.DeviceDist[0].Count != 2 {
		t.Errorf("crash detail %+v", d)
	}

	var e models.ExceptionListResponse
	getAdmin(t, router, "/api/exceptions", lastHour(), &e)
	if e.TotalCount != 2 || len(e.Exceptions) != 2 || e.Exceptions[0].Fingerprint != "E1" || e.Exceptions[0].Count != 7 || e.Exceptions[0].SessionCount != 2 {
		t.Errorf("exceptions %+v", e)
	}

	var versions struct{ Versions []string }
	getAdmin(t, router, "/api/versions", nil, &versions)
	if len(versions.Versions) != 2 {
		t.Errorf("versions %v", versions.Versions)
	}
	var scenes struct{ Scenes []string }
	getAdmin(t, router, "/api/scenes", nil, &scenes)
	if len(scenes.Scenes) != 2 {
		t.Errorf("scenes %v", scenes.Scenes)
	}
}
