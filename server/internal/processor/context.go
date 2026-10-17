package processor

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Length caps for the device fields (characters). GPU names are long
// ("Adreno (TM) 642L", "Apple M2 Max"), the others are short enums.
const (
	maxGPULength        = 128
	maxDeviceEnumLength = 32
)

// ContextStats counts what NormalizeEventContext changed in the game context.
type ContextStats struct {
	// Dropped: entries not stored (invalid key, object/array value, over
	// MaxContextKeys) plus "ctx" values that were not an object at all.
	Dropped int
	// Truncated: values cut to MaxContextValueLength.
	Truncated int
}

func (s *ContextStats) Add(o ContextStats) {
	s.Dropped += o.Dropped
	s.Truncated += o.Truncated
}

// NormalizeEventContext validates the APM-5 fields of one event in place.
// It never rejects the event: whatever does not fit is dropped or truncated
// and counted, so one bad key cannot cost the event (or the batch).
//
// rawPlatform is the platform as the client sent it, before
// Enricher.NormalizePlatform folds e.g. "WindowsEditor" into "Windows": an
// event without build_type whose platform names an editor is stored as
// build_type "editor", so editor data stays out of the default (release)
// view even from clients that do not send build_type.
func NormalizeEventContext(c *models.EventContext, rawPlatform string) ContextStats {
	c.BuildType = normalizeBuildType(c.BuildType, rawPlatform)
	c.GPU = truncateRunes(strings.TrimSpace(c.GPU), maxGPULength)
	c.RAMClass = truncateRunes(strings.TrimSpace(c.RAMClass), maxDeviceEnumLength)
	c.NetType = truncateRunes(strings.TrimSpace(c.NetType), maxDeviceEnumLength)

	var stats ContextStats
	c.Context, stats = parseContext(c.RawContext)
	c.RawContext = nil
	return stats
}

func normalizeBuildType(buildType, rawPlatform string) string {
	switch bt := strings.ToLower(strings.TrimSpace(buildType)); bt {
	case models.BuildTypeRelease, models.BuildTypeDev, models.BuildTypeEditor:
		return bt
	}
	if strings.Contains(strings.ToLower(rawPlatform), "editor") {
		return models.BuildTypeEditor
	}
	// Absent or not one of the three: unknown. Not guessed from anything else.
	return models.BuildTypeUnknown
}

// parseContext turns the "ctx" JSON object into the stored map.
//
//   - Keys must match models.ValidContextKey; others are dropped.
//   - String values are stored as they are; numbers and booleans as their
//     JSON text (a hand-written client may send "levelId": 3); null and ""
//     mean "not set" and are not stored (a missing key reads as "" anyway).
//     Objects and arrays are dropped.
//   - Values longer than MaxContextValueLength characters are truncated.
//   - At most MaxContextKeys entries are kept, the first in key order.
//   - A "ctx" that is not an object (or not valid JSON) is dropped as a whole.
//
// The result is never nil.
func parseContext(raw json.RawMessage) (map[string]string, ContextStats) {
	var stats ContextStats
	out := map[string]string{}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return out, stats
	}
	var fields map[string]json.RawMessage
	if raw[0] != '{' || json.Unmarshal(raw, &fields) != nil {
		stats.Dropped++
		return out, stats
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !models.ValidContextKey(k) {
			stats.Dropped++
			continue
		}
		value, ok := contextValue(fields[k])
		if !ok {
			stats.Dropped++
			continue
		}
		if value == "" {
			continue
		}
		if len(out) >= models.MaxContextKeys {
			stats.Dropped++
			continue
		}
		if t := truncateRunes(value, models.MaxContextValueLength); t != value {
			value = t
			stats.Truncated++
		}
		out[k] = value
	}
	return out, stats
}

// contextValue returns the stored text of one context value; ok is false for
// objects and arrays.
func contextValue(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", true
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		return s, true
	case '{', '[':
		return "", false
	case 'n': // null
		return "", true
	default: // number, true, false: keep the JSON text
		return string(raw), true
	}
}

// truncateRunes cuts s to at most n characters, never inside a UTF-8 sequence.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
