// Package opsimport reads the files the operations data imports take
// (APM-12): daily exchange rates (ops_fx_rates) and the ad networks' daily
// reports (ops_ad_reports). Both are CSV with a header line. The same code
// serves the CLI (cmd/opsimport) and the admin endpoints
// (POST /api/ops/import/fx-rates, /api/ops/import/ad-report).
//
// Everything is validated before anything is stored: a file with one bad
// line is refused as a whole, with the line number, so an import is never
// half done. Re-importing a file replaces its rows (the tables are
// ReplacingMergeTree on the row's key, the latest import wins).
package opsimport

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// MaxRows bounds one import (a year of daily reports per country and
// placement of a few networks fits easily).
const MaxRows = 200000

// MaxFileBytes bounds one imported file.
const MaxFileBytes = 32 << 20

const dateLayout = "2006-01-02"

// FX rate columns: date, currency and the rate in US dollars per unit
// (header names, case-insensitive; aliases accepted).
var fxColumns = map[string][]string{
	"date":         {"date", "day"},
	"currency":     {"currency", "code"},
	"usd_per_unit": {"usd_per_unit", "rate_to_usd", "usd"},
}

// ParseFXRates reads "date,currency,usd_per_unit" lines, e.g.
//
//	date,currency,usd_per_unit
//	2026-10-19,EUR,1.0712
//	2026-10-19,JPY,0.00667
//
// The rate is how many US dollars one unit of the currency was worth that
// day (e.g. from a central bank's daily reference rates). A currency is 3
// letters (ISO 4217). USD itself needs no rate.
func ParseFXRates(r io.Reader, source string) ([]models.OpsFXRate, error) {
	recs, idx, err := readCSV(r, fxColumns, nil, nil)
	if err != nil {
		return nil, err
	}
	out := make([]models.OpsFXRate, 0, len(recs))
	for n, rec := range recs {
		line := n + 2
		day, err := parseDate(rec[idx["date"]])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		cur, err := parseCurrency(rec[idx["currency"]])
		if err != nil || cur == "" {
			return nil, fmt.Errorf("line %d: currency %q: want 3 letters (ISO 4217)", line, rec[idx["currency"]])
		}
		rate, err := parseNumber(rec[idx["usd_per_unit"]])
		if err != nil || rate <= 0 {
			return nil, fmt.Errorf("line %d: usd_per_unit %q: want a positive number", line, rec[idx["usd_per_unit"]])
		}
		out = append(out, models.OpsFXRate{Day: day, Currency: cur, USDPerUnit: rate, Source: source})
	}
	return out, nil
}

// Ad report columns (canonical names; a file with other header names is
// read through a mapping, see AdReportOptions.Mapping).
var adColumns = map[string][]string{
	"date":        {"date", "day"},
	"network":     {"network", "ad_network"},
	"country":     {"country", "country_code"},
	"placement":   {"placement"},
	"ad_unit":     {"ad_unit", "ad_unit_id"},
	"impressions": {"impressions"},
	"revenue":     {"revenue", "estimated_revenue", "earnings"},
	"currency":    {"currency"},
}

var adOptional = map[string]bool{"network": true, "placement": true, "ad_unit": true, "currency": true}

// AdReportOptions says how to read an ad network's export.
type AdReportOptions struct {
	// Network is the network's name (admob, applovin, unity, ironsource...),
	// used when the file has no network column. Stored lower case: it must
	// match the "network" property of the SDK's ad events.
	Network string
	// Currency of the revenue column when the file has no currency column
	// (default USD).
	Currency string
	// Mapping maps a canonical column (date, network, country, placement,
	// ad_unit, impressions, revenue, currency) to the header name in the
	// file, e.g. {"revenue": "Estimated earnings (USD)", "ad_unit": "Ad unit"}.
	Mapping map[string]string
	// Source is stored with each row (the file name).
	Source string
}

// ParseAdReport reads an ad network's daily report: one line per day,
// country and placement (or ad unit) with impressions and revenue, e.g.
//
//	date,network,country,placement,impressions,revenue,currency
//	2026-10-19,admob,US,revive,1520,12.84,USD
//
// Countries are ISO 3166-1 alpha-2 codes (as the SDK's events carry);
// anything else is stored as "" (unknown). Numbers use "." as decimal
// separator; thousands separators ("1,234.5") are accepted.
func ParseAdReport(r io.Reader, opt AdReportOptions) ([]models.OpsAdReport, error) {
	recs, idx, err := readCSV(r, adColumns, adOptional, opt.Mapping)
	if err != nil {
		return nil, err
	}
	defNetwork := strings.ToLower(strings.TrimSpace(opt.Network))
	defCurrency := "USD"
	if strings.TrimSpace(opt.Currency) != "" {
		c, err := parseCurrency(opt.Currency)
		if err != nil || c == "" {
			return nil, fmt.Errorf("currency %q: want 3 letters (ISO 4217)", opt.Currency)
		}
		defCurrency = c
	}
	if _, ok := idx["network"]; !ok && defNetwork == "" {
		return nil, errors.New("the file has no network column: give the network (--network / ?network=)")
	}
	get := func(rec []string, col string) string {
		if i, ok := idx[col]; ok {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	out := make([]models.OpsAdReport, 0, len(recs))
	for n, rec := range recs {
		line := n + 2
		day, err := parseDate(get(rec, "date"))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		network := strings.ToLower(get(rec, "network"))
		if network == "" {
			network = defNetwork
		}
		if network == "" || len(network) > 64 {
			return nil, fmt.Errorf("line %d: no network", line)
		}
		cur := defCurrency
		if c := get(rec, "currency"); c != "" {
			if cur, err = parseCurrency(c); err != nil || cur == "" {
				return nil, fmt.Errorf("line %d: currency %q: want 3 letters (ISO 4217)", line, c)
			}
		}
		imp, err := parseNumber(get(rec, "impressions"))
		if err != nil || imp < 0 || imp != math.Trunc(imp) || imp > 1e15 {
			return nil, fmt.Errorf("line %d: impressions %q: want a whole number", line, get(rec, "impressions"))
		}
		rev, err := parseNumber(get(rec, "revenue"))
		if err != nil {
			return nil, fmt.Errorf("line %d: revenue: %w", line, err)
		}
		if rev < 0 {
			return nil, fmt.Errorf("line %d: revenue %q: want a number >= 0", line, get(rec, "revenue"))
		}
		out = append(out, models.OpsAdReport{
			Day: day, Network: network, Country: country(get(rec, "country")),
			Placement: limit(get(rec, "placement"), 128), AdUnit: limit(get(rec, "ad_unit"), 128),
			Impressions: uint64(imp), Revenue: rev, Currency: cur, Source: opt.Source,
		})
	}
	return out, nil
}

// ParseMapping reads "canonical=Header Name,canonical2=Other" (the CLI's
// --map and the endpoint's map parameter).
func ParseMapping(s string) (map[string]string, error) {
	m := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return m, nil
	}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("mapping %q: want canonical=Header", part)
		}
		if _, known := adColumns[k]; !known {
			return nil, fmt.Errorf("mapping %q: unknown column %q (date, network, country, placement, ad_unit, impressions, revenue, currency)", part, k)
		}
		m[k] = v
	}
	return m, nil
}

// readCSV reads the header and the records. idx maps each canonical column
// found to its index; a column not in optional must be present.
func readCSV(r io.Reader, columns map[string][]string, optional map[string]bool, mapping map[string]string) ([][]string, map[string]int, error) {
	cr := csv.NewReader(io.LimitReader(r, MaxFileBytes+1))
	cr.FieldsPerRecord = -1
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err == io.EOF {
		return nil, nil, errors.New("empty file: want a header line")
	}
	if err != nil {
		return nil, nil, fmt.Errorf("header: %w", err)
	}
	pos := map[string]int{}
	for i, h := range header {
		h = strings.TrimSpace(strings.TrimPrefix(h, "\ufeff")) // a BOM from spreadsheet exports
		pos[strings.ToLower(h)] = i
	}
	idx := map[string]int{}
	for col, aliases := range columns {
		if name, ok := mapping[col]; ok {
			i, found := pos[strings.ToLower(name)]
			if !found {
				return nil, nil, fmt.Errorf("column %q (mapped to %s) is not in the header", name, col)
			}
			idx[col] = i
			continue
		}
		for _, a := range aliases {
			if i, found := pos[a]; found {
				idx[col] = i
				break
			}
		}
		if _, found := idx[col]; !found && !optional[col] {
			return nil, nil, fmt.Errorf("missing column %s (header: %s)", col, strings.Join(header, ","))
		}
	}
	var recs [][]string
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		for col, i := range idx {
			if i >= len(rec) {
				return nil, nil, fmt.Errorf("line %d: no value for %s", len(recs)+2, col)
			}
		}
		if len(recs) >= MaxRows {
			return nil, nil, fmt.Errorf("more than %d rows: split the file", MaxRows)
		}
		recs = append(recs, rec)
	}
	if len(recs) == 0 {
		return nil, nil, errors.New("no rows")
	}
	return recs, idx, nil
}

func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{dateLayout, "2006/01/02", "20060102"} {
		if t, err := time.Parse(layout, s); err == nil {
			if t.Year() < 2000 || t.Year() > 2199 {
				break
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("date %q: want YYYY-MM-DD", s)
}

func parseCurrency(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 3 {
		return "", fmt.Errorf("currency %q", s)
	}
	for i := 0; i < 3; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return "", fmt.Errorf("currency %q", s)
		}
	}
	return s, nil
}

// parseNumber parses a decimal number; "1,234.5" (thousands separators) is
// accepted, "1,5" (a decimal comma) is not.
func parseNumber(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, ",") {
		if !strings.Contains(s, ".") && strings.Count(s, ",") == 1 && len(s)-strings.Index(s, ",") != 4 {
			return 0, fmt.Errorf("number %q: use '.' as the decimal separator", s)
		}
		s = strings.ReplaceAll(s, ",", "")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("number %q", s)
	}
	return f, nil
}

func country(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 2 || s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z' || s == "XX" || s == "ZZ" {
		return ""
	}
	return s
}

func limit(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
