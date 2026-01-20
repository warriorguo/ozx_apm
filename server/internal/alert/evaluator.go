package alert

import (
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/processor"
)

// Rule represents an alert rule
type Rule struct {
	ID          string
	Name        string
	Type        RuleType
	AppVersion  string // empty means all versions
	Threshold   float64
	WindowSize  time.Duration
	Cooldown    time.Duration
	LastFired   time.Time
}

type RuleType string

const (
	RuleTypeCrashRate      RuleType = "crash_rate"
	RuleTypeExceptionRate  RuleType = "exception_rate"
	RuleTypeJankRate       RuleType = "jank_rate"
	RuleTypeStartupP95     RuleType = "startup_p95"
)

// Alert represents a triggered alert
type Alert struct {
	Rule       *Rule
	AppVersion string
	Value      float64
	Threshold  float64
	Timestamp  time.Time
	Message    string
}

// Evaluator evaluates alert rules against real-time stats
type Evaluator struct {
	rules      []*Rule
	aggregator *processor.Aggregator
	notifier   *Notifier
	logger     *zap.Logger
	mu         sync.RWMutex
	stopCh     chan struct{}
}

func NewEvaluator(aggregator *processor.Aggregator, notifier *Notifier, logger *zap.Logger) *Evaluator {
	return &Evaluator{
		rules:      make([]*Rule, 0),
		aggregator: aggregator,
		notifier:   notifier,
		logger:     logger,
		stopCh:     make(chan struct{}),
	}
}

// AddRule adds an alert rule
func (e *Evaluator) AddRule(rule *Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = append(e.rules, rule)
}

// Start begins the evaluation loop
func (e *Evaluator) Start(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			e.evaluate()
		case <-e.stopCh:
			return
		}
	}
}

// Stop stops the evaluation loop
func (e *Evaluator) Stop() {
	close(e.stopCh)
}

func (e *Evaluator) evaluate() {
	e.mu.RLock()
	rules := make([]*Rule, len(e.rules))
	copy(rules, e.rules)
	e.mu.RUnlock()

	now := time.Now()

	for _, rule := range rules {
		// Check cooldown
		if !rule.LastFired.IsZero() && now.Sub(rule.LastFired) < rule.Cooldown {
			continue
		}

		var value float64
		var shouldFire bool

		switch rule.Type {
		case RuleTypeCrashRate:
			count, _ := e.aggregator.GetCrashRate(rule.AppVersion)
			value = float64(count)
			shouldFire = value >= rule.Threshold

		case RuleTypeExceptionRate:
			count, _ := e.aggregator.GetExceptionRate(rule.AppVersion)
			value = float64(count)
			shouldFire = value >= rule.Threshold

		case RuleTypeJankRate:
			count, _ := e.aggregator.GetJankRate(rule.AppVersion)
			value = float64(count)
			shouldFire = value >= rule.Threshold
		}

		if shouldFire {
			alert := &Alert{
				Rule:       rule,
				AppVersion: rule.AppVersion,
				Value:      value,
				Threshold:  rule.Threshold,
				Timestamp:  now,
				Message:    rule.Name + " threshold exceeded",
			}

			e.logger.Warn("alert triggered",
				zap.String("rule", rule.Name),
				zap.String("type", string(rule.Type)),
				zap.Float64("value", value),
				zap.Float64("threshold", rule.Threshold),
			)

			if e.notifier != nil {
				e.notifier.Send(alert)
			}

			e.mu.Lock()
			rule.LastFired = now
			e.mu.Unlock()
		}
	}
}

// DefaultRules returns a set of default alert rules
func DefaultRules() []*Rule {
	return []*Rule{
		{
			ID:         "crash_spike",
			Name:       "Crash Rate Spike",
			Type:       RuleTypeCrashRate,
			Threshold:  10, // 10 crashes per minute
			WindowSize: time.Minute,
			Cooldown:   5 * time.Minute,
		},
		{
			ID:         "exception_spike",
			Name:       "Exception Rate Spike",
			Type:       RuleTypeExceptionRate,
			Threshold:  100, // 100 exceptions per minute
			WindowSize: time.Minute,
			Cooldown:   5 * time.Minute,
		},
		{
			ID:         "jank_spike",
			Name:       "Jank Rate Spike",
			Type:       RuleTypeJankRate,
			Threshold:  50, // 50 janks per minute
			WindowSize: time.Minute,
			Cooldown:   5 * time.Minute,
		},
	}
}
