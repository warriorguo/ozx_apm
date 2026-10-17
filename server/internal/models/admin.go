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
	// APM-5: build type and the game context when the crash happened.
	BuildType string            `json:"build_type"`
	Context   map[string]string `json:"context"`
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

// AdminFilter is what every admin endpoint filters on (APM-5).
//
// BuildType is one of the BuildFilter values. The default view is
// BuildFilterRelease, which also includes events without a build type
// (sent by SDKs before APM-5): until the last such SDK is gone they cannot be
// told apart, and hiding them would make all pre-APM-5 data disappear from
// the default view. BuildFilterUnknown shows only those.
//
// Context filters are ANDed; the values of one filter are ORed. An empty
// value matches events that do not have the key.
type AdminFilter struct {
	StartTime  time.Time
	EndTime    time.Time
	AppVersion string
	Platform   string
	BuildType  string
	Context    []ContextFilter
}

type ContextFilter struct {
	Key    string
	Values []string
}

const (
	BuildFilterRelease = "release" // release and unknown (legacy); the default
	BuildFilterDev     = "dev"
	BuildFilterEditor  = "editor"
	BuildFilterUnknown = "unknown" // events without build_type only
	BuildFilterAll     = "all"
)

// MaxContextFilters bounds the context filters of one request.
const MaxContextFilters = 10

// Context endpoints

type ContextKeyStat struct {
	Key   string `json:"key"`
	Count int64  `json:"count"` // events that carry the key
}

type ContextKeysResponse struct {
	Keys []ContextKeyStat `json:"keys"`
}

type ContextValueStat struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

type ContextValuesResponse struct {
	Key    string             `json:"key"`
	Values []ContextValueStat `json:"values"`
}

// ContextDistributionRow is one value of the grouping key. Value is empty for
// events without the key. Count is events (exceptions: the sum of count, i.e.
// occurrences); P50/P95 are of the metric's value (Unit) and 0 for
// exceptions and crashes.
type ContextDistributionRow struct {
	Value    string  `json:"value"`
	Count    int64   `json:"count"`
	Sessions int64   `json:"sessions"`
	P50      float64 `json:"p50"`
	P95      float64 `json:"p95"`
}

type ContextDistribution struct {
	Key       string                   `json:"key"`
	Metric    string                   `json:"metric"`
	Unit      string                   `json:"unit"`
	Total     int64                    `json:"total"`
	Truncated bool                     `json:"truncated"` // more values than rows
	Rows      []ContextDistributionRow `json:"rows"`
}
