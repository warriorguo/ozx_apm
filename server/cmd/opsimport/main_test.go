package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api/handlers/admin"
	"github.com/warriorguo/ozx_apm/server/internal/models"
)

type fakeStore struct {
	fx  []models.OpsFXRate
	ads []models.OpsAdReport
}

func (s *fakeStore) InsertOpsFXRates(_ context.Context, r []models.OpsFXRate, _ time.Time) error {
	s.fx = append(s.fx, r...)
	return nil
}
func (s *fakeStore) InsertOpsAdReports(_ context.Context, r []models.OpsAdReport, _ time.Time) error {
	s.ads = append(s.ads, r...)
	return nil
}

// handlerTransport serves requests with an http.Handler in process.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.h.ServeHTTP(w, r)
	return w.Result(), nil
}

// The CLI against the real handler: the file (gzip), its options and the
// token reach the server; the summary is printed.
func TestRun_AgainstTheImportHandler(t *testing.T) {
	st := &fakeStore{}
	h := admin.NewOpsImportHandler(st, zap.NewNop(), "tok-123")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ops/import/fx-rates", h.FXRates)
	mux.HandleFunc("/api/ops/import/ad-report", h.AdReport)
	// The handler is called in process (no port: the test sandbox may not listen).
	srv := struct {
		URL    string
		Client func() *http.Client
	}{"http://admin.test", func() *http.Client { return &http.Client{Transport: handlerTransport{mux}} }}

	dir := t.TempDir()
	fx := filepath.Join(dir, "ecb.csv")
	if err := os.WriteFile(fx, []byte("date,currency,usd_per_unit\n2026-10-19,EUR,1.07\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := run("fx-rates", options{url: srv.URL + "/", timeout: time.Minute}, fx, "tok-123", srv.Client())
	if err != nil || !strings.Contains(out, `"rows":1`) || len(st.fx) != 1 || st.fx[0].Source != "ecb.csv" {
		t.Fatalf("%v %s %+v", err, out, st.fx)
	}

	ad := filepath.Join(dir, "admob.csv")
	if err := os.WriteFile(ad, []byte("Date,Country,Impressions,Estimated earnings (USD)\n2026-10-19,US,10,0.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := options{url: srv.URL, network: "admob", mapping: "date=Date,country=Country,impressions=Impressions,revenue=Estimated earnings (USD)",
		dryRun: true, timeout: time.Minute}
	out, err = run("ad-report", o, ad, "tok-123", srv.Client())
	if err != nil || !strings.Contains(out, `"dry_run":true`) || len(st.ads) != 0 {
		t.Fatalf("dry run: %v %s", err, out)
	}
	o.dryRun = false
	if _, err = run("ad-report", o, ad, "tok-123", srv.Client()); err != nil || len(st.ads) != 1 || st.ads[0].Network != "admob" {
		t.Fatalf("%v %+v", err, st.ads)
	}

	// Errors: the server's message, never the token.
	_, err = run("fx-rates", options{url: srv.URL, timeout: time.Minute}, fx, "wrong-token-x", srv.Client())
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "wrong-token-x") {
		t.Errorf("wrong token: %v", err)
	}
	if _, err = run("fx-rates", options{url: srv.URL}, fx, "", srv.Client()); err == nil || !strings.Contains(err.Error(), TokenEnv) {
		t.Errorf("no token: %v", err)
	}
	if _, err = run("fx-rates", options{}, fx, "tok-123", srv.Client()); err == nil {
		t.Error("no url accepted")
	}
}
