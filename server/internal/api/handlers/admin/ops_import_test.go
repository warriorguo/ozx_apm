package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/opsimport"
)

type fakeOpsImportStore struct {
	fx  []models.OpsFXRate
	ads []models.OpsAdReport
	err error
}

func (f *fakeOpsImportStore) InsertOpsFXRates(_ context.Context, r []models.OpsFXRate, _ time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.fx = append(f.fx, r...)
	return nil
}
func (f *fakeOpsImportStore) InsertOpsAdReports(_ context.Context, r []models.OpsAdReport, _ time.Time) error {
	if f.err != nil {
		return f.err
	}
	f.ads = append(f.ads, r...)
	return nil
}

const opsToken = "ops-token-0123456789"

func postImport(h http.HandlerFunc, url, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func TestOpsImport_Token(t *testing.T) {
	core, logs := observer.New(zapcore.InfoLevel)
	store := &fakeOpsImportStore{}
	off := NewOpsImportHandler(store, zap.New(core), "")
	if w := postImport(off.FXRates, "/api/ops/import/fx-rates", "date,currency,usd_per_unit\n2026-10-19,EUR,1.07\n", opsToken); w.Code != http.StatusForbidden {
		t.Errorf("disabled: %d", w.Code)
	}
	h := NewOpsImportHandler(store, zap.New(core), opsToken)
	for _, tok := range []string{"", "wrong"} {
		if w := postImport(h.FXRates, "/api/ops/import/fx-rates", "x", tok); w.Code != http.StatusUnauthorized {
			t.Errorf("token %q: %d", tok, w.Code)
		}
	}
	if len(store.fx) != 0 {
		t.Error("stored without authorization")
	}
	for _, e := range logs.All() {
		if strings.Contains(e.Message+toJSON(e.ContextMap()), opsToken) {
			t.Error("token logged")
		}
	}
}

func toJSON(v interface{}) string { b, _ := json.Marshal(v); return string(b) }

func TestOpsImport_FXRates(t *testing.T) {
	store := &fakeOpsImportStore{}
	h := NewOpsImportHandler(store, zap.NewNop(), opsToken)
	w := postImport(h.FXRates, "/api/ops/import/fx-rates?source=/tmp/ecb-2026-10.csv", "date,currency,usd_per_unit\n2026-10-19,EUR,1.07\n2026-10-19,JPY,0.0067\n", opsToken)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var res opsimport.Result
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res.Rows != 2 || res.Kind != "fx-rates" || len(store.fx) != 2 || store.fx[0].Source != "ecb-2026-10.csv" {
		t.Errorf("%+v %+v", res, store.fx)
	}
	// One bad line: 400 with the line, nothing stored.
	store.fx = nil
	w = postImport(h.FXRates, "/api/ops/import/fx-rates", "date,currency,usd_per_unit\n2026-10-19,EUR,1.07\n2026-10-19,EUR,-1\n", opsToken)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "line 3") || len(store.fx) != 0 {
		t.Errorf("%d %s", w.Code, w.Body.String())
	}
	// Dry run: parsed, not stored.
	w = postImport(h.FXRates, "/api/ops/import/fx-rates?dry_run=1", "date,currency,usd_per_unit\n2026-10-19,EUR,1.07\n", opsToken)
	if w.Code != http.StatusOK || len(store.fx) != 0 || !strings.Contains(w.Body.String(), `"dry_run":true`) {
		t.Errorf("dry run %d %s", w.Code, w.Body.String())
	}
	// Storage failure: 503.
	store.err = errors.New("down")
	if w = postImport(h.FXRates, "/api/ops/import/fx-rates", "date,currency,usd_per_unit\n2026-10-19,EUR,1.07\n", opsToken); w.Code != http.StatusServiceUnavailable {
		t.Errorf("store failure %d", w.Code)
	}
}

func TestOpsImport_AdReport(t *testing.T) {
	store := &fakeOpsImportStore{}
	h := NewOpsImportHandler(store, zap.NewNop(), opsToken)
	url := "/api/ops/import/ad-report?network=admob&currency=EUR&map=" + strings.ReplaceAll("date=Date,country=Country,impressions=Impressions,revenue=Estimated earnings (EUR)", " ", "%20")
	body := "Date,Country,Impressions,Estimated earnings (EUR)\n2026-10-19,US,100,1.5\n"
	w := postImport(h.AdReport, url, body, opsToken)
	if w.Code != http.StatusOK || len(store.ads) != 1 || store.ads[0].Network != "admob" || store.ads[0].Currency != "EUR" || store.ads[0].Revenue != 1.5 {
		t.Fatalf("%d %s %+v", w.Code, w.Body.String(), store.ads)
	}
	if w = postImport(h.AdReport, "/api/ops/import/ad-report?map=bogus", body, opsToken); w.Code != http.StatusBadRequest {
		t.Errorf("bad map %d", w.Code)
	}
}
