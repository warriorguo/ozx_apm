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
