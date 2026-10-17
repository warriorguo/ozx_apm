package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api"
	"github.com/warriorguo/ozx_apm/server/internal/api/handlers"
	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/tests/integration/testutil"
)

// APM-5 end to end: events in the SDK's wire format go through the real
// ingest router into a fresh database and are read back through the admin
// API with build type and context filters.
//
// Sessions (all events 30 minutes old):
//
//	s1 release  levelId=1 stageType=normal  perf 60, 50; exception E1 x2; jank 80ms; startup; scene load
//	s2 release  levelId=2 stageType=boss    perf 30; exception E1 x1; crash C1
//	s3 dev      levelId=1                   perf 20; exception E2 x5
//	s4 editor   levelId=1                   perf 10
//	s5 (legacy: no build_type, no ctx)      perf 40; exception E1 x1
//	s6 (platform WindowsEditor, no build_type -> editor)  perf 5
//
// The server computes the exception fingerprints (APM-4); E1 and E2 are the
// fingerprints the client sent.
var (
	contextFpE1 = fingerprint.Analyze(fingerprint.Input{Message: "error one"}).Fingerprint
	contextFpE2 = fingerprint.Analyze(fingerprint.Input{Message: "error two"}).Fingerprint
)

func setupContextTest(t *testing.T) http.Handler {
	t.Helper()
	admin := adminClient(t)
	db := fmt.Sprintf("ozx_apm_apm5_ctx_%d", time.Now().UnixNano()%1e9)
	createDB(t, admin, db)
	cfg := testutil.TestClickHouseConfig()
	cfg.Database = db
	client, err := testutil.SetupTestDatabase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	repo := storage.NewRepository(client, zap.NewNop())

	ts := time.Now().Add(-30 * time.Minute).UnixMilli()
	n := 0
	ev := func(typ, session, platform, apm5, extra string) string {
		n++
		return `{"type":"` + typ + `","event_id":"ctx-` + strconv.Itoa(n) + `","seq":` + strconv.Itoa(n) +
			`,"timestamp":` + strconv.FormatInt(ts, 10) + `,"app_version":"2.0","platform":"` + platform +
			`","device_model":"Pixel","os_version":"14","session_id":"` + session + `","device_id":"d-` + session +
			`","scene":"Main"` + apm5 + extra + `}`
	}
	s1 := `,"build_type":"release","gpu":"Adreno 642L","ram_class":"8GB","net_type":"wifi","ctx":{"levelId":"1","stageType":"normal"}`
	s2 := `,"build_type":"release","ctx":{"levelId":"2","stageType":"boss"}`
	s3 := `,"build_type":"dev","ctx":{"levelId":"1"}`
	s4 := `,"build_type":"editor","ctx":{"levelId":"1"}`
	events := []string{
		ev("perf_sample", "s1", "Android", s1, `,"fps":60`),
		ev("perf_sample", "s1", "Android", s1, `,"fps":50`),
		ev("exception", "s1", "Android", s1, `,"fingerprint":"E1","message":"error one","count":2`),
		ev("jank", "s1", "Android", s1, `,"duration_ms":80,"max_frame_ms":80,"recent_events":[]`),
		ev("startup", "s1", "Android", s1, `,"phase1_ms":100,"phase2_ms":200,"tti_ms":300`),
		ev("scene_load", "s1", "Android", s1, `,"scene_name":"Main","load_ms":500`),
		ev("perf_sample", "s2", "Android", s2, `,"fps":30`),
		ev("exception", "s2", "Android", s2, `,"fingerprint":"E1","message":"error one","count":1`),
		ev("crash", "s2", "Android", s2, `,"fingerprint":"C1","crash_type":"native","stack":"st","breadcrumbs":["b"]`),
		ev("perf_sample", "s3", "Android", s3, `,"fps":20`),
		ev("exception", "s3", "Android", s3, `,"fingerprint":"E2","message":"error two","count":5`),
		ev("perf_sample", "s4", "Android", s4, `,"fps":10`),
		ev("perf_sample", "s5", "Android", "", `,"fps":40`),
		ev("exception", "s5", "Android", "", `,"fingerprint":"E1","message":"error one","count":1`),
		ev("perf_sample", "s6", "WindowsEditor", "", `,"fps":5`),
	}
	ingest := api.NewRouter(&config.Config{ClickHouse: *cfg}, repo, zap.NewNop())
	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewBufferString(`{"events":[`+strings.Join(events, ",")+`]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	ingest.ServeHTTP(w, req)
	var resp handlers.IngestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusOK || resp.Accepted != len(events) || len(resp.Errors) != 0 {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	return api.NewAdminRouter(&config.Config{}, repo, zap.NewNop())
}

func withParams(base url.Values, kv ...string) url.Values {
	p := url.Values{}
	for k, v := range base {
		p[k] = append([]string(nil), v...)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		p.Add(kv[i], kv[i+1])
	}
	return p
}

func TestContext_BuildTypeAndContextFilters(t *testing.T) {
	router := setupContextTest(t)

	cases := []struct {
		name                           string
		params                         []string
		sessions, events, exc, crashes int64
		avgFPS                         float64
	}{
		{"default = release + legacy", nil, 3, 4, 4, 1, 45},
		{"release", []string{"build_type", "release"}, 3, 4, 4, 1, 45},
		{"dev", []string{"build_type", "dev"}, 1, 1, 5, 0, 20},
		{"editor (incl. the WindowsEditor platform)", []string{"build_type", "editor"}, 2, 2, 0, 0, 7.5},
		{"unknown = legacy only", []string{"build_type", "unknown"}, 1, 1, 1, 0, 40},
		{"all", []string{"build_type", "all"}, 6, 7, 9, 1, 215.0 / 7},
		{"ctx", []string{"ctx", "levelId:1"}, 1, 2, 2, 0, 55},
		{"ctx OR", []string{"ctx", "levelId:1", "ctx", "levelId:2"}, 2, 3, 3, 1, 140.0 / 3},
		{"ctx AND", []string{"ctx", "levelId:1", "ctx", "stageType:boss"}, 0, 0, 0, 0, 0},
		{"ctx key not set", []string{"ctx", "levelId:"}, 1, 1, 1, 0, 40},
		{"ctx + all builds", []string{"ctx", "levelId:1", "build_type", "all"}, 3, 4, 7, 0, 35},
		{"injection attempt is just a value", []string{"ctx", "levelId:1' OR 1=1 --"}, 0, 0, 0, 0, 0},
	}
	for _, c := range cases {
		var s models.DashboardSummary
		getAdmin(t, router, "/api/summary", withParams(lastHour(), c.params...), &s)
		if s.TotalSessions != c.sessions || s.TotalEvents != c.events || s.ExceptionCount != c.exc || s.CrashCount != c.crashes || !near(s.AvgFPS, c.avgFPS) {
			t.Errorf("%s: sessions %d events %d exceptions %d crashes %d fps %v; want %d %d %d %d %v", c.name,
				s.TotalSessions, s.TotalEvents, s.ExceptionCount, s.CrashCount, s.AvgFPS, c.sessions, c.events, c.exc, c.crashes, c.avgFPS)
		}
	}

	// The other endpoints take the same filter.
	var e models.ExceptionListResponse
	getAdmin(t, router, "/api/exceptions", withParams(lastHour(), "ctx", "levelId:2"), &e)
	if e.TotalCount != 1 || len(e.Exceptions) != 1 || e.Exceptions[0].Fingerprint != contextFpE1 || e.Exceptions[0].Count != 1 {
		t.Errorf("exceptions with levelId=2: %+v", e)
	}
	getAdmin(t, router, "/api/exceptions", withParams(lastHour(), "build_type", "dev"), &e)
	if e.TotalCount != 1 || e.Exceptions[0].Fingerprint != contextFpE2 || e.Exceptions[0].Count != 5 {
		t.Errorf("dev exceptions: %+v", e)
	}
	var c models.CrashListResponse
	getAdmin(t, router, "/api/crashes", withParams(lastHour(), "ctx", "stageType:boss"), &c)
	if c.TotalCount != 1 || c.Crashes[0].Fingerprint != "C1" {
		t.Errorf("crashes: %+v", c)
	}
	var d models.CrashDetail
	getAdmin(t, router, "/api/crashes/detail", withParams(lastHour(), "fingerprint", "C1"), &d)
	if len(d.Occurrences) != 1 || d.Occurrences[0].BuildType != "release" || d.Occurrences[0].Context["levelId"] != "2" || d.Occurrences[0].Context["stageType"] != "boss" {
		t.Errorf("crash occurrence: %+v", d.Occurrences)
	}
	var ts models.TimeSeriesResponse
	getAdmin(t, router, "/api/timeseries", withParams(lastHour(), "metric", "exceptions", "ctx", "levelId:1"), &ts)
	if len(ts.Data) != 1 || ts.Data[0].Value != 2 {
		t.Errorf("timeseries: %+v", ts.Data)
	}
	var dist models.DistributionResponse
	getAdmin(t, router, "/api/distribution", withParams(lastHour(), "metric", "fps", "build_type", "editor"), &dist)
	if dist.Buckets[0].Count != 2 { // fps 10 and 5
		t.Errorf("editor fps buckets: %+v", dist.Buckets)
	}
}

func TestContext_KeysValuesAndDistribution(t *testing.T) {
	router := setupContextTest(t)

	var keys models.ContextKeysResponse
	getAdmin(t, router, "/api/context/keys", lastHour(), &keys)
	// s1: 6 events, s2: 3 events, all six tables; release only.
	if len(keys.Keys) != 2 || keys.Keys[0] != (models.ContextKeyStat{Key: "levelId", Count: 9}) || keys.Keys[1] != (models.ContextKeyStat{Key: "stageType", Count: 9}) {
		t.Errorf("keys: %+v", keys.Keys)
	}
	getAdmin(t, router, "/api/context/keys", withParams(lastHour(), "build_type", "all", "source", "perf"), &keys)
	if len(keys.Keys) != 2 || keys.Keys[0] != (models.ContextKeyStat{Key: "levelId", Count: 5}) || keys.Keys[1].Count != 3 {
		t.Errorf("perf keys, all builds: %+v", keys.Keys)
	}

	var values models.ContextValuesResponse
	getAdmin(t, router, "/api/context/values", withParams(lastHour(), "key", "levelId"), &values)
	if len(values.Values) != 2 || values.Values[0] != (models.ContextValueStat{Value: "1", Count: 6}) || values.Values[1] != (models.ContextValueStat{Value: "2", Count: 3}) {
		t.Errorf("values: %+v", values)
	}

	dist := func(params ...string) models.ContextDistribution {
		var d models.ContextDistribution
		getAdmin(t, router, "/api/context/distribution", withParams(lastHour(), params...), &d)
		return d
	}
	d := dist("key", "levelId", "metric", "fps")
	// 1: fps 60, 50 (s1); "" : 40 (s5, legacy); 2: 30 (s2)
	if d.Total != 4 || d.Unit != "fps" || len(d.Rows) != 3 || d.Truncated ||
		d.Rows[0].Value != "1" || d.Rows[0].Count != 2 || d.Rows[0].Sessions != 1 || d.Rows[0].P50 < 50 || d.Rows[0].P50 > 60 ||
		d.Rows[1].Value != "" || d.Rows[1].Count != 1 || !near(d.Rows[1].P50, 40) ||
		d.Rows[2].Value != "2" || !near(d.Rows[2].P95, 30) {
		t.Errorf("fps by levelId: %+v", d)
	}
	d = dist("key", "levelId", "metric", "exceptions")
	if d.Total != 4 || len(d.Rows) != 3 || d.Rows[0] != (models.ContextDistributionRow{Value: "1", Count: 2, Sessions: 1}) {
		t.Errorf("exceptions by levelId: %+v", d)
	}
	d = dist("key", "levelId", "metric", "exceptions", "build_type", "all", "fingerprint", contextFpE2)
	if d.Total != 5 || len(d.Rows) != 1 || d.Rows[0].Value != "1" || d.Rows[0].Count != 5 {
		t.Errorf("E2 by levelId: %+v", d)
	}
	d = dist("key", "stageType", "metric", "crashes")
	if d.Total != 1 || len(d.Rows) != 1 || d.Rows[0].Value != "boss" {
		t.Errorf("crashes by stageType: %+v", d)
	}
	for metric, want := range map[string]float64{"janks": 80, "startup": 600, "scene_load": 500} {
		d = dist("key", "levelId", "metric", metric)
		if len(d.Rows) != 1 || d.Rows[0].Value != "1" || !near(d.Rows[0].P50, want) || d.Unit != "ms" {
			t.Errorf("%s by levelId: %+v", metric, d)
		}
	}
}

// Without start_time/end_time every endpoint uses its default range, which
// ends now (the data is 30 minutes old).
func TestContext_DefaultTimeRange(t *testing.T) {
	router := setupContextTest(t)
	var s models.DashboardSummary
	getAdmin(t, router, "/api/summary", nil, &s)
	var e models.ExceptionListResponse
	getAdmin(t, router, "/api/exceptions", nil, &e)
	var c models.CrashListResponse
	getAdmin(t, router, "/api/crashes", nil, &c)
	var d models.CrashDetail
	getAdmin(t, router, "/api/crashes/detail", url.Values{"fingerprint": {"C1"}}, &d)
	var ts models.TimeSeriesResponse
	getAdmin(t, router, "/api/timeseries", url.Values{"metric": {"fps"}}, &ts)
	var dist models.DistributionResponse
	getAdmin(t, router, "/api/distribution", url.Values{"metric": {"fps"}}, &dist)
	var keys models.ContextKeysResponse
	getAdmin(t, router, "/api/context/keys", nil, &keys)
	var cd models.ContextDistribution
	getAdmin(t, router, "/api/context/distribution", url.Values{"key": {"levelId"}, "metric": {"fps"}}, &cd)
	if s.TotalSessions != 3 || e.TotalCount != 1 || c.TotalCount != 1 || d.Count != 1 || len(ts.Data) != 1 ||
		dist.Buckets[3].Count+dist.Buckets[4].Count == 0 || len(keys.Keys) != 2 || cd.Total != 4 {
		t.Errorf("summary %d, exceptions %d, crashes %d, crash detail %d, timeseries %d, distribution %+v, keys %d, ctx distribution %d",
			s.TotalSessions, e.TotalCount, c.TotalCount, d.Count, len(ts.Data), dist.Buckets, len(keys.Keys), cd.Total)
	}
}

// The time range is compared at millisecond precision: an event 100 ms
// before end_time is inside it (the driver binds a time.Time argument with
// second precision, which used to drop the last second before end_time).
func TestAdminFilter_TimeRangeHasMillisecondPrecision(t *testing.T) {
	admin := adminClient(t)
	db := fmt.Sprintf("ozx_apm_apm5_ms_%d", time.Now().UnixNano()%1e9)
	createDB(t, admin, db)
	cfg := testutil.TestClickHouseConfig()
	cfg.Database = db
	client, err := testutil.SetupTestDatabase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	repo := storage.NewRepository(client, zap.NewNop())
	second := time.Now().Add(-time.Hour).Truncate(time.Second)
	at := second.Add(800 * time.Millisecond)
	must(t, repo.InsertPerfSamples(context.Background(), []models.PerfSample{{Timestamp: at, EventID: "ms", AppVersion: "1", Platform: "Android", SessionID: "s", DeviceID: "d", FPS: 60}}))
	router := api.NewAdminRouter(&config.Config{}, repo, zap.NewNop())

	for _, c := range []struct {
		start, end time.Time
		want       int64
	}{
		{second.Add(-time.Minute), second.Add(900 * time.Millisecond), 1}, // end in the same second, after the event
		{second.Add(-time.Minute), second.Add(700 * time.Millisecond), 0}, // end in the same second, before the event
		{second.Add(850 * time.Millisecond), second.Add(time.Minute), 0},  // start after the event
		{second.Add(800 * time.Millisecond), second.Add(800 * time.Millisecond), 1},
	} {
		var s models.DashboardSummary
		getAdmin(t, router, "/api/summary", url.Values{
			"start_time": {c.start.Format(time.RFC3339Nano)}, "end_time": {c.end.Format(time.RFC3339Nano)},
		}, &s)
		if s.TotalEvents != c.want {
			t.Errorf("range %s .. %s: %d events, want %d", c.start.Format("15:04:05.000"), c.end.Format("15:04:05.000"), s.TotalEvents, c.want)
		}
	}
}

// A crash detail whose filter matches nothing is a 404, not a 500.
func TestContext_CrashDetailFilteredToNothingIs404(t *testing.T) {
	router := setupContextTest(t)
	for _, path := range []string{
		"/api/crashes/detail?fingerprint=C1&build_type=dev",
		"/api/crashes/detail?fingerprint=C1&ctx=levelId:1",
		"/api/crashes/detail?fingerprint=nope",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestContext_BadParametersAre400(t *testing.T) {
	router := setupContextTest(t)
	for _, path := range []string{
		"/api/summary?build_type=debug",
		"/api/exceptions?ctx=no-separator",
		"/api/crashes?ctx=bad%20key:1",
		"/api/timeseries?metric=fps&build_type=x",
		"/api/context/keys?source=apm_perf_samples",
		"/api/context/keys?ctx=k'ey:1",
		"/api/context/values",
		"/api/context/values?key=bad%20key",
		"/api/context/distribution?key=levelId",
		"/api/context/distribution?key=levelId&metric=fps)",
		"/api/context/distribution?metric=fps",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

// An empty database (or a time range without data) never fails.
func TestContext_EmptyRange(t *testing.T) {
	router := setupContextTest(t)
	empty := url.Values{
		"start_time": {time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339)},
		"end_time":   {time.Now().Add(-9 * 24 * time.Hour).Format(time.RFC3339)},
	}
	var keys models.ContextKeysResponse
	getAdmin(t, router, "/api/context/keys", empty, &keys)
	var values models.ContextValuesResponse
	getAdmin(t, router, "/api/context/values", withParams(empty, "key", "levelId"), &values)
	var d models.ContextDistribution
	getAdmin(t, router, "/api/context/distribution", withParams(empty, "key", "levelId", "metric", "fps"), &d)
	if keys.Keys == nil || len(keys.Keys) != 0 || values.Values == nil || len(values.Values) != 0 || d.Rows == nil || len(d.Rows) != 0 || d.Total != 0 {
		t.Errorf("keys %+v values %+v dist %+v", keys, values, d)
	}
}
