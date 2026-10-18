package alert

import (
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/config"
)

// MinInterval is the shortest evaluation interval accepted.
const MinInterval = 10 * time.Second

// ConfigFrom converts the server configuration, raising values that would
// make the evaluator spin or alert on nothing to safe minimums.
func ConfigFrom(c config.AlertConfig) Config {
	atLeast := func(d, min time.Duration) time.Duration {
		if d < min {
			return min
		}
		return d
	}
	cfg := Config{
		Interval:                atLeast(c.Interval, MinInterval),
		IncludeDevBuilds:        c.IncludeDevBuilds,
		NewIssueMaxPerCycle:     c.NewIssueMaxPerCycle,
		NewIssueMaxPerHour:      c.NewIssueMaxPerHour,
		NewIssueWindow:          atLeast(c.NewIssueWindow, time.Hour),
		SweepInterval:           atLeast(c.SweepInterval, 10*time.Minute),
		CrashRateWindow:         atLeast(c.CrashRateWindow, 5*time.Minute),
		CrashRateBaselineWindow: atLeast(c.CrashRateBaselineWindow, time.Hour),
		CrashRateCheckInterval:  atLeast(c.CrashRateCheckInterval, time.Minute),
		CrashRateMinSessions:    c.CrashRateMinSessions,
		CrashRateMinCrashed:     c.CrashRateMinCrashed,
		CrashRateMinRate:        c.CrashRateMinRate,
		CrashRateFactor:         c.CrashRateFactor,
		CrashRateCooldown:       atLeast(c.CrashRateCooldown, 10*time.Minute),
		StartupEnabled:          c.StartupEnabled,
		StartupWindow:           atLeast(c.StartupWindow, time.Hour),
		StartupCheckInterval:    atLeast(c.StartupCheckInterval, 5*time.Minute),
		StartupMinSamples:       c.StartupMinSamples,
		StartupIncrease:         c.StartupIncrease,
		StartupMinDeltaMs:       c.StartupMinDeltaMs,
	}
	if cfg.NewIssueMaxPerCycle < 1 {
		cfg.NewIssueMaxPerCycle = 1
	}
	if cfg.NewIssueMaxPerHour < cfg.NewIssueMaxPerCycle {
		cfg.NewIssueMaxPerHour = cfg.NewIssueMaxPerCycle
	}
	if cfg.CrashRateMinSessions < 1 {
		cfg.CrashRateMinSessions = 1
	}
	if cfg.CrashRateMinCrashed < 1 {
		cfg.CrashRateMinCrashed = 1
	}
	if cfg.StartupMinSamples < 1 {
		cfg.StartupMinSamples = 1
	}
	return cfg
}
