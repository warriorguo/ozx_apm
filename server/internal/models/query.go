package models

import "time"

// QueryFilter contains common filter parameters for queries
type QueryFilter struct {
	AppVersion  string    `json:"app_version,omitempty"`
	Platform    string    `json:"platform,omitempty"`
	DeviceModel string    `json:"device_model,omitempty"`
	OSVersion   string    `json:"os_version,omitempty"`
	Scene       string    `json:"scene,omitempty"`
	Country     string    `json:"country,omitempty"`
	NetType     string    `json:"net_type,omitempty"`
	StartTime   time.Time `json:"start_time"`
	EndTime     time.Time `json:"end_time"`
	Limit       int       `json:"limit,omitempty"`
	Offset      int       `json:"offset,omitempty"`
}

// FPSMetrics represents FPS distribution metrics
type FPSMetrics struct {
	AppVersion string  `json:"app_version"`
	Platform   string  `json:"platform"`
	Scene      string  `json:"scene,omitempty"`
	Count      uint64  `json:"count"`
	AvgFPS     float64 `json:"avg_fps"`
	P50FPS     float64 `json:"p50_fps"`
	P90FPS     float64 `json:"p90_fps"`
	P95FPS     float64 `json:"p95_fps"`
	P99FPS     float64 `json:"p99_fps"`
}

// StartupMetrics represents startup time percentiles
type StartupMetrics struct {
	AppVersion string  `json:"app_version"`
	Platform   string  `json:"platform"`
	Count      uint64  `json:"count"`
	AvgPhase1  float64 `json:"avg_phase1_ms"`
	AvgPhase2  float64 `json:"avg_phase2_ms"`
	AvgTTI     float64 `json:"avg_tti_ms"`
	P50Total   float64 `json:"p50_total_ms"`
	P95Total   float64 `json:"p95_total_ms"`
	P99Total   float64 `json:"p99_total_ms"`
}

// JankMetrics represents jank statistics
type JankMetrics struct {
	AppVersion   string  `json:"app_version"`
	Platform     string  `json:"platform"`
	Scene        string  `json:"scene"`
	Count        uint64  `json:"count"`
	AvgDuration  float64 `json:"avg_duration_ms"`
	MaxDuration  float64 `json:"max_duration_ms"`
	SessionCount uint64  `json:"session_count"`
}

// ExceptionSummary represents aggregated exception data
type ExceptionSummary struct {
	Fingerprint   string    `json:"fingerprint"`
	Message       string    `json:"message"`
	AppVersion    string    `json:"app_version"`
	Platform      string    `json:"platform"`
	Count         uint64    `json:"count"`
	SessionCount  uint64    `json:"session_count"`
	FirstSeen     time.Time `json:"first_seen"`
	LastSeen      time.Time `json:"last_seen"`
	SampleStack   string    `json:"sample_stack"`
}

// CrashSummary represents aggregated crash data
type CrashSummary struct {
	Fingerprint  string    `json:"fingerprint"`
	CrashType    string    `json:"crash_type"`
	AppVersion   string    `json:"app_version"`
	Platform     string    `json:"platform"`
	Count        uint64    `json:"count"`
	SessionCount uint64    `json:"session_count"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	SampleStack  string    `json:"sample_stack"`
}

// MetricsResponse is a generic response wrapper
type MetricsResponse struct {
	Data       interface{} `json:"data"`
	TotalCount int64       `json:"total_count,omitempty"`
	Page       int         `json:"page,omitempty"`
	PageSize   int         `json:"page_size,omitempty"`
}
