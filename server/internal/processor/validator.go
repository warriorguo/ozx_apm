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

type Validator struct {
	maxTimestampAge time.Duration
}

func NewValidator() *Validator {
	return &Validator{
		maxTimestampAge: 7 * 24 * time.Hour, // Accept events up to 7 days old
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
