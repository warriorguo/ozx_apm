package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

type fakeCustomStore struct {
	filter models.AdminFilter
	metric bool
	name   string
	group  storage.CustomGroup
	err    error
}

func (f *fakeCustomStore) GetCustomEvents(_ context.Context, flt models.AdminFilter) (*models.CustomEventsResponse, error) {
	f.filter = flt
	return &models.CustomEventsResponse{Events: []models.CustomEventStat{{Name: "room_enter", Count: 3}}}, f.err
}

func (f *fakeCustomStore) GetCustomMetrics(_ context.Context, flt models.AdminFilter) (*models.CustomMetricsResponse, error) {
	f.filter = flt
	return &models.CustomMetricsResponse{Metrics: []models.CustomMetricStat{{Name: "room_switch_ms", Count: 2, P95: 900}}}, f.err
}

func (f *fakeCustomStore) GetCustomDistribution(_ context.Context, flt models.AdminFilter, metric bool, name string, g storage.CustomGroup) (*models.CustomDistribution, error) {
	f.filter, f.metric, f.name, f.group = flt, metric, name, g
	return &models.CustomDistribution{Name: name, GroupBy: g.String(), Rows: []models.CustomMetricStat{}}, f.err
}

func serve(h http.HandlerFunc, target string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

func TestCustomHandler_ListsTakeTheCommonFilter(t *testing.T) {
	store := &fakeCustomStore{}
	h := &CustomHandler{repo: store, logger: zap.NewNop()}
	w := serve(h.ListMetrics, "/api/custom/metrics?build_type=dev&ctx=roomId:12&ctx=roomId:13")
	if w.Code != 200 || store.filter.BuildType != "dev" || len(store.filter.Context) != 1 || len(store.filter.Context[0].Values) != 2 {
		t.Fatalf("%d %s %+v", w.Code, w.Body, store.filter)
	}
	var resp models.CustomMetricsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Metrics[0].P95 != 900 {
		t.Errorf("%s %v", w.Body, err)
	}
	if w := serve(h.ListEvents, "/api/custom/events"); w.Code != 200 || store.filter.BuildType != models.BuildFilterRelease {
		t.Errorf("%d %+v", w.Code, store.filter)
	}
	for _, target := range []string{"/api/custom/events?build_type=x", "/api/custom/events?ctx=bad%20key:1"} {
		if w := serve(h.ListEvents, target); w.Code != 400 {
			t.Errorf("%s: %d", target, w.Code)
		}
	}
	store.err = errors.New("boom")
	if w := serve(h.ListEvents, "/api/custom/events"); w.Code != 500 {
		t.Errorf("error: %d", w.Code)
	}
	if w := serve((&CustomHandler{logger: zap.NewNop()}).ListEvents, "/api/custom/events"); w.Code != 500 {
		t.Errorf("no repo: %d", w.Code)
	}
}

func TestCustomHandler_DistributionGroups(t *testing.T) {
	store := &fakeCustomStore{}
	h := &CustomHandler{repo: store, logger: zap.NewNop()}
	cases := []struct {
		handler http.HandlerFunc
		target  string
		code    int
		metric  bool
		group   string
	}{
		{h.MetricDistribution, "/x?name=room_switch_ms&key=roomId", 200, true, "ctx:roomId"},
		{h.MetricDistribution, "/x?name=room_switch_ms", 200, true, "app_version"},
		{h.MetricDistribution, "/x?name=room_switch_ms&by=device_model", 200, true, "device_model"},
		{h.EventDistribution, "/x?name=room_enter&prop=seed", 200, false, "prop:seed"},
		{h.MetricDistribution, "/x?name=room_switch_ms&prop=seed", 400, false, ""},
		{h.MetricDistribution, "/x?name=room_switch_ms&key=roomId&by=platform", 400, false, ""},
		{h.MetricDistribution, "/x?name=room_switch_ms&by=session_id", 400, false, ""},
		{h.MetricDistribution, "/x?name=room_switch_ms&by=app_version%3BDROP", 400, false, ""},
		{h.MetricDistribution, "/x?name=room%20switch", 400, false, ""},
		{h.EventDistribution, "/x", 400, false, ""},
		{h.EventDistribution, "/x?name=a&key=bad%20key", 400, false, ""},
	}
	for _, c := range cases {
		*store = fakeCustomStore{}
		w := serve(c.handler, c.target)
		if w.Code != c.code {
			t.Errorf("%s: %d %s", c.target, w.Code, w.Body)
			continue
		}
		if c.code == 200 && (store.metric != c.metric || store.group.String() != c.group) {
			t.Errorf("%s: metric %v group %s", c.target, store.metric, store.group)
		}
	}
}
