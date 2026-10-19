package processor

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

func TestNormalizePerfMemory_Bounds(t *testing.T) {
	m := models.PerfMemory{IntervalMs: -5, MemMonoHeapMB: -1, MemAllocMB: 512.5, MemReservedMB: 1e12, MemGfxMB: 80, MemRssMB: 1800}
	NormalizePerfMemory(&m)
	if m.IntervalMs != 0 || m.MemMonoHeapMB != 0 || m.MemAllocMB != 512.5 || m.MemReservedMB != MaxMemoryMB || m.MemGfxMB != 80 || m.MemRssMB != 1800 {
		t.Errorf("%+v", m)
	}
	m = models.PerfMemory{IntervalMs: 10 * 60 * 60 * 1000}
	NormalizePerfMemory(&m)
	if m.IntervalMs != maxIntervalMs {
		t.Errorf("interval %d", m.IntervalMs)
	}
}

// A perf sample of an older SDK has none of the fields; a wrong JSON type
// never rejects the sample, it reads as 0.
func TestPerfSample_MemoryFieldsAreLenient(t *testing.T) {
	var old models.PerfSample
	if err := json.Unmarshal([]byte(`{"type":"perf_sample","fps":60,"mem_mb":120.5}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.MemMB != 120.5 || old.MemRssMB != 0 || old.IntervalMs != 0 {
		t.Errorf("%+v", old)
	}
	var s models.PerfSample
	body := `{"type":"perf_sample","fps":60,"mem_mb":120.5,"interval_ms":1003,"mem_mono_heap_mb":"x","mem_alloc_mb":410.25,
		"mem_reserved_mb":null,"mem_gfx_mb":{"a":1},"mem_rss_mb":1500.5}`
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatal(err)
	}
	if s.IntervalMs != 1003 || s.MemMonoHeapMB != 0 || s.MemAllocMB != 410.25 || s.MemReservedMB != 0 || s.MemGfxMB != 0 || s.MemRssMB != 1500.5 {
		t.Errorf("%+v", s.PerfMemory)
	}
}

func validRun() models.Run {
	return models.Run{Timestamp: time.Now(), AppVersion: "1.0", Platform: "Android", DeviceID: "d", SessionID: "s",
		RunID: "run-7", EndReason: "end"}
}

func TestNormalizeRun(t *testing.T) {
	r := validRun()
	r.RunID = "  run\x00-" + strings.Repeat("x", 100) + "  "
	r.Result = "win\n"
	r.EndReason = "END"
	r.CrashType = "oom" // only kept for a crashed run
	r.DurationMs = -1
	r.Samples = 1 << 40
	r.PeakRssMB = -3
	r.PeakAllocMB = 2 * MaxMemoryMB
	r.EndRssMB = 1500
	NormalizeRun(&r)
	if len([]rune(r.RunID)) != MaxRunIDLength || strings.ContainsRune(r.RunID, 0) || !strings.HasPrefix(r.RunID, "run-") {
		t.Errorf("run id %q", r.RunID)
	}
	if r.Result != "win" || r.EndReason != "end" || r.CrashType != "" {
		t.Errorf("%q %q %q", r.Result, r.EndReason, r.CrashType)
	}
	if r.DurationMs != 0 || r.Samples != maxRunSamples || r.PeakRssMB != 0 || r.PeakAllocMB != MaxMemoryMB || r.EndRssMB != 1500 {
		t.Errorf("%+v", r)
	}

	c := validRun()
	c.EndReason, c.CrashType = "crash", "OOM"
	NormalizeRun(&c)
	if c.CrashType != "oom" {
		t.Errorf("crash type %q", c.CrashType)
	}
	c.CrashType = "exploded"
	NormalizeRun(&c)
	if c.CrashType != "unknown" {
		t.Errorf("unknown crash type %q", c.CrashType)
	}
	u := validRun()
	u.EndReason = "<script>"
	NormalizeRun(&u)
	if u.EndReason != "unknown" {
		t.Errorf("end reason %q", u.EndReason)
	}
}

func TestValidateRun(t *testing.T) {
	v := NewValidator()
	r := validRun()
	if err := v.ValidateRun(&r); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*models.Run){
		"run id":  func(r *models.Run) { r.RunID = "" },
		"session": func(r *models.Run) { r.SessionID = "" },
		"device":  func(r *models.Run) { r.DeviceID = "" },
		"version": func(r *models.Run) { r.AppVersion = "" },
		"old":     func(r *models.Run) { r.Timestamp = time.Now().Add(-40 * 24 * time.Hour) },
	} {
		x := validRun()
		mut(&x)
		if err := v.ValidateRun(&x); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
