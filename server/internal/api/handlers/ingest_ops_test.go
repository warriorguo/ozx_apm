package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/config"
	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// fakeOpsStore is fakeEventStore with the ops tables (APM-12).
type fakeOpsStore struct {
	fakeEventStore
	ops         []models.OpsEvent
	days        []models.OpsPlayerDay
	players     []models.OpsPlayer
	failOps     bool
	failPlayers int // fail this many player row inserts
}

func (f *fakeOpsStore) InsertOpsEvents(_ context.Context, e []models.OpsEvent) error {
	if f.failOps {
		return errors.New("clickhouse unavailable")
	}
	f.ops = append(f.ops, e...)
	for _, x := range e {
		f.remember(storage.TableOpsEvents.Name, x.EventID)
	}
	return nil
}
func (f *fakeOpsStore) InsertOpsPlayerDays(_ context.Context, d []models.OpsPlayerDay) error {
	if f.failPlayers > 0 {
		f.failPlayers--
		return errors.New("clickhouse unavailable")
	}
	f.days = append(f.days, d...)
	return nil
}
func (f *fakeOpsStore) InsertOpsPlayers(_ context.Context, p []models.OpsPlayer) error {
	f.players = append(f.players, p...)
	return nil
}

var opsTestNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func opsHandler(store EventStore, cfg config.OpsConfig) *IngestHandler {
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return opsTestNow }
	}
	return newIngestHandlerOps(store, zap.NewNop(), cfg)
}

// opsJSON builds an ops event as the SDK's ops queue sends it.
func opsJSON(id, player, name string, ts time.Time, extra string) string {
	m := map[string]interface{}{
		"type": "custom_event", "ops": true, "event_id": id, "seq": 1, "timestamp": ts.UnixMilli(),
		"app_version": "1.2.0", "platform": "Android", "session_id": "s-" + player, "build_type": "release",
		"name": name, "count": 1, "player_id": player, "channel": "googleplay", "country": "de",
	}
	b, _ := json.Marshal(m)
	s := string(b)
	if extra != "" {
		s = s[:len(s)-1] + "," + extra + "}"
	}
	return s
}

// The SDK's own ops events (testdata/sdk_events_apm12.jsonl, written by the
// APM-12 SDK's ops queue on Unity's Mono) are stored as sent: typed
// properties, the whitelisted context, the ops sequence and ids.
func TestIngestOps_EventsFromTheSDK(t *testing.T) {
	b, err := os.ReadFile("../../processor/testdata/sdk_events_apm12.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	ts := regexp.MustCompile(`"timestamp":(\d+)`)
	base := opsTestNow.Add(-time.Hour).UnixMilli() - 1792244223000
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		lines = append(lines, ts.ReplaceAllStringFunc(l, func(m string) string {
			n, _ := strconv.ParseInt(ts.FindStringSubmatch(m)[1], 10, 64)
			return `"timestamp":` + strconv.FormatInt(n+base, 10)
		}))
	}
	store := &fakeOpsStore{}
	h := opsHandler(store, config.OpsConfig{})
	w, resp := postBatch(t, h, batchOf(lines...), map[string]string{SDKVersionHeader: "2.0.0"})
	if w.Code != http.StatusOK || resp.Accepted != 7 || resp.Rejected != 0 || len(resp.Errors) != 0 {
		t.Fatalf("%d %+v", w.Code, resp)
	}
	if len(store.ops) != 7 || len(store.customs) != 0 {
		t.Fatalf("ops %d technical %d", len(store.ops), len(store.customs))
	}
	for i, e := range store.ops {
		if e.Seq != uint64(i+1) || len(e.EventID) != 32 || e.PlayerID != "5f0c1a7e9b2d4c3e8a6f1b0d2c4e6a8b" || e.Country != "DE" ||
			e.Channel != "googleplay" || e.Context["runId"] != "r-7" || e.Context["chapter"] != "3" || e.Context["masterSeed"] != "" {
			t.Errorf("event %d: %+v", i, e)
		}
	}
	run, show, end := store.ops[1], store.ops[4], store.ops[6]
	if run.Props["mode"] != `hard "x"` || run.NumProps["chapter"] != 3 || run.PropTypes["mode"] != 0 {
		t.Errorf("run_start %+v", run)
	}
	if show.Name != "ad_show" || show.NumProps["revenue"] != 0.012346 || show.Props["currency"] != "USD" || show.Props["network"] != "admob" {
		t.Errorf("ad_show %+v", show)
	}
	if end.NumProps["foreground_ms"] != 6000 || end.Props["reason"] != "pause" {
		t.Errorf("session end %+v", end)
	}
	if len(store.days) != 1 || len(store.players) != 1 || store.players[0].InstallChannel != "googleplay" {
		t.Errorf("days %+v players %+v", store.days, store.players)
	}
}

func TestIngestOps_StoredInOpsEventsOnly(t *testing.T) {
	store := &fakeOpsStore{}
	h := opsHandler(store, config.OpsConfig{})
	at := opsTestNow.Add(-time.Minute)
	w, resp := postBatch(t, h, batchOf(
		opsJSON("o1", "p1", "ad_show", at, `"props":{"placement":"revive","revenue":0.02,"currency":"USD"},"ctx":{"runId":"r1","masterSeed":"9"}`),
		opsJSON("o2", "p1", "ad_reward", at.Add(time.Second), ""),
		sdkEventJSON("custom_event", "c1", 3, `,"name":"room_enter","count":1`),
	), map[string]string{SDKVersionHeader: "2.0.0"})
	if w.Code != http.StatusOK || resp.Accepted != 3 || resp.Rejected != 0 || !resp.Stored {
		t.Fatalf("code %d resp %+v", w.Code, resp)
	}
	if len(store.ops) != 2 || len(store.customs) != 1 || store.customs[0].Name != "room_enter" {
		t.Fatalf("ops %d, technical %d", len(store.ops), len(store.customs))
	}
	o := store.ops[0]
	if o.Name != "ad_show" || o.PlayerID != "p1" || o.Country != "DE" || o.Channel != "googleplay" || o.NumProps["revenue"] != 0.02 ||
		o.Context["runId"] != "r1" || o.Context["masterSeed"] != "" || !o.ServerTime.Equal(opsTestNow) || !o.ClientTime.Equal(at.UTC()) {
		t.Errorf("ops event %+v", o)
	}
	if len(store.days) != 1 || store.days[0].PlayerID != "p1" || store.days[0].Day != time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC) {
		t.Errorf("player days %+v", store.days)
	}
	if len(store.players) != 1 || !store.players[0].FirstSeen.Equal(at.UTC()) || !store.players[0].LastSeen.Equal(at.Add(time.Second).UTC()) ||
		store.players[0].InstallChannel != "googleplay" || store.players[0].BuildType != "release" {
		t.Errorf("players %+v", store.players)
	}
}

// Re-sent ops events (the response was lost, the SDK sends the batch again)
// are dropped by event id: each event is stored once.
func TestIngestOps_ResentBatchIsStoredOnce(t *testing.T) {
	store := &fakeOpsStore{}
	h := opsHandler(store, config.OpsConfig{})
	at := opsTestNow.Add(-2 * time.Hour)
	batch := batchOf(opsJSON("o1", "p1", "run_start", at, ""), opsJSON("o2", "p1", "run_end", at.Add(time.Minute), ""),
		opsJSON("o2", "p1", "run_end", at.Add(time.Minute), ""))
	for i := 0; i < 3; i++ {
		w, resp := postBatch(t, h, batch, nil)
		if w.Code != http.StatusOK || resp.Accepted != 3 {
			t.Fatalf("round %d: %d %+v", i, w.Code, resp)
		}
		wantDups := 1
		if i > 0 {
			wantDups = 3
		}
		if resp.Duplicates != wantDups {
			t.Errorf("round %d: duplicates %d, want %d", i, resp.Duplicates, wantDups)
		}
	}
	if len(store.ops) != 2 {
		t.Fatalf("stored %d ops events, want 2", len(store.ops))
	}
	// The lookup filters on the client time, the same in every copy.
	last := store.lookups[len(store.lookups)-1]
	if last.Table.Name != "ops_events" || last.Table.TimeColumn != "client_time" || !last.MinTime.Equal(at.UTC()) {
		t.Errorf("lookup %+v", last)
	}
	// The derived rows are written once by this process (the cache).
	if len(store.days) != 1 || len(store.players) != 1 {
		t.Errorf("days %d players %d", len(store.days), len(store.players))
	}
}

func TestIngestOps_RejectsInvalid(t *testing.T) {
	store := &fakeOpsStore{}
	h := opsHandler(store, config.OpsConfig{})
	at := opsTestNow.Add(-time.Minute)
	noPlayer := opsJSON("o1", "", "x", at, "")
	noID := opsJSON("", "p1", "x", at, "")
	badName := opsJSON("o3", "p1", "bad name", at, "")
	w, resp := postBatch(t, h, batchOf(noPlayer, noID, badName, opsJSON("o4", "p1", "ok", at, "")), nil)
	if w.Code != http.StatusOK || resp.Accepted != 1 || resp.Rejected != 3 || len(store.ops) != 1 {
		t.Fatalf("code %d resp %+v", w.Code, resp)
	}
	if !strings.Contains(strings.Join(resp.Errors, "|"), "player_id") || !strings.Contains(strings.Join(resp.Errors, "|"), "event_id") {
		t.Errorf("errors %v", resp.Errors)
	}
}

// A store without the ops tables (an older repository in tests) refuses ops
// events with a reason instead of storing them as technical events.
func TestIngestOps_StoreWithoutOps(t *testing.T) {
	store := &fakeEventStore{}
	h := opsHandler(store, config.OpsConfig{})
	_, resp := postBatch(t, h, batchOf(opsJSON("o1", "p1", "x", opsTestNow, "")), nil)
	if resp.Rejected != 1 || len(store.customs) != 0 {
		t.Fatalf("resp %+v", resp)
	}
}

// A failed ops insert answers 503 (the SDK keeps the batch); so does a
// failed insert of the derived rows, and the resend then stores the rows
// although the events are duplicates.
func TestIngestOps_Failures(t *testing.T) {
	at := opsTestNow.Add(-time.Minute)
	batch := batchOf(opsJSON("o1", "p1", "x", at, ""), opsJSON("o2", "p2", "x", at, ""))

	store := &fakeOpsStore{failOps: true}
	w, resp := postBatch(t, opsHandler(store, config.OpsConfig{}), batch, nil)
	if w.Code != http.StatusServiceUnavailable || !resp.Retry || resp.Failed != 2 || resp.Accepted != 0 || len(store.days) != 0 {
		t.Fatalf("ops insert failure: %d %+v days %d", w.Code, resp, len(store.days))
	}

	store = &fakeOpsStore{failPlayers: 3} // the insert and its two retries
	h := opsHandler(store, config.OpsConfig{})
	w, resp = postBatch(t, h, batch, nil)
	if w.Code != http.StatusServiceUnavailable || resp.Failed != 2 || resp.Accepted != 0 || resp.Duplicates != 0 || len(store.ops) != 2 {
		t.Fatalf("player rows failure: %d %+v", w.Code, resp)
	}
	w, resp = postBatch(t, h, batch, nil)
	if w.Code != http.StatusOK || resp.Accepted != 2 || resp.Duplicates != 2 || len(store.ops) != 2 || len(store.days) != 2 || len(store.players) != 2 {
		t.Fatalf("resend: %d %+v ops %d days %d players %d", w.Code, resp, len(store.ops), len(store.days), len(store.players))
	}
}

// The country a trusted proxy reports wins; without the header configured
// it is ignored (a client could send it).
func TestIngestOps_CountryHeader(t *testing.T) {
	at := opsTestNow.Add(-time.Minute)
	store := &fakeOpsStore{}
	h := opsHandler(store, config.OpsConfig{CountryHeader: "CF-IPCountry"})
	postBatch(t, h, batchOf(opsJSON("o1", "p1", "x", at, "")), map[string]string{"CF-IPCountry": "BR"})
	store2 := &fakeOpsStore{}
	h2 := opsHandler(store2, config.OpsConfig{})
	postBatch(t, h2, batchOf(opsJSON("o1", "p1", "x", at, "")), map[string]string{"CF-IPCountry": "BR"})
	if store.ops[0].Country != "BR" || store2.ops[0].Country != "DE" {
		t.Errorf("with header %q, without %q", store.ops[0].Country, store2.ops[0].Country)
	}
}

// The configured whitelist decides which context keys are kept.
func TestIngestOps_ContextWhitelist(t *testing.T) {
	store := &fakeOpsStore{}
	h := opsHandler(store, config.OpsConfig{ContextKeys: []string{"bossId"}})
	postBatch(t, h, batchOf(opsJSON("o1", "p1", "x", opsTestNow, `"ctx":{"bossId":"b1","runId":"r1"}`)), nil)
	if c := store.ops[0].Context; len(c) != 1 || c["bossId"] != "b1" {
		t.Errorf("context %v", c)
	}
}

// Events played offline yesterday and uploaded today count on yesterday; a
// device clock a day ahead counts at the receive time.
func TestIngestOps_Days(t *testing.T) {
	store := &fakeOpsStore{}
	h := opsHandler(store, config.OpsConfig{})
	yesterday := opsTestNow.Add(-20 * time.Hour)
	postBatch(t, h, batchOf(opsJSON("o1", "p1", "x", yesterday, ""), opsJSON("o2", "p1", "x", opsTestNow.Add(26*time.Hour), "")), nil)
	if len(store.ops) != 2 {
		t.Fatal(len(store.ops))
	}
	if store.ops[0].Day != time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC) || !store.ops[1].EventTime.Equal(opsTestNow) ||
		store.ops[1].Day != time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC) {
		t.Errorf("days %v %v", store.ops[0].Day, store.ops[1].Day)
	}
	if len(store.days) != 2 {
		t.Errorf("player days %+v", store.days)
	}
	// One player row per (player, day): first/last seen of each day.
	if len(store.players) != 2 {
		t.Errorf("players %+v", store.players)
	}
}
