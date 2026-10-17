package models

import (
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
)

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

// ExceptionGroup is one fingerprint. The descriptive fields are those of the
// group's most recent occurrence. FingerprintVersion 0 marks a group of rows
// stored before APM-4 (the SDK's fingerprint; ExceptionType, MessageTemplate,
// ThrowSite and CallSite are then empty and Message is all there is).
type ExceptionGroup struct {
	Fingerprint  string    `json:"fingerprint"`
	Message      string    `json:"message"`
	Count        int64     `json:"count"`
	SessionCount int64     `json:"session_count"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	// APM-4
	FingerprintVersion int    `json:"fingerprint_version"`
	ExceptionType      string `json:"exception_type"`
	MessageTemplate    string `json:"message_template"`
	ThrowSite          string `json:"throw_site"`
	CallSite           string `json:"call_site"`
	// APM-3: sessions in which this group was the first error (error_seq 1),
	// and occurrences of it that no code caught (unhandled). 0 for rows of
	// older SDKs.
	FirstErrorSessions int64 `json:"first_error_sessions"`
	UnhandledCount     int64 `json:"unhandled_count"`
}

// ExceptionDetail is one exception group for the detail page (APM-11): the
// summary, a representative sample (the most recent occurrence) with its raw
// text and its parsed stack, the latest occurrences, and distributions.
// Distributions by context key are served by /api/context/distribution
// with metric=exceptions&fingerprint=F.
type ExceptionDetail struct {
	ExceptionGroup
	FirstVersion string                `json:"first_version"`
	LastVersion  string                `json:"last_version"`
	Sample       ExceptionSample       `json:"sample"`
	Occurrences  []ExceptionOccurrence `json:"occurrences"`
	VersionDist  []VersionDist         `json:"version_distribution"`
	DeviceDist   []DeviceDist          `json:"device_distribution"`
}

// ExceptionSample is the representative occurrence: what the SDK sent, and
// the stack parsed from it by the server's current parser
// (internal/fingerprint), outermost exception first.
type ExceptionSample struct {
	Timestamp          time.Time          `json:"timestamp"`
	EventID            string             `json:"event_id"`
	LogType            string             `json:"log_type"`
	Message            string             `json:"message"`
	Stack              string             `json:"stack"`
	EmbeddedException  string             `json:"embedded_exception"`
	ClientFingerprint  string             `json:"client_fingerprint"`
	FingerprintVersion int                `json:"fingerprint_version"`
	InnerExceptions    []string           `json:"inner_exceptions"`
	Parsed             fingerprint.Result `json:"parsed"`
}

// ExceptionOccurrence is one stored event of the group (a first occurrence
// in a de-duplication window, or the repeat count of that window in one game
// context).
type ExceptionOccurrence struct {
	Timestamp   time.Time         `json:"timestamp"`
	EventID     string            `json:"event_id"`
	Count       int64             `json:"count"`
	AppVersion  string            `json:"app_version"`
	BuildType   string            `json:"build_type"`
	Platform    string            `json:"platform"`
	DeviceModel string            `json:"device_model"`
	OSVersion   string            `json:"os_version"`
	Scene       string            `json:"scene"`
	SessionID   string            `json:"session_id"`
	Context     map[string]string `json:"context"`
	Breadcrumbs []string          `json:"breadcrumbs"`
	// APM-3 (0 / "" / false for rows of older SDKs): which error of its
	// session the row's first occurrence was, the milliseconds since the
	// session started, how it was unhandled ("", "domain",
	// "domain_terminating", "task"), and whether Count merges occurrences in
	// different contexts (Context then holds only what they shared).
	ErrorSeq  int64  `json:"error_seq"`
	SessionMs int64  `json:"session_ms"`
	Unhandled string `json:"unhandled"`
	CtxMerged bool   `json:"ctx_merged"`
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
	// FirstErrorOnly (first_error=1, APM-3) keeps only exception rows whose
	// occurrence was the first error of its session; other tables ignore it.
	FirstErrorOnly bool
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
	// Merged (exceptions, APM-3): occurrences whose value of the key is not
	// known because the SDK merged them across contexts; not in Rows.
	Merged int64 `json:"merged"`
}
