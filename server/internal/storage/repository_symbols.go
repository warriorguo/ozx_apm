package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/warriorguo/ozx_apm/server/internal/symbols"
)

// Uploaded symbols (APM-7). The tables are defined in schema.go
// (symbolTables); internal/symbols describes the upload and the lookup.

// The repository is the symbolizer's store.
var _ symbols.Store = (*Repository)(nil)

// InsertSymbolRows stores one part of an upload. Rows of the same upload
// and address collapse (ReplacingMergeTree), so a part sent again is harmless.
func (r *Repository) InsertSymbolRows(ctx context.Context, buildID, uploadID string, funcs []symbols.Func, lines []symbols.Line) error {
	now := time.Now()
	if len(funcs) > 0 {
		batch, err := r.client.conn.PrepareBatch(ctx, "INSERT INTO apm_symbol_funcs (build_id, upload_id, addr, size, name, raw_name, uploaded_at)")
		if err != nil {
			return fmt.Errorf("prepare functions: %w", err)
		}
		for _, f := range funcs {
			if err := batch.Append(buildID, uploadID, f.Addr, f.Size, f.Name, f.RawName, now); err != nil {
				return fmt.Errorf("append function: %w", err)
			}
		}
		if err := batch.Send(); err != nil {
			return fmt.Errorf("insert functions: %w", err)
		}
	}
	if len(lines) > 0 {
		batch, err := r.client.conn.PrepareBatch(ctx, "INSERT INTO apm_symbol_lines (build_id, upload_id, addr, file, line, uploaded_at)")
		if err != nil {
			return fmt.Errorf("prepare lines: %w", err)
		}
		for _, l := range lines {
			if err := batch.Append(buildID, uploadID, l.Addr, l.File, l.Line, now); err != nil {
				return fmt.Errorf("append line: %w", err)
			}
		}
		if err := batch.Send(); err != nil {
			return fmt.Errorf("insert lines: %w", err)
		}
	}
	return nil
}

// CountSymbolRows returns the distinct addresses stored for an upload.
func (r *Repository) CountSymbolRows(ctx context.Context, buildID, uploadID string) (funcs, lines uint64, err error) {
	q := `SELECT
		(SELECT uniqExact(addr) FROM apm_symbol_funcs WHERE build_id = ? AND upload_id = ?),
		(SELECT uniqExact(addr) FROM apm_symbol_lines WHERE build_id = ? AND upload_id = ?)`
	err = r.client.conn.QueryRow(ctx, q, buildID, uploadID, buildID, uploadID).Scan(&funcs, &lines)
	return funcs, lines, err
}

// InsertSymbolUpload records a committed upload.
func (r *Repository) InsertSymbolUpload(ctx context.Context, c symbols.Commit) error {
	batch, err := r.client.conn.PrepareBatch(ctx, `INSERT INTO apm_symbol_files (build_id, upload_id, module, arch, app_version, app_build,
		functions, lines, line_map, method_map, source, tool, uploaded_at)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	if err := batch.Append(c.BuildID, c.UploadID, c.Module, c.Arch, c.AppVersion, c.AppBuild, c.Functions, c.Lines,
		boolColumn(c.LineMap), boolColumn(c.MethodMap), c.Source, c.Tool, time.Now()); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	return batch.Send()
}

const symbolUploadColumns = `build_id, upload_id, module, arch, app_version, app_build, functions, lines, line_map, method_map, source, tool, uploaded_at`

func scanSymbolUpload(rows driver.Rows) (symbols.UploadRef, error) {
	var u symbols.UploadRef
	var lineMap, methodMap uint8
	err := rows.Scan(&u.BuildID, &u.UploadID, &u.Module, &u.Arch, &u.AppVersion, &u.AppBuild, &u.Functions, &u.Lines,
		&lineMap, &methodMap, &u.Source, &u.Tool, &u.UploadedAt)
	u.LineMap, u.MethodMap = lineMap == 1, methodMap == 1
	return u, err
}

// LatestSymbolUpload returns the newest committed upload of a build id.
func (r *Repository) LatestSymbolUpload(ctx context.Context, buildID string) (symbols.UploadRef, bool, error) {
	ups, err := r.ListSymbolUploads(ctx, SymbolUploadFilter{BuildID: buildID, Limit: 1})
	if err != nil || len(ups) == 0 {
		return symbols.UploadRef{}, false, err
	}
	return ups[0], true, nil
}

// SymbolUploadsForVersion returns the committed uploads of a module for an
// app version, newest first.
func (r *Repository) SymbolUploadsForVersion(ctx context.Context, appVersion, module string) ([]symbols.UploadRef, error) {
	return r.ListSymbolUploads(ctx, SymbolUploadFilter{AppVersion: appVersion, Module: module, Limit: 20})
}

// SymbolUploadFilter selects committed uploads; empty fields match anything.
type SymbolUploadFilter struct {
	BuildID    string
	AppVersion string
	Module     string
	Limit      int
}

// ListSymbolUploads lists committed uploads, newest first.
func (r *Repository) ListSymbolUploads(ctx context.Context, f SymbolUploadFilter) ([]symbols.UploadRef, error) {
	var conds []string
	var args []interface{}
	if f.BuildID != "" {
		conds = append(conds, "build_id = ?")
		args = append(args, f.BuildID)
	}
	if f.AppVersion != "" {
		conds = append(conds, "app_version = ?")
		args = append(args, f.AppVersion)
	}
	if f.Module != "" {
		conds = append(conds, "module = ?")
		args = append(args, f.Module)
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	// FINAL: a commit sent twice is one row.
	q := fmt.Sprintf(`SELECT %s FROM apm_symbol_files FINAL %s ORDER BY uploaded_at DESC, upload_id LIMIT %d`, symbolUploadColumns, where, limit)
	var out []symbols.UploadRef
	err := r.queryRows(ctx, q, args, func(rows driver.Rows) error {
		u, err := scanSymbolUpload(rows)
		if err != nil {
			return err
		}
		out = append(out, u)
		return nil
	})
	return out, err
}

// LookupSymbols returns, for each address, the function at or before it
// and the line row at or before it, of one upload. One query per table:
// a UNION ALL of one "largest key <= address" read per address, each of
// which reads a granule or two through the sort key.
func (r *Repository) LookupSymbols(ctx context.Context, ref symbols.UploadRef, addrs []uint64) (map[uint64]symbols.Hit, error) {
	out := map[uint64]symbols.Hit{}
	if len(addrs) == 0 {
		return out, nil
	}
	fq, fargs := symbolLookupQuery("apm_symbol_funcs", "addr, size, name, raw_name", ref, addrs)
	err := r.queryRows(ctx, fq, fargs, func(rows driver.Rows) error {
		var pc uint64
		var f symbols.Func
		if err := rows.Scan(&pc, &f.Addr, &f.Size, &f.Name, &f.RawName); err != nil {
			return err
		}
		h := out[pc]
		h.Func = f
		out[pc] = h
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("symbol functions: %w", err)
	}
	if ref.Lines == 0 {
		return out, nil
	}
	lq, largs := symbolLookupQuery("apm_symbol_lines", "addr, toString(file), line", ref, addrs)
	err = r.queryRows(ctx, lq, largs, func(rows driver.Rows) error {
		var pc uint64
		var l symbols.Line
		if err := rows.Scan(&pc, &l.Addr, &l.File, &l.Line); err != nil {
			return err
		}
		h := out[pc]
		h.Line = l
		out[pc] = h
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("symbol lines: %w", err)
	}
	return out, nil
}

// symbolLookupQuery builds the UNION ALL; every value is a bound argument.
func symbolLookupQuery(table, columns string, ref symbols.UploadRef, addrs []uint64) (string, []interface{}) {
	parts := make([]string, 0, len(addrs))
	args := make([]interface{}, 0, 4*len(addrs))
	for _, a := range addrs {
		parts = append(parts, fmt.Sprintf(`SELECT * FROM (SELECT toUInt64(?) AS pc, %s FROM %s WHERE build_id = ? AND upload_id = ? AND addr <= ? ORDER BY addr DESC LIMIT 1)`, columns, table))
		args = append(args, a, ref.BuildID, ref.UploadID, a)
	}
	return strings.Join(parts, " UNION ALL "), args
}
