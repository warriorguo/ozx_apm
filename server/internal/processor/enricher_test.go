package processor

import (
	"testing"
)

func TestEnricher_NormalizePlatform(t *testing.T) {
	e := NewEnricher()

	tests := []struct {
		input    string
		expected string
	}{
		{"android", "Android"},
		{"Android", "Android"},
		{"ANDROID", "Android"},
		{"AndroidPlayer", "Android"},
		{"ios", "iOS"},
		{"iOS", "iOS"},
		{"IPhonePlayer", "iOS"},
		{"iphone", "iOS"},
		{"windows", "Windows"},
		{"WindowsPlayer", "Windows"},
		{"WindowsEditor", "Windows"},
		{"osx", "macOS"},
		{"OSXPlayer", "macOS"},
		{"macos", "macOS"},
		{"linux", "Linux"},
		{"LinuxPlayer", "Linux"},
		{"webgl", "WebGL"},
		{"unknown", "unknown"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := e.NormalizePlatform(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizePlatform(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestEnricher_NormalizeDeviceModel(t *testing.T) {
	e := NewEnricher()

	tests := []struct {
		input    string
		expected string
	}{
		{"Samsung Galaxy S21", "Galaxy S21"},
		{"samsung SM-G998B", "SM-G998B"},
		{"SAMSUNG SM-G998B", "SM-G998B"},
		{"iPhone 13 Pro", "iPhone 13 Pro"},
		{"Pixel 6", "Pixel 6"},
		{"", "Unknown"},
		{"  ", "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := e.NormalizeDeviceModel(tt.input)
			if result != tt.expected {
				t.Errorf("NormalizeDeviceModel(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestEnricher_EnrichFromIP(t *testing.T) {
	e := NewEnricher()

	tests := []struct {
		ip            string
		expectCountry string
		expectRegion  string
	}{
		// Private IPs should return "Local"
		{"192.168.1.1", "Local", ""},
		{"10.0.0.1", "Local", ""},
		{"172.16.0.1", "Local", ""},
		{"127.0.0.1", "Local", ""},

		// Invalid IPs
		{"invalid", "", ""},
		{"", "", ""},

		// Public IPs (without GeoIP database, returns empty)
		{"8.8.8.8", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			country, region := e.EnrichFromIP(tt.ip)
			if country != tt.expectCountry {
				t.Errorf("EnrichFromIP(%q) country = %q, want %q", tt.ip, country, tt.expectCountry)
			}
			if region != tt.expectRegion {
				t.Errorf("EnrichFromIP(%q) region = %q, want %q", tt.ip, region, tt.expectRegion)
			}
		})
	}
}

func TestNewEnricher(t *testing.T) {
	e := NewEnricher()
	if e == nil {
		t.Error("expected non-nil enricher")
	}
}
