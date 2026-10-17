package processor

import (
	"errors"
	"fmt"
	"math"

	"github.com/warriorguo/ozx_apm/server/internal/metrics"
	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Custom events and metrics (APM-6).

const (
	// MaxCustomUnitLength bounds a metric's unit (printable ASCII).
	MaxCustomUnitLength = 16
	// MaxHistogramKeys bounds the buckets of one metric row (the SDK sends
	// at most 128).
	MaxHistogramKeys = 2048
	// maxWindowMs bounds window_ms (the SDK's window is a minute).
	maxWindowMs = 24 * 60 * 60 * 1000
)

var (
	ErrInvalidCustomName = errors.New("invalid name: want 1-64 characters from [A-Za-z0-9_.-]")
	errCountMismatch     = errors.New("histogram counts do not add up to count")
)

// ValidateCustomEvent checks a custom event after NormalizeCustomEvent.
func (v *Validator) ValidateCustomEvent(e *models.CustomEvent) error {
	if err := v.validateTimestamp(e.Timestamp); err != nil {
		return err
	}
	if e.AppVersion == "" {
		return ErrMissingAppVersion
	}
	if e.Platform == "" {
		return ErrMissingPlatform
	}
	if e.DeviceID == "" {
		return ErrMissingDeviceID
	}
	if e.SessionID == "" {
		return ErrMissingSessionID
	}
	if !models.ValidContextKey(e.Name) {
		return ErrInvalidCustomName
	}
	return nil
}

// NormalizeCustomEvent bounds the count and validates the properties like
// the game context (parseContext: the same key rule and limits; entries that
// do not fit are dropped and counted, never the event). A missing or
// non-positive count reads as 1; it is capped at 2^32-1.
func NormalizeCustomEvent(e *models.CustomEvent) ContextStats {
	if e.Count < 1 {
		e.Count = 1
	}
	if e.Count > math.MaxUint32 {
		e.Count = math.MaxUint32
	}
	var stats ContextStats
	e.Props, stats = parseContext(e.RawProps)
	e.RawProps = nil
	return stats
}

// ValidateCustomMetric checks a custom metric row. It is rejected (never
// stored) unless its numbers are consistent: count > 0, finite sum/min/max
// with min <= max, a histogram of a known version whose keys are valid and
// whose counts add up to count. An unprintable or over-long unit is dropped.
func (v *Validator) ValidateCustomMetric(m *models.CustomMetric) error {
	if err := v.validateTimestamp(m.Timestamp); err != nil {
		return err
	}
	if m.AppVersion == "" {
		return ErrMissingAppVersion
	}
	if m.Platform == "" {
		return ErrMissingPlatform
	}
	if m.DeviceID == "" {
		return ErrMissingDeviceID
	}
	if m.SessionID == "" {
		return ErrMissingSessionID
	}
	if !models.ValidContextKey(m.Name) {
		return ErrInvalidCustomName
	}
	if !validUnit(m.Unit) {
		m.Unit = ""
	}
	if m.Count == 0 {
		return errors.New("count must be positive")
	}
	if m.Sum == nil || m.Min == nil || m.Max == nil || !finite(*m.Sum) || !finite(*m.Min) || !finite(*m.Max) {
		return errors.New("sum, min and max must be finite numbers")
	}
	if *m.Min > *m.Max {
		return errors.New("min is greater than max")
	}
	if m.HistVersion != metrics.Version {
		return fmt.Errorf("unsupported hist_v %d (this server reads %d)", m.HistVersion, metrics.Version)
	}
	if len(m.HistKeys) != len(m.HistCounts) || len(m.HistKeys) == 0 || len(m.HistKeys) > MaxHistogramKeys {
		return errors.New("hist_keys and hist_counts must have the same length, 1 to 2048")
	}
	var total uint64
	for i, k := range m.HistKeys {
		if !metrics.ValidKey(int(k)) {
			return fmt.Errorf("invalid histogram key %d", k)
		}
		c := m.HistCounts[i]
		if c == 0 || total+c < total {
			return errCountMismatch
		}
		total += c
	}
	if total != m.Count {
		return errCountMismatch
	}
	if m.WindowMs < 0 {
		m.WindowMs = 0
	}
	if m.WindowMs > maxWindowMs {
		m.WindowMs = maxWindowMs
	}
	return nil
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

func validUnit(u string) bool {
	if len(u) > MaxCustomUnitLength {
		return false
	}
	for i := 0; i < len(u); i++ {
		if c := u[i]; c < 0x20 || c > 0x7e {
			return false
		}
	}
	return true
}
