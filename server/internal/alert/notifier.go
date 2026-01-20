package alert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.uber.org/zap"
)

// Notifier sends alerts to configured destinations
type Notifier struct {
	webhookURL string
	httpClient *http.Client
	logger     *zap.Logger
}

func NewNotifier(webhookURL string, logger *zap.Logger) *Notifier {
	return &Notifier{
		webhookURL: webhookURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger,
	}
}

// WebhookPayload is the payload sent to webhook endpoints
type WebhookPayload struct {
	AlertName  string    `json:"alert_name"`
	AlertType  string    `json:"alert_type"`
	AppVersion string    `json:"app_version,omitempty"`
	Value      float64   `json:"value"`
	Threshold  float64   `json:"threshold"`
	Message    string    `json:"message"`
	Timestamp  time.Time `json:"timestamp"`
}

// Send sends an alert to configured destinations
func (n *Notifier) Send(alert *Alert) error {
	if n.webhookURL == "" {
		return nil
	}

	payload := WebhookPayload{
		AlertName:  alert.Rule.Name,
		AlertType:  string(alert.Rule.Type),
		AppVersion: alert.AppVersion,
		Value:      alert.Value,
		Threshold:  alert.Threshold,
		Message:    alert.Message,
		Timestamp:  alert.Timestamp,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, n.webhookURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		n.logger.Error("failed to send webhook",
			zap.String("url", n.webhookURL),
			zap.Error(err),
		)
		return fmt.Errorf("send webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		n.logger.Error("webhook returned error",
			zap.String("url", n.webhookURL),
			zap.Int("status", resp.StatusCode),
		)
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	n.logger.Info("alert sent",
		zap.String("alert", alert.Rule.Name),
		zap.String("url", n.webhookURL),
	)

	return nil
}
