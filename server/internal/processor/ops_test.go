package processor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

func opsEvent(t *testing.T, raw string) *models.OpsEvent {
	t.Helper()
	var e models.OpsEvent
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatal(err)
	}
	var w struct {
		Timestamp int64 `json:"timestamp"`
	}
	_ = json.Unmarshal([]byte(raw), &w)
	if w.Timestamp > 0 {
		e.ClientTime = time.UnixMilli(w.Timestamp)
	}
	return &e
}

var opsNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func TestOpsNormalize_KeepsOnlyWhatOpsEventsStores(t *testing.T) {
	n := NewOpsNormalizer([]string{"runId", "chapter", "revivesUsed"})
	e := opsEvent(t, `{"type":"custom_event","ops":true,"event_id":"e1","seq":7,"timestamp":1792497600000,
		"name":"ad_show","player_id":"p0123456789abcdef","session_id":"s1","app_version":"1.2.0","platform":"AndroidPlayer",
		"build_type":"Release","channel":"googleplay","country":"de",
		"props":{"placement":"revive","network":"admob","revenue":0.0123,"rewarded":true,"bad key":"x","obj":{"a":1}},
		"ctx":{"runId":"r-9","chapter":3,"revivesUsed":"1","masterSeed":"184467","gitCommit":"abc"}}`)
	stats, err := n.Normalize(e, opsNow, "")
	if err != nil {
		t.Fatal(err)
	}
	if e.Platform != "Android" || e.BuildType != "release" || e.Channel != "googleplay" || e.Country != "DE" || e.Seq != 7 {
		t.Errorf("fields %+v", e)
	}
	if len(e.Context) != 3 || e.Context["runId"] != "r-9" || e.Context["chapter"] != "3" || e.Context["revivesUsed"] != "1" {
		t.Errorf("context %v: only the whitelisted keys", e.Context)
	}
	if e.Props["placement"] != "revive" || e.Props["revenue"] != "0.0123" || e.Props["rewarded"] != "true" || len(e.Props) != 4 {
		t.Errorf("props %v", e.Props)
	}
	if e.PropTypes["revenue"] != models.PropTypeNumber || e.PropTypes["rewarded"] != models.PropTypeBool || len(e.PropTypes) != 2 {
		t.Errorf("prop types %v", e.PropTypes)
	}
	if e.NumProps["revenue"] != 0.0123 || len(e.NumProps) != 1 {
		t.Errorf("num props %v", e.NumProps)
	}
	if stats.Dropped != 2 {
		t.Errorf("dropped %d, want 2 (invalid key, object value); whitelisted-out context keys are not errors", stats.Dropped)
	}
	if e.RawProps != nil || e.RawContext != nil {
		t.Error("raw values not released")
	}
	want := time.UnixMilli(1792497600000).UTC()
	if !e.ClientTime.Equal(want) || !e.EventTime.Equal(want) || !e.ServerTime.Equal(opsNow) {
		t.Errorf("times client %v event %v server %v", e.ClientTime, e.EventTime, e.ServerTime)
	}
	if e.Day != time.Date(want.Year(), want.Month(), want.Day(), 0, 0, 0, 0, time.UTC) {
		t.Errorf("day %v", e.Day)
	}
}

func TestOpsNormalize_Rejects(t *testing.T) {
	n := NewOpsNormalizer(nil)
	base := map[string]interface{}{"event_id": "e1", "name": "run_start", "player_id": "p1", "app_version": "1.0", "platform": "Android", "timestamp": opsNow.UnixMilli()}
	cases := map[string]map[string]interface{}{
		"no event id":    {"event_id": ""},
		"long event id":  {"event_id": strings.Repeat("a", 65)},
		"event id chars": {"event_id": "a b"},
		"no player":      {"player_id": ""},
		"player chars":   {"player_id": "p/1"},
		"long player":    {"player_id": strings.Repeat("p", 65)},
		"name":           {"name": "run start"},
		"no name":        {"name": ""},
		"no version":     {"app_version": " "},
		"no platform":    {"platform": ""},
	}
	for name, change := range cases {
		m := map[string]interface{}{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range change {
			m[k] = v
		}
		b, _ := json.Marshal(m)
		if _, err := n.Normalize(opsEvent(t, string(b)), opsNow, ""); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	b, _ := json.Marshal(base)
	if _, err := n.Normalize(opsEvent(t, string(b)), opsNow, ""); err != nil {
		t.Errorf("base rejected: %v", err)
	}
}

func TestOpsNormalize_Fields(t *testing.T) {
	n := NewOpsNormalizer(nil)
	e := opsEvent(t, `{"event_id":"e1","name":"x","player_id":"p1","app_version":"1.0","platform":"iPhonePlayer",
		"channel":"bad channel","country":"XX","session_id":"`+strings.Repeat("s", 129)+`","build_type":"weird",
		"ctx":{"runId":"r1","roomId":"3","seed":"9"}}`)
	if _, err := n.Normalize(e, opsNow, ""); err != nil {
		t.Fatal(err)
	}
	if e.Platform != "iOS" || e.Channel != "" || e.Country != "" || e.SessionID != "" || e.BuildType != "" {
		t.Errorf("fields %+v", e)
	}
	// A nil whitelist in the normalizer keeps nothing; the server passes the
	// configured one (config.DefaultOpsContextKeys when not configured).
	if len(e.Context) != 0 {
		t.Errorf("context %v", e.Context)
	}
	if e.Props == nil || e.Context == nil {
		t.Error("maps must never be nil (stored as empty maps)")
	}
}

func TestOpsCountry(t *testing.T) {
	for in, want := range map[string]string{"de": "DE", " US ": "US", "XX": "", "ZZ": "", "T1": "", "DEU": "", "": "", "d": "", "ü1": ""} {
		if got := OpsCountry(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// The trusted proxy's country wins over the device's region setting; an
// unusable header leaves the client's.
func TestOpsNormalize_CountryHeaderWins(t *testing.T) {
	n := NewOpsNormalizer(nil)
	raw := `{"event_id":"e1","name":"x","player_id":"p1","app_version":"1.0","platform":"Android","country":"de"}`
	e := opsEvent(t, raw)
	if _, err := n.Normalize(e, opsNow, "br"); err != nil || e.Country != "BR" {
		t.Errorf("header: %q %v", e.Country, err)
	}
	e = opsEvent(t, raw)
	if _, err := n.Normalize(e, opsNow, "XX"); err != nil || e.Country != "DE" {
		t.Errorf("unknown header: %q %v", e.Country, err)
	}
}

// Which day an event counts on: the client's time, unless the device clock
// is evidently wrong; an event uploaded days later after offline play keeps
// its day.
func TestOpsEventTime(t *testing.T) {
	server := opsNow
	cases := []struct {
		name   string
		client time.Time
		want   time.Time
	}{
		{"normal", server.Add(-3 * time.Second), server.Add(-3 * time.Second)},
		{"offline for two days", server.Add(-49 * time.Hour), server.Add(-49 * time.Hour)},
		{"slightly ahead", server.Add(4 * time.Minute), server.Add(4 * time.Minute)},
		{"clock a day ahead", server.Add(24 * time.Hour), server},
		{"clock years behind", server.Add(-3 * 365 * 24 * time.Hour), server},
		{"no time", time.Time{}, server},
		{"epoch", time.UnixMilli(0), server},
	}
	for _, c := range cases {
		if got := OpsEventTime(c.client, server); !got.Equal(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if d := OpsDay(time.Date(2026, 10, 19, 23, 59, 59, 0, time.FixedZone("x", -3600))); d != time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC) {
		t.Errorf("day is the UTC date: %v", d)
	}
}
