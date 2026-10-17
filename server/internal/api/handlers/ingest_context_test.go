package handlers

import (
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// APM-5: every event type carries build_type, device details and the game
// context; a bad context entry never costs the event or the batch.
func TestIngest_StoresContextOnEveryEventType(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	apm5 := `,"build_type":"release","gpu":"Adreno (TM) 642L","ram_class":"8GB","net_type":"wifi","ctx":{"levelId":"3","roomId":"r-12"}`

	w, resp := postBatch(t, h, batchOf(
		sdkEventJSON("perf_sample", "p", 1, `,"fps":59.5`+apm5),
		sdkEventJSON("jank", "j", 2, `,"duration_ms":80,"recent_events":[]`+apm5),
		sdkEventJSON("exception", "e", 3, `,"fingerprint":"abc","count":1`+apm5),
		sdkEventJSON("crash", "c", 4, `,"fingerprint":"abc","crash_type":"native","breadcrumbs":[]`+apm5),
	), nil)
	if w.Code != http.StatusOK || resp.Accepted != 4 || resp.Rejected != 0 || len(resp.Errors) != 0 {
		t.Fatalf("code %d resp %+v", w.Code, resp)
	}
	for name, ec := range map[string]struct{ bt, gpu, ram, net, level, room string }{
		"perf":      {store.perfSamples[0].BuildType, store.perfSamples[0].GPU, store.perfSamples[0].RAMClass, store.perfSamples[0].NetType, store.perfSamples[0].Context["levelId"], store.perfSamples[0].Context["roomId"]},
		"jank":      {store.janks[0].BuildType, store.janks[0].GPU, store.janks[0].RAMClass, store.janks[0].NetType, store.janks[0].Context["levelId"], store.janks[0].Context["roomId"]},
		"exception": {store.exceptions[0].BuildType, store.exceptions[0].GPU, store.exceptions[0].RAMClass, store.exceptions[0].NetType, store.exceptions[0].Context["levelId"], store.exceptions[0].Context["roomId"]},
		"crash":     {store.crashes[0].BuildType, store.crashes[0].GPU, store.crashes[0].RAMClass, store.crashes[0].NetType, store.crashes[0].Context["levelId"], store.crashes[0].Context["roomId"]},
	} {
		if ec.bt != "release" || ec.gpu != "Adreno (TM) 642L" || ec.ram != "8GB" || ec.net != "wifi" || ec.level != "3" || ec.room != "r-12" {
			t.Errorf("%s: %+v", name, ec)
		}
	}
}

func TestIngest_BadContextKeepsTheEvent(t *testing.T) {
	store := &fakeEventStore{}
	core, logs := observer.New(zapcore.InfoLevel)
	h := newIngestHandler(store, zap.New(core))

	w, resp := postBatch(t, h, batchOf(
		sdkEventJSON("perf_sample", "a", 1, `,"fps":60,"ctx":{"ok":"1","bad key":"x","obj":{"a":1},"long":"`+strings.Repeat("x", 300)+`"}`),
		sdkEventJSON("perf_sample", "b", 2, `,"fps":60,"ctx":"not an object"`),
		sdkEventJSON("perf_sample", "c", 3, `,"fps":60,"ctx":null`),
	), nil)
	if w.Code != http.StatusOK || resp.Accepted != 3 || resp.Rejected != 0 || !resp.Stored {
		t.Fatalf("code %d resp %+v", w.Code, resp)
	}
	if len(resp.Errors) != 1 || !strings.Contains(resp.Errors[0], "3 entries dropped, 1 values truncated") {
		t.Errorf("errors %v", resp.Errors)
	}
	a := store.perfSamples[0].Context
	if len(a) != 2 || a["ok"] != "1" || len(a["long"]) != 256 {
		t.Errorf("context %v", a)
	}
	for _, e := range store.perfSamples[1:] {
		if e.Context == nil || len(e.Context) != 0 {
			t.Errorf("context %v, want empty", e.Context)
		}
	}
	entries := logs.FilterMessage("ingested events").All()
	if len(entries) != 1 || entries[0].ContextMap()["ctx_dropped"] != int64(3) || entries[0].ContextMap()["ctx_truncated"] != int64(1) {
		t.Errorf("log %+v", entries)
	}
}

// Events from SDKs before APM-5 have none of the new fields.
func TestIngest_LegacyEventHasEmptyContext(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	_, resp := postBatch(t, h, batchOf(sdkEventJSON("perf_sample", "", 0, `,"fps":30`)), nil)
	if resp.Accepted != 1 || len(resp.Errors) != 0 {
		t.Fatalf("resp %+v", resp)
	}
	e := store.perfSamples[0]
	if e.BuildType != "" || e.GPU != "" || e.Context == nil || len(e.Context) != 0 {
		t.Errorf("%+v", e.EventContext)
	}
}

// The enricher folds "WindowsEditor" into "Windows"; build_type keeps the editor.
func TestIngest_EditorPlatformBecomesEditorBuildType(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	ev := strings.Replace(sdkEventJSON("perf_sample", "a", 1, `,"fps":30`), `"platform":"Android"`, `"platform":"WindowsEditor"`, 1)
	if _, resp := postBatch(t, h, batchOf(ev), nil); resp.Accepted != 1 {
		t.Fatalf("resp %+v", resp)
	}
	if e := store.perfSamples[0]; e.Platform != "Windows" || e.BuildType != "editor" {
		t.Errorf("platform %q build_type %q", e.Platform, e.BuildType)
	}
}
