package alert

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Alert types.
const (
	TypeNewIssue        = "new_issue"
	TypeNewIssueDigest  = "new_issue_digest"
	TypeRegression      = "regression"
	TypeCrashRateSpike  = "crash_rate_spike"
	TypeStartupP95      = "startup_p95_regression"
	TypeTest            = "test"
	typeBaselineMarker  = "baseline"
	PayloadSchema       = "ozx-apm-alert/1"
	maxGameTextInTitle  = 120
	maxGameTextInFact   = 300
	maxGameTextInText   = 300
	maxFingerprintChars = 80
)

// Severities.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Fact is one labelled value of an alert, in display order.
type Fact struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// Alert is what an alert says. It is stored as JSON in the pending outbox
// row when the alert is detected, so every instance sends the same content;
// the admin link and the text are added when it is sent (Payload).
//
// Every string that comes from game data is passed through CleanText
// before it is put here.
type Alert struct {
	Key         string             `json:"alert_key"`
	Type        string             `json:"type"`
	Severity    string             `json:"severity"`
	Title       string             `json:"title"`
	Kind        string             `json:"kind,omitempty"`
	Fingerprint string             `json:"fingerprint,omitempty"`
	AppVersion  string             `json:"app_version,omitempty"`
	Facts       []Fact             `json:"facts"`
	Metrics     map[string]float64 `json:"metrics,omitempty"`
	AdminPath   string             `json:"admin_path,omitempty"`
	DetectedAt  time.Time          `json:"detected_at"`
}

// Payload is the generic JSON body (webhook_format "generic"): the alert,
// the absolute admin link and a plain-text rendering usable as the message
// of a chat webhook.
type Payload struct {
	Schema string `json:"schema"`
	Alert
	URL  string `json:"url,omitempty"`
	Text string `json:"text"`
}

// Webhook formats.
const (
	FormatGeneric = "generic"
	FormatSlack   = "slack"
	FormatFeishu  = "feishu"
)

// ValidFormat reports whether f is a known webhook format.
func ValidFormat(f string) bool {
	return f == FormatGeneric || f == FormatSlack || f == FormatFeishu
}

// AdminURL joins the admin base URL and an alert's admin path ("" when
// either is missing).
func AdminURL(base, path string) string {
	if base == "" || path == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + path
}

// Text renders the alert as plain text: the title, one line per fact, the
// admin link. The values are already inert (CleanText).
func (a Alert) Text(link string) string {
	var sb strings.Builder
	sb.WriteString("[OZX APM] ")
	sb.WriteString(a.Title)
	for _, f := range a.Facts {
		sb.WriteString("\n")
		sb.WriteString(f.Label)
		sb.WriteString(": ")
		sb.WriteString(f.Value)
	}
	if link != "" {
		sb.WriteString("\n")
		sb.WriteString(link)
	}
	return sb.String()
}

// Body returns the request body for the webhook format.
func (a Alert) Body(format, adminBaseURL string) ([]byte, error) {
	link := AdminURL(adminBaseURL, a.AdminPath)
	text := a.Text(link)
	switch format {
	case FormatSlack:
		return json.Marshal(map[string]string{"text": text})
	case FormatFeishu:
		return json.Marshal(map[string]interface{}{
			"msg_type": "text",
			"content":  map[string]string{"text": text},
		})
	default:
		// The key contains a fingerprint and a version (game data).
		a.Key = CleanText(a.Key, 400)
		return json.Marshal(Payload{Schema: PayloadSchema, Alert: a, URL: link, Text: text})
	}
}

// exceptionPath and crashPath are the admin pages of an issue.
func issuePath(kind, fingerprint string) string {
	q := url.Values{"fingerprint": {fingerprint}}.Encode()
	if kind == "crash" {
		return "/crashes?" + q
	}
	return "/exceptions/detail?" + q
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04 UTC")
}

func fmtPercent(x float64) string {
	return fmt.Sprintf("%.2f%%", x*100)
}

func kindLabel(kind string) string {
	if kind == "crash" {
		return "crash"
	}
	return "exception"
}

// versionList renders up to n cleaned version names.
func versionList(versions []string, n int) string {
	var out []string
	for i, v := range versions {
		if i == n {
			out = append(out, fmt.Sprintf("+%d more", len(versions)-n))
			break
		}
		out = append(out, CleanText(v, 60))
	}
	return strings.Join(out, ", ")
}
