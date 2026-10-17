package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/api"
	"github.com/warriorguo/ozx_apm/server/internal/api/handlers"
	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
	"github.com/warriorguo/ozx_apm/server/tests/integration/testutil"
)

// APM-4 end to end: exception events in the SDK's wire format go through the
// real ingest router into a fresh database and are read back through the
// admin API (list, detail, context distribution), on both insert paths.

const apm4BusPrefix = "[GameEventBus] Error handling event RoomEnteredEvent: "

// apm4Handler is the exception a bus handler threw, as Exception.ToString()
// on IL2CPP with line numbers.
func apm4Handler(typ, method, msg string, line int) string {
	return typ + ": " + msg + "\n" +
		"  at Ozx.Rooms." + method + " (System.Int32 roomId) [0x00000] in /Users/builder/ozx/Assets/Scripts/Rooms/Rooms.cs:" + strconv.Itoa(line) + " \n" +
		"  at Ozx.Core.GameEventBus.Publish[T] (T evt) [0x00000] in /Users/builder/ozx/Assets/Scripts/Core/GameEventBus.cs:48 \n"
}

const apm4Chain = "System.IO.InvalidDataException: Save file 'inventory.json' is corrupt ---> Newtonsoft.Json.JsonReaderException: Unexpected character at 12\n" +
	"  at Newtonsoft.Json.JsonTextReader.ParseValue () [0x00000] in <00000000000000000000000000000000>:0 \n" +
	"  at Ozx.Save.SaveLoader.Parse (System.String json) [0x00000] in /Users/builder/ozx/Assets/Scripts/Save/SaveLoader.cs:30 \n" +
	"   --- End of inner exception stack trace ---\n" +
	"  at Ozx.Save.SaveLoader.Load (System.String path) [0x00000] in /Users/builder/ozx/Assets/Scripts/Save/SaveLoader.cs:44 \n"

const apm4Tricky = "it's a \"quote\", a back\\slash, ünïcødé 中文 🎮"

type apm4Env struct {
	client *storage.ClickHouseClient
	ingest http.Handler
	admin  http.Handler
	params url.Values
	ts     int64
	n      int
}

func setupAPM4(t *testing.T, async bool) *apm4Env {
	t.Helper()
	admin := adminClient(t)
	db := fmt.Sprintf("ozx_apm_apm4_e2e_%v_%d", async, time.Now().UnixNano()%1e9)
	createDB(t, admin, db)
	cfg := testutil.TestClickHouseConfig()
	cfg.Database = db
	cfg.AsyncInsert = async
	client, err := testutil.SetupTestDatabase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	repo := storage.NewRepository(client, zap.NewNop())
	ts := time.Now().Add(-30 * time.Minute)
	return &apm4Env{
		client: client,
		ingest: api.NewRouter(&config.Config{ClickHouse: *cfg}, repo, zap.NewNop()),
		admin:  api.NewAdminRouter(&config.Config{}, repo, zap.NewNop()),
		params: url.Values{"start_time": {ts.Add(-time.Hour).Format(time.RFC3339)}, "end_time": {time.Now().Format(time.RFC3339)}},
		ts:     ts.UnixMilli(),
	}
}

// event builds an exception event as the SDK sends it; fields are merged in.
func (e *apm4Env) event(session, roomID string, fields map[string]interface{}) string {
	e.n++
	m := map[string]interface{}{
		"type": "exception", "event_id": fmt.Sprintf("apm4-%d", e.n), "seq": e.n, "timestamp": e.ts + int64(e.n),
		"app_version": "2.1", "platform": "Android", "device_model": "SM-A336B", "os_version": "14",
		"session_id": session, "device_id": "d-" + session, "scene": "Main", "build_type": "release",
		"ctx": map[string]string{"roomId": roomID, "levelId": "3"}, "count": 1,
	}
	for k, v := range fields {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (e *apm4Env) post(t *testing.T, events ...string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/events", bytes.NewBufferString(`{"events":[`+strings.Join(events, ",")+`]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ingest.ServeHTTP(w, req)
	var resp handlers.IngestResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || w.Code != http.StatusOK || resp.Accepted != len(events) || len(resp.Errors) != 0 {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
}

func (e *apm4Env) busError(session, roomID, embedded string, extra map[string]interface{}) string {
	f := map[string]interface{}{
		"fingerprint": "client-fp", "log_type": "error", "message": apm4BusPrefix + embedded,
		"stack":              "Ozx.Core.GameEventBus:Publish(RoomEnteredEvent) (at /Users/builder/ozx/Assets/Scripts/Core/GameEventBus.cs:52)\nOzx.Rooms.RoomManager:EnterRoom(Int32)\n",
		"embedded_exception": embedded,
		"breadcrumbs":        []string{"[10:00:00.000] Scene: Main", "[10:00:01.500] room " + roomID + " " + apm4Tricky},
	}
	for k, v := range extra {
		f[k] = v
	}
	return e.event(session, roomID, f)
}

func groupsByType(t *testing.T, e *apm4Env, params url.Values) map[string]models.ExceptionGroup {
	t.Helper()
	var list models.ExceptionListResponse
	getAdmin(t, e.admin, "/api/exceptions", params, &list)
	out := map[string]models.ExceptionGroup{}
	for _, g := range list.Exceptions {
		key := g.ExceptionType
		if key == "" {
			key = "plain:" + g.MessageTemplate + g.Message
		}
		if _, dup := out[key]; dup {
			t.Errorf("two groups for %q", key)
		}
		out[key] = g
	}
	if int(list.TotalCount) != len(list.Exceptions) {
		t.Errorf("total_count %d, %d groups", list.TotalCount, len(list.Exceptions))
	}
	return out
}

// APM-1 verification 1 and 2 through ingest, ClickHouse and the admin API.
func TestExceptions_ServerFingerprintEndToEnd(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async_insert=%v", async), func(t *testing.T) {
			e := setupAPM4(t, async)
			loader := func(room string) string {
				return apm4Handler("System.InvalidOperationException", "RoomLoader.LoadTemplate", "Room template not found for roomId="+room, 71)
			}
			e.post(t,
				e.busError("s1", "12", loader("12"), nil),
				e.busError("s2", "7", loader("7"), map[string]interface{}{"count": 4}), // a window's repeats
				e.busError("s1", "12", apm4Handler("System.NullReferenceException", "EnemySpawner.SpawnWave", "Object reference not set to an instance of an object.", 88), nil),
				e.event("s3", "5", map[string]interface{}{"fingerprint": "client-chain", "log_type": "exception",
					"message": "JsonReaderException: Unexpected character at 12", "stack": "", "embedded_exception": nil}),
				e.busError("s3", "5", apm4Chain, nil),
				// An SDK before APM-4: no event_id, log type, embedded exception or breadcrumbs.
				e.event("s4", "", map[string]interface{}{"event_id": nil, "seq": nil, "build_type": nil, "ctx": nil,
					"fingerprint": "0123456789abcdef", "message": "Asset not found: anim/arack_red 3",
					"stack": "UnityEngine.Debug:LogError (object)\nOzx.Pool.Factory:Load (string) (at Assets/Pool/Factory.cs:33)\n"}),
			)

			groups := groupsByType(t, e, e.params)
			ioe, nre, chain := groups["System.InvalidOperationException"], groups["System.NullReferenceException"], groups["System.IO.InvalidDataException"]
			if len(groups) != 5 {
				t.Fatalf("groups: %+v", groups)
			}
			// Two handlers: two groups with the real type and throw site.
			if ioe.Count != 5 || ioe.SessionCount != 2 || ioe.ThrowSite != "Ozx.Rooms.RoomLoader.LoadTemplate (at /Users/builder/ozx/Assets/Scripts/Rooms/Rooms.cs:71)" ||
				ioe.CallSite != "Ozx.Core.GameEventBus.Publish (at /Users/builder/ozx/Assets/Scripts/Core/GameEventBus.cs:52)" ||
				ioe.MessageTemplate != "Room template not found for roomId=<v>" || ioe.FingerprintVersion != fingerprint.Version {
				t.Errorf("RoomLoader group: %+v", ioe)
			}
			if nre.Count != 1 || nre.ThrowSite != "Ozx.Rooms.EnemySpawner.SpawnWave (at /Users/builder/ozx/Assets/Scripts/Rooms/Rooms.cs:88)" {
				t.Errorf("EnemySpawner group: %+v", nre)
			}
			// The chain logged as LogError(ex) by the bus is one group; the
			// innermost exception logged alone by Unity is another type.
			if chain.Count != 1 || chain.ThrowSite != "Ozx.Save.SaveLoader.Load (at /Users/builder/ozx/Assets/Scripts/Save/SaveLoader.cs:44)" {
				t.Errorf("chain group: %+v", chain)
			}
			legacy := groups["plain:Asset not found: anim/arack_red <n>Asset not found: anim/arack_red 3"]
			if legacy.Count != 1 || legacy.CallSite != "Ozx.Pool.Factory.Load (at Assets/Pool/Factory.cs:33)" || legacy.FingerprintVersion != 1 {
				t.Errorf("legacy SDK group: %+v (groups %v)", legacy, groups)
			}

			// Detail of the RoomLoader group.
			var d models.ExceptionDetail
			getAdmin(t, e.admin, "/api/exceptions/detail", withParams(e.params, "fingerprint", ioe.Fingerprint), &d)
			if d.Count != 5 || d.FirstVersion != "2.1" || len(d.Occurrences) != 2 || len(d.VersionDist) != 1 || d.VersionDist[0].Count != 5 ||
				len(d.DeviceDist) != 1 || d.DeviceDist[0].Count != 5 {
				t.Errorf("detail: %+v", d)
			}
			s := d.Sample
			if s.LogType != "error" || s.ClientFingerprint != "client-fp" || s.FingerprintVersion != fingerprint.Version ||
				s.EmbeddedException != loader("7") || len(s.Parsed.Chain) != 1 || s.Parsed.Chain[0].Type != "System.InvalidOperationException" ||
				len(s.Parsed.Chain[0].Frames) != 2 || s.Parsed.Chain[0].Frames[0].Line != 71 || !s.Parsed.Wrapped ||
				len(s.Parsed.LogFrames) != 2 || s.Parsed.Fingerprint != ioe.Fingerprint {
				t.Errorf("sample: %+v", s)
			}
			latest := d.Occurrences[0]
			if latest.Count != 4 || latest.SessionID != "s2" || latest.Context["roomId"] != "7" || len(latest.Breadcrumbs) != 2 ||
				latest.Breadcrumbs[1] != "[10:00:01.500] room 7 "+apm4Tricky || latest.EventID == "" || latest.BuildType != "release" {
				t.Errorf("latest occurrence: %+v", latest)
			}

			// The chain's detail has the inner exception.
			getAdmin(t, e.admin, "/api/exceptions/detail", withParams(e.params, "fingerprint", chain.Fingerprint), &d)
			if len(d.Sample.InnerExceptions) != 1 || d.Sample.InnerExceptions[0] != "Newtonsoft.Json.JsonReaderException: Unexpected character at 12" ||
				len(d.Sample.Parsed.Chain) != 2 || d.Sample.Parsed.Chain[1].Frames[1].Line != 30 {
				t.Errorf("chain detail: %+v", d.Sample)
			}

			// Distribution by roomId (APM-1 verification 2): one group, two rooms.
			var dist models.ContextDistribution
			getAdmin(t, e.admin, "/api/context/distribution", withParams(e.params, "key", "roomId", "metric", "exceptions", "fingerprint", ioe.Fingerprint), &dist)
			rooms := map[string]int64{}
			for _, r := range dist.Rows {
				rooms[r.Value] = r.Count
			}
			if dist.Total != 5 || len(rooms) != 2 || rooms["12"] != 1 || rooms["7"] != 4 {
				t.Errorf("roomId distribution: %+v", dist)
			}

			// Not found, and filtered to nothing: 404.
			for _, p := range []url.Values{
				withParams(e.params, "fingerprint", "nope"),
				withParams(e.params, "fingerprint", ioe.Fingerprint, "build_type", "dev"),
			} {
				req := httptest.NewRequest(http.MethodGet, "/api/exceptions/detail?"+p.Encode(), nil)
				w := httptest.NewRecorder()
				e.admin.ServeHTTP(w, req)
				if w.Code != http.StatusNotFound {
					t.Errorf("%s: %d", p.Encode(), w.Code)
				}
			}
		})
	}
}

// Rows stored before APM-4 (by the previous binary: no APM-4 columns in its
// INSERT) keep their fingerprint and still list and open: version 0, the
// message as the description, a best-effort parse of the stack.
func TestExceptions_PreAPM4RowsStillDisplay(t *testing.T) {
	e := setupAPM4(t, false)
	ctx := context.Background()
	ts := time.UnixMilli(e.ts)
	// The INSERT of 4f0bf21.
	if err := e.client.Exec(ctx, `INSERT INTO apm_exceptions (timestamp, app_version, platform, device_model, os_version, session_id,
		device_id, scene, fingerprint, message, stack, count, event_id, seq, build_type, gpu, ram_class, net_type, ctx)
		VALUES (fromUnixTimestamp64Milli(toInt64(?)), '2.0', 'Android', 'Pixel', '14', 'old', 'd', 'Main', 'a1b2c3d4e5f60718',
		?, 'Ozx.Enemies.EnemyAI.Update () (at Assets/Scripts/Enemies/EnemyAI.cs:31)', 3, 'old-1', 1, '', '', '', '', map())`,
		ts.UnixMilli(), "NullReferenceException: Object reference not set to an instance of an object"); err != nil {
		t.Fatal(err)
	}
	groups := groupsByType(t, e, e.params)
	g, ok := groups["plain:NullReferenceException: Object reference not set to an instance of an object"]
	if !ok || len(groups) != 1 || g.Fingerprint != "a1b2c3d4e5f60718" || g.FingerprintVersion != 0 || g.Count != 3 || g.ThrowSite != "" {
		t.Fatalf("legacy group: %+v", groups)
	}
	var d models.ExceptionDetail
	getAdmin(t, e.admin, "/api/exceptions/detail", withParams(e.params, "fingerprint", g.Fingerprint), &d)
	if d.Sample.FingerprintVersion != 0 || d.Sample.ClientFingerprint != "" || d.Sample.InnerExceptions == nil ||
		d.Sample.Parsed.Type() != "NullReferenceException" || len(d.Occurrences) != 1 || d.Occurrences[0].Breadcrumbs == nil {
		t.Errorf("legacy detail: %+v", d)
	}
}
