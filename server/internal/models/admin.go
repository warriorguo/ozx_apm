package models

import "time"

// Dashboard types

type DashboardSummary struct {
	TimeRange      TimeRange       `json:"time_range"`
	TotalSessions  int64           `json:"total_sessions"`
	TotalEvents    int64           `json:"total_events"`
	CrashCount     int64           `json:"crash_count"`
	CrashRate      float64         `json:"crash_rate"`
	ExceptionCount int64           `json:"exception_count"`
	JankCount      int64           `json:"jank_count"`
	AvgFPS         float64         `json:"avg_fps"`
	AvgStartupMs   float64         `json:"avg_startup_ms"`
	TopVersions    []VersionStats  `json:"top_versions"`
	TopPlatforms   []PlatformStats `json:"top_platforms"`
}

type TimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

type VersionStats struct {
	Version      string  `json:"version"`
	SessionCount int64   `json:"session_count"`
	CrashCount   int64   `json:"crash_count"`
	CrashRate    float64 `json:"crash_rate"`
}

type PlatformStats struct {
	Platform     string  `json:"platform"`
	SessionCount int64   `json:"session_count"`
	AvgFPS       float64 `json:"avg_fps"`
}

type TimeSeriesPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

type TimeSeriesResponse struct {
	Metric string            `json:"metric"`
	Data   []TimeSeriesPoint `json:"data"`
}

type DistributionBucket struct {
	Bucket string  `json:"bucket"`
	Count  int64   `json:"count"`
	Pct    float64 `json:"pct"`
}

type DistributionResponse struct {
	Metric  string               `json:"metric"`
	Buckets []DistributionBucket `json:"buckets"`
	P50     float64              `json:"p50"`
	P90     float64              `json:"p90"`
	P95     float64              `json:"p95"`
	P99     float64              `json:"p99"`
}

// Crash types

type CrashGroup struct {
	Fingerprint      string    `json:"fingerprint"`
	CrashType        string    `json:"crash_type"`
	SampleMessage    string    `json:"sample_message"`
	Count            int64     `json:"count"`
	SessionCount     int64     `json:"session_count"`
	FirstSeen        time.Time `json:"first_seen"`
	LastSeen         time.Time `json:"last_seen"`
	AffectedVersions []string  `json:"affected_versions"`
	TopDevices       []string  `json:"top_devices"`
}

type CrashListResponse struct {
	Crashes    []CrashGroup `json:"crashes"`
	TotalCount int64        `json:"total_count"`
	Page       int          `json:"page"`
	PageSize   int          `json:"page_size"`
}

type CrashDetail struct {
	Fingerprint  string            `json:"fingerprint"`
	CrashType    string            `json:"crash_type"`
	Stack        string            `json:"stack"`
	Count        int64             `json:"count"`
	SessionCount int64             `json:"session_count"`
	FirstSeen    time.Time         `json:"first_seen"`
	LastSeen     time.Time         `json:"last_seen"`
	Occurrences  []CrashOccurrence `json:"occurrences"`
	VersionDist  []VersionDist     `json:"version_distribution"`
	DeviceDist   []DeviceDist      `json:"device_distribution"`
	OSDist       []OSDist          `json:"os_distribution"`
}

type CrashOccurrence struct {
	Timestamp   time.Time `json:"timestamp"`
	AppVersion  string    `json:"app_version"`
	Platform    string    `json:"platform"`
	DeviceModel string    `json:"device_model"`
	OSVersion   string    `json:"os_version"`
	Scene       string    `json:"scene"`
	Breadcrumbs []string  `json:"breadcrumbs"`
}

type VersionDist struct {
	Version string `json:"version"`
	Count   int64  `json:"count"`
}

type DeviceDist struct {
	Device string `json:"device"`
	Count  int64  `json:"count"`
}

type OSDist struct {
	OS    string `json:"os"`
	Count int64  `json:"count"`
}

// Exception types

type ExceptionGroup struct {
	Fingerprint  string    `json:"fingerprint"`
	Message      string    `json:"message"`
	Count        int64     `json:"count"`
	SessionCount int64     `json:"session_count"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
}

type ExceptionListResponse struct {
	Exceptions []ExceptionGroup `json:"exceptions"`
	TotalCount int64            `json:"total_count"`
	Page       int              `json:"page"`
	PageSize   int              `json:"page_size"`
}
