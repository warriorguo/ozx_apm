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

// Migrate (with the APM-2 ALTERs) is idempotent, and event_id/seq round-trip
// through ClickHouse for every table.
func TestEventIDAndSeq_RoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	client, err := storage.NewClickHouseClient(testutil.TestClickHouseConfig(), zap.NewNop())
	if err != nil {
		t.Skipf("ClickHouse not available: %v", err)
	}
	defer client.Close()
	ctx := context.Background()

	// Run twice: must be idempotent on an already migrated schema.
	for i := 0; i < 2; i++ {
		if err := client.Migrate(ctx); err != nil {
			t.Fatalf("migrate #%d: %v", i+1, err)
		}
	}

	repo := storage.NewRepository(client, zap.NewNop())
	session := fmt.Sprintf("eid-%d", time.Now().UnixNano())
	now := time.Now()
	const id, seq = "0f8fad5bd9cb469fa16570867728950e", uint64(123456789012)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(repo.InsertPerfSamples(ctx, []models.PerfSample{{Timestamp: now, EventID: id, Seq: seq, AppVersion: "1", Platform: "Android", SessionID: session, DeviceID: "d"}}))
	must(repo.InsertJanks(ctx, []models.Jank{{Timestamp: now, EventID: id, Seq: seq, AppVersion: "1", Platform: "Android", SessionID: session, DeviceID: "d"}}))
	must(repo.InsertStartups(ctx, []models.Startup{{Timestamp: now, EventID: id, Seq: seq, AppVersion: "1", Platform: "Android", SessionID: session, DeviceID: "d"}}))
	must(repo.InsertSceneLoads(ctx, []models.SceneLoad{{Timestamp: now, EventID: id, Seq: seq, AppVersion: "1", Platform: "Android", SessionID: session, DeviceID: "d", SceneName: "s"}}))
	must(repo.InsertExceptions(ctx, []models.Exception{{Timestamp: now, EventID: id, Seq: seq, AppVersion: "1", Platform: "Android", SessionID: session, DeviceID: "d", Fingerprint: "f", Count: 1}}))
	must(repo.InsertCrashes(ctx, []models.Crash{{Timestamp: now, EventID: id, Seq: seq, AppVersion: "1", Platform: "Android", SessionID: session, DeviceID: "d", Fingerprint: "f"}}))

	for _, table := range eventTables {
		var gotID string
		var gotSeq uint64
		row := client.Conn().QueryRow(ctx, "SELECT event_id, seq FROM "+table+" WHERE session_id = ?", session)
		if err := row.Scan(&gotID, &gotSeq); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if gotID != id || gotSeq != seq {
			t.Errorf("%s: got (%q, %d), want (%q, %d)", table, gotID, gotSeq, id, seq)
		}
	}
}
