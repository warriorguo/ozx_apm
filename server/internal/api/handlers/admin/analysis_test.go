package admin

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

type fakeAnalysisStore struct {
	filter     models.AdminFilter
	group      storage.CustomGroup
	metric     string
	crashTypes []string
	steps      []models.FunnelStep
	window     int64
	by         string
	err        error
}

func (f *fakeAnalysisStore) GetMemoryBreakdown(_ context.Context, flt models.AdminFilter, g storage.CustomGroup) (*models.MemoryBreakdownResponse, error) {
	f.filter, f.group = flt, g
	return &models.MemoryBreakdownResponse{GroupBy: g.String(), Rows: []models.MemoryBreakdownRow{}}, f.err
}

func (f *fakeAnalysisStore) GetRunPeaks(_ context.Context, flt models.AdminFilter, metric string, g storage.CustomGroup) (*models.RunPeaksResponse, error) {
	f.filter, f.metric, f.group = flt, metric, g
	return &models.RunPeaksResponse{Metric: metric, Rows: []models.RunPeakRow{}}, f.err
}

func (f *fakeAnalysisStore) GetSessionHealth(_ context.Context, flt models.AdminFilter, g storage.CustomGroup, types []string) (*models.SessionHealthResponse, error) {
	f.filter, f.group, f.crashTypes = flt, g, types
	return &models.SessionHealthResponse{Rows: []models.SessionHealthRow{}}, f.err
}

func (f *fakeAnalysisStore) GetFunnel(_ context.Context, flt models.AdminFilter, steps []models.FunnelStep, window int64, by string) (*models.FunnelResponse, error) {
	f.filter, f.steps, f.window, f.by = flt, steps, window, by
	return &models.FunnelResponse{Groups: []models.FunnelGroup{}}, f.err
}

func TestAnalysis_DefaultsAndGroups(t *testing.T) {
	store := &fakeAnalysisStore{}
	h := &AnalysisHandler{repo: store, logger: zap.NewNop()}

	if w := serve(h.MemoryBreakdown, "/api/memory/breakdown"); w.Code != 200 || store.group.String() != "app_version" || store.filter.BuildType != models.BuildFilterRelease {
		t.Fatalf("%d %s %+v", w.Code, w.Body, store.group)
	}
	if d := store.filter.EndTime.Sub(store.filter.StartTime); d != 7*24*time.Hour {
		t.Errorf("default range %v", d)
	}
	if w := serve(h.RunPeaks, "/api/memory/runs?key=stageType&ctx=roomId:3"); w.Code != 200 || store.metric != "peak_rss" || store.group.Kind != "ctx" || store.group.Key != "stageType" || len(store.filter.Context) != 1 {
		t.Errorf("%d %s %s %+v", w.Code, w.Body, store.metric, store.group)
	}
	if w := serve(h.RunPeaks, "/api/memory/runs?metric=peak_alloc&by=ram_class"); w.Code != 200 || store.metric != "peak_alloc" || store.group.Key != "ram_class" {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	if w := serve(h.Health, "/api/health/sessions?by=build_type&build_type=all"); w.Code != 200 || store.crashTypes != nil || store.group.Key != "build_type" {
		t.Errorf("%d %s %v", w.Code, w.Body, store.crashTypes)
	}
	if w := serve(h.Health, "/api/health/sessions?key=dataVersion&crash_types=native,%20oom,native"); w.Code != 200 || strings.Join(store.crashTypes, ",") != "native,oom" {
		t.Errorf("%d %s %v", w.Code, w.Body, store.crashTypes)
	}
}

func TestAnalysis_BadRequests(t *testing.T) {
	store := &fakeAnalysisStore{}
	h := &AnalysisHandler{repo: store, logger: zap.NewNop()}
	now := time.Now().UTC()
	longRange := "start_time=" + url.QueryEscape(now.Add(-40*24*time.Hour).Format(time.RFC3339)) + "&end_time=" + url.QueryEscape(now.Format(time.RFC3339))
	backwards := "start_time=" + url.QueryEscape(now.Format(time.RFC3339)) + "&end_time=" + url.QueryEscape(now.Add(-time.Hour).Format(time.RFC3339))
	cases := map[string]string{
		"/api/memory/breakdown?" + longRange:               "31 days",
		"/api/memory/breakdown?" + backwards:               "after",
		"/api/memory/breakdown?by=session_id":              "by must be",
		"/api/memory/breakdown?prop=x":                     "prop",
		"/api/memory/breakdown?key=bad%20key":              "key",
		"/api/memory/breakdown?build_type=nope":            "build_type",
		"/api/memory/runs?metric=fps":                      "metric",
		"/api/health/sessions?crash_types=native,exploded": "crash_types",
		"/api/funnel":              "steps parameter required",
		"/api/funnel?steps=%5B%5D": "1 to 8",
		"/api/funnel?steps=nope":   "JSON",
		"/api/funnel?steps=" + url.QueryEscape(`[{"name":"a b"}]`):                                            "name",
		"/api/funnel?steps=" + url.QueryEscape(`[{"name":"a","props":{"k":"v"},"x":1}]`):                      "JSON",
		"/api/funnel?steps=" + url.QueryEscape(`[{"name":"a","props":{"<k>":"v"}}]`):                          "property keys",
		"/api/funnel?steps=" + url.QueryEscape(`[{"name":"a","props":{"k":"`+strings.Repeat("v", 257)+`"}}]`): "256",
		"/api/funnel?steps=" + url.QueryEscape(`[{"name":"a"}]`) + "&window=0":                                "window",
		"/api/funnel?steps=" + url.QueryEscape(`[{"name":"a"}]`) + "&window=86401":                            "window",
		"/api/funnel?steps=" + url.QueryEscape(`[{"name":"a"}]`) + "&by=device_id":                            "by must be",
		"/api/funnel?steps=" + url.QueryEscape(`[{"name":"a"}]`) + "&" + longRange:                            "31 days",
		"/api/funnel?steps=" + url.QueryEscape("["+strings.Repeat(`{"name":"a"},`, 8)+`{"name":"a"}]`):        "1 to 8",
	}
	for target, want := range cases {
		handler := h.MemoryBreakdown
		switch {
		case strings.HasPrefix(target, "/api/memory/runs"):
			handler = h.RunPeaks
		case strings.HasPrefix(target, "/api/health"):
			handler = h.Health
		case strings.HasPrefix(target, "/api/funnel"):
			handler = h.Funnel
		}
		rec := serve(handler, target)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %q, want 400 with %q", target, rec.Code, rec.Body.String(), want)
		}
	}
	if store.steps != nil || store.metric != "" {
		t.Error("the store was called for a bad request")
	}
}

func TestAnalysis_Funnel(t *testing.T) {
	store := &fakeAnalysisStore{}
	h := &AnalysisHandler{repo: store, logger: zap.NewNop()}
	steps := `[{"name":"run_start"},{"name":"room_enter","props":{"roomType":"boss"}},{"name":"run_end","props":{}}]`
	w := serve(h.Funnel, "/api/funnel?steps="+url.QueryEscape(steps)+"&app_version=1.2&by=app_version")
	if w.Code != 200 || len(store.steps) != 3 || store.steps[1].Props["roomType"] != "boss" || store.steps[2].Props != nil {
		t.Fatalf("%d %s %+v", w.Code, w.Body, store.steps)
	}
	if store.window != storage.DefaultFunnelWindowSec || store.by != "app_version" || store.filter.AppVersion != "1.2" {
		t.Errorf("%d %q %+v", store.window, store.by, store.filter)
	}
	if w := serve(h.Funnel, "/api/funnel?steps="+url.QueryEscape(`[{"name":"a"}]`)+"&window=600"); w.Code != 200 || store.window != 600 || store.by != "" {
		t.Errorf("%d %d", w.Code, store.window)
	}
}

func TestAnalysis_StorageErrors(t *testing.T) {
	store := &fakeAnalysisStore{err: context.DeadlineExceeded}
	h := &AnalysisHandler{repo: store, logger: zap.NewNop()}
	if w := serve(h.Health, "/api/health/sessions"); w.Code != 503 {
		t.Errorf("timeout: %d", w.Code)
	}
	store.err = context.Canceled
	if w := serve(h.MemoryBreakdown, "/api/memory/breakdown"); w.Code != 503 {
		t.Errorf("canceled: %d", w.Code)
	}
	store.err = errors.New("clickhouse: secret detail")
	if w := serve(h.RunPeaks, "/api/memory/runs"); w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
		t.Errorf("error: %d %s", w.Code, w.Body)
	}
	if w := serve((&AnalysisHandler{logger: zap.NewNop()}).Funnel, "/api/funnel?steps="+url.QueryEscape(`[{"name":"a"}]`)); w.Code != 500 {
		t.Errorf("no repository: %d", w.Code)
	}
}
