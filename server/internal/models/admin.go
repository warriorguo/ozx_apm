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
	Fingerprint string `json:"fingerprint"`
	CrashType   string `json:"crash_type"`
	// SampleMessage is the summary of the group's latest crash (APM-7), with
	// the top in-app frame symbolicated now when it was stored by address.
	SampleMessage    string    `json:"sample_message"`
	Count            int64     `json:"count"`
	SessionCount     int64     `json:"session_count"`
	FirstSeen        time.Time `json:"first_seen"`
	LastSeen         time.Time `json:"last_seen"`
	AffectedVersions []string  `json:"affected_versions"`
	TopDevices       []string  `json:"top_devices"`
	// APM-8: the issue's state (status, resolved_in_version, ...).
	IssueStatusInfo
	// APM-7 (empty for rows of older SDKs).
	ExitReason   string `json:"exit_reason"`
	Signal       string `json:"signal"`
	CrashSite    string `json:"crash_site"`
	TopFrame     string `json:"top_frame"`    // "Method (File.cs:12)" of the latest crash's first in-app frame
	Foreground   int64  `json:"foreground"`   // crashes in the foreground
	Symbolicated bool   `json:"symbolicated"` // the top frame was named from uploaded symbols

	// The latest crash's id, stack, version and ABI, for the top frame.
	SampleEventID string `json:"-"`
	SampleStack   string `json:"-"`
	SampleVersion string `json:"-"`
	SampleABI     string `json:"-"`
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
	// APM-8: the issue's state.
	IssueStatusInfo
	// APM-7: the latest crash with its stack symbolicated now (nil when the
	// group has none in the filter), and the symbols of its libraries.
	Sample             *CrashOccurrence    `json:"sample"`
	FingerprintVersion uint8               `json:"fingerprint_version"`
	Symbols            []CrashSymbolStatus `json:"symbols"`
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
	// APM-7: the session and device (links to the session time line), what
	// Android said about the exit, the signal, and the details.
	EventID         string            `json:"event_id"`
	SessionID       string            `json:"session_id"`
	DeviceID        string            `json:"device_id"`
	CrashType       string            `json:"crash_type"`
	Summary         string            `json:"summary"`
	ExitReason      string            `json:"exit_reason"`
	ExitStatus      int32             `json:"exit_status"`
	ExitDescription string            `json:"exit_description"`
	Importance      uint16            `json:"importance"`
	PssKB           uint64            `json:"pss_kb"`
	RssKB           uint64            `json:"rss_kb"`
	Signal          string            `json:"signal"`
	SignalCode      string            `json:"signal_code"`
	FaultAddr       string            `json:"fault_addr"`
	AbortMessage    string            `json:"abort_message"`
	ABI             string            `json:"abi"`
	SessionMs       uint64            `json:"session_ms"`
	Foreground      bool              `json:"foreground"`
	Details         map[string]string `json:"details"`
	// Sample only: the raw stack and its threads, symbolicated.
	Stack   string            `json:"stack,omitempty"`
	Threads []CrashThreadView `json:"threads,omitempty"`
}

// CrashThreadView is one thread of a crash stack as the admin shows it.
type CrashThreadView struct {
	Name    string           `json:"name"`
	Tid     int              `json:"tid"`
	Crashed bool             `json:"crashed"`
	Frames  []CrashFrameView `json:"frames"`
	Omitted int              `json:"omitted"`
}

// CrashFrameView is one frame: Function is the C# method (or C++ function)
// from the uploaded symbols when Symbolicated, else what the device reported
// (a system library's symbol, a Java method), else empty (an address only).
type CrashFrameView struct {
	Index        int    `json:"index"`
	Native       bool   `json:"native"`
	PC           string `json:"pc,omitempty"` // "0x5417bc"
	Module       string `json:"module,omitempty"`
	BuildID      string `json:"build_id,omitempty"`
	Function     string `json:"function,omitempty"`
	Offset       uint64 `json:"offset,omitempty"`
	File         string `json:"file,omitempty"`
	Line         int    `json:"line,omitempty"`
	InApp        bool   `json:"in_app"`
	Symbolicated bool   `json:"symbolicated"`
	SymbolsBy    string `json:"symbols_by,omitempty"` // "build_id" or "app_version"
	Raw          string `json:"raw"`
}

// CrashSymbolStatus says whether symbols are uploaded for a library of the sample.
type CrashSymbolStatus struct {
	Module     string    `json:"module"`
	BuildID    string    `json:"build_id"`
	Found      bool      `json:"found"`
	UploadID   string    `json:"upload_id,omitempty"`
	UploadedAt time.Time `json:"uploaded_at,omitempty"`
	LineMap    bool      `json:"line_map"`
	MethodMap  bool      `json:"method_map"`
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
	// APM-11: sessions whose rows of this group carry an error sequence
	// (SDK with APM-3). FirstErrorSessions / SessionsWithSeq is the share of
	// sessions in which this group was the first error; rows of older SDKs
	// are in neither.
	SessionsWithSeq int64 `json:"sessions_with_seq"`
	// APM-8: the issue's state (status, resolved_in_version, ...).
	IssueStatusInfo
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
	// APM-11: occurrences (sum of count) per time bucket over the filter's range.
	Trend ExceptionTrend `json:"trend"`
}

// ExceptionTrend is a group's occurrences per bucket of IntervalSeconds;
// buckets without occurrences are left out.
type ExceptionTrend struct {
	IntervalSeconds int                   `json:"interval_seconds"`
	Points          []ExceptionTrendPoint `json:"points"`
}

type ExceptionTrendPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Count     int64     `json:"count"` // occurrences (sum of count)
	Sessions  int64     `json:"sessions"`
}

// ExceptionOccurrencesResponse is one page of a group's occurrences, most
// recent first (/api/exceptions/occurrences, APM-11).
type ExceptionOccurrencesResponse struct {
	Fingerprint string                `json:"fingerprint"`
	Occurrences []ExceptionOccurrence `json:"occurrences"`
	TotalCount  int64                 `json:"total_count"` // rows
	Page        int                   `json:"page"`
	PageSize    int                   `json:"page_size"`
}

// ExceptionDistribution is a group's occurrences (sum of count) per value of
// a column (app_version, build_type, device_model, platform, os_version) or of
// a context key (GroupBy "ctx:<key>"; Value "" = key not set). Merged:
// occurrences whose value of the key is unknown because the SDK merged them
// across contexts (APM-3); not in Rows, Total includes them.
type ExceptionDistribution struct {
	Fingerprint string                     `json:"fingerprint"`
	GroupBy     string                     `json:"group_by"`
	Total       int64                      `json:"total"`
	Merged      int64                      `json:"merged"`
	Truncated   bool                       `json:"truncated"`
	Rows        []ExceptionDistributionRow `json:"rows"`
}

type ExceptionDistributionRow struct {
	Value    string `json:"value"`
	Count    int64  `json:"count"`
	Sessions int64  `json:"sessions"`
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
	DeviceID    string            `json:"device_id"` // APM-11: the anonymous device id (to list its sessions)
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
	// IssueStatus (status=resolved|unresolved on the exception list, APM-8)
	// keeps only exception groups in that state; "" all. Other tables ignore it.
	IssueStatus string
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
// occurrences); P05/P50/P90/P95/P99 are of the metric's value (Unit) and 0
// for exceptions, crashes and jank_rate (P05, P90 and P99 since APM-9: for
// fps the low percentiles are the slow moments). jank_rate (APM-9): Count is
// janks, Minutes the play time of the value (the perf samples' windows) and
// Rate janks per minute of play: both set on every jank_rate row (0 is 0),
// except Rate when the value has no play time (minutes 0: no rate). Absent
// for the other metrics.
type ContextDistributionRow struct {
	Value    string   `json:"value"`
	Count    int64    `json:"count"`
	Sessions int64    `json:"sessions"`
	P05      float64  `json:"p05"`
	P50      float64  `json:"p50"`
	P90      float64  `json:"p90"`
	P95      float64  `json:"p95"`
	P99      float64  `json:"p99"`
	Minutes  *float64 `json:"minutes,omitempty"`
	Rate     *float64 `json:"rate,omitempty"`
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
	// Bucket (APM-9): "count" when numeric values were grouped into the
	// ranges of CountBucketLabels (rows then in range order).
	Bucket string `json:"bucket,omitempty"`
}

// CountBucketLabels are the ranges a numeric context value (e.g. an alive
// enemy count) is grouped into with bucket=count (APM-9). The SDK's
// ApmClient.SetContextCount writes these labels itself (GameContext.cs,
// CountBucket); both must change together. A value that is not a number (a
// label already, or text) is kept as it is.
var CountBucketLabels = []string{"0", "1-5", "6-10", "11-20", "21-30", "31-50", "51-100", "101-200", "201+"}

// CountBucketUpper are the exclusive upper bounds of CountBucketLabels but the last.
var CountBucketUpper = []float64{1, 6, 11, 21, 31, 51, 101, 201}

// CountBucketRank is the position of a label in CountBucketLabels, or
// len(CountBucketLabels) for any other value (sorted after the ranges).
func CountBucketRank(label string) int {
	for i, l := range CountBucketLabels {
		if l == label {
			return i
		}
	}
	return len(CountBucketLabels)
}
