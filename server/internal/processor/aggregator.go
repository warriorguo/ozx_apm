package processor

import (
	"sync"
	"time"
)

// RealTimeStats holds real-time aggregated statistics
type RealTimeStats struct {
	mu sync.RWMutex

	// Per-version crash counts for the last minute
	CrashCounts map[string]*VersionCrashStats

	// Per-version exception counts for the last minute
	ExceptionCounts map[string]*VersionExceptionStats

	// Per-version jank counts for the last minute
	JankCounts map[string]*VersionJankStats
}

type VersionCrashStats struct {
	AppVersion string
	Count      int64
	LastSeen   time.Time
	Sessions   map[string]struct{} // unique sessions
}

type VersionExceptionStats struct {
	AppVersion string
	Count      int64
	LastSeen   time.Time
	Sessions   map[string]struct{}
}

type VersionJankStats struct {
	AppVersion string
	Count      int64
	LastSeen   time.Time
	Sessions   map[string]struct{}
}

type Aggregator struct {
	stats        *RealTimeStats
	windowSize   time.Duration
	cleanupTick  time.Duration
	stopCh       chan struct{}
}

func NewAggregator() *Aggregator {
	a := &Aggregator{
		stats: &RealTimeStats{
			CrashCounts:     make(map[string]*VersionCrashStats),
			ExceptionCounts: make(map[string]*VersionExceptionStats),
			JankCounts:      make(map[string]*VersionJankStats),
		},
		windowSize:  time.Minute,
		cleanupTick: 10 * time.Second,
		stopCh:      make(chan struct{}),
	}
	go a.cleanupLoop()
	return a
}

func (a *Aggregator) Stop() {
	close(a.stopCh)
}

func (a *Aggregator) cleanupLoop() {
	ticker := time.NewTicker(a.cleanupTick)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.cleanup()
		case <-a.stopCh:
			return
		}
	}
}

func (a *Aggregator) cleanup() {
	a.stats.mu.Lock()
	defer a.stats.mu.Unlock()

	cutoff := time.Now().Add(-a.windowSize)

	for k, v := range a.stats.CrashCounts {
		if v.LastSeen.Before(cutoff) {
			delete(a.stats.CrashCounts, k)
		}
	}

	for k, v := range a.stats.ExceptionCounts {
		if v.LastSeen.Before(cutoff) {
			delete(a.stats.ExceptionCounts, k)
		}
	}

	for k, v := range a.stats.JankCounts {
		if v.LastSeen.Before(cutoff) {
			delete(a.stats.JankCounts, k)
		}
	}
}

// RecordCrash records a crash event for real-time stats
func (a *Aggregator) RecordCrash(appVersion, sessionID string) {
	a.stats.mu.Lock()
	defer a.stats.mu.Unlock()

	stats, ok := a.stats.CrashCounts[appVersion]
	if !ok {
		stats = &VersionCrashStats{
			AppVersion: appVersion,
			Sessions:   make(map[string]struct{}),
		}
		a.stats.CrashCounts[appVersion] = stats
	}

	stats.Count++
	stats.LastSeen = time.Now()
	stats.Sessions[sessionID] = struct{}{}
}

// RecordException records an exception event for real-time stats
func (a *Aggregator) RecordException(appVersion, sessionID string) {
	a.stats.mu.Lock()
	defer a.stats.mu.Unlock()

	stats, ok := a.stats.ExceptionCounts[appVersion]
	if !ok {
		stats = &VersionExceptionStats{
			AppVersion: appVersion,
			Sessions:   make(map[string]struct{}),
		}
		a.stats.ExceptionCounts[appVersion] = stats
	}

	stats.Count++
	stats.LastSeen = time.Now()
	stats.Sessions[sessionID] = struct{}{}
}

// RecordJank records a jank event for real-time stats
func (a *Aggregator) RecordJank(appVersion, sessionID string) {
	a.stats.mu.Lock()
	defer a.stats.mu.Unlock()

	stats, ok := a.stats.JankCounts[appVersion]
	if !ok {
		stats = &VersionJankStats{
			AppVersion: appVersion,
			Sessions:   make(map[string]struct{}),
		}
		a.stats.JankCounts[appVersion] = stats
	}

	stats.Count++
	stats.LastSeen = time.Now()
	stats.Sessions[sessionID] = struct{}{}
}

// GetCrashRate returns crashes per minute for a version
func (a *Aggregator) GetCrashRate(appVersion string) (count int64, sessions int) {
	a.stats.mu.RLock()
	defer a.stats.mu.RUnlock()

	if stats, ok := a.stats.CrashCounts[appVersion]; ok {
		return stats.Count, len(stats.Sessions)
	}
	return 0, 0
}

// GetExceptionRate returns exceptions per minute for a version
func (a *Aggregator) GetExceptionRate(appVersion string) (count int64, sessions int) {
	a.stats.mu.RLock()
	defer a.stats.mu.RUnlock()

	if stats, ok := a.stats.ExceptionCounts[appVersion]; ok {
		return stats.Count, len(stats.Sessions)
	}
	return 0, 0
}

// GetJankRate returns janks per minute for a version
func (a *Aggregator) GetJankRate(appVersion string) (count int64, sessions int) {
	a.stats.mu.RLock()
	defer a.stats.mu.RUnlock()

	if stats, ok := a.stats.JankCounts[appVersion]; ok {
		return stats.Count, len(stats.Sessions)
	}
	return 0, 0
}
