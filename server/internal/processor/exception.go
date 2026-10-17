package processor

import (
	"strings"

	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Caps on the APM-4 exception fields (characters). The SDK sends at most
// 8000 characters of embedded exception and 20 breadcrumbs of 200 characters;
// the server allows twice that and cuts the rest, so a misbehaving client
// cannot store arbitrarily large rows.
const (
	MaxEmbeddedExceptionLength = 16000
	MaxBreadcrumbs             = 50
	MaxBreadcrumbLength        = 512
	maxExceptionTypeLength     = 256
	maxSiteLength              = 512
	maxInnerExceptions         = 10
	maxInnerExceptionLength    = 500
)

// ApplyFingerprint fills in the APM-4 fields of an exception event in place:
// it caps what the SDK sent, parses the event (internal/fingerprint) and
// replaces the client's fingerprint with the server's, keeping the client's
// as ClientFingerprint. Run it after validation (which requires the client
// fingerprint) and before the duplicate lookup, which matches on the stored,
// i.e. the server's, fingerprint.
//
// The result depends only on fields that are stored (log_type, message,
// stack, embedded_exception), so a later version of the algorithm can
// recompute it from the table.
func ApplyFingerprint(e *models.Exception) {
	d := &e.ExceptionDetails
	switch d.LogType {
	case fingerprint.LogTypeException, fingerprint.LogTypeError, fingerprint.LogTypeAssert:
	default:
		d.LogType = ""
	}
	d.EmbeddedException = truncateRunes(d.EmbeddedException, MaxEmbeddedExceptionLength)
	d.Breadcrumbs = capBreadcrumbs(d.Breadcrumbs)

	r := fingerprint.Analyze(fingerprint.Input{
		LogType:  d.LogType,
		Message:  e.Message,
		Stack:    e.Stack,
		Embedded: d.EmbeddedException,
	})
	d.ClientFingerprint = e.Fingerprint
	e.Fingerprint = r.Fingerprint
	d.FingerprintVersion = uint8(r.Version)
	d.ExceptionType = truncateRunes(r.Type(), maxExceptionTypeLength)
	d.MessageTemplate = r.MessageTemplate
	d.ThrowSite = truncateRunes(r.ThrowSite, maxSiteLength)
	d.CallSite = truncateRunes(r.CallSite, maxSiteLength)
	inner := r.InnerExceptions()
	if len(inner) > maxInnerExceptions {
		inner = inner[:maxInnerExceptions]
	}
	for i := range inner {
		inner[i] = truncateRunes(inner[i], maxInnerExceptionLength)
	}
	d.InnerExceptions = inner
}

// capBreadcrumbs keeps the last MaxBreadcrumbs entries (the most recent ones),
// each cut to MaxBreadcrumbLength characters, and never returns nil.
func capBreadcrumbs(b []string) []string {
	if len(b) > MaxBreadcrumbs {
		b = b[len(b)-MaxBreadcrumbs:]
	}
	out := make([]string, 0, len(b))
	for _, s := range b {
		out = append(out, truncateRunes(strings.TrimRight(s, "\r\n"), MaxBreadcrumbLength))
	}
	return out
}
