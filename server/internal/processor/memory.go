package processor

import (
	"errors"
	"strings"

	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// APM-9: memory figures of perf samples and game runs.

const (
	// MaxMemoryMB bounds every memory figure: 1 TB is not a phone's.
	MaxMemoryMB = 1 << 20
	// maxIntervalMs bounds a perf sample's window (the SDK's is about a second).
	maxIntervalMs = 60 * 60 * 1000
	// MaxRunDurationMs bounds a run's length (a week).
	MaxRunDurationMs = 7 * 24 * 60 * 60 * 1000
	// maxRunSamples bounds a run's sample count (a week at 1 Hz).
	maxRunSamples = 7 * 24 * 60 * 60
	// MaxRunIDLength bounds run_id and result (characters).
	MaxRunIDLength = 64
)

var ErrMissingRunID = errors.New("missing run_id")

// memoryMB clamps a memory figure to [0, MaxMemoryMB]; NaN/Inf already read as 0.
func memoryMB(v models.LenientFloat) models.LenientFloat {
	if v < 0 {
		return 0
	}
	if v > MaxMemoryMB {
		return MaxMemoryMB
	}
	return v
}

// NormalizePerfMemory bounds the APM-9 fields of a perf sample in place. It
// never rejects the sample (they are 0 from older SDKs).
func NormalizePerfMemory(m *models.PerfMemory) {
	m.IntervalMs = models.LenientInt(clamp(int64(m.IntervalMs), maxIntervalMs))
	m.MemMonoHeapMB = memoryMB(m.MemMonoHeapMB)
	m.MemAllocMB = memoryMB(m.MemAllocMB)
	m.MemReservedMB = memoryMB(m.MemReservedMB)
	m.MemGfxMB = memoryMB(m.MemGfxMB)
	m.MemRssMB = memoryMB(m.MemRssMB)
}

// shortID keeps printable characters of an id-like text (run id, result), cut
// to MaxRunIDLength characters. Game text: the admin escapes it on display.
func shortID(s string) string {
	return truncateRunes(strings.TrimSpace(printable(s)), MaxRunIDLength)
}

// NormalizeRun bounds a run event in place: ids and the result cut to 64
// printable characters, an unknown end reason stored as "unknown", the crash
// type only for a crashed run (and then one of the APM-7 types), numbers
// clamped. It never rejects the event; ValidateRun does.
func NormalizeRun(r *models.Run) {
	r.RunID = shortID(r.RunID)
	r.Result = shortID(r.Result)
	r.EndReason = strings.ToLower(strings.TrimSpace(r.EndReason))
	if !models.RunEndReasons[r.EndReason] {
		r.EndReason = "unknown"
	}
	r.CrashType = strings.ToLower(strings.TrimSpace(r.CrashType))
	if r.EndReason != models.RunEndCrash {
		r.CrashType = ""
	} else if !CrashTypes[r.CrashType] {
		r.CrashType = fingerprint.CrashTypeUnknown
	}
	r.DurationMs = models.LenientInt(clamp(int64(r.DurationMs), MaxRunDurationMs))
	r.Samples = models.LenientInt(clamp(int64(r.Samples), maxRunSamples))
	for _, f := range []*models.LenientFloat{&r.PeakMemMB, &r.PeakMonoHeapMB, &r.PeakAllocMB, &r.PeakReservedMB,
		&r.PeakGfxMB, &r.PeakRssMB, &r.StartAllocMB, &r.StartRssMB, &r.EndAllocMB, &r.EndRssMB} {
		*f = memoryMB(*f)
	}
}

// ValidateRun checks a run event after NormalizeRun: the common fields and a run id.
func (v *Validator) ValidateRun(r *models.Run) error {
	if err := v.validateTimestamp(r.Timestamp); err != nil {
		return err
	}
	if r.AppVersion == "" {
		return ErrMissingAppVersion
	}
	if r.Platform == "" {
		return ErrMissingPlatform
	}
	if r.DeviceID == "" {
		return ErrMissingDeviceID
	}
	if r.SessionID == "" {
		return ErrMissingSessionID
	}
	if r.RunID == "" {
		return ErrMissingRunID
	}
	return nil
}
