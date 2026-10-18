package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Sender delivers one alert (one attempt). The evaluator retries failed
// attempts with backoff through the outbox.
type Sender interface {
	Send(ctx context.Context, a Alert) error
}

// Notifier posts alerts to one webhook URL. The URL usually contains a
// secret (Slack, Feishu): it is never logged, stored or returned; errors
// name only its host.
type Notifier struct {
	webhookURL   string
	host         string
	format       string
	adminBaseURL string
	client       *http.Client
}

// WebhookTimeout bounds one webhook request (connect, send, response).
const WebhookTimeout = 10 * time.Second

// NewNotifier checks the URL (http or https, with a host) and the format.
func NewNotifier(webhookURL, format, adminBaseURL string) (*Notifier, error) {
	u, err := url.Parse(webhookURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("alert webhook URL must be an http or https URL")
	}
	if format == "" {
		format = FormatGeneric
	}
	if !ValidFormat(format) {
		return nil, fmt.Errorf("alert webhook format must be generic, slack or feishu")
	}
	return &Notifier{
		webhookURL:   webhookURL,
		host:         u.Host,
		format:       format,
		adminBaseURL: adminBaseURL,
		client: &http.Client{
			Timeout: WebhookTimeout,
			// A redirect would turn the POST into a GET without the message
			// and its 2xx would count as delivered: a 3xx is a failure.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Host is the webhook's host, for logs.
func (n *Notifier) Host() string { return n.host }

// Format is the webhook format.
func (n *Notifier) Format() string { return n.format }

// Send posts the alert once. A 2xx answer is success, except a Feishu answer
// whose JSON has a non-zero "code" (Feishu answers 200 to rejected messages).
func (n *Notifier) Send(ctx context.Context, a Alert) error {
	body, err := a.Body(n.format, n.adminBaseURL)
	if err != nil {
		return fmt.Errorf("encode alert: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, WebhookTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.webhookURL, bytes.NewReader(body))
	if err != nil {
		return errors.New("build webhook request: invalid URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ozx-apm-alert/1")
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook %s: %s", n.host, redactURLError(err))
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook %s answered %d", n.host, resp.StatusCode)
	}
	if n.format == FormatFeishu {
		var r struct {
			Code *int   `json:"code"`
			Msg  string `json:"msg"`
		}
		if json.Unmarshal(respBody, &r) == nil && r.Code != nil && *r.Code != 0 {
			return fmt.Errorf("webhook %s rejected the message: code %d %s", n.host, *r.Code, CleanText(r.Msg, 200))
		}
	}
	return nil
}

// redactURLError returns the error without the request URL (a *url.Error
// prints it, secret included).
func redactURLError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return "timeout"
		}
		return ue.Err.Error()
	}
	return err.Error()
}
