package opsimport

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Store is where imports are written (storage.Repository).
type Store interface {
	InsertOpsFXRates(ctx context.Context, rates []models.OpsFXRate, importedAt time.Time) error
	InsertOpsAdReports(ctx context.Context, rows []models.OpsAdReport, importedAt time.Time) error
}

// Result summarizes an import (no row data).
type Result struct {
	Kind   string   `json:"kind"`
	Rows   int      `json:"rows"`
	From   string   `json:"from"`
	To     string   `json:"to"`
	Keys   []string `json:"keys"` // currencies (fx rates) or networks (ad reports)
	DryRun bool     `json:"dry_run,omitempty"`
}

// insertChunk bounds one INSERT.
const insertChunk = 10000

// ImportFXRates parses and stores an exchange rate file. dryRun only parses.
func ImportFXRates(ctx context.Context, store Store, r io.Reader, source string, now time.Time, dryRun bool) (Result, error) {
	rates, err := ParseFXRates(r, source)
	if err != nil {
		return Result{}, err
	}
	res := Result{Kind: "fx-rates", Rows: len(rates), DryRun: dryRun}
	keys := map[string]bool{}
	for i, x := range rates {
		res.span(i, x.Day)
		keys[x.Currency] = true
	}
	res.Keys = sorted(keys)
	if dryRun {
		return res, nil
	}
	for start := 0; start < len(rates); start += insertChunk {
		end := min(start+insertChunk, len(rates))
		if err := store.InsertOpsFXRates(ctx, rates[start:end], now); err != nil {
			return res, fmt.Errorf("store rows %d-%d: %w (re-run the import: it replaces what was stored)", start+1, end, err)
		}
	}
	return res, nil
}

// ImportAdReport parses and stores an ad network report. dryRun only parses.
func ImportAdReport(ctx context.Context, store Store, r io.Reader, opt AdReportOptions, now time.Time, dryRun bool) (Result, error) {
	rows, err := ParseAdReport(r, opt)
	if err != nil {
		return Result{}, err
	}
	res := Result{Kind: "ad-report", Rows: len(rows), DryRun: dryRun}
	keys := map[string]bool{}
	for i, x := range rows {
		res.span(i, x.Day)
		keys[x.Network] = true
	}
	res.Keys = sorted(keys)
	if dryRun {
		return res, nil
	}
	for start := 0; start < len(rows); start += insertChunk {
		end := min(start+insertChunk, len(rows))
		if err := store.InsertOpsAdReports(ctx, rows[start:end], now); err != nil {
			return res, fmt.Errorf("store rows %d-%d: %w (re-run the import: it replaces what was stored)", start+1, end, err)
		}
	}
	return res, nil
}

func (r *Result) span(i int, day time.Time) {
	d := day.Format(dateLayout)
	if i == 0 || d < r.From {
		r.From = d
	}
	if i == 0 || d > r.To {
		r.To = d
	}
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
