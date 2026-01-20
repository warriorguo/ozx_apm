package models

import (
	"encoding/json"
	"time"
)

// BaseEvent is the wrapper for all events sent by SDK
type BaseEvent struct {
	Type EventType       `json:"type"`
	Data json.RawMessage `json:"data"`
}

// EventBatch represents a batch of events from the SDK
type EventBatch struct {
	AppKey string     `json:"app_key,omitempty"`
	Events []RawEvent `json:"events"`
}

// RawEvent is used for initial parsing before type-specific deserialization
type RawEvent struct {
	Type      EventType              `json:"type"`
	Timestamp int64                  `json:"timestamp"` // Unix milliseconds
	Data      map[string]interface{} `json:"-"`         // Additional fields merged at top level
}

// PerfSample represents a performance sample event
type PerfSample struct {
	Timestamp    time.Time `json:"timestamp" ch:"timestamp"`
	AppVersion   string    `json:"app_version" ch:"app_version"`
	Platform     string    `json:"platform" ch:"platform"`
	DeviceModel  string    `json:"device_model" ch:"device_model"`
	OSVersion    string    `json:"os_version" ch:"os_version"`
	SessionID    string    `json:"session_id" ch:"session_id"`
	DeviceID     string    `json:"device_id" ch:"device_id"`
	Scene        string    `json:"scene" ch:"scene"`
	FPS          float32   `json:"fps" ch:"fps"`
	FrameTimeMs  float32   `json:"frame_time_ms" ch:"frame_time_ms"`
	MainThreadMs float32   `json:"main_thread_ms" ch:"main_thread_ms"`
	GCAllocKB    float32   `json:"gc_alloc_kb" ch:"gc_alloc_kb"`
	MemMB        float32   `json:"mem_mb" ch:"mem_mb"`
}

// Jank represents a jank event
type Jank struct {
	Timestamp       time.Time `json:"timestamp" ch:"timestamp"`
	AppVersion      string    `json:"app_version" ch:"app_version"`
	Platform        string    `json:"platform" ch:"platform"`
	DeviceModel     string    `json:"device_model" ch:"device_model"`
	OSVersion       string    `json:"os_version" ch:"os_version"`
	SessionID       string    `json:"session_id" ch:"session_id"`
	DeviceID        string    `json:"device_id" ch:"device_id"`
	Scene           string    `json:"scene" ch:"scene"`
	DurationMs      float32   `json:"duration_ms" ch:"duration_ms"`
	MaxFrameMs      float32   `json:"max_frame_ms" ch:"max_frame_ms"`
	RecentGCCount   uint32    `json:"recent_gc_count" ch:"recent_gc_count"`
	RecentGCAllocKB float32   `json:"recent_gc_alloc_kb" ch:"recent_gc_alloc_kb"`
	RecentEvents    []string  `json:"recent_events" ch:"recent_events"`
}

// Startup represents a startup timing event
type Startup struct {
	Timestamp   time.Time `json:"timestamp" ch:"timestamp"`
	AppVersion  string    `json:"app_version" ch:"app_version"`
	Platform    string    `json:"platform" ch:"platform"`
	DeviceModel string    `json:"device_model" ch:"device_model"`
	OSVersion   string    `json:"os_version" ch:"os_version"`
	SessionID   string    `json:"session_id" ch:"session_id"`
	DeviceID    string    `json:"device_id" ch:"device_id"`
	Phase1Ms    float32   `json:"phase1_ms" ch:"phase1_ms"` // app -> unity
	Phase2Ms    float32   `json:"phase2_ms" ch:"phase2_ms"` // unity -> first frame
	TTIMs       float32   `json:"tti_ms" ch:"tti_ms"`       // first frame -> interactive
}

// SceneLoad represents a scene load timing event
type SceneLoad struct {
	Timestamp   time.Time `json:"timestamp" ch:"timestamp"`
	AppVersion  string    `json:"app_version" ch:"app_version"`
	Platform    string    `json:"platform" ch:"platform"`
	DeviceModel string    `json:"device_model" ch:"device_model"`
	SessionID   string    `json:"session_id" ch:"session_id"`
	DeviceID    string    `json:"device_id" ch:"device_id"`
	SceneName   string    `json:"scene_name" ch:"scene_name"`
	LoadMs      float32   `json:"load_ms" ch:"load_ms"`
	ActivateMs  float32   `json:"activate_ms" ch:"activate_ms"`
}

// Exception represents a non-fatal exception event
type Exception struct {
	Timestamp   time.Time `json:"timestamp" ch:"timestamp"`
	AppVersion  string    `json:"app_version" ch:"app_version"`
	Platform    string    `json:"platform" ch:"platform"`
	DeviceModel string    `json:"device_model" ch:"device_model"`
	OSVersion   string    `json:"os_version" ch:"os_version"`
	SessionID   string    `json:"session_id" ch:"session_id"`
	DeviceID    string    `json:"device_id" ch:"device_id"`
	Scene       string    `json:"scene" ch:"scene"`
	Fingerprint string    `json:"fingerprint" ch:"fingerprint"`
	Message     string    `json:"message" ch:"message"`
	Stack       string    `json:"stack" ch:"stack"`
	Count       uint32    `json:"count" ch:"count"`
}

// Crash represents a fatal crash event
type Crash struct {
	Timestamp   time.Time `json:"timestamp" ch:"timestamp"`
	AppVersion  string    `json:"app_version" ch:"app_version"`
	Platform    string    `json:"platform" ch:"platform"`
	DeviceModel string    `json:"device_model" ch:"device_model"`
	OSVersion   string    `json:"os_version" ch:"os_version"`
	SessionID   string    `json:"session_id" ch:"session_id"`
	DeviceID    string    `json:"device_id" ch:"device_id"`
	Scene       string    `json:"scene" ch:"scene"`
	CrashType   string    `json:"crash_type" ch:"crash_type"`
	Fingerprint string    `json:"fingerprint" ch:"fingerprint"`
	Stack       string    `json:"stack" ch:"stack"`
	Breadcrumbs []string  `json:"breadcrumbs" ch:"breadcrumbs"`
}
