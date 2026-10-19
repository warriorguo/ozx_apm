package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// EventTable describes how to look up existing event_ids in one event table.
//
// KeyColumn is the second column of the table's sort key (the first is always
// app_version). Filtering on both, plus the batch's timestamp range, lets the
// primary key and the event_id skip index prune the lookup to a few granules.
type EventTable struct {
	Name      string
	KeyColumn string
}

// The event tables and their sort-key prefix (app_version, KeyColumn, timestamp).
var (
	TablePerfSamples = EventTable{Name: "apm_perf_samples", KeyColumn: "platform"}
	TableJanks       = EventTable{Name: "apm_janks", KeyColumn: "scene"}
	TableStartups    = EventTable{Name: "apm_startups", KeyColumn: "platform"}
	TableSceneLoads  = EventTable{Name: "apm_scene_loads", KeyColumn: "scene_name"}
	TableExceptions  = EventTable{Name: "apm_exceptions", KeyColumn: "fingerprint"}
	TableCrashes     = EventTable{Name: "apm_crashes", KeyColumn: "fingerprint"}
	// APM-6
	TableCustomEvents  = EventTable{Name: "apm_custom_events", KeyColumn: "name"}
	TableCustomMetrics = EventTable{Name: "apm_custom_metrics", KeyColumn: "name"}
	// APM-9
	TableRuns = EventTable{Name: "apm_runs", KeyColumn: "end_reason"}
)

// EventTables lists every event table.
var EventTables = []EventTable{TablePerfSamples, TableJanks, TableStartups, TableSceneLoads, TableExceptions, TableCrashes,
	TableCustomEvents, TableCustomMetrics, TableRuns}

// EventIDLookup asks which of EventIDs are already stored in Table.
//
// AppVersions and Keys are the distinct app_version and KeyColumn values of
// the batch's events, MinTime/MaxTime their timestamp range. A re-sent event
// has the same values as the stored copy, so these filters never hide it;
// they only bound the rows ClickHouse has to read.
type EventIDLookup struct {
	Table       EventTable
	AppVersions []string
	Keys        []string
	// AnyKey: do not filter on KeyColumn (Keys may be empty). Crashes (APM-7):
	// their fingerprint depends on the symbols available when a copy was
	// stored, so a re-sent crash can have another one; crashes are few, the
	// app_version and time filters and the event_id index still bound the read.
	AnyKey   bool
	MinTime  time.Time
	MaxTime  time.Time
	EventIDs []string
}

func groupSet(values []string) clickhouse.GroupSet {
	set := make([]any, len(values))
	for i, v := range values {
		set[i] = v
	}
	return clickhouse.GroupSet{Value: set}
}

// buildEventIDLookup returns the query and its arguments. String values are
// bound by the driver (quoted and escaped); the timestamps are integer
// milliseconds so DateTime64(3) is compared at full precision.
func buildEventIDLookup(q EventIDLookup) (string, []any, error) {
	if q.Table.Name == "" || q.Table.KeyColumn == "" {
		return "", nil, fmt.Errorf("event id lookup: unknown table %+v", q.Table)
	}
	if len(q.EventIDs) == 0 || len(q.AppVersions) == 0 || (len(q.Keys) == 0 && !q.AnyKey) {
		return "", nil, fmt.Errorf("event id lookup on %s: empty filter", q.Table.Name)
	}
	var sb strings.Builder
	sb.WriteString("SELECT DISTINCT event_id FROM ")
	sb.WriteString(q.Table.Name)
	sb.WriteString(" WHERE app_version IN ? AND ")
	args := []any{groupSet(q.AppVersions)}
	if !q.AnyKey {
		sb.WriteString(q.Table.KeyColumn)
		sb.WriteString(" IN ? AND ")
		args = append(args, groupSet(q.Keys))
	}
	sb.WriteString("timestamp >= fromUnixTimestamp64Milli(toInt64(?)) AND timestamp <= fromUnixTimestamp64Milli(toInt64(?)) AND event_id IN ?")
	args = append(args, q.MinTime.UnixMilli(), q.MaxTime.UnixMilli(), groupSet(q.EventIDs))
	return sb.String(), args, nil
}

// ExistingEventIDs returns the subset of q.EventIDs already stored in q.Table.
func (r *Repository) ExistingEventIDs(ctx context.Context, q EventIDLookup) (map[string]struct{}, error) {
	query, args, err := buildEventIDLookup(q)
	if err != nil {
		return nil, err
	}
	rows, err := r.client.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		found[id] = struct{}{}
	}
	return found, rows.Err()
}
