package processor

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Ops events (APM-12).

var (
	ErrMissingPlayerID    = errors.New("missing or invalid player_id (1-64 characters from [A-Za-z0-9_.-])")
	ErrMissingOpsEventID  = errors.New("missing or invalid event_id (ops events are counted exactly: the id de-duplicates retries)")
	ErrInvalidOpsEventKey = errors.New("invalid name: want 1-64 characters from [A-Za-z0-9_.-]")
)

// OpsNormalizer turns a decoded ops event into what ops_events stores.
// ContextKeys is the whitelist of game context keys kept (config
// ops.context_keys); every other key is dropped without counting it as an
// error (the SDK sends the whole context).
type OpsNormalizer struct {
	contextKeys map[string]struct{}
	enricher    *Enricher
}

func NewOpsNormalizer(contextKeys []string) *OpsNormalizer {
	n := &OpsNormalizer{contextKeys: map[string]struct{}{}, enricher: NewEnricher()}
	for _, k := range contextKeys {
		if k = strings.TrimSpace(k); k != "" {
			n.contextKeys[k] = struct{}{}
		}
	}
	return n
}

// Normalize validates and fills e in place. received is the server's receive
// time; headerCountry the country a trusted proxy reported for the request
// ("" if none or not configured), which wins over the client's. Properties
// and context are bounded like a custom event's (their entries are dropped
// or cut and counted, never the event). An error rejects the event.
func (n *OpsNormalizer) Normalize(e *models.OpsEvent, received time.Time, headerCountry string) (ContextStats, error) {
	var stats ContextStats
	e.ServerTime = received.UTC()
	e.Name = strings.TrimSpace(e.Name)
	if !models.ValidContextKey(e.Name) {
		return stats, ErrInvalidOpsEventKey
	}
	if !validOpsID(e.EventID, models.MaxOpsEventIDLength) {
		return stats, ErrMissingOpsEventID
	}
	if !validOpsID(e.PlayerID, models.MaxPlayerIDLength) {
		return stats, ErrMissingPlayerID
	}
	if e.AppVersion = strings.TrimSpace(e.AppVersion); e.AppVersion == "" {
		return stats, ErrMissingAppVersion
	}
	e.Platform = n.enricher.NormalizePlatform(e.Platform)
	if e.Platform == "" {
		return stats, ErrMissingPlatform
	}
	e.AppVersion = truncateRunes(e.AppVersion, maxDeviceEnumLength*2)
	e.Platform = truncateRunes(e.Platform, maxDeviceEnumLength)
	if len(e.SessionID) > models.MaxOpsSessionLength {
		e.SessionID = ""
	}
	e.BuildType = normalizeBuildType(e.BuildType, "")
	if !validOpsID(e.Channel, models.MaxOpsChannelLength) {
		e.Channel = ""
	}
	// The proxy's country (from the connection) wins over the device's
	// region setting; neither keeps more than the country.
	clientCountry := e.Country
	if e.Country = OpsCountry(headerCountry); e.Country == "" {
		e.Country = OpsCountry(clientCountry)
	}

	// Stored as sent, unless it cannot be stored (DateTime64 covers
	// 1900-2299): then 1970-01-01, "unknown". Either way the same for every
	// copy of the event (the duplicate lookup filters on it).
	if e.ClientTime.Before(time.UnixMilli(0)) || e.ClientTime.After(opsMaxClientTime) {
		e.ClientTime = time.UnixMilli(0)
	}
	e.ClientTime = e.ClientTime.UTC()
	e.EventTime = OpsEventTime(e.ClientTime, e.ServerTime)
	e.Day = OpsDay(e.EventTime)

	var s ContextStats
	e.Props, s = parseContext(e.RawProps)
	stats.Add(s)
	e.PropTypes = propTypes(e.RawProps, e.Props)
	e.NumProps = numProps(e.Props, e.PropTypes)
	e.RawProps = nil

	ctx, s := parseContext(e.RawContext)
	stats.Add(s)
	e.RawContext = nil
	e.Context = map[string]string{}
	for k, v := range ctx {
		if _, ok := n.contextKeys[k]; ok {
			e.Context[k] = v
		}
	}
	return stats, nil
}

// opsMaxClientTime bounds the stored client time (DateTime64(3) ends in 2299).
var opsMaxClientTime = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

// OpsEventTime is the time an ops event counts at: the client's time, unless
// it is later than the receive time plus models.OpsClockSkewTolerance (the
// device clock is ahead: an event is never received before it happened) or
// older than models.OpsMaxLateness (a clock in the past, or an event that
// would be past the retention), in which case the receive time. A client
// time of zero (absent) is the receive time too.
//
// An event uploaded late after offline play keeps its client time: it counts
// on the day it was played, not on the day it reached the server.
func OpsEventTime(client, server time.Time) time.Time {
	if client.IsZero() || client.UnixMilli() <= 0 {
		return server.UTC()
	}
	if client.After(server.Add(models.OpsClockSkewTolerance)) || client.Before(server.Add(-models.OpsMaxLateness)) {
		return server.UTC()
	}
	return client.UTC()
}

// OpsDay is the UTC calendar day of t (midnight UTC), the day every daily
// ops figure groups by.
func OpsDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// OpsCountry returns c as an ISO 3166-1 alpha-2 code (upper case), or "" if
// it is not two ASCII letters or is a placeholder for "unknown" (XX, ZZ).
// Only a country is kept: never a region, city or address.
func OpsCountry(c string) string {
	c = strings.TrimSpace(c)
	if len(c) != 2 {
		return ""
	}
	b := []byte(strings.ToUpper(c))
	for _, ch := range b {
		if ch < 'A' || ch > 'Z' {
			return ""
		}
	}
	if s := string(b); s != "XX" && s != "ZZ" {
		return s
	}
	return ""
}

// validOpsID: 1..max characters from [A-Za-z0-9_.-] (player ids, event ids,
// channels).
func validOpsID(s string, max int) bool {
	if len(s) == 0 || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') {
			return false
		}
	}
	return true
}

// numProps returns the properties sent as JSON numbers, as numbers (nil if
// none): what designers sum and average in SQL (num_props['revenue']).
func numProps(props map[string]string, types map[string]uint8) map[string]float64 {
	var out map[string]float64
	for k, t := range types {
		if t != models.PropTypeNumber {
			continue
		}
		f, err := strconv.ParseFloat(props[k], 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			continue
		}
		if out == nil {
			out = map[string]float64{}
		}
		out[k] = f
	}
	return out
}
