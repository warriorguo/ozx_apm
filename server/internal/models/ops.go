package models

import (
	"encoding/json"
	"time"
)

// Operations (ops) events (APM-12): business data for designers and
// operations (retention, funnels, ads), stored apart from the technical data
// in ops_events (one year) and keyed by an anonymous player id instead of the
// device id.
//
// On the wire an ops event is a custom event (type "custom_event") with
// "ops": true, sent by the SDK's own ops queue only with analytics consent.
// It carries only what the ops tables keep: the event name, its properties,
// the player id, the session, version, platform, build type, the install
// channel, a coarse country and the game context (of which ingest keeps the
// whitelisted keys, config ops.context_keys). No device id, device details,
// scene, stacks, logs or breadcrumbs. An event the game also wants as
// technical data is sent a second time through the technical queue as an
// ordinary custom event (each copy obeys its own consent category).

// OpsEvent is one ops event as decoded and normalized at ingest
// (processor.NormalizeOpsEvent).
//
// Times (all UTC):
//   - ClientTime: the device's clock when the event happened ("timestamp").
//   - ServerTime: when ingest received it.
//   - EventTime: the time the event counts at: ClientTime, unless the device
//     clock was evidently wrong (ClientTime later than ServerTime plus
//     OpsClockSkewTolerance, or older than OpsMaxLateness), then ServerTime.
//   - Day: the UTC date of EventTime; every daily figure groups by it.
type OpsEvent struct {
	ClientTime time.Time `json:"-"`
	ServerTime time.Time `json:"-"`
	EventTime  time.Time `json:"-"`
	Day        time.Time `json:"-"`

	EventID    string `json:"event_id"`
	Seq        uint64 `json:"seq"`
	Name       string `json:"name"`
	PlayerID   string `json:"player_id"`
	SessionID  string `json:"session_id"`
	AppVersion string `json:"app_version"`
	Platform   string `json:"platform"`
	BuildType  string `json:"build_type"`
	Channel    string `json:"channel"`
	Country    string `json:"country"`

	RawProps   json.RawMessage `json:"props"`
	RawContext json.RawMessage `json:"ctx"`

	// Props: every property as text; PropTypes: the JSON type of those sent
	// as numbers or booleans (PropTypeNumber, PropTypeBool); NumProps: the
	// numeric properties as numbers (e.g. an ad impression's revenue).
	Props     map[string]string  `json:"-"`
	PropTypes map[string]uint8   `json:"-"`
	NumProps  map[string]float64 `json:"-"`
	// Context: the whitelisted game context keys.
	Context map[string]string `json:"-"`
}

// Time rules of ops events (processor.NormalizeOpsEvent).
const (
	// OpsClockSkewTolerance: a device clock may run this much ahead of the
	// server before its time is replaced by the receive time. An event is
	// always received after it happened, so a client time further ahead is a
	// wrong clock.
	OpsClockSkewTolerance = 5 * time.Minute
	// OpsMaxLateness: an event whose client time is older than this when it
	// is received counts at the receive time (it would be past the
	// retention of ops_events at once).
	OpsMaxLateness = 365 * 24 * time.Hour
)

// Limits of the ops identifiers (bytes; ASCII).
const (
	MaxPlayerIDLength   = 64
	MaxOpsEventIDLength = 64
	MaxOpsChannelLength = 64
	MaxOpsSessionLength = 128
)

// OpsPlayerDay is one row of ops_player_days: the player was active (sent
// at least one ops event) on Day. The other fields are the day's values (of
// one of its events).
type OpsPlayerDay struct {
	Day        time.Time
	PlayerID   string
	AppVersion string
	Platform   string
	BuildType  string
	Channel    string
	Country    string
}

// OpsPlayer is one row of ops_players (an AggregatingMergeTree: the rows of
// a player are merged into first_seen = min, last_seen = max, and the max of
// the text fields).
type OpsPlayer struct {
	PlayerID       string
	FirstSeen      time.Time
	LastSeen       time.Time
	InstallChannel string
	Platform       string
	BuildType      string
}

// OpsFXRate is one row of ops_fx_rates: how many US dollars one unit of
// Currency was worth on Day.
type OpsFXRate struct {
	Day        time.Time
	Currency   string
	USDPerUnit float64
	Source     string
}

// OpsAdReport is one row of ops_ad_reports: an ad network's own figures for
// one day, country and placement (or ad unit), as exported from its console.
type OpsAdReport struct {
	Day         time.Time
	Network     string
	Country     string
	Placement   string
	AdUnit      string
	Impressions uint64
	Revenue     float64
	Currency    string
	Source      string
}
