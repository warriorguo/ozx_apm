package alert

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// roundTrip fakes the webhook (no listening socket needed).
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func respond(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

const secretURL = "https://hooks.example.com/services/T000/B000/s3cr3tTOKEN"

func testAlert() Alert {
	return Alert{Key: "new_issue:exception:fp", Type: TypeNewIssue, Severity: SeverityWarning, Title: "New exception: E",
		Kind: "exception", Fingerprint: "fp", AppVersion: "2.0.0", Facts: []Fact{{"Versions", "2.0.0"}},
		AdminPath: "/exceptions/detail?fingerprint=fp", DetectedAt: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
}

func TestNotifier_Formats(t *testing.T) {
	for _, format := range []string{FormatGeneric, FormatSlack, FormatFeishu} {
		n, err := NewNotifier(secretURL, format, "https://apm-admin.local.playquota.com/")
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]interface{}
		var ct string
		n.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
			ct = r.Header.Get("Content-Type")
			b, _ := io.ReadAll(r.Body)
			if err := json.Unmarshal(b, &got); err != nil {
				t.Errorf("%s: body is not JSON: %s", format, b)
			}
			return respond(200, `{"code":0}`), nil
		})
		if err := n.Send(context.Background(), testAlert()); err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if ct != "application/json" {
			t.Errorf("content type %q", ct)
		}
		link := "https://apm-admin.local.playquota.com/exceptions/detail?fingerprint=fp"
		var text string
		switch format {
		case FormatGeneric:
			text, _ = got["text"].(string)
			if got["schema"] != PayloadSchema || got["type"] != TypeNewIssue || got["url"] != link ||
				got["fingerprint"] != "fp" || got["app_version"] != "2.0.0" || got["alert_key"] != "new_issue:exception:fp" {
				t.Errorf("generic payload %v", got)
			}
			if _, ok := got["facts"].([]interface{}); !ok {
				t.Errorf("facts missing: %v", got)
			}
		case FormatSlack:
			text, _ = got["text"].(string)
			if len(got) != 1 {
				t.Errorf("slack payload has extra fields: %v", got)
			}
		case FormatFeishu:
			content, _ := got["content"].(map[string]interface{})
			text, _ = content["text"].(string)
			if got["msg_type"] != "text" {
				t.Errorf("feishu payload %v", got)
			}
		}
		if !strings.HasPrefix(text, "[OZX APM] New exception: E\nVersions: 2.0.0\n") || !strings.HasSuffix(text, link) {
			t.Errorf("%s text %q", format, text)
		}
	}
}

func TestNotifier_FailuresNeverShowTheURL(t *testing.T) {
	n, _ := NewNotifier(secretURL, FormatFeishu, "")
	cases := []struct {
		rt   roundTrip
		want string
	}{
		{func(*http.Request) (*http.Response, error) { return respond(500, "oops"), nil }, "answered 500"},
		{func(*http.Request) (*http.Response, error) {
			return respond(200, `{"code":19021,"msg":"sign match fail @all"}`), nil
		}, "code 19021"},
		{func(*http.Request) (*http.Response, error) { return nil, errors.New("connection refused") }, "connection refused"},
	}
	for _, c := range cases {
		n.client.Transport = c.rt
		err := n.Send(context.Background(), testAlert())
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("error %v, want %q", err, c.want)
			continue
		}
		if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "/services/") || strings.Contains(err.Error(), "@all") {
			t.Errorf("error leaks the URL or game text: %v", err)
		}
		if !strings.Contains(err.Error(), "hooks.example.com") {
			t.Errorf("error does not name the host: %v", err)
		}
	}
}

func TestNotifier_Timeout(t *testing.T) {
	n, _ := NewNotifier(secretURL, FormatGeneric, "")
	n.client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := n.Send(ctx, testAlert())
	if err == nil || strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("timeout error %v", err)
	}
}

func TestNewNotifier_Validation(t *testing.T) {
	for _, u := range []string{"", "ftp://x", "not a url", "https://"} {
		if _, err := NewNotifier(u, "", ""); err == nil {
			t.Errorf("accepted %q", u)
		}
	}
	if _, err := NewNotifier(secretURL, "teams", ""); err == nil {
		t.Error("accepted an unknown format")
	}
	n, err := NewNotifier(secretURL, "", "")
	if err != nil || n.Format() != FormatGeneric || n.Host() != "hooks.example.com" {
		t.Errorf("defaults: %v %+v", err, n)
	}
}

func TestAdminURL(t *testing.T) {
	if AdminURL("", "/x") != "" || AdminURL("https://a", "") != "" {
		t.Error("missing part must give no link")
	}
	if got := AdminURL("https://a/", "/exceptions/detail?fingerprint=a%2Fb"); got != "https://a/exceptions/detail?fingerprint=a%2Fb" {
		t.Errorf("got %q", got)
	}
	if got := issuePath("crash", "a&b=c"); got != "/crashes?fingerprint=a%26b%3Dc" {
		t.Errorf("crash path %q", got)
	}
}
