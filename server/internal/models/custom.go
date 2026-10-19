package models

import (
	"encoding/json"
	"time"
)

// Custom events and metrics (APM-6). Both are technical data (CLAUDE.md
// privacy rules: 30 days of detail): they say what the game was doing, for
// diagnostics. Business/operations events are APM-12.

const (
	EventTypeCustomEvent  EventType = "custom_event"
	EventTypeCustomMetric EventType = "custom_metric"
)

// CustomEvent is one ApmClient.TrackEvent call (Count 1, with its
// properties), or a count of calls beyond the SDK's per-minute budget in one
// game context (Count > 1 or Aggregated, properties not kept). CtxMerged:
// the count merges calls in different contexts; the context holds only what
// they shared. Props are validated like the game context
// (processor.NormalizeCustomEvent).
type CustomEvent struct {
	Timestamp   time.Time         `json:"-"`
	EventID     string            `json:"event_id"`
	Seq         uint64            `json:"seq"`
	AppVersion  string            `json:"app_version"`
	Platform    string            `json:"platform"`
	DeviceModel string            `json:"device_model"`
	OSVersion   string            `json:"os_version"`
	SessionID   string            `json:"session_id"`
	DeviceID    string            `json:"device_id"`
	Scene       string            `json:"scene"`
	Name        string            `json:"name"`
	Count       LenientInt        `json:"count"`
	Aggregated  LenientBool       `json:"aggregated"`
	CtxMerged   LenientBool       `json:"ctx_merged"`
	RawProps    json.RawMessage   `json:"props"`
	Props       map[string]string `json:"-"`
	// PropTypes: the JSON type of the props that were not strings (APM-10):
	// PropTypeNumber or PropTypeBool; Props holds their JSON text.
	PropTypes map[string]uint8 `json:"-"`
	EventContext
}

// CustomMetric is the samples of one metric (ApmClient.RecordMetric or a
// timer) in one game context and one window of the SDK: their count, sum,
// min and max, and a histogram of bucket keys (internal/metrics, version
// HistVersion) from which percentiles are computed. The numbers are decoded
// strictly: an event whose numbers do not add up is rejected, never stored.
type CustomMetric struct {
	Timestamp   time.Time   `json:"-"`
	EventID     string      `json:"event_id"`
	Seq         uint64      `json:"seq"`
	AppVersion  string      `json:"app_version"`
	Platform    string      `json:"platform"`
	DeviceModel string      `json:"device_model"`
	OSVersion   string      `json:"os_version"`
	SessionID   string      `json:"session_id"`
	DeviceID    string      `json:"device_id"`
	Scene       string      `json:"scene"`
	Name        string      `json:"name"`
	Unit        string      `json:"unit"`
	Count       uint64      `json:"count"`
	Sum         *float64    `json:"sum"`
	Min         *float64    `json:"min"`
	Max         *float64    `json:"max"`
	WindowMs    LenientInt  `json:"window_ms"`
	HistVersion int         `json:"hist_v"`
	HistKeys    []int32     `json:"hist_keys"`
	HistCounts  []uint64    `json:"hist_counts"`
	CtxMerged   LenientBool `json:"ctx_merged"`
	EventContext
}

// Values of CustomEvent.PropTypes (stored in apm_custom_events.prop_types).
// A property without an entry is a string.
const (
	PropTypeNumber uint8 = 1
	PropTypeBool   uint8 = 2
)

// Custom endpoints of the admin API.

// CustomEventStat is one event name.
type CustomEventStat struct {
	Name     string    `json:"name"`
	Count    int64     `json:"count"` // calls (sum of count)
	Sessions int64     `json:"sessions"`
	Devices  int64     `json:"devices"`
	LastSeen time.Time `json:"last_seen"`
}

type CustomEventsResponse struct {
	Events    []CustomEventStat `json:"events"`
	Truncated bool              `json:"truncated"`
}

// CustomMetricStat is one metric name over the filter, or one group of a
// distribution. P50..P99 come from the merged histograms: exact rank, value
// within internal/metrics.RelativeAccuracy (2%) of the sample at that rank.
type CustomMetricStat struct {
	Name     string  `json:"name,omitempty"`
	Value    string  `json:"value"`
	Unit     string  `json:"unit,omitempty"`
	Count    int64   `json:"count"` // samples
	Sessions int64   `json:"sessions"`
	Avg      float64 `json:"avg"`
	Min      float64 `json:"min"`
	Max      float64 `json:"max"`
	P50      float64 `json:"p50"`
	P90      float64 `json:"p90"`
	P95      float64 `json:"p95"`
	P99      float64 `json:"p99"`
}

type CustomMetricsResponse struct {
	Metrics   []CustomMetricStat `json:"metrics"`
	Truncated bool               `json:"truncated"`
}

// CustomDistribution groups one event or metric name by a context key
// ("ctx:<key>"), a property ("prop:<key>", events) or a column
// (app_version, platform, device_model, os_version, build_type). Rows of
// metrics carry the percentiles; rows of events only Count and Sessions.
// Merged: calls or samples whose value of the key is not known (counts the
// SDK merged across contexts, or aggregated events without properties);
// they are not in Rows, Total includes them.
type CustomDistribution struct {
	Name      string             `json:"name"`
	GroupBy   string             `json:"group_by"`
	Unit      string             `json:"unit,omitempty"`
	Total     int64              `json:"total"`
	Merged    int64              `json:"merged"`
	Truncated bool               `json:"truncated"`
	Rows      []CustomMetricStat `json:"rows"`
}
