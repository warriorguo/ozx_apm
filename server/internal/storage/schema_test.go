package storage

import (
	"strings"
	"testing"
)

var eventTables = []string{
	"apm_perf_samples",
	"apm_janks",
	"apm_startups",
	"apm_scene_loads",
	"apm_exceptions",
	"apm_crashes",
}

// Existing deployments only get new columns through additive, idempotent
// ALTER statements; Migrate runs them on every start.
func TestSchemaStatements_AddEventIDAndSeqToEveryEventTable(t *testing.T) {
	for _, table := range eventTables {
		for _, col := range []string{"event_id String", "seq UInt64"} {
			want := "ALTER TABLE " + table + " ADD COLUMN IF NOT EXISTS " + col
			found := false
			for _, stmt := range schemaStatements {
				if strings.HasPrefix(stmt, want) {
					found = true
				}
			}
			if !found {
				t.Errorf("missing migration %q", want)
			}
		}
	}
}

func TestSchemaStatements_CreateTablesIncludeEventIDAndSeq(t *testing.T) {
	for _, table := range eventTables {
		var create string
		for _, stmt := range schemaStatements {
			if strings.HasPrefix(stmt, "CREATE TABLE IF NOT EXISTS "+table+" ") {
				create = stmt
			}
		}
		if create == "" {
			t.Fatalf("no CREATE TABLE for %s", table)
		}
		if !strings.Contains(create, "event_id String") || !strings.Contains(create, "seq UInt64") {
			t.Errorf("CREATE TABLE %s lacks event_id/seq", table)
		}
	}
}

func TestSchemaStatements_NoDestructiveMigrations(t *testing.T) {
	for _, stmt := range schemaStatements {
		upper := strings.ToUpper(stmt)
		if strings.Contains(upper, "DROP ") || strings.Contains(upper, "MODIFY COLUMN") {
			t.Errorf("non-additive migration: %s", stmt)
		}
	}
}
