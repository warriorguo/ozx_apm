package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2"
	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Privacy deletion (APM-14).
//
// Rows of one data subject (a device_id today, a player_id once APM-12
// stores one) are removed with a mutation, ALTER TABLE <t> DELETE WHERE
// <column> = ?, waited for with mutations_sync = 2. It needs the ALTER DELETE
// privilege, which the production `apm` account does not have; RELEASE.md
// (APM-14) has the GRANT. Without it ClickHouse answers 497 and the request
// fails with ErrPrivacyDeleteNotPermitted; nothing is ever required at
// startup.
//
// Lightweight DELETE FROM was evaluated and is not used: on 24.1 and 26.1 it
// also needs ALTER UPDATE(_row_exists), which 26.1 can only grant per table
// (a column grant on ozx_apm.* is a syntax error) and 24.1 turns into
// ALTER UPDATE on every column when granted on ozx_apm.* (verified). The
// mutation needs one database-level grant on every version. Its cost: each
// part that holds a row of the subject is rewritten (parts without one are
// left as they are), which is fine at the current volume.
//
// ReplacingMergeTree (APM-19): the mutation applies to every part that
// exists when it starts, duplicates included, and merges never bring a
// deleted row back. A row inserted after the mutation started (an SDK still
// uploading, an async insert still buffered) is not deleted: verify, and run
// the deletion again (it is idempotent).
//
// The apm_*_daily aggregates are not touched: they are sums per date,
// version, platform and scene/fingerprint with no device or player column,
// and ClickHouse never propagates a DELETE on a source table to a
// materialized view.

// PrivacyAuditTable records every deletion request (who asked, when, which
// tables, rows before and after). It stores a hash of the subject id, never
// the id. No TTL: one small row per request, kept as evidence that the
// request was honoured.
const PrivacyAuditTable = "apm_privacy_deletions"

const privacyAuditTableDDL = `CREATE TABLE IF NOT EXISTS ` + PrivacyAuditTable + ` (
    request_id String,
    at DateTime64(3),
    id_type LowCardinality(String),
    id_hash String,
    status LowCardinality(String),
    tables Array(String),
    rows_before UInt64,
    rows_after UInt64,
    error String,
    requested_by String
) ENGINE = MergeTree()
ORDER BY (id_hash, at)`

// ErrPrivacyDeleteNotPermitted: the ClickHouse account lacks ALTER DELETE.
var ErrPrivacyDeleteNotPermitted = errors.New("the ClickHouse account may not delete rows: an admin must add " + PrivacyDeleteGrant +
	" to its grants (users.d/apm.xml on n192, RELEASE.md APM-14)")

// PrivacyDeleteGrant is the privilege the service account needs (RELEASE.md, APM-14).
const PrivacyDeleteGrant = "GRANT ALTER DELETE ON ozx_apm.*"

// chAccessDenied is ClickHouse's ACCESS_DENIED error code.
const chAccessDenied = 497

func isAccessDenied(err error) bool {
	var ex *clickhouse.Exception
	return errors.As(err, &ex) && ex.Code == chAccessDenied
}

// migratePrivacyAudit creates the audit table. CREATE TABLE is within the
// service account's grants; should it fail anyway, startup continues and
// deletion requests fail (they are never run without a record).
func (c *ClickHouseClient) migratePrivacyAudit(ctx context.Context) {
	if err := c.conn.Exec(ctx, privacyAuditTableDDL); err != nil {
		c.logger.Warn("could not create the privacy deletion audit table; deletion requests will fail until it exists (RELEASE.md, APM-14)",
			zap.String("table", PrivacyAuditTable), zap.Error(err))
	}
}

func quoteIdent(name string) string {
	return "`" + strings.ReplaceAll(strings.ReplaceAll(name, `\`, `\\`), "`", "\\`") + "`"
}

// PrivacyTables lists the MergeTree-family tables of the current database
// that have the column: the six event tables, any table a later release adds
// with the column (ops_events with player_id, APM-12), and leftovers that
// still hold copies, such as the *_pre_apm19 backups of the APM-19 migration.
// Views and the apm_*_daily aggregates have no such column.
func (r *Repository) PrivacyTables(ctx context.Context, column string) ([]string, error) {
	rows, err := r.client.conn.Query(ctx, `
		SELECT table FROM system.columns
		WHERE database = currentDatabase() AND name = ?
		  AND table IN (SELECT name FROM system.tables WHERE database = currentDatabase() AND engine LIKE '%MergeTree')
		ORDER BY table`, column)
	if err != nil {
		return nil, fmt.Errorf("list tables with %s: %w", column, err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		tables = append(tables, t)
	}
	return tables, rows.Err()
}

// CountPrivacyRows counts the rows of the subject in the table (a full scan
// of the column: it is not in any sort key).
func (r *Repository) CountPrivacyRows(ctx context.Context, table, column, id string) (uint64, error) {
	var n uint64
	query := "SELECT count() FROM " + quoteIdent(table) + " WHERE " + quoteIdent(column) + " = ?"
	if err := r.client.conn.QueryRow(ctx, query, id).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return n, nil
}

// DeletePrivacyRows deletes the subject's rows from the table and waits for
// the mutation. ErrPrivacyDeleteNotPermitted without ALTER DELETE.
func (r *Repository) DeletePrivacyRows(ctx context.Context, table, column, id string) error {
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"mutations_sync": 2}))
	query := "ALTER TABLE " + quoteIdent(table) + " DELETE WHERE " + quoteIdent(column) + " = ?"
	if err := r.client.conn.Exec(ctx, query, id); err != nil {
		if isAccessDenied(err) {
			return fmt.Errorf("%w (%v)", ErrPrivacyDeleteNotPermitted, err)
		}
		return fmt.Errorf("delete from %s: %w", table, err)
	}
	return nil
}

// InsertPrivacyAudit appends one record to the audit table.
func (r *Repository) InsertPrivacyAudit(ctx context.Context, rec models.PrivacyAuditRecord) error {
	batch, err := r.client.conn.PrepareBatch(ctx, "INSERT INTO "+PrivacyAuditTable+
		" (request_id, at, id_type, id_hash, status, tables, rows_before, rows_after, error, requested_by)")
	if err != nil {
		return fmt.Errorf("prepare audit insert: %w", err)
	}
	tables := rec.Tables
	if tables == nil {
		tables = []string{}
	}
	if err := batch.Append(rec.RequestID, rec.At, rec.IDType, rec.IDHash, rec.Status, tables,
		rec.RowsBefore, rec.RowsAfter, rec.Error, rec.RequestedBy); err != nil {
		return fmt.Errorf("append audit record: %w", err)
	}
	return batch.Send()
}

// PrivacyAuditHistory returns the latest audit records of a subject, newest first.
func (r *Repository) PrivacyAuditHistory(ctx context.Context, idType, idHash string, limit int) ([]models.PrivacyAuditRecord, error) {
	rows, err := r.client.conn.Query(ctx, "SELECT request_id, at, id_type, id_hash, status, tables, rows_before, rows_after, error, requested_by FROM "+
		PrivacyAuditTable+" WHERE id_hash = ? AND id_type = ? ORDER BY at DESC LIMIT ?", idHash, idType, limit)
	if err != nil {
		return nil, fmt.Errorf("read audit: %w", err)
	}
	defer rows.Close()
	out := []models.PrivacyAuditRecord{}
	for rows.Next() {
		var rec models.PrivacyAuditRecord
		if err := rows.Scan(&rec.RequestID, &rec.At, &rec.IDType, &rec.IDHash, &rec.Status, &rec.Tables,
			&rec.RowsBefore, &rec.RowsAfter, &rec.Error, &rec.RequestedBy); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
