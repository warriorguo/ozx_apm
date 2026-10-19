package models

import "time"

// Session time line (APM-15): everything stored for one session_id, merged
// by time. Each item is one stored row (or, for breadcrumbs and context
// changes, something derived from stored rows); Type says which, and only the
// fields of that type are set.

// Timeline item types.
const (
	TimelineCustomEvent  = "custom_event"  // apm_custom_events (aggregated: a count of calls beyond the SDK's budget)
	TimelineCustomMetric = "custom_metric" // apm_custom_metrics: one aggregate of a metric series over a window
	TimelineException    = "exception"     // apm_exceptions: a first occurrence or a repeat count
	TimelineCrash        = "crash"
	TimelineJank         = "jank"
	TimelineSceneLoad    = "scene_load"
	TimelineStartup      = "startup"
	TimelineRun          = "run"        // apm_runs (APM-9): a game run that ended, at its end
	TimelineContext      = "context"    // a change of the game context, derived from consecutive rows
	TimelineBreadcrumb   = "breadcrumb" // a line of the breadcrumbs exceptions, crashes and janks carry
)

// TimelineTypes lists the item types (also the keys of SessionTimeline.Counts).
var TimelineTypes = []string{TimelineCustomEvent, TimelineCustomMetric, TimelineException, TimelineCrash, TimelineJank,
	TimelineSceneLoad, TimelineStartup, TimelineRun, TimelineContext, TimelineBreadcrumb}

// Breadcrumb kinds, from the line's prefix (SDK Timeline.cs).
const (
	CrumbEvent     = "event"     // "Event: room_enter roomId=12" (ApmClient.TrackEvent)
	CrumbScene     = "scene"     // "Scene: Main"
	CrumbWarning   = "warning"   // "Warning: ..." (a recent log line)
	CrumbError     = "error"     // "Error: ..."
	CrumbAssert    = "assert"    // "Assert: ..."
	CrumbException = "exception" // "Exception: <first line>"
	CrumbText      = "text"      // ApmClient.RecordBreadcrumb, or a line of an older SDK
)

// TimelineItem is one entry of the time line.
type TimelineItem struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`
	EventID string    `json:"event_id,omitempty"`
	// Seq is the SDK's per-install sequence number of the row: it orders rows
	// with the same millisecond in the order the SDK stored them.
	Seq   uint64 `json:"seq,omitempty"`
	Scene string `json:"scene,omitempty"`

	// custom_event, custom_metric
	Name       string            `json:"name,omitempty"`
	Aggregated bool              `json:"aggregated,omitempty"` // custom_event: Count calls beyond the budget, counted from Time to the end of the SDK's window
	Props      map[string]string `json:"props,omitempty"`
	Unit       string            `json:"unit,omitempty"`
	Sum        float64           `json:"sum,omitempty"`
	Min        float64           `json:"min,omitempty"`
	Max        float64           `json:"max,omitempty"`
	WindowMs   int64             `json:"window_ms,omitempty"`

	// custom_event, custom_metric, exception: occurrences, calls or samples.
	Count int64 `json:"count,omitempty"`
	// CtxMerged: Count merges rows in different game contexts; Context
	// holds only what they shared.
	CtxMerged bool              `json:"ctx_merged,omitempty"`
	Context   map[string]string `json:"context,omitempty"`

	// exception, crash
	Fingerprint   string `json:"fingerprint,omitempty"`
	Message       string `json:"message,omitempty"` // exception: the log message; crash: its summary (APM-7), else the start of the stack
	ExceptionType string `json:"exception_type,omitempty"`
	LogType       string `json:"log_type,omitempty"`
	ThrowSite     string `json:"throw_site,omitempty"`
	CallSite      string `json:"call_site,omitempty"`
	ErrorSeq      int64  `json:"error_seq,omitempty"`
	SessionMs     int64  `json:"session_ms,omitempty"`
	Unhandled     string `json:"unhandled,omitempty"`
	CrashType     string `json:"crash_type,omitempty"`

	// jank
	DurationMs      float64  `json:"duration_ms,omitempty"`
	MaxFrameMs      float64  `json:"max_frame_ms,omitempty"`
	RecentGCCount   int64    `json:"recent_gc_count,omitempty"`
	RecentGCAllocKB float64  `json:"recent_gc_alloc_kb,omitempty"`
	RecentEvents    []string `json:"recent_events,omitempty"`

	// scene_load
	SceneName  string  `json:"scene_name,omitempty"`
	LoadMs     float64 `json:"load_ms,omitempty"`
	ActivateMs float64 `json:"activate_ms,omitempty"`

	// startup
	Phase1Ms float64 `json:"phase1_ms,omitempty"`
	Phase2Ms float64 `json:"phase2_ms,omitempty"`
	TTIMs    float64 `json:"tti_ms,omitempty"`

	// run (APM-9; DurationMs: its length; CrashType when it ended in a crash)
	RunID       string  `json:"run_id,omitempty"`
	EndReason   string  `json:"end_reason,omitempty"`
	Result      string  `json:"result,omitempty"`
	PeakRssMB   float64 `json:"peak_rss_mb,omitempty"`
	PeakAllocMB float64 `json:"peak_alloc_mb,omitempty"`
	PeakMemMB   float64 `json:"peak_mem_mb,omitempty"`

	// context: what changed between the previous row that carried a context
	// and this one (Source: the table of the row it was first seen on).
	Changes []ContextChange `json:"changes,omitempty"`
	Source  string          `json:"source,omitempty"` // context, breadcrumb: the item type it was derived from

	// breadcrumb
	Kind string `json:"kind,omitempty"` // CrumbEvent, ...
	Text string `json:"text,omitempty"` // the line without its time
	// Repeats: the SDK counted this log line Repeats times, the last at
	// RepeatUntil ("(x37 until 12:00:05.120)").
	Repeats     int64      `json:"repeats,omitempty"`
	RepeatUntil *time.Time `json:"repeat_until,omitempty"`
	// TimeUnknown: the line had no time; Time is that of the row carrying it.
	TimeUnknown bool `json:"time_unknown,omitempty"`
	// AfterSeq (not sent): the breadcrumb was recorded right after the row
	// with this sequence number, in the same millisecond (its carrier's lines
	// are in recording order); it orders the breadcrumb after that row.
	AfterSeq uint64 `json:"-"`
}

// ContextChange is one key of a context change. From is "" when the key was
// not set before (Added), To is "" when it was removed.
type ContextChange struct {
	Key  string `json:"key"`
	From string `json:"from"`
	To   string `json:"to"`
}

// SessionInfo describes the session: when its rows start and end, and the
// device and build of its latest row.
type SessionInfo struct {
	SessionID string    `json:"session_id"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	// StartedAt is when the SDK started the session, from an exception's
	// session_ms (APM-3); nil when no exception of the session carries it.
	StartedAt   *time.Time `json:"started_at,omitempty"`
	DeviceID    string     `json:"device_id"`
	AppVersion  string     `json:"app_version"`
	Platform    string     `json:"platform"`
	DeviceModel string     `json:"device_model"`
	OSVersion   string     `json:"os_version"`
	BuildType   string     `json:"build_type"`
	Rows        int64      `json:"rows"` // rows of every table in the range
}

// PerfPoint is the performance samples of one bucket of the session
// (a sparse track: at most MaxPerfPoints buckets).
type PerfPoint struct {
	Time       time.Time `json:"time"`
	Samples    int64     `json:"samples"`
	FPSAvg     float64   `json:"fps_avg"`
	FPSMin     float64   `json:"fps_min"`
	FrameMsMax float64   `json:"frame_ms_max"`
	MemMBMax   float64   `json:"mem_mb_max"`
}

// SessionTimeline is the response of /api/sessions/timeline.
type SessionTimeline struct {
	SessionID string    `json:"session_id"`
	StartTime time.Time `json:"start_time"` // the range searched
	EndTime   time.Time `json:"end_time"`
	// Found: the session has rows in the range (otherwise Session and Items
	// are empty).
	Found   bool           `json:"found"`
	Session SessionInfo    `json:"session"`
	Items   []TimelineItem `json:"items"`
	// Counts per item type (after limits). Truncated lists the types that had
	// more rows than returned: the ones nearest to At (or the earliest) are kept.
	Counts    map[string]int `json:"counts"`
	Truncated []string       `json:"truncated"`
	// Breadcrumb lines dropped as duplicates: the same line carried by
	// several rows, or a line of a row that is itself on the time line
	// (a custom event, an exception, a scene load).
	DuplicateBreadcrumbs int `json:"duplicate_breadcrumbs"`
	// Perf is set when requested (perf=1): average/min fps, max frame time
	// and memory per bucket of PerfIntervalSeconds.
	Perf                []PerfPoint `json:"perf"`
	PerfIntervalSeconds int         `json:"perf_interval_seconds"`
}

// SessionSummary is one session of a device (/api/sessions?device_id=).
type SessionSummary struct {
	SessionID   string    `json:"session_id"`
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	Rows        int64     `json:"rows"`
	Exceptions  int64     `json:"exceptions"` // occurrences (sum of count)
	Crashes     int64     `json:"crashes"`
	AppVersion  string    `json:"app_version"`
	Platform    string    `json:"platform"`
	DeviceModel string    `json:"device_model"`
	BuildType   string    `json:"build_type"`
}

type DeviceSessionsResponse struct {
	DeviceID  string           `json:"device_id"`
	StartTime time.Time        `json:"start_time"`
	EndTime   time.Time        `json:"end_time"`
	Sessions  []SessionSummary `json:"sessions"`
	Truncated bool             `json:"truncated"`
}
