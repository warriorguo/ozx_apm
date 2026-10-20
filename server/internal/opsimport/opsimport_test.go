package opsimport

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

func TestParseFXRates(t *testing.T) {
	rates, err := ParseFXRates(strings.NewReader("\ufeffDate,Currency,USD_per_unit\n2026-10-19,eur,1.0712\n2026-10-19, JPY ,0.00667\n\n2026/10/20,GBP,1.31\n"), "ecb.csv")
	if err != nil {
		t.Fatal(err)
	}
	if len(rates) != 3 || rates[0].Currency != "EUR" || rates[0].USDPerUnit != 1.0712 || rates[1].Currency != "JPY" ||
		rates[2].Day != time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC) || rates[0].Source != "ecb.csv" {
		t.Errorf("%+v", rates)
	}
	// Aliases.
	if r, err := ParseFXRates(strings.NewReader("day,code,rate_to_usd\n20261019,EUR,1.07\n"), ""); err != nil || len(r) != 1 {
		t.Errorf("aliases: %v %v", r, err)
	}
}

func TestParseFXRates_RefusesTheWholeFileOnOneBadLine(t *testing.T) {
	cases := map[string]string{
		"":                                      "empty",
		"date,currency\n2026-10-19,EUR\n":       "missing column usd_per_unit",
		"date,currency,usd_per_unit\n":          "no rows",
		"date,currency,usd_per_unit\nx,EUR,1\n": "line 2: date",
		"date,currency,usd_per_unit\n2026-10-19,EURO,1\n":                  "line 2: currency",
		"date,currency,usd_per_unit\n2026-10-19,EUR,1\n2026-10-19,EUR,0\n": "line 3: usd_per_unit",
		"date,currency,usd_per_unit\n2026-10-19,EUR,1,5\n":                 "", // extra field ignored
		"date,currency,usd_per_unit\n2026-10-19,EUR,1;5\n":                 "line 2",
		"date,currency,usd_per_unit\n1999-01-01,EUR,1\n":                   "date",
	}
	for in, want := range cases {
		_, err := ParseFXRates(strings.NewReader(in), "")
		if want == "" {
			if err != nil {
				t.Errorf("%q: %v", in, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", in, err, want)
		}
	}
}

func TestParseAdReport(t *testing.T) {
	in := "date,network,country,placement,impressions,revenue,currency\n" +
		"2026-10-19,AdMob,us,revive,1520,12.84,USD\n" +
		"2026-10-19,admob,XX,revive,3,0.01,usd\n" +
		"2026-10-19,applovin,DE,,\"1,234\",\"1,020.5\",EUR\n"
	rows, err := ParseAdReport(strings.NewReader(in), AdReportOptions{Source: "r.csv"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatal(len(rows))
	}
	a := rows[0]
	if a.Network != "admob" || a.Country != "US" || a.Placement != "revive" || a.Impressions != 1520 || a.Revenue != 12.84 || a.Currency != "USD" || a.Source != "r.csv" {
		t.Errorf("%+v", a)
	}
	if rows[1].Country != "" || rows[1].Currency != "USD" {
		t.Errorf("unknown country / lower-case currency: %+v", rows[1])
	}
	if rows[2].Impressions != 1234 || rows[2].Revenue != 1020.5 || rows[2].Currency != "EUR" || rows[2].Placement != "" {
		t.Errorf("thousands separators: %+v", rows[2])
	}
}

// A console export with its own header names: mapped, the network and the
// currency given for the whole file.
func TestParseAdReport_Mapping(t *testing.T) {
	m, err := ParseMapping("date=Date, country=Country, ad_unit=Ad unit, impressions=Impressions, revenue=Estimated earnings (EUR)")
	if err != nil {
		t.Fatal(err)
	}
	in := "Date,Country,Ad unit,Impressions,Estimated earnings (EUR)\n2026-10-19,BR,Rewarded revive,100,0.80\n"
	rows, err := ParseAdReport(strings.NewReader(in), AdReportOptions{Network: "AdMob", Currency: "eur", Mapping: m})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Network != "admob" || rows[0].AdUnit != "Rewarded revive" || rows[0].Currency != "EUR" || rows[0].Revenue != 0.8 || rows[0].Country != "BR" {
		t.Errorf("%+v", rows)
	}
	for _, bad := range []string{"revenue", "x=y", "date="} {
		if _, err := ParseMapping(bad); err == nil {
			t.Errorf("mapping %q accepted", bad)
		}
	}
}

func TestParseAdReport_Errors(t *testing.T) {
	header := "date,country,impressions,revenue\n"
	cases := []struct {
		in   string
		opt  AdReportOptions
		want string
	}{
		{header + "2026-10-19,US,1,1\n", AdReportOptions{}, "no network column"},
		{header + "2026-10-19,US,1.5,1\n", AdReportOptions{Network: "admob"}, "impressions"},
		{header + "2026-10-19,US,1,-1\n", AdReportOptions{Network: "admob"}, "revenue"},
		{header + "2026-10-19,US,1,1,5\n", AdReportOptions{Network: "admob"}, ""},
		{header + "2026-10-19,US,1,\"1,5\"\n", AdReportOptions{Network: "admob"}, "decimal separator"},
		{header + "2026-10-19,US,1,1\n", AdReportOptions{Network: "admob", Currency: "dollars"}, "currency"},
		{"date,impressions,revenue\n2026-10-19,1,1\n", AdReportOptions{Network: "admob"}, "missing column country"},
		{header + "2026-10-19,US,1,1\n", AdReportOptions{Network: "admob", Mapping: map[string]string{"revenue": "Earnings"}}, "not in the header"},
	}
	for _, c := range cases {
		_, err := ParseAdReport(strings.NewReader(c.in), c.opt)
		if c.want == "" {
			if err != nil {
				t.Errorf("%q: %v", c.in, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.in, err, c.want)
		}
	}
}

type fakeStore struct {
	fx  []models.OpsFXRate
	ads []models.OpsAdReport
	err error
	at  time.Time
}

func (f *fakeStore) InsertOpsFXRates(_ context.Context, r []models.OpsFXRate, at time.Time) error {
	f.fx = append(f.fx, r...)
	f.at = at
	return f.err
}
func (f *fakeStore) InsertOpsAdReports(_ context.Context, r []models.OpsAdReport, at time.Time) error {
	f.ads = append(f.ads, r...)
	f.at = at
	return f.err
}

func TestImport(t *testing.T) {
	now := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	s := &fakeStore{}
	res, err := ImportFXRates(context.Background(), s, strings.NewReader("date,currency,usd_per_unit\n2026-10-19,EUR,1.07\n2026-10-17,JPY,0.0067\n"), "f", now, false)
	if err != nil || res.Rows != 2 || res.From != "2026-10-17" || res.To != "2026-10-19" || strings.Join(res.Keys, ",") != "EUR,JPY" || len(s.fx) != 2 || !s.at.Equal(now) {
		t.Errorf("%+v %v", res, err)
	}
	s = &fakeStore{}
	res, err = ImportAdReport(context.Background(), s, strings.NewReader("date,country,impressions,revenue\n2026-10-19,US,10,0.1\n"), AdReportOptions{Network: "unity"}, now, true)
	if err != nil || !res.DryRun || res.Rows != 1 || len(s.ads) != 0 || res.Keys[0] != "unity" {
		t.Errorf("dry run: %+v %v %d", res, err, len(s.ads))
	}
	s = &fakeStore{err: errors.New("down")}
	if _, err := ImportAdReport(context.Background(), s, strings.NewReader("date,country,impressions,revenue\n2026-10-19,US,10,0.1\n"), AdReportOptions{Network: "unity"}, now, false); err == nil || !strings.Contains(err.Error(), "re-run") {
		t.Errorf("store error: %v", err)
	}
}
