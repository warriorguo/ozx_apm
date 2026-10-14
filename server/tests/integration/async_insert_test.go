package integration

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/tests/integration/testutil"
)

const trickyText = "it's a \"quote\", a back\\slash, a\nnewline, a tab\t, ünïcødé 中文 and an emoji 🎮"

// Both insert paths must store exactly the same values, including
// millisecond timestamps, escapes and arrays.
func TestInsertPaths_RoundTripIdentically(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			cfg := testutil.TestClickHouseConfig()
			cfg.AsyncInsert = async
			client, err := storage.NewClickHouseClient(cfg, zap.NewNop())
			if err != nil {
				t.Skipf("ClickHouse not available: %v", err)
			}
			defer client.Close()
			ctx := context.Background()
			repo := storage.NewRepository(client, zap.NewNop())

			session := fmt.Sprintf("rt-%v-%d", async, time.Now().UnixNano())
			ts := time.UnixMilli(time.Now().UnixMilli()).UTC()

			jank := models.Jank{
				Timestamp: ts, EventID: "e1", Seq: 18446744073709551615, AppVersion: "1.2.3", Platform: "Android",
				DeviceModel: trickyText, OSVersion: "14", SessionID: session, DeviceID: "d", Scene: "S",
				DurationMs: 75.5, MaxFrameMs: 60.25, RecentGCCount: 3, RecentGCAllocKB: 1.5,
				RecentEvents: []string{trickyText, "", "[x]"},
			}
			exc := models.Exception{
				Timestamp: ts, EventID: "e2", Seq: 2, AppVersion: "1.2.3", Platform: "Android",
				SessionID: session, DeviceID: "d", Fingerprint: "fp", Message: trickyText, Stack: "at A\nat B", Count: 7,
			}
			if err := repo.InsertJanks(ctx, []models.Jank{jank, jank}); err != nil {
				t.Fatalf("insert janks: %v", err)
			}
			if err := repo.InsertExceptions(ctx, []models.Exception{exc}); err != nil {
				t.Fatalf("insert exceptions: %v", err)
			}

			var got models.Jank
			row := client.Conn().QueryRow(ctx, `SELECT timestamp, event_id, seq, app_version, platform, device_model, os_version,
				session_id, device_id, scene, duration_ms, max_frame_ms, recent_gc_count, recent_gc_alloc_kb, recent_events
				FROM apm_janks WHERE session_id = ? LIMIT 1`, session)
			if err := row.Scan(&got.Timestamp, &got.EventID, &got.Seq, &got.AppVersion, &got.Platform, &got.DeviceModel,
				&got.OSVersion, &got.SessionID, &got.DeviceID, &got.Scene, &got.DurationMs, &got.MaxFrameMs,
				&got.RecentGCCount, &got.RecentGCAllocKB, &got.RecentEvents); err != nil {
				t.Fatal(err)
			}
			got.Timestamp = got.Timestamp.UTC()
			if !reflect.DeepEqual(got, jank) {
				t.Errorf("jank round trip differs:\n got %+v\nwant %+v", got, jank)
			}

			var n uint64
			if err := client.Conn().QueryRow(ctx, "SELECT count() FROM apm_janks WHERE session_id = ?", session).Scan(&n); err != nil || n != 2 {
				t.Errorf("expected 2 jank rows, got %d (%v)", n, err)
			}

			var msg, stack string
			var count uint32
			var excTs time.Time
			if err := client.Conn().QueryRow(ctx, "SELECT timestamp, message, stack, count FROM apm_exceptions WHERE session_id = ?", session).
				Scan(&excTs, &msg, &stack, &count); err != nil {
				t.Fatal(err)
			}
			if msg != trickyText || stack != exc.Stack || count != 7 || !excTs.Equal(ts) {
				t.Errorf("exception round trip differs: %q %q %d %v", msg, stack, count, excTs)
			}
		})
	}
}

// With async_insert, concurrent requests share parts instead of one part each.
func TestAsyncInsert_ReducesParts(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	cfg := testutil.TestClickHouseConfig()
	cfg.AsyncInsert = true
	client, err := storage.NewClickHouseClient(cfg, zap.NewNop())
	if err != nil {
		t.Skipf("ClickHouse not available: %v", err)
	}
	defer client.Close()
	ctx := context.Background()
	repo := storage.NewRepository(client, zap.NewNop())

	// Stop merges so the parts this test creates can be counted.
	if err := client.Exec(ctx, "SYSTEM STOP MERGES apm_perf_samples"); err != nil {
		t.Fatal(err)
	}
	defer client.Exec(ctx, "SYSTEM START MERGES apm_perf_samples")
	session := fmt.Sprintf("parts-%d", time.Now().UnixNano())
	before := activeParts(t, client, "apm_perf_samples")

	const requests = 40
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- repo.InsertPerfSamples(ctx, []models.PerfSample{{
				Timestamp: time.Now(), AppVersion: "1", Platform: "Android", SessionID: session, DeviceID: "d", FPS: float32(i),
			}})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	created := activeParts(t, client, "apm_perf_samples") - before
	var version string
	_ = client.Conn().QueryRow(ctx, "SELECT version()").Scan(&version)
	t.Logf("ClickHouse %s: %d concurrent inserts created %d parts", version, requests, created)
	if created >= requests/2 {
		t.Errorf("async_insert did not batch: %d inserts -> %d parts", requests, created)
	}
}

func activeParts(t *testing.T, client *storage.ClickHouseClient, table string) int {
	t.Helper()
	var n uint64
	err := client.Conn().QueryRow(context.Background(),
		"SELECT count() FROM system.parts WHERE database = currentDatabase() AND table = ? AND active", table).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}
