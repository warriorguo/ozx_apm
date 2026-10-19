package storage

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// rowWriter is the part of driver.Batch the insert functions use.
type rowWriter interface {
	Append(v ...any) error
	Send() error
}

// newInsert starts an insert of "INSERT INTO table (columns)".
//
// By default it is a native-protocol batch. With clickhouse.async_insert
// enabled, rows are sent as one "INSERT ... VALUES" with async_insert=1 and
// wait_for_async_insert=1: ClickHouse buffers inserts from concurrent requests
// into one part instead of one part per request (fewer parts, fewer merges
// under many clients), and the call still returns only after the data is
// written, so the ingest response stays truthful.
//
// Measured with 50 concurrent single-row inserts (stop merges, count parts):
//
//	                         23.8.16   24.1.8
//	native batch              50        50
//	native batch + settings   50        5     <- the setting is ignored on 23.8
//	VALUES + AsyncInsert       5         5     <- this path
//
// The VALUES path is used because production may run 23.8
// (deployments/docker-compose.yml). It costs up to async_insert_busy_timeout_ms
// (200ms by default) of extra latency per request.
func (r *Repository) newInsert(ctx context.Context, query string) (rowWriter, error) {
	if !r.client.asyncInsert {
		return r.client.conn.PrepareBatch(ctx, query)
	}
	open := strings.Index(query, "(")
	if open < 0 || !strings.HasSuffix(query, ")") {
		return nil, fmt.Errorf("async insert needs an explicit column list: %q", query)
	}
	columns := strings.Count(query[open:], ",") + 1
	return &asyncInsert{ctx: ctx, client: r.client, query: query, columns: columns}, nil
}

type asyncInsert struct {
	ctx     context.Context
	client  *ClickHouseClient
	query   string
	columns int
	rows    int
	args    []any
}

func (a *asyncInsert) Append(v ...any) error {
	if len(v) != a.columns {
		return fmt.Errorf("expected %d arguments, got %d", a.columns, len(v))
	}
	a.args = append(a.args, v...)
	a.rows++
	return nil
}

// appendMapLiteral writes map(?, ?, ...) for m and appends the keys and
// values to args. An empty map is map(), which ClickHouse casts to the column type.
func appendMapLiteral[V string | uint8](sb *strings.Builder, args []any, m map[string]V) []any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sb.WriteString("map(")
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("?, ?")
		args = append(args, k, m[k])
	}
	sb.WriteString(")")
	return args
}

func (a *asyncInsert) Send() error {
	if a.rows == 0 {
		return nil
	}
	var sb strings.Builder
	sb.Grow(len(a.query) + 8 + a.rows*a.columns*4)
	sb.WriteString(a.query)
	sb.WriteString(" VALUES ")
	args := make([]any, 0, len(a.args))
	for i, v := range a.args {
		col := i % a.columns
		if col == 0 {
			if i > 0 {
				sb.WriteString("), ")
			}
			sb.WriteString("(")
		} else {
			sb.WriteString(", ")
		}
		if t, ok := v.(time.Time); ok {
			// The driver binds time.Time with second precision; write the
			// instant as an integer literal instead (no injection risk).
			sb.WriteString("fromUnixTimestamp64Milli(")
			sb.WriteString(strconv.FormatInt(t.UnixMilli(), 10))
			sb.WriteString(")")
			continue
		}
		if m, ok := v.(map[string]string); ok {
			// The driver formats a bound Go map with its keys unescaped
			// (clickhouse-go v2.17 bind.go, reflect.Map case): a key with a
			// quote would break out of the literal. Bind every key and value
			// as its own string parameter instead. Keys in a fixed order so
			// the statement text is stable.
			args = appendMapLiteral(&sb, args, m)
			continue
		}
		if m, ok := v.(map[string]uint8); ok {
			// The same for prop_types (APM-10).
			args = appendMapLiteral(&sb, args, m)
			continue
		}
		sb.WriteString("?")
		args = append(args, v)
	}
	sb.WriteString(")")
	return a.client.conn.AsyncInsert(a.ctx, sb.String(), true, args...)
}
