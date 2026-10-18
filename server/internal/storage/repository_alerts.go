package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Alerting and issue state (APM-8). Two small tables, created by Migrate
// with the CREATE TABLE grant the service account has; nothing here needs
// ALTER UPDATE, MODIFY or ADD INDEX.
//
// apm_issue_states: the state of an issue (kind + fingerprint) as set by an
// admin (resolved / unresolved) or by the alert evaluator (reopened on a
// regression). Every change is a new row; ReplacingMergeTree(version) keeps
// the latest per issue, and reads use FINAL so that unmerged older rows are
// never seen. An issue without a row is unresolved.
//
// apm_alerts: the alert outbox, append-only. Each alert has a deterministic
// key (alert package); its rows say what happened to it: pending (detected,
// with the payload), claimed / sent / failed per attempt, abandoned, or
// known (a fingerprint recorded without an alert). The key's rows are what
// makes alerting idempotent across restarts and replicas: a key with any row
// is never detected again, and an attempt is sent only by the instance whose
// claim is first. Rows of type new_issue are also the record of every known
// fingerprint, so the table has no TTL (a few rows per alert, one per
// fingerprint).

const IssueStatesTable = "apm_issue_states"
const AlertsTable = "apm_alerts"

const issueStatesTableDDL = `CREATE TABLE IF NOT EXISTS ` + IssueStatesTable + ` (
    kind LowCardinality(String),
    fingerprint String,
    status LowCardinality(String),
    resolved_in_version String,
    last_seen_version String,
    resolved_at DateTime64(3),
    regressed_in_version String,
    regressed_at DateTime64(3),
    updated_at DateTime64(3),
    updated_by LowCardinality(String),
    comment String,
    version UInt64
) ENGINE = ReplacingMergeTree(version)
ORDER BY (kind, fingerprint)`

const alertsTableDDL = `CREATE TABLE IF NOT EXISTS ` + AlertsTable + ` (
    alert_key String,
    at DateTime64(3),
    status LowCardinality(String),
    attempt UInt16,
    instance String,
    type LowCardinality(String),
    kind LowCardinality(String),
    fingerprint String,
    app_version String,
    payload String,
    error String,
    inserted_at DateTime64(3) DEFAULT now64(3)
) ENGINE = MergeTree()
ORDER BY (alert_key, at)`

// migrateAlertTables creates the two tables. Like the privacy audit table,
// a failure is logged and startup continues: ingest and the admin pages do
// not need them (the state endpoints and the evaluator then fail).
func (c *ClickHouseClient) migrateAlertTables(ctx context.Context) {
	for _, ddl := range []struct{ table, ddl string }{
		{IssueStatesTable, issueStatesTableDDL},
		{AlertsTable, alertsTableDDL},
	} {
		if err := c.conn.Exec(ctx, ddl.ddl); err != nil {
			c.logger.Warn("could not create an alerting table; issue states and alerts will fail until it exists (RELEASE.md, APM-8)",
				zap.String("table", ddl.table), zap.Error(err))
		}
	}
}

// issueTable returns the event table of an issue kind.
func issueTable(kind string) (string, error) {
	switch kind {
	case models.IssueKindException:
		return TableExceptions.Name, nil
	case models.IssueKindCrash:
		return TableCrashes.Name, nil
	}
	return "", fmt.Errorf("unknown issue kind %q", kind)
}

// BuildClause is the build type condition of the alert queries: release
// builds (and rows of SDKs before APM-5 without a build type) unless dev and
// editor builds are included.
func BuildClause(includeDev bool) string {
	if includeDev {
		return "1"
	}
	return "build_type IN ('release', '')"
}

func msArg(t time.Time) int64 { return t.UnixMilli() }

func optTime(t time.Time) *time.Time {
	if t.IsZero() || t.UnixMilli() <= 0 {
		return nil
	}
	u := t.UTC()
	return &u
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.UnixMilli(0)
	}
	return *t
}

const issueStateColumns = `kind, fingerprint, status, resolved_in_version, last_seen_version, resolved_at,
	regressed_in_version, regressed_at, updated_at, updated_by, comment, version`

func scanIssueState(rows interface{ Scan(dest ...any) error }) (models.IssueState, error) {
	var s models.IssueState
	var resolvedAt, regressedAt time.Time
	if err := rows.Scan(&s.Kind, &s.Fingerprint, &s.Status, &s.ResolvedInVersion, &s.LastSeenVersion, &resolvedAt,
		&s.RegressedInVersion, &regressedAt, &s.UpdatedAt, &s.UpdatedBy, &s.Comment, &s.Version); err != nil {
		return s, err
	}
	s.ResolvedAt = optTime(resolvedAt)
	s.RegressedAt = optTime(regressedAt)
	s.UpdatedAt = s.UpdatedAt.UTC()
	return s, nil
}

// InsertIssueState appends a state row. Version must increase per issue
// (the callers use UpdatedAt in milliseconds).
func (r *Repository) InsertIssueState(ctx context.Context, s models.IssueState) error {
	batch, err := r.client.conn.PrepareBatch(ctx, "INSERT INTO "+IssueStatesTable+" ("+issueStateColumns+")")
	if err != nil {
		return fmt.Errorf("prepare issue state insert: %w", err)
	}
	if err := batch.Append(s.Kind, s.Fingerprint, s.Status, s.ResolvedInVersion, s.LastSeenVersion, timeOrZero(s.ResolvedAt),
		s.RegressedInVersion, timeOrZero(s.RegressedAt), s.UpdatedAt, s.UpdatedBy, s.Comment, s.Version); err != nil {
		return fmt.Errorf("append issue state: %w", err)
	}
	return batch.Send()
}

// IssueStates returns the current state of the given issues of one kind
// (issues without a row are absent from the map).
func (r *Repository) IssueStates(ctx context.Context, kind string, fingerprints []string) (map[string]models.IssueState, error) {
	out := map[string]models.IssueState{}
	if len(fingerprints) == 0 {
		return out, nil
	}
	query := "SELECT " + issueStateColumns + " FROM " + IssueStatesTable + " FINAL WHERE kind = ? AND fingerprint IN ?"
	err := r.queryRows(ctx, query, []interface{}{kind, fingerprints}, func(rows driver.Rows) error {
		s, err := scanIssueState(rows)
		if err != nil {
			return err
		}
		out[s.Fingerprint] = s
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("issue states: %w", err)
	}
	return out, nil
}

// ResolvedIssues returns resolved issues of one kind, most recently resolved
// first, at most limit.
func (r *Repository) ResolvedIssues(ctx context.Context, kind string, limit int) ([]models.IssueState, error) {
	query := "SELECT " + issueStateColumns + " FROM " + IssueStatesTable +
		" FINAL WHERE kind = ? AND status = 'resolved' ORDER BY resolved_at DESC, fingerprint LIMIT ?"
	var out []models.IssueState
	err := r.queryRows(ctx, query, []interface{}{kind, limit}, func(rows driver.Rows) error {
		s, err := scanIssueState(rows)
		if err != nil {
			return err
		}
		out = append(out, s)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("resolved issues: %w", err)
	}
	return out, nil
}

// ResolvedFingerprintsClause is a condition on fingerprint for the exception
// list's status filter: resolved issues only (in) or all but them.
func ResolvedFingerprintsClause(kind string, in bool) (string, []interface{}) {
	op := "NOT IN"
	if in {
		op = "IN"
	}
	return "fingerprint " + op + " (SELECT fingerprint FROM " + IssueStatesTable +
		" FINAL WHERE kind = ? AND status = 'resolved')", []interface{}{kind}
}

// IssueVersions returns the versions an issue occurred in over all retained
// rows: in rows of the builds the alerting considers (release), and whether
// it has any row at all.
func (r *Repository) IssueVersions(ctx context.Context, kind, fingerprint string, includeDev bool) ([]string, bool, error) {
	table, err := issueTable(kind)
	if err != nil {
		return nil, false, err
	}
	query := fmt.Sprintf(`SELECT app_version, toUInt8(max(%s)) FROM %s WHERE fingerprint = ? GROUP BY app_version ORDER BY app_version LIMIT 1000`,
		BuildClause(includeDev), table)
	var versions []string
	found := false
	err = r.queryRows(ctx, query, []interface{}{fingerprint}, func(rows driver.Rows) error {
		var v string
		var counted uint8
		if err := rows.Scan(&v, &counted); err != nil {
			return err
		}
		found = true
		if counted == 1 {
			versions = append(versions, v)
		}
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("issue versions: %w", err)
	}
	return versions, found, nil
}

// newIssueColumns are the per-fingerprint aggregates of a new issue.
var newIssueColumns = map[string]string{
	models.IssueKindException: `fingerprint, min(timestamp) AS first_seen, argMin(app_version, timestamp),
		groupUniqArray(10)(app_version), toInt64(sum(count)), toInt64(uniqExact(session_id)),
		substringUTF8(argMax(exception_type, timestamp), 1, 300),
		substringUTF8(argMax(message, timestamp), 1, 1000),
		substringUTF8(argMax(throw_site, timestamp), 1, 300)`,
	models.IssueKindCrash: `fingerprint, min(timestamp) AS first_seen, argMin(app_version, timestamp),
		groupUniqArray(10)(app_version), toInt64(count()), toInt64(uniqExact(session_id)),
		substringUTF8(argMax(crash_type, timestamp), 1, 300),
		substringUTF8(argMax(stack, timestamp), 1, 1000),
		''`,
}

// NewIssueCandidates returns fingerprints with rows since from that the
// alerting does not know yet (no apm_alerts row of type new_issue), oldest
// first, at most limit.
func (r *Repository) NewIssueCandidates(ctx context.Context, kind string, from time.Time, includeDev bool, limit int) ([]models.IssueCandidate, error) {
	table, err := issueTable(kind)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`
		SELECT %s
		FROM %s
		WHERE timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND fingerprint != '' AND %s
		  AND fingerprint NOT IN (SELECT fingerprint FROM %s WHERE type = 'new_issue' AND kind = ?)
		GROUP BY fingerprint
		ORDER BY first_seen, fingerprint
		LIMIT ?`, newIssueColumns[kind], table, BuildClause(includeDev), AlertsTable)
	var out []models.IssueCandidate
	err = r.queryRows(ctx, query, []interface{}{msArg(from), kind, limit}, func(rows driver.Rows) error {
		c := models.IssueCandidate{Kind: kind}
		if err := rows.Scan(&c.Fingerprint, &c.FirstSeen, &c.FirstVersion, &c.Versions, &c.Count, &c.Sessions,
			&c.Title, &c.Message, &c.Site); err != nil {
			return err
		}
		c.FirstSeen = c.FirstSeen.UTC()
		if c.Versions == nil {
			c.Versions = []string{}
		}
		out = append(out, c)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("new %s candidates: %w", kind, err)
	}
	return out, nil
}

// IssueHits returns, per fingerprint and version, the occurrences since
// from of the given issues: first and last time, count, sessions.
func (r *Repository) IssueHits(ctx context.Context, kind string, fingerprints []string, from time.Time, includeDev bool) ([]models.IssueVersionHit, error) {
	if len(fingerprints) == 0 {
		return nil, nil
	}
	table, err := issueTable(kind)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf(`
		SELECT fingerprint, app_version, min(timestamp), max(timestamp), toInt64(%s), toInt64(uniqExact(session_id))
		FROM %s
		WHERE timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND fingerprint IN ? AND %s
		GROUP BY fingerprint, app_version
		ORDER BY fingerprint, app_version
		LIMIT 100000`, issueCountExpr(kind), table, BuildClause(includeDev))
	var hits []models.IssueVersionHit
	err = r.queryRows(ctx, query, []interface{}{msArg(from), fingerprints}, func(rows driver.Rows) error {
		var h models.IssueVersionHit
		if err := rows.Scan(&h.Fingerprint, &h.AppVersion, &h.FirstSeen, &h.LastSeen, &h.Count, &h.Sessions); err != nil {
			return err
		}
		h.FirstSeen, h.LastSeen = h.FirstSeen.UTC(), h.LastSeen.UTC()
		hits = append(hits, h)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s hits: %w", kind, err)
	}
	return hits, nil
}

// issueCountExpr counts occurrences: an exception row carries a count (the
// SDK merges repeats), a crash row is one crash.
func issueCountExpr(kind string) string {
	if kind == models.IssueKindException {
		return "sum(count)"
	}
	return "count()"
}

// IssueHitSince counts one issue's occurrences in one version since a time.
func (r *Repository) IssueHitSince(ctx context.Context, kind, fingerprint, version string, since time.Time, includeDev bool) (models.IssueVersionHit, error) {
	h := models.IssueVersionHit{Fingerprint: fingerprint, AppVersion: version}
	table, err := issueTable(kind)
	if err != nil {
		return h, err
	}
	query := fmt.Sprintf(`
		SELECT min(timestamp), max(timestamp), toInt64(%s), toInt64(uniqExact(session_id))
		FROM %s
		WHERE timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND fingerprint = ? AND app_version = ? AND %s`,
		issueCountExpr(kind), table, BuildClause(includeDev))
	if err := r.client.conn.QueryRow(ctx, query, msArg(since), fingerprint, version).Scan(&h.FirstSeen, &h.LastSeen, &h.Count, &h.Sessions); err != nil {
		return h, fmt.Errorf("%s hit: %w", kind, err)
	}
	h.FirstSeen, h.LastSeen = h.FirstSeen.UTC(), h.LastSeen.UTC()
	return h, nil
}

// RecordBaseline records every fingerprint of the kind in the retained rows
// as known to the alerting (status known, error "baseline"), in one
// INSERT ... SELECT run by ClickHouse (no row passes through the server, no
// limit), and returns how many baseline rows of the kind exist.
func (r *Repository) RecordBaseline(ctx context.Context, kind string, includeDev bool, instance string) (uint64, error) {
	table, err := issueTable(kind)
	if err != nil {
		return 0, err
	}
	query := fmt.Sprintf(`
		INSERT INTO %s (alert_key, at, status, attempt, instance, type, kind, fingerprint, app_version, payload, error)
		SELECT concat('new_issue:', ?, ':', fingerprint), now64(3), 'known', 0, ?, 'new_issue', ?, fingerprint,
			argMin(app_version, timestamp), '', 'baseline'
		FROM %s
		WHERE fingerprint != '' AND %s
		GROUP BY fingerprint`, AlertsTable, table, BuildClause(includeDev))
	if err := r.client.conn.Exec(ctx, query, kind, instance, kind); err != nil {
		return 0, fmt.Errorf("%s baseline: %w", kind, err)
	}
	var n uint64
	if err := r.client.conn.QueryRow(ctx, "SELECT count() FROM "+AlertsTable+
		" WHERE type = 'new_issue' AND kind = ? AND status = 'known' AND error = 'baseline'", kind).Scan(&n); err != nil {
		return 0, fmt.Errorf("%s baseline count: %w", kind, err)
	}
	return n, nil
}

// CountRecentAlerts counts the alerts of a type and kind detected (a pending
// row) since the given time: the hourly cap of new-issue alerts and the
// digest interval.
func (r *Repository) CountRecentAlerts(ctx context.Context, alertType, kind string, since time.Time) (int, error) {
	var n uint64
	err := r.client.conn.QueryRow(ctx, "SELECT uniqExact(alert_key) FROM "+AlertsTable+
		" WHERE type = ? AND kind = ? AND status = 'pending' AND at >= fromUnixTimestamp64Milli(toInt64(?))",
		alertType, kind, msArg(since)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("recent alerts: %w", err)
	}
	return int(n), nil
}

// VersionFirstSeen returns the earliest startup per version in the retained
// rows: the fallback order of versions whose names cannot be compared.
func (r *Repository) VersionFirstSeen(ctx context.Context, includeDev bool) (map[string]time.Time, error) {
	query := fmt.Sprintf(`SELECT app_version, min(timestamp) FROM %s WHERE %s GROUP BY app_version LIMIT 10000`,
		TableStartups.Name, BuildClause(includeDev))
	out := map[string]time.Time{}
	err := r.queryRows(ctx, query, nil, func(rows driver.Rows) error {
		var v string
		var t time.Time
		if err := rows.Scan(&v, &t); err != nil {
			return err
		}
		out[v] = t.UTC()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("version first seen: %w", err)
	}
	return out, nil
}

// CrashRates returns per version the sessions with any row (perf samples,
// startups, crashes) and the sessions with a crash in [from, to).
func (r *Repository) CrashRates(ctx context.Context, from, to time.Time, includeDev bool) ([]models.VersionRate, error) {
	cond := fmt.Sprintf("timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND timestamp < fromUnixTimestamp64Milli(toInt64(?)) AND %s",
		BuildClause(includeDev))
	sessionsQuery := fmt.Sprintf(`
		SELECT app_version, toInt64(uniq(session_id)) FROM (
			SELECT app_version, session_id FROM %s WHERE %s
			UNION ALL SELECT app_version, session_id FROM %s WHERE %s
			UNION ALL SELECT app_version, session_id FROM %s WHERE %s
		) GROUP BY app_version ORDER BY app_version LIMIT 10000`,
		TablePerfSamples.Name, cond, TableStartups.Name, cond, TableCrashes.Name, cond)
	a, b := msArg(from), msArg(to)
	rates := map[string]*models.VersionRate{}
	var order []string
	err := r.queryRows(ctx, sessionsQuery, []interface{}{a, b, a, b, a, b}, func(rows driver.Rows) error {
		var v models.VersionRate
		if err := rows.Scan(&v.AppVersion, &v.Sessions); err != nil {
			return err
		}
		rates[v.AppVersion] = &v
		order = append(order, v.AppVersion)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("sessions per version: %w", err)
	}
	crashQuery := fmt.Sprintf(`
		SELECT app_version, toInt64(uniqExact(session_id)), toInt64(uniqExact(event_id))
		FROM %s WHERE %s GROUP BY app_version ORDER BY app_version LIMIT 10000`, TableCrashes.Name, cond)
	err = r.queryRows(ctx, crashQuery, []interface{}{a, b}, func(rows driver.Rows) error {
		var v string
		var crashed, crashes int64
		if err := rows.Scan(&v, &crashed, &crashes); err != nil {
			return err
		}
		rate, ok := rates[v]
		if !ok {
			rate = &models.VersionRate{AppVersion: v}
			rates[v] = rate
			order = append(order, v)
		}
		rate.CrashedSessions = crashed
		rate.Crashes = crashes
		if rate.Sessions < crashed {
			rate.Sessions = crashed // uniq() is approximate
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("crashes per version: %w", err)
	}
	out := make([]models.VersionRate, 0, len(order))
	for _, v := range order {
		out = append(out, *rates[v])
	}
	return out, nil
}

// StartupStats returns per version the number of startups since from, the
// 95th percentile of the total startup time (phase1 + phase2 + tti, as the
// dashboard's startup metric) and the version's first startup in the range.
func (r *Repository) StartupStats(ctx context.Context, from time.Time, includeDev bool) ([]models.VersionStartup, error) {
	query := fmt.Sprintf(`
		SELECT app_version, toInt64(count()), %s, min(timestamp)
		FROM %s
		WHERE timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND %s
		GROUP BY app_version ORDER BY app_version LIMIT 10000`,
		finiteFloat("quantile(0.95)(phase1_ms + phase2_ms + tti_ms)"), TableStartups.Name, BuildClause(includeDev))
	var out []models.VersionStartup
	err := r.queryRows(ctx, query, []interface{}{msArg(from)}, func(rows driver.Rows) error {
		var v models.VersionStartup
		if err := rows.Scan(&v.AppVersion, &v.Samples, &v.P95Ms, &v.FirstSeen); err != nil {
			return err
		}
		v.FirstSeen = v.FirstSeen.UTC()
		out = append(out, v)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("startup stats: %w", err)
	}
	return out, nil
}

// InsertAlertRecords appends rows to the alert outbox (one synchronous
// insert: the rows are visible to every reader when it returns).
func (r *Repository) InsertAlertRecords(ctx context.Context, recs []models.AlertRecord) error {
	if len(recs) == 0 {
		return nil
	}
	batch, err := r.client.conn.PrepareBatch(ctx, "INSERT INTO "+AlertsTable+
		" (alert_key, at, status, attempt, instance, type, kind, fingerprint, app_version, payload, error)")
	if err != nil {
		return fmt.Errorf("prepare alert insert: %w", err)
	}
	for _, a := range recs {
		if err := batch.Append(a.AlertKey, a.At, a.Status, a.Attempt, a.Instance, a.Type, a.Kind, a.Fingerprint,
			a.AppVersion, a.Payload, a.Error); err != nil {
			return fmt.Errorf("append alert: %w", err)
		}
	}
	return batch.Send()
}

// ExistingAlertKeys returns which of the keys have any row.
func (r *Repository) ExistingAlertKeys(ctx context.Context, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(keys) == 0 {
		return out, nil
	}
	err := r.queryRows(ctx, "SELECT DISTINCT alert_key FROM "+AlertsTable+" WHERE alert_key IN ?", []interface{}{keys},
		func(rows driver.Rows) error {
			var k string
			if err := rows.Scan(&k); err != nil {
				return err
			}
			out[k] = true
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("existing alert keys: %w", err)
	}
	return out, nil
}

// RecentAlertExists reports whether an alert of the type for the version was
// detected (has a pending row) since the given time: the cooldown of rate
// alerts.
func (r *Repository) RecentAlertExists(ctx context.Context, alertType, appVersion string, since time.Time) (bool, error) {
	var n uint64
	err := r.client.conn.QueryRow(ctx, "SELECT count() FROM "+AlertsTable+
		" WHERE type = ? AND app_version = ? AND status = 'pending' AND at >= fromUnixTimestamp64Milli(toInt64(?))",
		alertType, appVersion, msArg(since)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("recent alert: %w", err)
	}
	return n > 0, nil
}

// AlertsToSend returns alerts detected since the given time that were
// neither sent nor abandoned, oldest first, at most limit.
func (r *Repository) AlertsToSend(ctx context.Context, since time.Time, limit int) ([]models.AlertOutboxEntry, error) {
	query := `
		SELECT alert_key,
			any(type),
			anyIf(payload, status = 'pending'),
			minIf(at, status = 'pending') AS created,
			toInt64(countIf(status = 'failed')),
			maxIf(at, status = 'failed'),
			toInt64(maxIf(attempt, status = 'claimed')),
			maxIf(at, status = 'claimed'),
			toInt64(maxIf(attempt, status IN ('sent', 'failed'))),
			argMaxIf(status, attempt, status IN ('sent', 'failed'))
		FROM ` + AlertsTable + `
		WHERE alert_key IN (SELECT alert_key FROM ` + AlertsTable + ` WHERE status = 'pending' AND at >= fromUnixTimestamp64Milli(toInt64(?)))
		GROUP BY alert_key
		HAVING countIf(status IN ('sent', 'abandoned')) = 0
		ORDER BY created, alert_key
		LIMIT ?`
	var out []models.AlertOutboxEntry
	err := r.queryRows(ctx, query, []interface{}{msArg(since), limit}, func(rows driver.Rows) error {
		var e models.AlertOutboxEntry
		var failed, lastClaim, lastDone int64
		if err := rows.Scan(&e.AlertKey, &e.Type, &e.Payload, &e.CreatedAt, &failed, &e.LastFailedAt,
			&lastClaim, &e.LastClaimAt, &lastDone, &e.LastDoneStatus); err != nil {
			return err
		}
		e.Failed, e.LastClaim, e.LastDone = int(failed), int(lastClaim), int(lastDone)
		out = append(out, e)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("alerts to send: %w", err)
	}
	return out, nil
}

// AlertClaims returns the claimed attempts of the keys.
func (r *Repository) AlertClaims(ctx context.Context, keys []string) ([]models.AlertClaim, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	var out []models.AlertClaim
	// At is when ClickHouse stored the claim (one clock for every instance).
	err := r.queryRows(ctx, "SELECT alert_key, toInt64(attempt), inserted_at, instance FROM "+AlertsTable+
		" WHERE alert_key IN ? AND status = 'claimed' ORDER BY alert_key, inserted_at, instance", []interface{}{keys},
		func(rows driver.Rows) error {
			var c models.AlertClaim
			var attempt int64
			if err := rows.Scan(&c.AlertKey, &attempt, &c.At, &c.Instance); err != nil {
				return err
			}
			c.Attempt = int(attempt)
			out = append(out, c)
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("alert claims: %w", err)
	}
	return out, nil
}

// AlertBaselineDone reports whether the baseline marker key exists.
func (r *Repository) AlertBaselineDone(ctx context.Context, key string) (bool, error) {
	var n uint64
	if err := r.client.conn.QueryRow(ctx, "SELECT count() FROM "+AlertsTable+" WHERE alert_key = ?", key).Scan(&n); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("alert baseline: %w", err)
	}
	return n > 0, nil
}

// AlertSummaryRow is one alert of ListAlerts with its pending payload.
type AlertSummaryRow struct {
	models.AlertSummary
	Payload string
}

// ListAlerts returns the most recently detected alerts (keys with a pending
// row), newest first, with their latest status.
func (r *Repository) ListAlerts(ctx context.Context, limit int) ([]AlertSummaryRow, error) {
	query := `
		SELECT alert_key, any(type), anyIf(kind, status = 'pending'), anyIf(fingerprint, status = 'pending'),
			anyIf(app_version, status = 'pending'),
			minIf(at, status = 'pending') AS created,
			argMax(status, (at, attempt)), max(at),
			toInt64(countIf(status IN ('sent', 'failed'))),
			argMaxIf(error, at, status = 'failed'),
			anyIf(payload, status = 'pending')
		FROM ` + AlertsTable + `
		WHERE alert_key IN (SELECT alert_key FROM ` + AlertsTable + ` WHERE status = 'pending' ORDER BY at DESC LIMIT ?)
		GROUP BY alert_key
		ORDER BY created DESC, alert_key`
	out := []AlertSummaryRow{}
	err := r.queryRows(ctx, query, []interface{}{limit}, func(rows driver.Rows) error {
		var a AlertSummaryRow
		var attempts int64
		if err := rows.Scan(&a.AlertKey, &a.Type, &a.Kind, &a.Fingerprint, &a.AppVersion, &a.CreatedAt,
			&a.Status, &a.UpdatedAt, &attempts, &a.Error, &a.Payload); err != nil {
			return err
		}
		a.Attempts = int(attempts)
		a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
		out = append(out, a)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	return out, nil
}
