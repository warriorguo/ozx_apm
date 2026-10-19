package processor

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/metrics"
	"github.com/warriorguo/ozx_apm/server/internal/models"
)

func TestNormalizeCustomEvent_BoundsCountAndProps(t *testing.T) {
	e := models.CustomEvent{Count: math.MaxUint32 + 10, RawProps: json.RawMessage(`{"a":"1","bad key":"2","":"3","b":null}`)}
	stats := NormalizeCustomEvent(&e)
	if e.Count != math.MaxUint32 || len(e.Props) != 1 || e.Props["a"] != "1" || stats.Dropped != 2 || e.RawProps != nil {
		t.Errorf("%+v %+v", e, stats)
	}
	e = models.CustomEvent{Count: -3}
	NormalizeCustomEvent(&e)
	if e.Count != 1 || e.Props == nil {
		t.Errorf("%+v", e)
	}
}

// APM-10: numbers and booleans keep their JSON text in props (as before) and
// their type in PropTypes; strings that look like numbers stay strings.
func TestNormalizeCustomEvent_PropTypes(t *testing.T) {
	e := models.CustomEvent{RawProps: json.RawMessage(`{"amount":34.322,"n":-3,"crit":false,"dead":true,` +
		`"id":"12","flag":"true","huge":1e400,"roomId":"r-1","pos.x":4.990,"zero":0,"nul":null,"bad key":1}`)}
	NormalizeCustomEvent(&e)
	want := map[string]uint8{"amount": models.PropTypeNumber, "n": models.PropTypeNumber, "crit": models.PropTypeBool,
		"dead": models.PropTypeBool, "pos.x": models.PropTypeNumber, "zero": models.PropTypeNumber}
	if len(e.PropTypes) != len(want) {
		t.Errorf("types %v", e.PropTypes)
	}
	for k, v := range want {
		if e.PropTypes[k] != v {
			t.Errorf("%s: %d, want %d", k, e.PropTypes[k], v)
		}
	}
	if e.Props["amount"] != "34.322" || e.Props["pos.x"] != "4.990" || e.Props["huge"] != "1e400" || e.Props["id"] != "12" || e.Props["flag"] != "true" {
		t.Errorf("props %v", e.Props)
	}
	// All strings (TrackEvent, old SDKs): no types at all.
	e = models.CustomEvent{RawProps: json.RawMessage(`{"roomId":"12","seed":"991"}`)}
	NormalizeCustomEvent(&e)
	if e.PropTypes != nil {
		t.Errorf("types %v", e.PropTypes)
	}
	// A number longer than the value limit is cut: text, not a number.
	long := strings.Repeat("1", models.MaxContextValueLength+5)
	e = models.CustomEvent{RawProps: json.RawMessage(`{"n":` + long + `}`)}
	NormalizeCustomEvent(&e)
	if e.PropTypes != nil || len(e.Props["n"]) != models.MaxContextValueLength {
		t.Errorf("cut number: %v %d", e.PropTypes, len(e.Props["n"]))
	}
}

func TestValidateCustomMetric_WindowAndUnit(t *testing.T) {
	v := NewValidator()
	one := 1.0
	m := models.CustomMetric{Timestamp: time.Now(), AppVersion: "1", Platform: "Android", DeviceID: "d", SessionID: "s",
		Name: "load_ms", Unit: strings.Repeat("u", 17), Count: 1, Sum: &one, Min: &one, Max: &one,
		HistVersion: metrics.Version, HistKeys: []int32{int32(metrics.Key(1))}, HistCounts: []uint64{1}, WindowMs: 1 << 40}
	if err := v.ValidateCustomMetric(&m); err != nil {
		t.Fatal(err)
	}
	if m.Unit != "" || m.WindowMs != maxWindowMs {
		t.Errorf("%+v", m)
	}
	m.HistCounts = []uint64{math.MaxUint64}
	m.HistKeys = []int32{int32(metrics.Key(1))}
	m.Count = math.MaxUint64
	if err := v.ValidateCustomMetric(&m); err != nil {
		t.Errorf("max count: %v", err)
	}
	m.HistKeys = []int32{int32(metrics.Key(1)), int32(metrics.Key(2))}
	m.HistCounts = []uint64{math.MaxUint64, 2}
	if err := v.ValidateCustomMetric(&m); err == nil {
		t.Error("overflowing counts accepted")
	}
	m.Name = "bad name"
	if err := v.ValidateCustomMetric(&m); err != ErrInvalidCustomName {
		t.Errorf("name: %v", err)
	}
}
