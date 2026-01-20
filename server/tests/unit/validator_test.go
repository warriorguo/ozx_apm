package unit

import (
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/processor"
)

func TestValidator_ValidatePerfSample(t *testing.T) {
	v := processor.NewValidator()

	tests := []struct {
		name    string
		sample  *models.PerfSample
		wantErr bool
	}{
		{
			name: "valid sample",
			sample: &models.PerfSample{
				Timestamp:   time.Now(),
				AppVersion:  "1.0.0",
				Platform:    "Android",
				DeviceID:    "device123",
				SessionID:   "session123",
				FPS:         60,
				FrameTimeMs: 16.67,
			},
			wantErr: false,
		},
		{
			name: "missing timestamp",
			sample: &models.PerfSample{
				AppVersion: "1.0.0",
				Platform:   "Android",
				DeviceID:   "device123",
				SessionID:  "session123",
			},
			wantErr: true,
		},
		{
			name: "missing app version",
			sample: &models.PerfSample{
				Timestamp: time.Now(),
				Platform:  "Android",
				DeviceID:  "device123",
				SessionID: "session123",
			},
			wantErr: true,
		},
		{
			name: "missing platform",
			sample: &models.PerfSample{
				Timestamp:  time.Now(),
				AppVersion: "1.0.0",
				DeviceID:   "device123",
				SessionID:  "session123",
			},
			wantErr: true,
		},
		{
			name: "missing device ID",
			sample: &models.PerfSample{
				Timestamp:  time.Now(),
				AppVersion: "1.0.0",
				Platform:   "Android",
				SessionID:  "session123",
			},
			wantErr: true,
		},
		{
			name: "missing session ID",
			sample: &models.PerfSample{
				Timestamp:  time.Now(),
				AppVersion: "1.0.0",
				Platform:   "Android",
				DeviceID:   "device123",
			},
			wantErr: true,
		},
		{
			name: "timestamp too old",
			sample: &models.PerfSample{
				Timestamp:  time.Now().Add(-8 * 24 * time.Hour),
				AppVersion: "1.0.0",
				Platform:   "Android",
				DeviceID:   "device123",
				SessionID:  "session123",
			},
			wantErr: true,
		},
		{
			name: "timestamp in future",
			sample: &models.PerfSample{
				Timestamp:  time.Now().Add(2 * time.Hour),
				AppVersion: "1.0.0",
				Platform:   "Android",
				DeviceID:   "device123",
				SessionID:  "session123",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidatePerfSample(tt.sample)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePerfSample() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidator_ValidateException(t *testing.T) {
	v := processor.NewValidator()

	tests := []struct {
		name    string
		exc     *models.Exception
		wantErr bool
	}{
		{
			name: "valid exception",
			exc: &models.Exception{
				Timestamp:   time.Now(),
				AppVersion:  "1.0.0",
				Platform:    "Android",
				DeviceID:    "device123",
				SessionID:   "session123",
				Fingerprint: "abc123",
				Message:     "NullReferenceException",
				Stack:       "at Foo.Bar()",
			},
			wantErr: false,
		},
		{
			name: "missing fingerprint",
			exc: &models.Exception{
				Timestamp:  time.Now(),
				AppVersion: "1.0.0",
				Platform:   "Android",
				DeviceID:   "device123",
				SessionID:  "session123",
				Message:    "NullReferenceException",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateException(tt.exc)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateException() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidator_ValidateCrash(t *testing.T) {
	v := processor.NewValidator()

	tests := []struct {
		name    string
		crash   *models.Crash
		wantErr bool
	}{
		{
			name: "valid crash",
			crash: &models.Crash{
				Timestamp:   time.Now(),
				AppVersion:  "1.0.0",
				Platform:    "Android",
				DeviceID:    "device123",
				SessionID:   "session123",
				Fingerprint: "crash123",
				CrashType:   "SIGSEGV",
				Stack:       "native crash",
			},
			wantErr: false,
		},
		{
			name: "missing fingerprint",
			crash: &models.Crash{
				Timestamp:  time.Now(),
				AppVersion: "1.0.0",
				Platform:   "Android",
				DeviceID:   "device123",
				SessionID:  "session123",
				CrashType:  "SIGSEGV",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := v.ValidateCrash(tt.crash)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCrash() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
