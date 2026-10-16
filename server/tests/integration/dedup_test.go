package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api/handlers"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/tests/integration/testutil"
)

// APM-19: ExistingEventIDs finds exactly the stored ids on every table,
// including ids that need escaping, and the event_id skip index exists.
func TestExistingEventIDs_RealClickHouse(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	client, err := testutil.SetupTestDatabase(testutil.TestClickHouseConfig())
	if err != nil {
		t.Skipf("ClickHouse not available: %v", err)
	}
	defer client.Close()
	ctx := context.Background()
	repo := storage.NewRepository(client, zap.NewNop())

	session := fmt.Sprintf("dd-%d", time.Now().UnixNano())
	ts := time.UnixMilli(time.Now().UnixMilli())
	stored := []string{session + "-a", session + `-it's \ "odd" ?`, session + "-c"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range stored {
		must(repo.InsertPerfSamples(ctx, []models.PerfSample{{Timestamp: ts, EventID: id, AppVersion: "9.9", Platform: "Android", SessionID: session, DeviceID: "d"}}))
		must(repo.InsertJanks(ctx, []models.Jank{{Timestamp: ts, EventID: id, AppVersion: "9.9", Platform: "Android", Scene: "S", SessionID: session, DeviceID: "d"}}))
		must(repo.InsertStartups(ctx, []models.Startup{{Timestamp: ts, EventID: id, AppVersion: "9.9", Platform: "Android", SessionID: session, DeviceID: "d"}}))
		must(repo.InsertSceneLoads(ctx, []models.SceneLoad{{Timestamp: ts, EventID: id, AppVersion: "9.9", Platform: "Android", SceneName: "S", SessionID: session, DeviceID: "d"}}))
		must(repo.InsertExceptions(ctx, []models.Exception{{Timestamp: ts, EventID: id, AppVersion: "9.9", Platform: "Android", Fingerprint: "F", SessionID: session, DeviceID: "d", Count: 1}}))
		must(repo.InsertCrashes(ctx, []models.Crash{{Timestamp: ts, EventID: id, AppVersion: "9.9", Platform: "Android", Fingerprint: "F", SessionID: session, DeviceID: "d"}}))
	}

	keyFor := map[string]string{"platform": "Android", "scene": "S", "scene_name": "S", "fingerprint": "F"}
	for _, table := range storage.EventTables {
		q := storage.EventIDLookup{
			Table:       table,
			AppVersions: []string{"9.9"},
			Keys:        []string{keyFor[table.KeyColumn]},
			MinTime:     ts,
			MaxTime:     ts,
			EventIDs:    []string{stored[1], session + "-new", stored[2]},
		}
		found, err := repo.ExistingEventIDs(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", table.Name, err)
		}
		if len(found) != 2 {
			t.Errorf("%s: found %v, want %q and %q", table.Name, found, stored[1], stored[2])
		}
		for _, id := range stored[1:] {
			if _, ok := found[id]; !ok {
				t.Errorf("%s: %q not found", table.Name, id)
			}
		}
		// The time range is inclusive at millisecond precision.
		q.MinTime, q.MaxTime = ts.Add(time.Millisecond), ts.Add(time.Hour)
		if found, err := repo.ExistingEventIDs(ctx, q); err != nil || len(found) != 0 {
			t.Errorf("%s: outside the time range found %v (err %v)", table.Name, found, err)
		}

		var indexes uint64
		row := client.Conn().QueryRow(ctx, "SELECT count() FROM system.data_skipping_indices WHERE database = currentDatabase() AND table = ? AND name = ?", table.Name, storage.EventIDIndexName)
		if err := row.Scan(&indexes); err != nil || indexes != 1 {
			t.Errorf("%s: event_id index count %d (err %v)", table.Name, indexes, err)
		}
	}
}

// APM-19 end to end through the ingest handler, with both insert paths: a
// resent batch, and a resend that overlaps with new events, store no
// duplicate rows; old-SDK events are stored every time.
func TestIngest_DropsDuplicates_RealClickHouse(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			cfg := testutil.TestClickHouseConfig()
			cfg.AsyncInsert = async
			client, err := testutil.SetupTestDatabase(cfg)
			if err != nil {
				t.Skipf("ClickHouse not available: %v", err)
			}
			defer client.Close()
			repo := storage.NewRepository(client, zap.NewNop())
			h := handlers.NewIngestHandler(repo, zap.NewNop())

			session := fmt.Sprintf("ddi-%v-%d", async, time.Now().UnixNano())
			now := time.Now().UnixMilli()
			ev := func(typ, id string, i int, extra string) string {
				ids := ""
				if id != "" {
					ids = `"event_id":"` + session + "-" + id + `","seq":` + strconv.Itoa(i) + `,`
				}
				return `{"type":"` + typ + `",` + ids + `"timestamp":` + strconv.FormatInt(now+int64(i), 10) +
					`,"app_version":"1.0.0","platform":"Android","device_model":"Pixel","os_version":"14","session_id":"` +
					session + `","device_id":"d","scene":"Main"` + extra + `}`
			}
			post := func(events ...string) handlers.IngestResponse {
				t.Helper()
				body := `{"events":[` + strings.Join(events, ",") + `]}`
				req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewBufferString(body))
				w := httptest.NewRecorder()
				h.IngestEvents(w, req)
				var resp handlers.IngestResponse
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusOK {
					t.Fatalf("code %d body %s", w.Code, w.Body.String())
				}
				return resp
			}
			count := func(table string) uint64 {
				t.Helper()
				var n uint64
				if err := client.Conn().QueryRow(context.Background(), "SELECT count() FROM "+table+" WHERE session_id = ?", session).Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n
			}

			batch := []string{
				ev("perf_sample", "p1", 1, `,"fps":60`),
				ev("perf_sample", "p2", 2, `,"fps":59`),
				ev("exception", "x1", 3, `,"fingerprint":"fp","count":1`),
				ev("crash", "c1", 4, `,"fingerprint":"cfp","crash_type":"native"`),
			}
			if r := post(batch...); r.Accepted != 4 || r.Duplicates != 0 {
				t.Fatalf("first: %+v", r)
			}
			if r := post(batch...); r.Accepted != 4 || r.Duplicates != 4 {
				t.Fatalf("resend: %+v", r)
			}
			overlap := append(batch, ev("perf_sample", "p3", 5, `,"fps":58`), ev("exception", "x2", 6, `,"fingerprint":"fp","count":2`))
			if r := post(overlap...); r.Accepted != 6 || r.Duplicates != 4 {
				t.Fatalf("overlap: %+v", r)
			}
			legacy := ev("perf_sample", "", 7, `,"fps":30`)
			for i := 0; i < 2; i++ {
				if r := post(legacy, legacy); r.Accepted != 2 || r.Duplicates != 0 {
					t.Fatalf("legacy: %+v", r)
				}
			}
			if got := count("apm_perf_samples"); got != 3+4 {
				t.Errorf("perf rows %d, want 7 (3 distinct ids + 4 legacy)", got)
			}
			if got := count("apm_exceptions"); got != 2 {
				t.Errorf("exception rows %d, want 2", got)
			}
			if got := count("apm_crashes"); got != 1 {
				t.Errorf("crash rows %d, want 1", got)
			}
		})
	}
}
