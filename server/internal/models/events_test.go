package models

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPerfSample_JSON(t *testing.T) {
	sample := PerfSample{
		Timestamp:    time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		AppVersion:   "1.0.0",
		Platform:     "Android",
		DeviceModel:  "Pixel 6",
		OSVersion:    "Android 13",
		SessionID:    "session-123",
		DeviceID:     "device-456",
		Scene:        "GamePlay",
		FPS:          60.0,
		FrameTimeMs:  16.67,
		MainThreadMs: 12.5,
		GCAllocKB:    128.0,
		MemMB:        512.0,
	}

	data, err := json.Marshal(sample)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded PerfSample
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.AppVersion != sample.AppVersion {
		t.Errorf("expected app_version=%s, got %s", sample.AppVersion, decoded.AppVersion)
	}
	if decoded.FPS != sample.FPS {
		t.Errorf("expected fps=%f, got %f", sample.FPS, decoded.FPS)
	}
	if decoded.Scene != sample.Scene {
		t.Errorf("expected scene=%s, got %s", sample.Scene, decoded.Scene)
	}
}

func TestJank_JSON(t *testing.T) {
	jank := Jank{
		Timestamp:       time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		AppVersion:      "1.0.0",
		Platform:        "Android",
		DeviceModel:     "Pixel 6",
		OSVersion:       "Android 13",
		SessionID:       "session-123",
		DeviceID:        "device-456",
		Scene:           "GamePlay",
		DurationMs:      100.0,
		MaxFrameMs:      80.0,
		RecentGCCount:   3,
		RecentGCAllocKB: 1024.0,
		RecentEvents:    []string{"asset_load", "scene_change", "network_request"},
	}

	data, err := json.Marshal(jank)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded Jank
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.DurationMs != jank.DurationMs {
		t.Errorf("expected duration_ms=%f, got %f", jank.DurationMs, decoded.DurationMs)
	}
	if len(decoded.RecentEvents) != len(jank.RecentEvents) {
		t.Errorf("expected %d recent_events, got %d", len(jank.RecentEvents), len(decoded.RecentEvents))
	}
}

func TestStartup_JSON(t *testing.T) {
	startup := Startup{
		Timestamp:   time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		AppVersion:  "1.0.0",
		Platform:    "iOS",
		DeviceModel: "iPhone 14",
		OSVersion:   "iOS 17.0",
		SessionID:   "session-123",
		DeviceID:    "device-456",
		Phase1Ms:    500.0,
		Phase2Ms:    1000.0,
		TTIMs:       2000.0,
	}

	data, err := json.Marshal(startup)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded Startup
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Phase1Ms != startup.Phase1Ms {
		t.Errorf("expected phase1_ms=%f, got %f", startup.Phase1Ms, decoded.Phase1Ms)
	}
	if decoded.TTIMs != startup.TTIMs {
		t.Errorf("expected tti_ms=%f, got %f", startup.TTIMs, decoded.TTIMs)
	}
}

func TestSceneLoad_JSON(t *testing.T) {
	sceneLoad := SceneLoad{
		Timestamp:   time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		AppVersion:  "1.0.0",
		Platform:    "Android",
		DeviceModel: "Pixel 6",
		SessionID:   "session-123",
		DeviceID:    "device-456",
		SceneName:   "Level_01",
		LoadMs:      1500.0,
		ActivateMs:  200.0,
	}

	data, err := json.Marshal(sceneLoad)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded SceneLoad
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.SceneName != sceneLoad.SceneName {
		t.Errorf("expected scene_name=%s, got %s", sceneLoad.SceneName, decoded.SceneName)
	}
	if decoded.LoadMs != sceneLoad.LoadMs {
		t.Errorf("expected load_ms=%f, got %f", sceneLoad.LoadMs, decoded.LoadMs)
	}
}

func TestException_JSON(t *testing.T) {
	exc := Exception{
		Timestamp:   time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		AppVersion:  "1.0.0",
		Platform:    "Android",
		DeviceModel: "Pixel 6",
		OSVersion:   "Android 13",
		SessionID:   "session-123",
		DeviceID:    "device-456",
		Scene:       "GamePlay",
		Fingerprint: "exc-fingerprint-123",
		Message:     "NullReferenceException: Object reference not set to an instance",
		Stack:       "at MyGame.Player.Update()\nat UnityEngine.MonoBehaviour.InternalUpdate()",
		Count:       5,
	}

	data, err := json.Marshal(exc)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded Exception
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Fingerprint != exc.Fingerprint {
		t.Errorf("expected fingerprint=%s, got %s", exc.Fingerprint, decoded.Fingerprint)
	}
	if decoded.Message != exc.Message {
		t.Errorf("expected message=%s, got %s", exc.Message, decoded.Message)
	}
	if decoded.Count != exc.Count {
		t.Errorf("expected count=%d, got %d", exc.Count, decoded.Count)
	}
}

func TestCrash_JSON(t *testing.T) {
	crash := Crash{
		Timestamp:   time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		AppVersion:  "1.0.0",
		Platform:    "Android",
		DeviceModel: "Pixel 6",
		OSVersion:   "Android 13",
		SessionID:   "session-123",
		DeviceID:    "device-456",
		Scene:       "GamePlay",
		CrashType:   "SIGSEGV",
		Fingerprint: "crash-fingerprint-456",
		Stack:       "native crash at libunity.so+0x12345",
		Breadcrumbs: []string{"scene_load:MainMenu", "button_click:play", "scene_load:GamePlay"},
	}

	data, err := json.Marshal(crash)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded Crash
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.CrashType != crash.CrashType {
		t.Errorf("expected crash_type=%s, got %s", crash.CrashType, decoded.CrashType)
	}
	if decoded.Fingerprint != crash.Fingerprint {
		t.Errorf("expected fingerprint=%s, got %s", crash.Fingerprint, decoded.Fingerprint)
	}
	if len(decoded.Breadcrumbs) != len(crash.Breadcrumbs) {
		t.Errorf("expected %d breadcrumbs, got %d", len(crash.Breadcrumbs), len(decoded.Breadcrumbs))
	}
}

func TestEventBatch_JSON(t *testing.T) {
	batch := EventBatch{
		AppKey: "test-key",
		Events: []RawEvent{
			{
				Type:      EventType("perf_sample"),
				Timestamp: 1705315800000,
			},
			{
				Type:      EventType("jank"),
				Timestamp: 1705315800000,
			},
		},
	}

	data, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded EventBatch
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(decoded.Events) != 2 {
		t.Errorf("expected 2 events, got %d", len(decoded.Events))
	}
	if decoded.Events[0].Type != EventType("perf_sample") {
		t.Errorf("expected type=perf_sample, got %s", decoded.Events[0].Type)
	}
}

func TestRawEvent_JSON(t *testing.T) {
	event := RawEvent{
		Type:      EventType("perf_sample"),
		Timestamp: 1705315800000,
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded RawEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Type != EventType("perf_sample") {
		t.Errorf("expected type=perf_sample, got %s", decoded.Type)
	}
	if decoded.Timestamp != 1705315800000 {
		t.Errorf("expected timestamp=1705315800000, got %d", decoded.Timestamp)
	}
}

func TestBaseEvent_JSON(t *testing.T) {
	event := BaseEvent{
		Type: EventType("perf_sample"),
		Data: json.RawMessage(`{"fps": 60, "frame_time_ms": 16.67}`),
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var decoded BaseEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if decoded.Type != EventType("perf_sample") {
		t.Errorf("expected type=perf_sample, got %s", decoded.Type)
	}
}
