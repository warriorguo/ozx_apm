package processor

import (
	"errors"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

var (
	ErrInvalidTimestamp  = errors.New("invalid timestamp")
	ErrMissingAppVersion = errors.New("missing app_version")
	ErrMissingPlatform   = errors.New("missing platform")
	ErrMissingDeviceID   = errors.New("missing device_id")
	ErrMissingSessionID  = errors.New("missing session_id")
	ErrInvalidEventType  = errors.New("invalid event type")
)

// MaxEventAge is the oldest client timestamp the server accepts.
//
// The SDK persists events on the device and keeps retrying until the server
// confirms them, so a player who is offline for days uploads late, and the
// event keeps the time it actually happened. Rejecting late events would
// silently drop exactly the data the offline queue exists to keep.
// 30 days matches the detail-data retention (TTL 30 DAY, CLAUDE.md privacy
// rules): anything older would expire as soon as it was written. Clients
// with a wrong clock are bounded by the same window.
const MaxEventAge = 30 * 24 * time.Hour

type Validator struct {
	maxTimestampAge time.Duration
}

func NewValidator() *Validator {
	return &Validator{
		maxTimestampAge: MaxEventAge,
	}
}

// ValidatePerfSample validates a performance sample event
func (v *Validator) ValidatePerfSample(s *models.PerfSample) error {
	if err := v.validateTimestamp(s.Timestamp); err != nil {
		return err
	}
	if s.AppVersion == "" {
		return ErrMissingAppVersion
	}
	if s.Platform == "" {
		return ErrMissingPlatform
	}
	if s.DeviceID == "" {
		return ErrMissingDeviceID
	}
	if s.SessionID == "" {
		return ErrMissingSessionID
	}
	return nil
}

// ValidateJank validates a jank event
func (v *Validator) ValidateJank(j *models.Jank) error {
	if err := v.validateTimestamp(j.Timestamp); err != nil {
		return err
	}
	if j.AppVersion == "" {
		return ErrMissingAppVersion
	}
	if j.Platform == "" {
		return ErrMissingPlatform
	}
	if j.DeviceID == "" {
		return ErrMissingDeviceID
	}
	if j.SessionID == "" {
		return ErrMissingSessionID
	}
	return nil
}

// ValidateStartup validates a startup event
func (v *Validator) ValidateStartup(s *models.Startup) error {
	if err := v.validateTimestamp(s.Timestamp); err != nil {
		return err
	}
	if s.AppVersion == "" {
		return ErrMissingAppVersion
	}
	if s.Platform == "" {
		return ErrMissingPlatform
	}
	if s.DeviceID == "" {
		return ErrMissingDeviceID
	}
	if s.SessionID == "" {
		return ErrMissingSessionID
	}
	return nil
}

// ValidateSceneLoad validates a scene load event
func (v *Validator) ValidateSceneLoad(l *models.SceneLoad) error {
	if err := v.validateTimestamp(l.Timestamp); err != nil {
		return err
	}
	if l.AppVersion == "" {
		return ErrMissingAppVersion
	}
	if l.Platform == "" {
		return ErrMissingPlatform
	}
	if l.DeviceID == "" {
		return ErrMissingDeviceID
	}
	if l.SessionID == "" {
		return ErrMissingSessionID
	}
	if l.SceneName == "" {
		return errors.New("missing scene_name")
	}
	return nil
}

// ValidateException validates an exception event
func (v *Validator) ValidateException(e *models.Exception) error {
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
	if e.Fingerprint == "" {
		return errors.New("missing fingerprint")
	}
	return nil
}

// ValidateCrash validates a crash event
func (v *Validator) ValidateCrash(c *models.Crash) error {
	if err := v.validateTimestamp(c.Timestamp); err != nil {
		return err
	}
	if c.AppVersion == "" {
		return ErrMissingAppVersion
	}
	if c.Platform == "" {
		return ErrMissingPlatform
	}
	if c.DeviceID == "" {
		return ErrMissingDeviceID
	}
	if c.SessionID == "" {
		return ErrMissingSessionID
	}
	if c.Fingerprint == "" {
		return errors.New("missing fingerprint")
	}
	return nil
}

func (v *Validator) validateTimestamp(t time.Time) error {
	if t.IsZero() {
		return ErrInvalidTimestamp
	}
	if time.Since(t) > v.maxTimestampAge {
		return ErrInvalidTimestamp
	}
	if t.After(time.Now().Add(time.Hour)) {
		return ErrInvalidTimestamp
	}
	return nil
}
