package handlers

import (
	"encoding/json"
	"testing"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
)

func busError(eventID string, seq int, embedded string) string {
	fields := map[string]interface{}{
		"fingerprint":        "client-" + eventID,
		"log_type":           "error",
		"message":            "[GameEventBus] Error handling event RoomEnteredEvent: " + embedded,
		"stack":              "Ozx.Core.GameEventBus:Publish(RoomEnteredEvent)\n",
		"embedded_exception": embedded,
		"breadcrumbs":        []string{"[10:00:00.000] Scene: Main"},
		"count":              1,
		// Computed by the server; a client cannot set them.
		"client_fingerprint":  "forged",
		"fingerprint_version": 99,
		"exception_type":      "Forged",
		"throw_site":          "Forged",
	}
	b, _ := json.Marshal(fields)
	return sdkEventJSON("exception", eventID, seq, ","+string(b[1:len(b)-1]))
}

func handlerException(typ, method, msg string) string {
	return typ + ": " + msg + "\n" +
		"  at Ozx.Rooms." + method + " (System.Int32 roomId) [0x00000] in <00000000000000000000000000000000>:0 \n" +
		"  at Ozx.Core.GameEventBus.Publish[T] (T evt) [0x00000] in <00000000000000000000000000000000>:0 \n"
}

// APM-1 verification 1 and 2 at the ingest API: two handlers -> two
// fingerprints; the same exception with another roomId -> the same one.
func TestIngest_ExceptionFingerprintIsComputedByTheServer(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	_, resp := postBatch(t, h, batchOf(
		busError("e1", 1, handlerException("System.InvalidOperationException", "RoomLoader.LoadTemplate", "Room template not found for roomId=12")),
		busError("e2", 2, handlerException("System.InvalidOperationException", "RoomLoader.LoadTemplate", "Room template not found for roomId=7")),
		busError("e3", 3, handlerException("System.NullReferenceException", "EnemySpawner.SpawnWave", "Object reference not set to an instance of an object.")),
		sdkEventJSON("exception", "", 0, `,"fingerprint":"0123456789abcdef","message":"Asset not found: anim/x","stack":"Ozx.Pool:Load()\n","count":3`),
	), nil)
	if resp.Accepted != 4 || len(store.exceptions) != 4 {
		t.Fatalf("response %+v, stored %d", resp, len(store.exceptions))
	}
	room12, room7, spawner, legacy := store.exceptions[0], store.exceptions[1], store.exceptions[2], store.exceptions[3]
	if room12.Fingerprint != room7.Fingerprint {
		t.Errorf("roomIds split the group: %s %s", room12.Fingerprint, room7.Fingerprint)
	}
	if room12.Fingerprint == spawner.Fingerprint {
		t.Errorf("two handlers in one group: %s", room12.Fingerprint)
	}
	if room12.ExceptionType != "System.InvalidOperationException" || room12.ThrowSite != "Ozx.Rooms.RoomLoader.LoadTemplate" ||
		room12.CallSite != "Ozx.Core.GameEventBus.Publish" || room12.MessageTemplate != "Room template not found for roomId=<v>" {
		t.Errorf("parsed fields: %+v", room12.ExceptionDetails)
	}
	if spawner.ExceptionType != "System.NullReferenceException" || spawner.ThrowSite != "Ozx.Rooms.EnemySpawner.SpawnWave" {
		t.Errorf("parsed fields: %+v", spawner.ExceptionDetails)
	}
	if room12.ClientFingerprint != "client-e1" || room12.FingerprintVersion != fingerprint.Version || room12.LogType != "error" {
		t.Errorf("client fingerprint %q version %d", room12.ClientFingerprint, room12.FingerprintVersion)
	}
	if len(room12.Breadcrumbs) != 1 || room12.Breadcrumbs[0] != "[10:00:00.000] Scene: Main" {
		t.Errorf("breadcrumbs %q", room12.Breadcrumbs)
	}
	// An old SDK's event: server fingerprint, the old one kept.
	if legacy.ClientFingerprint != "0123456789abcdef" || legacy.Fingerprint == legacy.ClientFingerprint ||
		legacy.CallSite != "Ozx.Pool.Load" || legacy.Count != 3 || legacy.Breadcrumbs == nil {
		t.Errorf("legacy: %+v", legacy)
	}
}

// The client fingerprint is still required (old validator behaviour).
func TestIngest_ExceptionWithoutClientFingerprintIsRejected(t *testing.T) {
	store := &fakeEventStore{}
	h := newIngestHandler(store, zap.NewNop())
	_, resp := postBatch(t, h, batchOf(sdkEventJSON("exception", "e1", 1, `,"message":"x","count":1`)), nil)
	if resp.Rejected != 1 || len(store.exceptions) != 0 {
		t.Errorf("%+v", resp)
	}
}
