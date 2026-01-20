package processor

import (
	"net"
	"strings"
)

type Enricher struct {
	// In production, this would use a GeoIP database like MaxMind
}

func NewEnricher() *Enricher {
	return &Enricher{}
}

// EnrichFromIP adds geo information based on IP address
// In production, this would use MaxMind GeoIP2 or similar
func (e *Enricher) EnrichFromIP(ip string) (country, region string) {
	// Placeholder implementation
	// In production, use maxminddb-golang with GeoLite2-City database
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return "", ""
	}

	// Check for private IPs
	if isPrivateIP(parsedIP) {
		return "Local", ""
	}

	// In production: lookup in GeoIP database
	return "", ""
}

// NormalizePlatform normalizes platform strings
func (e *Enricher) NormalizePlatform(platform string) string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	switch platform {
	case "android", "androidplayer":
		return "Android"
	case "ios", "iphone", "iphoneplayer":
		return "iOS"
	case "windows", "windowsplayer", "windowseditor":
		return "Windows"
	case "osx", "osxplayer", "osxeditor", "macos":
		return "macOS"
	case "linux", "linuxplayer", "linuxeditor":
		return "Linux"
	case "webgl":
		return "WebGL"
	default:
		return platform
	}
}

// NormalizeDeviceModel cleans up device model strings
func (e *Enricher) NormalizeDeviceModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "Unknown"
	}
	// Remove common prefixes that add noise (case-insensitive)
	lowered := strings.ToLower(model)
	if strings.HasPrefix(lowered, "samsung ") {
		model = model[8:] // Remove "Samsung " (8 chars)
	}
	return model
}

func isPrivateIP(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		return ip4[0] == 10 ||
			(ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) ||
			(ip4[0] == 192 && ip4[1] == 168) ||
			ip4[0] == 127
	}
	return false
}
