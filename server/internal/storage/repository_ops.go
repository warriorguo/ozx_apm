package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Operations data (APM-12): inserts into the ops tables (schema.go,
// opsTables). Dates are bound as "YYYY-MM-DD" text: a time.Time would be
// converted to a Date in the server's time zone on the async insert path,
// and the days are UTC days.

const opsDateLayout = "2006-01-02"

func opsDate(t time.Time) string { return t.UTC().Format(opsDateLayout) }

// InsertOpsEvents batch inserts normalized ops events
// (processor.OpsNormalizer).
func (r *Repository) InsertOpsEvents(ctx context.Context, events []models.OpsEvent) error {
	if len(events) == 0 {
		return nil
	}
	batch, err := r.newInsert(ctx, "INSERT INTO ops_events (event_id, name, day, event_time, client_time, server_time, seq, "+
		"player_id, session_id, app_version, platform, build_type, channel, country, props, prop_types, num_props, ctx)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, e := range events {
		err := batch.Append(
			e.EventID,
			e.Name,
			opsDate(e.Day),
			e.EventTime,
			e.ClientTime,
			e.ServerTime,
			e.Seq,
			e.PlayerID,
			e.SessionID,
			e.AppVersion,
			e.Platform,
			e.BuildType,
			e.Channel,
			e.Country,
			contextColumn(e.Props),
			propTypesColumn(e.PropTypes),
			numPropsColumn(e.NumProps),
			contextColumn(e.Context),
		)
		if err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}
	return batch.Send()
}

func numPropsColumn(m map[string]float64) map[string]float64 {
	if m == nil {
		return map[string]float64{}
	}
	return m
}

// InsertOpsPlayerDays inserts (day, player) activity rows; duplicates are
// merged by the ReplacingMergeTree.
func (r *Repository) InsertOpsPlayerDays(ctx context.Context, rows []models.OpsPlayerDay) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := r.newInsert(ctx, "INSERT INTO ops_player_days (day, player_id, app_version, platform, build_type, channel, country)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, d := range rows {
		if err := batch.Append(opsDate(d.Day), d.PlayerID, d.AppVersion, d.Platform, d.BuildType, d.Channel, d.Country); err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}
	return batch.Send()
}

// InsertOpsPlayers inserts player rows; the AggregatingMergeTree merges the
// rows of a player (min first_seen, max last_seen and text fields).
func (r *Repository) InsertOpsPlayers(ctx context.Context, rows []models.OpsPlayer) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := r.newInsert(ctx, "INSERT INTO ops_players (player_id, first_seen, last_seen, install_channel, platform, build_type)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	for _, p := range rows {
		if err := batch.Append(p.PlayerID, p.FirstSeen, p.LastSeen, p.InstallChannel, p.Platform, p.BuildType); err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}
	return batch.Send()
}

// InsertOpsFXRates inserts imported exchange rates (a re-import replaces a
// (currency, day): the latest imported_at wins with FINAL).
func (r *Repository) InsertOpsFXRates(ctx context.Context, rates []models.OpsFXRate, importedAt time.Time) error {
	if len(rates) == 0 {
		return nil
	}
	batch, err := r.newInsert(ctx, "INSERT INTO ops_fx_rates (day, currency, usd_per_unit, source, imported_at)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	at := importedAt.UTC().Truncate(time.Second)
	for _, x := range rates {
		if err := batch.Append(opsDate(x.Day), x.Currency, x.USDPerUnit, x.Source, at); err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}
	return batch.Send()
}

// InsertOpsAdReports inserts imported ad network report rows (a re-import of
// a day, network, country, placement and ad unit replaces it with FINAL).
func (r *Repository) InsertOpsAdReports(ctx context.Context, rows []models.OpsAdReport, importedAt time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := r.newInsert(ctx, "INSERT INTO ops_ad_reports (day, network, country, placement, ad_unit, impressions, revenue, currency, source, imported_at)")
	if err != nil {
		return fmt.Errorf("prepare batch: %w", err)
	}
	at := importedAt.UTC().Truncate(time.Second)
	for _, a := range rows {
		if err := batch.Append(opsDate(a.Day), a.Network, a.Country, a.Placement, a.AdUnit, a.Impressions, a.Revenue, a.Currency, a.Source, at); err != nil {
			return fmt.Errorf("append to batch: %w", err)
		}
	}
	return batch.Send()
}
