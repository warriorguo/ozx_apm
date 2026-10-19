package processor

import (
	"strconv"
	"strings"

	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Caps on the APM-7 crash fields (characters). The SDK sends at most ~16 KB
// of stack (3 threads of 64 frames), 30 breadcrumbs and short texts; the
// server allows more and cuts the rest.
const (
	MaxCrashStackLength       = 32000
	maxExitDescriptionLength  = 1000
	maxAbortMessageLength     = 2000
	maxCrashEnumLength        = 32
	maxFaultAddrLength        = 32
	maxCrashSummaryLength     = 300
	maxCrashSiteLength        = 512
	maxCrashImportance        = 1000
	maxCrashMemoryKB          = 1 << 40
	maxCrashDetailsValueRunes = 256
)

// CrashTypes are the crash types the SDK sends; anything else is stored as
// "unknown" (rows of SDKs before APM-7 never had a crash event).
var CrashTypes = map[string]bool{
	fingerprint.CrashTypeNative: true, fingerprint.CrashTypeANR: true, fingerprint.CrashTypeJava: true,
	fingerprint.CrashTypeOOM: true, fingerprint.CrashTypeKilled: true, fingerprint.CrashTypeUnknown: true,
}

// NormalizeCrash bounds the APM-7 fields of a crash event in place and
// parses its details (validated like the game context: keys
// [A-Za-z0-9_.-]{1,64}, values cut to 256 characters, at most 64). It never
// rejects the event.
func NormalizeCrash(c *models.Crash) ContextStats {
	c.CrashType = strings.ToLower(strings.TrimSpace(c.CrashType))
	if !CrashTypes[c.CrashType] {
		c.CrashType = fingerprint.CrashTypeUnknown
	}
	c.Stack = truncateRunes(c.Stack, MaxCrashStackLength)
	c.Breadcrumbs = capBreadcrumbs(c.Breadcrumbs)
	d := &c.CrashDetails
	d.ExitReason = enumText(d.ExitReason)
	d.Signal = enumText(d.Signal)
	d.SignalCode = enumText(d.SignalCode)
	d.ABI = enumText(d.ABI)
	d.FaultAddr = truncateRunes(printable(d.FaultAddr), maxFaultAddrLength)
	d.ExitDescription = truncateRunes(d.ExitDescription, maxExitDescriptionLength)
	d.AbortMessage = truncateRunes(d.AbortMessage, maxAbortMessageLength)
	d.ExitStatus = models.LenientInt(clampSigned(int64(d.ExitStatus), 1<<31-1))
	d.Importance = models.LenientInt(clamp(int64(d.Importance), maxCrashImportance))
	d.PssKB = models.LenientInt(clamp(int64(d.PssKB), maxCrashMemoryKB))
	d.RssKB = models.LenientInt(clamp(int64(d.RssKB), maxCrashMemoryKB))
	d.SessionMs = models.LenientInt(clamp(int64(d.SessionMs), MaxSessionMs))
	var stats ContextStats
	d.Details, stats = parseContext(d.RawDetails)
	d.RawDetails = nil
	return stats
}

// enumText keeps a short identifier ("CRASH_NATIVE", "SIGSEGV", "arm64-v8a"):
// printable ASCII without spaces, cut to 32 characters.
func enumText(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < maxCrashEnumLength; i++ {
		c := s[i]
		if c > ' ' && c < 0x7f {
			b.WriteByte(c)
		}
	}
	return b.String()
}

func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func clampSigned(v, max int64) int64 {
	if v > max {
		return max
	}
	if v < -max {
		return -max
	}
	return v
}

// CrashResolver names a native frame from uploaded symbols ("" if unknown).
type CrashResolver func(f fingerprint.CrashFrame) string

// ApplyCrashFingerprint computes the server's fingerprint, summary and
// crash site of a (normalized) crash in place, keeping what the SDK sent as
// ClientFingerprint. resolve may be nil (no symbols: native frames of the
// app's libraries are keyed by address). Run it after validation and before
// the duplicate lookup, like ApplyFingerprint for exceptions.
//
// A crash without anything the server can group by (no parsable frame and
// none of the APM-7 fields: a producer other than the APM-7 SDK, or a hand
// written event) keeps the fingerprint its client sent, with
// fingerprint_version 0: the server's would put every such crash of a type
// into one group, and the client's is the only grouping it has.
func ApplyCrashFingerprint(c *models.Crash, threads []fingerprint.CrashThread, resolve CrashResolver) fingerprint.CrashResult {
	if KeepsClientFingerprint(c, threads) {
		d := &c.CrashDetails
		d.ClientFingerprint = c.Fingerprint
		d.FingerprintVersion = 0
		d.CrashSite = ""
		d.Summary = truncateRunes(CrashSummary(c, ""), maxCrashSummaryLength)
		return fingerprint.CrashResult{Fingerprint: c.Fingerprint}
	}
	r := fingerprint.CrashFingerprint(fingerprint.CrashInput{
		CrashType:       c.CrashType,
		Signal:          c.Signal,
		ExitReason:      c.ExitReason,
		ExitDescription: c.ExitDescription,
		Cause:           c.Details["cause"],
		Threads:         threads,
		Resolve:         resolve,
	})
	d := &c.CrashDetails
	d.ClientFingerprint = c.Fingerprint
	c.Fingerprint = r.Fingerprint
	d.FingerprintVersion = uint8(r.Version)
	d.CrashSite = truncateRunes(r.Site, maxCrashSiteLength)
	d.Summary = truncateRunes(CrashSummary(c, r.Site), maxCrashSummaryLength)
	return r
}

// CrashSummary is the one-line description shown in lists and on the
// session time line. site is the first frame key ("libil2cpp.so!Game.Load"
// or "libil2cpp.so+0x5417bc").
func CrashSummary(c *models.Crash, site string) string {
	d := &c.CrashDetails
	where := ""
	if site != "" {
		where = " in " + siteDisplay(site)
	}
	switch c.CrashType {
	case fingerprint.CrashTypeNative:
		sig := d.Signal
		if sig == "" {
			sig = "Native crash"
		}
		if d.SignalCode != "" {
			sig += " (" + d.SignalCode + ")"
		}
		if msg := firstLineOf(d.AbortMessage); msg != "" {
			return sig + ": " + truncateRunes(msg, 160) + where
		}
		// Unity's own tombstone writes "fault addr --------" (no address).
		if strings.Trim(d.FaultAddr, "- ") != "" && (d.Signal == "SIGSEGV" || d.Signal == "SIGBUS") {
			sig += " at " + d.FaultAddr
		}
		return sig + where
	case fingerprint.CrashTypeANR:
		// The same text for the watchdog's report and the exit trace's: the
		// ANR's kind and where the main thread is blocked.
		kind := fingerprint.AnrKind(d.Details["cause"], d.ExitDescription)
		if kind == "ANR" {
			return "ANR" + where
		}
		return "ANR: " + truncateRunes(kind, 120) + where
	case fingerprint.CrashTypeJava:
		if desc := firstLineOf(d.ExitDescription); desc != "" {
			return "Java crash: " + truncateRunes(desc, 200)
		}
		return "Java crash"
	case fingerprint.CrashTypeOOM:
		s := "Killed for low memory"
		if bool(d.Foreground) {
			s += " in the foreground"
		}
		if d.PssKB > 0 {
			s += " (PSS " + strconv.FormatInt(int64(d.PssKB)/1024, 10) + " MB)"
		}
		return s
	case fingerprint.CrashTypeKilled:
		s := "Killed by the system"
		if d.ExitReason != "" {
			s += ": " + d.ExitReason
		}
		if desc := firstLineOf(d.ExitDescription); desc != "" {
			s += " (" + truncateRunes(desc, 160) + ")"
		}
		return s
	}
	return "Ended in the foreground without a clean exit (no exit reason from the system)"
}

// siteDisplay turns a frame key into "Game.Load (libil2cpp.so)".
func siteDisplay(site string) string {
	if i := strings.IndexByte(site, '!'); i > 0 {
		return site[i+1:] + " (" + site[:i] + ")"
	}
	return site
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// KeepsClientFingerprint reports whether a crash carries nothing the server
// fingerprint could use (see ApplyCrashFingerprint): a client fingerprint,
// no frame in its stack, and none of the fields the APM-7 SDK sends.
func KeepsClientFingerprint(c *models.Crash, threads []fingerprint.CrashThread) bool {
	if c.Fingerprint == "" {
		return false
	}
	for _, t := range threads {
		if len(t.Frames) > 0 {
			return false
		}
	}
	d := &c.CrashDetails
	return d.ExitReason == "" && d.Signal == "" && d.ABI == "" && d.AbortMessage == "" &&
		d.ExitDescription == "" && len(d.Details) == 0 && !bool(d.Foreground) && d.SessionMs == 0
}
