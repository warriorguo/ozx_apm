package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/alert"
	"github.com/warriorguo/ozx_apm/server/internal/api"
	"github.com/warriorguo/ozx_apm/server/internal/api/handlers/admin"
	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/tests/integration/testutil"
)

// APM-8: alerting on a real ClickHouse: SDK events through the ingest
// router, the evaluator reading ClickHouse and posting to a local webhook,
// issue states through the admin API.

const alertTestToken = "apm8-integration-token"
const alertAdminBase = "https://apm-admin.example.test"

// webhookSink records the bodies posted to it; fail answers 500.
type webhookSink struct {
	mu     sync.Mutex
	bodies []alert.Payload
	raw    []string
	fail   bool
	srv    *httptest.Server
}

func newWebhookSink(t *testing.T) *webhookSink {
	s := &webhookSink{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var p alert.Payload
		if err := json.Unmarshal(b, &p); err != nil {
			t.Errorf("webhook body is not the generic payload: %s", b)
		}
		s.bodies = append(s.bodies, p)
		s.raw = append(s.raw, string(b))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *webhookSink) received() []alert.Payload {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]alert.Payload{}, s.bodies...)
}

func (s *webhookSink) url() string { return s.srv.URL + "/hooks/T000/s3cr3t" }

type alertEnv struct {
	*apm4Env
	repo  *storage.Repository
	sink  *webhookSink
	admin http.Handler
}

func alertConfig() alert.Config {
	cfg := config.AlertConfig{}
	// The defaults of config.Load.
	cfg.Interval, cfg.NewIssueMaxPerCycle, cfg.NewIssueMaxPerHour, cfg.NewIssueWindow, cfg.SweepInterval = time.Minute, 10, 30, 26*time.Hour, 6*time.Hour
	cfg.CrashRateWindow, cfg.CrashRateBaselineWindow, cfg.CrashRateCheckInterval = time.Hour, 24*time.Hour, 5*time.Minute
	cfg.CrashRateMinSessions, cfg.CrashRateMinCrashed, cfg.CrashRateMinRate, cfg.CrashRateFactor, cfg.CrashRateCooldown = 50, 5, 0.005, 2, 6*time.Hour
	cfg.StartupEnabled, cfg.StartupWindow, cfg.StartupCheckInterval = true, 7*24*time.Hour, time.Hour
	cfg.StartupMinSamples, cfg.StartupIncrease, cfg.StartupMinDeltaMs = 100, 0.2, 100
	return alert.ConfigFrom(cfg)
}

func setupAlertEnv(t *testing.T) *alertEnv {
	t.Helper()
	e := setupAPM4(t, false)
	repo := storage.NewRepository(e.client, zap.NewNop())
	cfg := &config.Config{}
	cfg.AdminServer.AlertToken = alertTestToken
	return &alertEnv{apm4Env: e, repo: repo, sink: newWebhookSink(t), admin: api.NewAdminRouter(cfg, repo, zap.NewNop())}
}

// evaluator returns a new evaluator (a new process) posting to the sink.
func (e *alertEnv) evaluator(t *testing.T) *alert.Evaluator {
	t.Helper()
	n, err := alert.NewNotifier(e.sink.url(), alert.FormatGeneric, alertAdminBase)
	if err != nil {
		t.Fatal(err)
	}
	return alert.NewEvaluator(e.repo, n, alertConfig(), zap.NewNop())
}

func cycle(ev *alert.Evaluator) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ev.Cycle(ctx)
}

// exceptionEvent is an SDK exception event of the type, thrown in method, in
// the version and build type.
// The event carries a fingerprint as the SDK sends one (ingest requires it):
// the same for the same type and stack, whatever the message's numbers. The
// server stores its own fingerprint computed from the same fields.
func (e *alertEnv) exceptionEvent(typ, method, msg, version, buildType, session string) string {
	message, stack := typ+": "+msg, apm4Handler(typ, method, msg, 42)
	clientFP := fingerprint.Analyze(fingerprint.Input{LogType: "exception", Message: message, Stack: stack}).Fingerprint
	return e.event(session, "1", map[string]interface{}{
		"app_version": version, "build_type": buildType, "log_type": "exception", "fingerprint": clientFP,
		"message": message, "stack": stack,
		"timestamp": time.Now().Add(-time.Minute).UnixMilli() + int64(e.n),
	})
}

func (e *alertEnv) postState(t *testing.T, body string) (int, models.IssueState) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/issues/state", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(admin.AdminActionHeader, "1")
	w := httptest.NewRecorder()
	e.admin.ServeHTTP(w, req)
	var st models.IssueState
	if w.Code == http.StatusOK {
		decodeInto(t, w.Body.Bytes(), &st)
	}
	return w.Code, st
}

func (e *alertEnv) alertList(t *testing.T) []models.AlertSummary {
	t.Helper()
	var resp struct {
		Alerts []models.AlertSummary `json:"alerts"`
	}
	getAdmin(t, e.admin, "/api/alerts", url.Values{}, &resp)
	return resp.Alerts
}

// APM-1 acceptance 8: a new version introduces a new fingerprint -> a
// webhook is received, once.
func TestAlert_NewFingerprintInNewVersionReachesTheWebhook(t *testing.T) {
	e := setupAlertEnv(t)
	e.post(t,
		e.exceptionEvent("System.InvalidOperationException", "RoomBuilder.LoadLayout", "Room 7 not ready", "1.0.0", "release", "s1"),
		e.exceptionEvent("System.InvalidOperationException", "RoomBuilder.LoadLayout", "Room 8 not ready", "1.0.0", "release", "s2"))
	ev := e.evaluator(t)
	cycle(ev) // baseline: the existing group is not new
	if got := e.sink.received(); len(got) != 0 {
		t.Fatalf("baseline sent %d alerts", len(got))
	}

	// 2.0.0 brings a new exception; a dev build brings another one.
	e.post(t,
		e.exceptionEvent("System.NullReferenceException", "EnemySpawner.Spawn", "Object reference not set", "2.0.0", "release", "s3"),
		// The latest row's message is the alert's.
		e.exceptionEvent("System.NullReferenceException", "EnemySpawner.Spawn", "Object reference not set @all <!channel> https://evil.example", "2.0.0", "release", "s4"),
		e.exceptionEvent("System.ArgumentException", "DebugMenu.Open", "dev only", "2.0.0-dev", "dev", "s5"),
		e.exceptionEvent("System.InvalidOperationException", "RoomBuilder.LoadLayout", "Room 9 not ready", "2.0.0", "release", "s6"))
	cycle(ev)
	got := e.sink.received()
	if len(got) != 1 {
		t.Fatalf("webhooks %d, want 1: %+v", len(got), got)
	}
	groups := groupsByType(t, e.apm4Env, lastHour())
	nre := groups["System.NullReferenceException"]
	p := got[0]
	if p.Schema != alert.PayloadSchema || p.Type != alert.TypeNewIssue || p.Kind != "exception" || p.AppVersion != "2.0.0" ||
		p.Fingerprint != nre.Fingerprint || nre.Fingerprint == "" {
		t.Fatalf("payload %+v (group %q)", p, nre.Fingerprint)
	}
	if p.URL != alertAdminBase+"/exceptions/detail?fingerprint="+url.QueryEscape(nre.Fingerprint) {
		t.Errorf("url %q", p.URL)
	}
	if !strings.Contains(p.Text, "NullReferenceException") || !strings.Contains(p.Text, "Object reference not set") || strings.Contains(p.Text, "@all") ||
		strings.Contains(p.Text, "<!channel>") || strings.Contains(p.Text, "https://evil") {
		t.Errorf("text %q", p.Text)
	}
	if p.Metrics["count"] != 2 || p.Metrics["sessions"] != 2 {
		t.Errorf("metrics %v", p.Metrics)
	}
	if strings.Contains(e.sink.raw[0], "s3cr3t") {
		t.Error("the webhook URL is in the payload")
	}

	// Later evaluations and a restarted process send nothing more.
	cycle(ev)
	cycle(e.evaluator(t))
	if n := len(e.sink.received()); n != 1 {
		t.Fatalf("webhooks %d after more evaluations", n)
	}
	list := e.alertList(t)
	if len(list) != 1 || list[0].Status != models.AlertSent || list[0].Attempts != 1 || !strings.Contains(list[0].Title, "NullReferenceException") {
		t.Errorf("alert list %+v", list)
	}
	// Each group in the list is unresolved.
	if nre.Status != models.IssueUnresolved {
		t.Errorf("group status %q", nre.Status)
	}
}

func (e *alertEnv) insertExceptions(t *testing.T, fp, version string, at time.Time, n int) {
	t.Helper()
	var rows []models.Exception
	for i := 0; i < n; i++ {
		e.n++
		rows = append(rows, models.Exception{Timestamp: at.Add(time.Duration(i) * time.Millisecond), EventID: fmt.Sprintf("apm8-%d", e.n),
			AppVersion: version, Platform: "Android", SessionID: fmt.Sprintf("s-%s-%d", version, i), DeviceID: "d", Scene: "Main",
			Fingerprint: fp, Message: "boom", Stack: "at X", Count: 1, EventContext: models.EventContext{BuildType: "release"}})
	}
	must(t, e.repo.InsertExceptions(context.Background(), rows))
}

// Resolving through the admin API, the status filter, and a regression:
// old clients after the resolution are not one, the fix version is.
func TestAlert_ResolveAndRegression(t *testing.T) {
	e := setupAlertEnv(t)
	now := time.Now()
	e.insertExceptions(t, "fp-room", "1.4.2", now.Add(-2*time.Hour), 3)
	e.insertExceptions(t, "fp-room", "1.4.10", now.Add(-90*time.Minute), 1)
	e.insertExceptions(t, "fp-other", "1.4.2", now.Add(-2*time.Hour), 1)
	ev := e.evaluator(t)
	cycle(ev)

	code, st := e.postState(t, `{"kind":"exception","fingerprint":"fp-room","status":"resolved","resolved_in_version":"1.5.0","comment":"null check"}`)
	if code != 200 || st.Status != "resolved" || st.LastSeenVersion != "1.4.10" || st.ResolvedInVersion != "1.5.0" || st.ResolvedAt == nil {
		t.Fatalf("resolve: %d %+v", code, st)
	}
	if code, _ := e.postState(t, `{"kind":"exception","fingerprint":"no-such-fp","status":"resolved"}`); code != 404 {
		t.Errorf("unknown fingerprint: %d", code)
	}
	params := url.Values{"start_time": {now.Add(-3 * time.Hour).Format(time.RFC3339)}, "end_time": {now.Add(time.Minute).Format(time.RFC3339)}}
	var resolved, open models.ExceptionListResponse
	getAdmin(t, e.admin, "/api/exceptions", withParams(params, "status", "resolved"), &resolved)
	getAdmin(t, e.admin, "/api/exceptions", withParams(params, "status", "unresolved"), &open)
	if len(resolved.Exceptions) != 1 || resolved.Exceptions[0].Fingerprint != "fp-room" || resolved.Exceptions[0].Status != "resolved" ||
		resolved.Exceptions[0].ResolvedInVersion != "1.5.0" || len(open.Exceptions) != 1 || open.Exceptions[0].Fingerprint != "fp-other" {
		t.Fatalf("status filter: resolved %+v open %+v", resolved.Exceptions, open.Exceptions)
	}
	var detail models.ExceptionDetail
	getAdmin(t, e.admin, "/api/exceptions/detail", withParams(params, "fingerprint", "fp-room"), &detail)
	if detail.Status != "resolved" {
		t.Errorf("detail status %q", detail.Status)
	}

	// Old clients still on 1.4.x after the resolution: no regression.
	e.insertExceptions(t, "fp-room", "1.4.10", time.Now(), 2)
	cycle(ev)
	if n := len(e.sink.received()); n != 0 {
		t.Fatalf("an old version's occurrence alerted (%d)", n)
	}
	// The fix version has it: one regression alert, the issue reopened.
	e.insertExceptions(t, "fp-room", "1.5.0", time.Now(), 2)
	cycle(ev)
	cycle(ev)
	got := e.sink.received()
	if len(got) != 1 || got[0].Type != alert.TypeRegression || got[0].AppVersion != "1.5.0" || got[0].Metrics["count"] != 2 {
		t.Fatalf("regression webhooks %+v", got)
	}
	var cur models.IssueState
	getAdmin(t, e.admin, "/api/issues/state", url.Values{"kind": {"exception"}, "fingerprint": {"fp-room"}}, &cur)
	if cur.Status != "unresolved" || cur.RegressedInVersion != "1.5.0" || cur.UpdatedBy != "alert" || cur.ResolvedInVersion != "1.5.0" {
		t.Fatalf("state after the regression %+v", cur)
	}
	// Two rows were written (a merge may since have kept only the latest);
	// FINAL reads the latest.
	if n := queryUint(t, e.client, "SELECT count() FROM apm_issue_states FINAL WHERE fingerprint = 'fp-room' AND status = 'unresolved'"); n != 1 {
		t.Errorf("FINAL rows %d", n)
	}

	// Reopened and resolved again by hand, then it comes back in 1.6.0.
	if code, _ := e.postState(t, `{"kind":"exception","fingerprint":"fp-room","status":"resolved"}`); code != 200 {
		t.Fatal(code)
	}
	time.Sleep(5 * time.Millisecond)
	e.insertExceptions(t, "fp-room", "1.6.0", time.Now(), 1)
	cycle(ev)
	if got := e.sink.received(); len(got) != 2 || got[1].AppVersion != "1.6.0" {
		t.Fatalf("second regression %+v", got)
	}
}

// Crash rate spike and a new crash group; crash states through the API.
func TestAlert_CrashRateSpikeAndNewCrash(t *testing.T) {
	e := setupAlertEnv(t)
	cycle(e.evaluator(t)) // empty baseline
	now := time.Now().Add(-10 * time.Minute)
	var perf []models.PerfSample
	var crashes []models.Crash
	for i := 0; i < 120; i++ {
		s := fmt.Sprintf("cs-%d", i)
		perf = append(perf, models.PerfSample{Timestamp: now, EventID: s + "p", AppVersion: "3.0.0", Platform: "Android",
			SessionID: s, DeviceID: "d" + s, FPS: 60, EventContext: models.EventContext{BuildType: "release"}})
		if i < 12 {
			crashes = append(crashes, models.Crash{Timestamp: now.Add(time.Second), EventID: s + "c", AppVersion: "3.0.0", Platform: "Android",
				SessionID: s, DeviceID: "d" + s, CrashType: "native_signal", Fingerprint: "crash-fp", Stack: "SIGSEGV @here\n#00 pc 0001 libgame.so",
				Breadcrumbs: []string{}, EventContext: models.EventContext{BuildType: "release"}})
		}
	}
	must(t, e.repo.InsertPerfSamples(context.Background(), perf))
	must(t, e.repo.InsertCrashes(context.Background(), crashes))

	cycle(e.evaluator(t)) // a new process: the crash check is due
	byType := map[string]alert.Payload{}
	for _, p := range e.sink.received() {
		byType[p.Type] = p
	}
	if len(e.sink.received()) != 2 {
		t.Fatalf("webhooks %+v", e.sink.received())
	}
	spike := byType[alert.TypeCrashRateSpike]
	if spike.AppVersion != "3.0.0" || spike.Metrics["crashed_sessions"] != 12 || spike.Metrics["sessions"] < 110 {
		t.Errorf("spike %+v", spike)
	}
	nc := byType[alert.TypeNewIssue]
	if nc.Kind != "crash" || nc.Fingerprint != "crash-fp" || nc.URL != alertAdminBase+"/crashes?fingerprint=crash-fp" || strings.Contains(nc.Text, "@here") {
		t.Errorf("new crash %+v", nc)
	}
	// The cooldown: another process right after sends nothing.
	cycle(e.evaluator(t))
	if n := len(e.sink.received()); n != 2 {
		t.Errorf("webhooks %d after the cooldown check", n)
	}

	if code, st := e.postState(t, `{"kind":"crash","fingerprint":"crash-fp","status":"resolved","resolved_in_version":"3.0.1"}`); code != 200 || st.LastSeenVersion != "3.0.0" {
		t.Fatalf("resolve crash: %d %+v", code, st)
	}
	var list models.CrashListResponse
	getAdmin(t, e.admin, "/api/crashes", lastHour(), &list)
	if len(list.Crashes) != 1 || list.Crashes[0].Status != "resolved" || list.Crashes[0].ResolvedInVersion != "3.0.1" {
		t.Errorf("crash list %+v", list.Crashes)
	}
}

func TestAlert_StartupP95Regression(t *testing.T) {
	e := setupAlertEnv(t)
	var rows []models.Startup
	for i := 0; i < 120; i++ {
		for _, v := range []struct {
			version string
			ms      float32
			age     time.Duration
		}{{"1.9.0", 2000, 48 * time.Hour}, {"2.0.0", 3000, time.Hour}} {
			rows = append(rows, models.Startup{Timestamp: time.Now().Add(-v.age + time.Duration(i)*time.Second),
				EventID: fmt.Sprintf("st-%s-%d", v.version, i), AppVersion: v.version, Platform: "Android",
				SessionID: fmt.Sprintf("ss-%s-%d", v.version, i), Phase1Ms: v.ms / 2, Phase2Ms: v.ms / 4, TTIMs: v.ms / 4,
				EventContext: models.EventContext{BuildType: "release"}})
		}
	}
	must(t, e.repo.InsertStartups(context.Background(), rows))
	cycle(e.evaluator(t))
	cycle(e.evaluator(t))
	got := e.sink.received()
	if len(got) != 1 || got[0].Type != alert.TypeStartupP95 || got[0].AppVersion != "2.0.0" ||
		got[0].Metrics["p95_ms"] < 2900 || got[0].Metrics["previous_p95_ms"] > 2100 {
		t.Fatalf("startup alerts %+v", got)
	}
}

// Two processes evaluating at the same time send each alert once.
func TestAlert_TwoEvaluatorsSendOnce(t *testing.T) {
	e := setupAlertEnv(t)
	cycle(e.evaluator(t))
	e.insertExceptions(t, "fp-a", "4.0.0", time.Now(), 1)
	e.insertExceptions(t, "fp-b", "4.0.0", time.Now(), 1)
	a, b := e.evaluator(t), e.evaluator(t)
	var wg sync.WaitGroup
	for _, ev := range []*alert.Evaluator{a, b} {
		wg.Add(1)
		go func(ev *alert.Evaluator) {
			defer wg.Done()
			cycle(ev)
		}(ev)
	}
	wg.Wait()
	cycle(a)
	cycle(b)
	got := e.sink.received()
	fps := map[string]int{}
	for _, p := range got {
		fps[p.Fingerprint]++
	}
	if len(got) != 2 || fps["fp-a"] != 1 || fps["fp-b"] != 1 {
		t.Fatalf("webhooks %v", fps)
	}
}

// The test endpoint queues an alert that the evaluator sends; a failing
// webhook is recorded without the URL.
func TestAlert_TestEndpointAndFailure(t *testing.T) {
	e := setupAlertEnv(t)
	post := func(auth string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/alerts/test", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		e.admin.ServeHTTP(w, req)
		return w.Code
	}
	if code := post(""); code != 401 {
		t.Fatalf("without the token: %d", code)
	}
	if code := post("Bearer " + alertTestToken); code != 202 {
		t.Fatalf("with the token: %d", code)
	}
	cycle(e.evaluator(t))
	got := e.sink.received()
	if len(got) != 1 || got[0].Type != alert.TypeTest || got[0].URL != alertAdminBase+"/" {
		t.Fatalf("test alert %+v", got)
	}

	// A failing webhook: the attempt is recorded as failed, without the URL.
	e.sink.mu.Lock()
	e.sink.fail = true
	e.sink.mu.Unlock()
	time.Sleep(10 * time.Second) // the endpoint allows one test per 10 s
	if code := post("Bearer " + alertTestToken); code != 202 {
		t.Fatalf("second test: %d", code)
	}
	cycle(e.evaluator(t))
	var failed *models.AlertSummary
	for _, a := range e.alertList(t) {
		if a.Status == models.AlertFailed {
			a := a
			failed = &a
		}
	}
	if failed == nil || !strings.Contains(failed.Error, "answered 500") || strings.Contains(failed.Error, "s3cr3t") || strings.Contains(failed.Error, "/hooks/") {
		t.Fatalf("failed alert %+v", failed)
	}
}

// The production service account (SELECT, INSERT, CREATE TABLE, ALTER ADD
// COLUMN, ALTER DELETE) creates the tables and runs everything.
func TestAlert_ServiceAccount(t *testing.T) {
	adminCH := adminClient(t)
	ctx := context.Background()
	user := fmt.Sprintf("apm8_svc_%d", time.Now().UnixNano()%1e9)
	if err := adminCH.Exec(ctx, "CREATE USER "+user+" IDENTIFIED WITH no_password"); err != nil {
		t.Skipf("cannot create a user on this ClickHouse (access management off?): %v", err)
	}
	t.Cleanup(func() { adminCH.Exec(context.Background(), "DROP USER IF EXISTS "+user) })
	db := schemaTestDB(t, "apm8_svc")
	createDB(t, adminCH, db)
	owner, _ := connect(t, db, "")
	execAll(t, owner, legacySchema(t, "production"))
	must(t, adminCH.Exec(ctx, "GRANT SELECT, INSERT, CREATE TABLE, ALTER ADD COLUMN, ALTER DELETE ON "+db+".* TO "+user))

	client, logs := connect(t, db, user)
	must(t, client.Migrate(ctx))
	if got := warnings(logs, "could not create an alerting table"); len(got) != 0 {
		t.Fatalf("alert table warnings %v", got)
	}
	for _, table := range []string{storage.IssueStatesTable, storage.AlertsTable} {
		if n := queryUint(t, client, "SELECT count() FROM system.tables WHERE database = currentDatabase() AND name = ?", table); n != 1 {
			t.Errorf("%s not created", table)
		}
	}
	must(t, client.Migrate(ctx)) // idempotent

	repo := storage.NewRepository(client, zap.NewNop())
	sink := newWebhookSink(t)
	n, _ := alert.NewNotifier(sink.url(), alert.FormatGeneric, "")
	ev := alert.NewEvaluator(repo, n, alertConfig(), zap.NewNop())
	cycle(ev)
	must(t, repo.InsertExceptions(ctx, []models.Exception{{Timestamp: time.Now(), EventID: "svc-1", AppVersion: "5.0.0",
		SessionID: "s", Fingerprint: "svc-fp", Message: "m", Count: 1, EventContext: models.EventContext{BuildType: "release"}}}))
	cycle(ev)
	if got := sink.received(); len(got) != 1 || got[0].Fingerprint != "svc-fp" {
		t.Fatalf("as the service account: %+v", got)
	}
	cfg := &config.Config{}
	adminRouter := api.NewAdminRouter(cfg, repo, zap.NewNop())
	req := httptest.NewRequest(http.MethodPost, "/api/issues/state", strings.NewReader(`{"kind":"exception","fingerprint":"svc-fp","status":"resolved"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(admin.AdminActionHeader, "1")
	w := httptest.NewRecorder()
	adminRouter.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("resolve as the service account: %d %s", w.Code, w.Body)
	}
}

// Migrate on a fresh database creates both tables with the schema.go
// definitions, and migrate.sql's statements for them are the same.
func TestAlert_SchemaFromMigrateAndScript(t *testing.T) {
	adminCH := adminClient(t)
	db := schemaTestDB(t, "apm8_schema")
	createDB(t, adminCH, db)
	cfg := testutil.TestClickHouseConfig()
	cfg.Database = db
	client, err := testutil.SetupTestDatabase(cfg)
	must(t, err)
	t.Cleanup(func() { client.Close() })
	var engine, key string
	must(t, client.Conn().QueryRow(context.Background(),
		"SELECT engine_full, sorting_key FROM system.tables WHERE database = currentDatabase() AND name = 'apm_issue_states'").Scan(&engine, &key))
	if !strings.HasPrefix(engine, "ReplacingMergeTree(version)") || key != "kind, fingerprint" {
		t.Errorf("apm_issue_states: %s / %s", engine, key)
	}
	found := 0
	for _, stmt := range sqlStatements(t, "../../scripts/migrate.sql") {
		if strings.Contains(stmt, "CREATE TABLE IF NOT EXISTS apm_issue_states") || strings.Contains(stmt, "CREATE TABLE IF NOT EXISTS apm_alerts") {
			must(t, client.Exec(context.Background(), stmt))
			found++
		}
	}
	if found != 2 {
		t.Errorf("migrate.sql has %d of the 2 tables", found)
	}
}
