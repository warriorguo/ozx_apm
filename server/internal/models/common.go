package models

import "time"

// CommonContext contains fields shared by all events
type CommonContext struct {
	Timestamp    time.Time `json:"timestamp" ch:"timestamp"`
	AppVersion   string    `json:"app_version" ch:"app_version"`
	Build        string    `json:"build,omitempty" ch:"build"`
	UnityVersion string    `json:"unity_version,omitempty" ch:"unity_version"`
	Platform     string    `json:"platform" ch:"platform"`
	OSVersion    string    `json:"os_version" ch:"os_version"`
	DeviceModel  string    `json:"device_model" ch:"device_model"`
	CPU          string    `json:"cpu,omitempty" ch:"cpu"`
	GPU          string    `json:"gpu,omitempty" ch:"gpu"`
	RAMClass     string    `json:"ram_class,omitempty" ch:"ram_class"`
	SessionID    string    `json:"session_id" ch:"session_id"`
	DeviceID     string    `json:"device_id" ch:"device_id"`
	UserID       string    `json:"user_id,omitempty" ch:"user_id"`
	Scene        string    `json:"scene,omitempty" ch:"scene"`
	LevelID      string    `json:"level_id,omitempty" ch:"level_id"`
	NetType      string    `json:"net_type,omitempty" ch:"net_type"`
	Country      string    `json:"country,omitempty" ch:"country"`
}

// EventType represents the type of APM event
type EventType string

const (
	EventTypePerfSample EventType = "perf_sample"
	EventTypeJank       EventType = "jank"
	EventTypeStartup    EventType = "startup"
	EventTypeSceneLoad  EventType = "scene_load"
	EventTypeAssetLoad  EventType = "asset_load"
	EventTypeHTTP       EventType = "http"
	EventTypeException  EventType = "exception"
	EventTypeCrash      EventType = "crash"
)
