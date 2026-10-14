package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/tests/integration/testutil"
)

var eventTables = []string{
	"apm_perf_samples", "apm_janks", "apm_startups",
	"apm_scene_loads", "apm_exceptions", "apm_crashes",
}

// The inserts must keep working when a later release adds columns
// (ALTER TABLE ... ADD COLUMN). This is what makes this binary a safe
// rollback target: an old binary that used positional "INSERT INTO t"
// failed with "expected N arguments, got M" against the migrated schema.
func TestInsert_WorksAfterAdditiveColumnMigration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	client, err := storage.NewClickHouseClient(testutil.TestClickHouseConfig(), zap.NewNop())
	if err != nil {
		t.Skipf("ClickHouse not available: %v", err)
	}
	defer client.Close()
	ctx := context.Background()

	// Exactly the columns the next release adds.
	for _, table := range eventTables {
		for _, stmt := range []string{
			"ALTER TABLE " + table + " ADD COLUMN IF NOT EXISTS event_id String DEFAULT ''",
			"ALTER TABLE " + table + " ADD COLUMN IF NOT EXISTS seq UInt64 DEFAULT 0",
		} {
			if err := client.Exec(ctx, stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
	}

	repo := storage.NewRepository(client, zap.NewNop())
	session := fmt.Sprintf("compat-%d", time.Now().UnixNano())
	now := time.Now()

	inserts := map[string]func() error{
		"apm_perf_samples": func() error {
			return repo.InsertPerfSamples(ctx, []models.PerfSample{{Timestamp: now, AppVersion: "1", Platform: "Android", SessionID: session, DeviceID: "d", FPS: 60}})
		},
		"apm_janks": func() error {
			return repo.InsertJanks(ctx, []models.Jank{{Timestamp: now, AppVersion: "1", Platform: "Android", SessionID: session, DeviceID: "d", RecentEvents: []string{"a"}}})
		},
		"apm_startups": func() error {
			return repo.InsertStartups(ctx, []models.Startup{{Timestamp: now, AppVersion: "1", Platform: "Android", SessionID: session, DeviceID: "d"}})
		},
		"apm_scene_loads": func() error {
			return repo.InsertSceneLoads(ctx, []models.SceneLoad{{Timestamp: now, AppVersion: "1", Platform: "Android", SessionID: session, DeviceID: "d", SceneName: "s"}})
		},
		"apm_exceptions": func() error {
			return repo.InsertExceptions(ctx, []models.Exception{{Timestamp: now, AppVersion: "1", Platform: "Android", SessionID: session, DeviceID: "d", Fingerprint: "f", Count: 1}})
		},
		"apm_crashes": func() error {
			return repo.InsertCrashes(ctx, []models.Crash{{Timestamp: now, AppVersion: "1", Platform: "Android", SessionID: session, DeviceID: "d", Fingerprint: "f", Breadcrumbs: []string{"b"}}})
		},
	}

	for _, table := range eventTables {
		if err := inserts[table](); err != nil {
			t.Errorf("insert into %s after ADD COLUMN failed: %v", table, err)
			continue
		}
		var n uint64
		row := client.Conn().QueryRow(ctx, "SELECT count() FROM "+table+" WHERE session_id = ?", session)
		if err := row.Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("%s: expected 1 row, got %d", table, n)
		}
	}
}
