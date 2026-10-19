package models

import (
	"encoding/json"
	"math"
	"time"
)

// APM-9: the memory breakdown of perf samples, game runs with their peak
// memory, and the admin endpoints for memory, session health and funnels.

// EventTypeRun is a game run that ended (ApmClient.BeginRun / EndRun).
const EventTypeRun EventType = "run"

// LenientFloat decodes a JSON number; anything else (a string, null, an
// object) reads as 0 instead of an error, so a field added by APM-9 never
// makes ingest reject an event.
type LenientFloat float64

func (v *LenientFloat) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		f = 0
	}
	*v = LenientFloat(f)
	return nil
}

// PerfMemory is what APM-9 added to perf samples; 0 from older SDKs and for
// a figure the platform does not give. IntervalMs is the length of the
// sample's window (the play time it covers). Memory in MB: the managed heap
// reserved by the runtime, Unity's native memory allocated and reserved, the
// graphics memory Unity estimates, and the process's resident memory
// (Android: RSS; other platforms: Unity's System Used Memory). PerfSample's
// MemMB keeps its meaning: the managed heap in use.
// processor.NormalizePerfMemory bounds them.
type PerfMemory struct {
	IntervalMs    LenientInt   `json:"interval_ms"`
	MemMonoHeapMB LenientFloat `json:"mem_mono_heap_mb"`
	MemAllocMB    LenientFloat `json:"mem_alloc_mb"`
	MemReservedMB LenientFloat `json:"mem_reserved_mb"`
	MemGfxMB      LenientFloat `json:"mem_gfx_mb"`
	MemRssMB      LenientFloat `json:"mem_rss_mb"`
}

// Run end reasons.
const (
	RunEndEnd        = "end"         // EndRun
	RunEndNewRun     = "new_run"     // BeginRun while a run was open
	RunEndShutdown   = "shutdown"    // the SDK shut down (quit) with a run open
	RunEndCrash      = "crash"       // the process crashed / was killed (CrashType says how)
	RunEndProcessEnd = "process_end" // the process ended without a crash (background kill, ...)
)

// RunEndReasons are the end reasons the server stores; anything else is
// stored as "unknown".
var RunEndReasons = map[string]bool{
	RunEndEnd: true, RunEndNewRun: true, RunEndShutdown: true, RunEndCrash: true, RunEndProcessEnd: true,
}

// Run is one game run, sent by the SDK when it ends (or, for a run its
// process did not survive, at the next launch from the crash journal).
// Peak*: the highest value of each memory figure among the run's samples;
// Start*/End*: native allocated and resident memory at its first and last
// sample. Numbers are decoded leniently and bounded by processor.NormalizeRun.
type Run struct {
	Timestamp      time.Time    `json:"-"`
	EventID        string       `json:"event_id"`
	Seq            uint64       `json:"seq"`
	AppVersion     string       `json:"app_version"`
	Platform       string       `json:"platform"`
	DeviceModel    string       `json:"device_model"`
	OSVersion      string       `json:"os_version"`
	SessionID      string       `json:"session_id"`
	DeviceID       string       `json:"device_id"`
	Scene          string       `json:"scene"`
	RunID          string       `json:"run_id"`
	EndReason      string       `json:"end_reason"`
	Result         string       `json:"result"`
	CrashType      string       `json:"crash_type"`
	DurationMs     LenientInt   `json:"duration_ms"`
	Samples        LenientInt   `json:"samples"`
	PeakMemMB      LenientFloat `json:"peak_mem_mb"`
	PeakMonoHeapMB LenientFloat `json:"peak_mono_heap_mb"`
	PeakAllocMB    LenientFloat `json:"peak_alloc_mb"`
	PeakReservedMB LenientFloat `json:"peak_reserved_mb"`
	PeakGfxMB      LenientFloat `json:"peak_gfx_mb"`
	PeakRssMB      LenientFloat `json:"peak_rss_mb"`
	StartAllocMB   LenientFloat `json:"start_alloc_mb"`
	StartRssMB     LenientFloat `json:"start_rss_mb"`
	EndAllocMB     LenientFloat `json:"end_alloc_mb"`
	EndRssMB       LenientFloat `json:"end_rss_mb"`
	EventContext
}

// ---- Admin API ----

// MemoryFigure is one memory column with its percentiles over the rows
// that have it (> 0); Rows counts those.
type MemoryFigure struct {
	Rows int64   `json:"rows"`
	P50  float64 `json:"p50"`
	P95  float64 `json:"p95"`
	Max  float64 `json:"max"`
}

// MemoryBreakdownRow is one group of GET /api/memory/breakdown: the perf
// samples' memory figures (MB) by name (mem_mb, mem_mono_heap_mb,
// mem_alloc_mb, mem_reserved_mb, mem_gfx_mb, mem_rss_mb).
type MemoryBreakdownRow struct {
	Value    string                  `json:"value"`
	Samples  int64                   `json:"samples"`
	Sessions int64                   `json:"sessions"`
	Figures  map[string]MemoryFigure `json:"figures"`
}

type MemoryBreakdownResponse struct {
	GroupBy   string               `json:"group_by"`
	Rows      []MemoryBreakdownRow `json:"rows"`
	Truncated bool                 `json:"truncated"`
}

// RunPeakRow is one group of GET /api/memory/runs: the distribution of one
// peak figure (Metric) over the runs that have it, and how the runs ended.
type RunPeakRow struct {
	Value       string  `json:"value"`
	Runs        int64   `json:"runs"`
	WithValue   int64   `json:"with_value"`
	Sessions    int64   `json:"sessions"`
	Devices     int64   `json:"devices"`
	CrashedRuns int64   `json:"crashed_runs"`
	OOMRuns     int64   `json:"oom_runs"`
	KilledRuns  int64   `json:"killed_runs"`
	P50         float64 `json:"p50"`
	P90         float64 `json:"p90"`
	P99         float64 `json:"p99"`
	Max         float64 `json:"max"`
	AvgDuration float64 `json:"avg_duration_ms"`
	AvgGrowthMB float64 `json:"avg_growth_mb"` // end - start of the metric's start/end pair (alloc or rss), runs that ended normally
}

// RunPeakBucket is one bar of the peak histogram (MB, [Low, High); High 0 = open).
type RunPeakBucket struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
	Runs int64   `json:"runs"`
	OOM  int64   `json:"oom"`
}

type RunPeaksResponse struct {
	Metric    string          `json:"metric"`
	GroupBy   string          `json:"group_by"`
	Rows      []RunPeakRow    `json:"rows"`
	Buckets   []RunPeakBucket `json:"buckets"`
	Truncated bool            `json:"truncated"`
}

// SessionHealthRow is one group (a version, a build type, or a value of a
// context key such as the data pack version) of GET /api/health.
//
// Sessions/Users: sessions and devices with any row in the range. Crashed:
// sessions/devices with a crash of a counted type (CrashTypes of the
// response); ANR: with an ANR (never counted as a crash here, like Play
// Console's separate ANR rate). Exceptions: occurrences (sum of count) and
// sessions with at least one. Rates are fractions (0.993 = 99.3%).
type SessionHealthRow struct {
	Value                string           `json:"value"`
	BuildType            string           `json:"build_type"`
	Sessions             int64            `json:"sessions"`
	Users                int64            `json:"users"`
	CrashedSessions      int64            `json:"crashed_sessions"`
	CrashedUsers         int64            `json:"crashed_users"`
	CrashFreeSessions    float64          `json:"crash_free_sessions"`
	CrashFreeUsers       float64          `json:"crash_free_users"`
	ANRSessions          int64            `json:"anr_sessions"`
	ANRRate              float64          `json:"anr_rate"`
	ExceptionSessions    int64            `json:"exception_sessions"`
	Exceptions           int64            `json:"exceptions"`
	ExceptionSessionRate float64          `json:"exception_session_rate"`
	ExceptionsPerSession float64          `json:"exceptions_per_session"`
	CrashesByType        map[string]int64 `json:"crashes_by_type"`
}

type SessionHealthResponse struct {
	GroupBy    string   `json:"group_by"`
	CrashTypes []string `json:"crash_types"`
	// UserKillsCounted: "killed" crashes with an exit reason of the player
	// (USER_REQUESTED, USER_STOPPED: force-stop) count as crashes; by
	// default they do not (they are in crashes_by_type as killed_by_user).
	UserKillsCounted bool               `json:"user_kills_counted"`
	Rows             []SessionHealthRow `json:"rows"`
	Truncated        bool               `json:"truncated"`
}

// FunnelStep is one step of GET /api/funnel: a custom event name and
// optional property filters (all must match; such a step only matches
// events sent with their properties, not counts the SDK aggregated).
type FunnelStep struct {
	Name  string            `json:"name"`
	Props map[string]string `json:"props,omitempty"`
}

// FunnelStepResult: sessions that reached the step (in order, within the
// window), the share of the first step's sessions and of the previous step's.
type FunnelStepResult struct {
	Name         string            `json:"name"`
	Props        map[string]string `json:"props,omitempty"`
	Sessions     int64             `json:"sessions"`
	Conversion   float64           `json:"conversion"`
	FromPrevious float64           `json:"from_previous"`
}

type FunnelGroup struct {
	Value string             `json:"value"`
	Steps []FunnelStepResult `json:"steps"`
}

type FunnelResponse struct {
	StartTime     time.Time     `json:"start_time"`
	EndTime       time.Time     `json:"end_time"`
	WindowSeconds int64         `json:"window_seconds"`
	GroupBy       string        `json:"group_by"`
	Steps         []FunnelStep  `json:"steps"`
	Groups        []FunnelGroup `json:"groups"`
	Truncated     bool          `json:"truncated"`
}
