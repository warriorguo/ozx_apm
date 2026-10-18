package alert

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// The alert evaluator (APM-8) reads ClickHouse, never ingest's memory, so a
// restart neither loses nor repeats alerts, and any number of instances may
// run it: every alert has a deterministic key, detection skips keys that
// already have a row in apm_alerts, and an attempt to send is made only by
// the instance whose claim of it is first (Evaluator.send).
//
// One evaluation (Cycle), every Interval:
//
//  1. baseline (once per kind, ever): every fingerprint in the retained rows
//     is recorded as known without an alert, so enabling alerting on an
//     existing deployment does not report every old issue as new;
//  2. new issues: fingerprints with rows in the last NewIssueWindow (event
//     time; the whole retention every SweepInterval, for late uploads) that
//     are not known yet. Known = has an apm_alerts row of type new_issue,
//     which is never deleted, so an issue that stops occurring for longer
//     than the 30-day event retention and comes back is not new again;
//  3. regressions: resolved issues with rows after their resolution in a
//     version newer than the one they were resolved in (models.IssueState);
//     the issue is reopened;
//  4. crash rate spike, every CrashRateCheckInterval;
//  5. startup P95 regression, every StartupCheckInterval;
//  6. sending: pending alerts are claimed and posted, failed attempts are
//     retried with backoff, at most maxSendsPerCycle per evaluation.
//
// Queries run one at a time (one pooled connection at most; the pool is
// shared with ingest), each bounded by LIMIT.

// Store is what the evaluator needs from ClickHouse (*storage.Repository).
type Store interface {
	AlertBaselineDone(ctx context.Context, key string) (bool, error)
	AllIssueFingerprints(ctx context.Context, kind string, includeDev bool, limit int) ([]models.IssueCandidate, error)
	NewIssueCandidates(ctx context.Context, kind string, from time.Time, includeDev bool, limit int) ([]models.IssueCandidate, error)
	ResolvedIssues(ctx context.Context, kind string, limit int) ([]models.IssueState, error)
	IssueHits(ctx context.Context, kind string, fingerprints []string, from time.Time, includeDev bool) ([]models.IssueVersionHit, error)
	IssueHitSince(ctx context.Context, kind, fingerprint, version string, since time.Time, includeDev bool) (models.IssueVersionHit, error)
	VersionFirstSeen(ctx context.Context, includeDev bool) (map[string]time.Time, error)
	InsertIssueState(ctx context.Context, s models.IssueState) error
	CrashRates(ctx context.Context, from, to time.Time, includeDev bool) ([]models.VersionRate, error)
	StartupStats(ctx context.Context, from time.Time, includeDev bool) ([]models.VersionStartup, error)
	InsertAlertRecords(ctx context.Context, recs []models.AlertRecord) error
	ExistingAlertKeys(ctx context.Context, keys []string) (map[string]bool, error)
	RecentAlertExists(ctx context.Context, alertType, appVersion string, since time.Time) (bool, error)
	AlertsToSend(ctx context.Context, since time.Time, limit int) ([]models.AlertOutboxEntry, error)
	AlertClaims(ctx context.Context, keys []string) ([]models.AlertClaim, error)
}

// Config is the evaluator's configuration (config.AlertConfig).
type Config struct {
	Interval         time.Duration
	IncludeDevBuilds bool

	NewIssueMaxPerCycle int
	NewIssueWindow      time.Duration
	SweepInterval       time.Duration

	CrashRateWindow         time.Duration
	CrashRateBaselineWindow time.Duration
	CrashRateCheckInterval  time.Duration
	CrashRateMinSessions    int64
	CrashRateMinCrashed     int64
	CrashRateMinRate        float64
	CrashRateFactor         float64
	CrashRateCooldown       time.Duration

	StartupEnabled       bool
	StartupWindow        time.Duration
	StartupCheckInterval time.Duration
	StartupMinSamples    int64
	StartupIncrease      float64
	StartupMinDeltaMs    float64
}

const (
	// retention bounds a sweep: event rows are kept 30 days.
	retention = 31 * 24 * time.Hour
	// maxCandidates bounds the new issues read per kind and evaluation.
	maxCandidates = 200
	// maxBaseline bounds the fingerprints recorded by a baseline.
	maxBaseline = 200000
	// maxResolved bounds the resolved issues checked for regressions.
	maxResolved = 1000
	// maxSendsPerCycle bounds the webhook requests per evaluation.
	maxSendsPerCycle = 20
	// MaxAttempts: an alert is abandoned after this many failed attempts.
	MaxAttempts = 6
	// sendWindow: pending alerts older than this are no longer sent.
	sendWindow = 24 * time.Hour
	// claimStale: a claim without an outcome after this long is a failed
	// attempt (its instance stopped while sending).
	claimStale = 2 * time.Minute
	// claimSettle: the wait between writing claims and reading them back, so
	// that a concurrent instance's claims are visible (inserts are
	// synchronous; this only covers clock skew and in-flight inserts).
	claimSettle = 2 * time.Second
	// cycleTimeout bounds one evaluation.
	cycleTimeout = 3 * time.Minute
	// firstCycleDelay: the first evaluation starts after the server is up.
	firstCycleDelay = 15 * time.Second
)

// Evaluator runs the alert rules. Run it in one goroutine.
type Evaluator struct {
	store    Store
	sender   Sender
	cfg      Config
	logger   *zap.Logger
	instance string

	now   func() time.Time
	sleep func(ctx context.Context, d time.Duration) error

	baselineDone     map[string]bool
	lastSweep        time.Time
	lastCrashCheck   time.Time
	lastStartupCheck time.Time
	crashBase        struct {
		at              time.Time
		sessions        int64
		crashedSessions int64
	}
}

// NewEvaluator returns an evaluator; sender may be nil (detection only, as
// in tests).
func NewEvaluator(store Store, sender Sender, cfg Config, logger *zap.Logger) *Evaluator {
	return &Evaluator{
		store:        store,
		sender:       sender,
		cfg:          cfg,
		logger:       logger,
		instance:     InstanceID(),
		now:          time.Now,
		sleep:        sleepCtx,
		baselineDone: map[string]bool{},
	}
}

// InstanceID names this process in claims: host name (the pod) and a random
// suffix (a restarted process is another instance).
func InstanceID() string {
	host, _ := os.Hostname()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return CleanText(host, 60) + "-" + hex.EncodeToString(b)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Run evaluates every Interval until ctx is done.
func (e *Evaluator) Run(ctx context.Context) {
	if err := e.sleep(ctx, firstCycleDelay); err != nil {
		return
	}
	ticker := time.NewTicker(e.cfg.Interval)
	defer ticker.Stop()
	for {
		cctx, cancel := context.WithTimeout(ctx, cycleTimeout)
		e.Cycle(cctx)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Cycle runs one evaluation. Each step's error is logged and the next step
// still runs.
func (e *Evaluator) Cycle(ctx context.Context) {
	now := e.now()
	sweep := e.lastSweep.IsZero() || now.Sub(e.lastSweep) >= e.cfg.SweepInterval
	sweepOK := true
	for _, kind := range models.IssueKinds {
		if err := e.ensureBaseline(ctx, kind); err != nil {
			e.logger.Error("alert: baseline failed", zap.String("kind", kind), zap.Error(err))
			sweepOK = false
			continue
		}
		if err := e.detectNewIssues(ctx, kind, now, sweep); err != nil {
			e.logger.Error("alert: new issue detection failed", zap.String("kind", kind), zap.Error(err))
			sweepOK = false
		}
		if err := e.detectRegressions(ctx, kind, now, sweep); err != nil {
			e.logger.Error("alert: regression detection failed", zap.String("kind", kind), zap.Error(err))
			sweepOK = false
		}
	}
	if sweep && sweepOK {
		e.lastSweep = now
	}
	if e.lastCrashCheck.IsZero() || now.Sub(e.lastCrashCheck) >= e.cfg.CrashRateCheckInterval {
		if err := e.detectCrashRate(ctx, now); err != nil {
			e.logger.Error("alert: crash rate check failed", zap.Error(err))
		} else {
			e.lastCrashCheck = now
		}
	}
	if e.cfg.StartupEnabled && (e.lastStartupCheck.IsZero() || now.Sub(e.lastStartupCheck) >= e.cfg.StartupCheckInterval) {
		if err := e.detectStartupRegression(ctx, now); err != nil {
			e.logger.Error("alert: startup check failed", zap.Error(err))
		} else {
			e.lastStartupCheck = now
		}
	}
	if err := e.send(ctx); err != nil {
		e.logger.Error("alert: sending failed", zap.Error(err))
	}
}

// Alert keys. Deterministic: two instances detecting the same thing write
// the same key.
func newIssueKey(kind, fp string) string { return TypeNewIssue + ":" + kind + ":" + fp }
func baselineKey(kind string) string     { return typeBaselineMarker + ":" + kind }
func regressionKey(kind, fp string, stateVersion uint64) string {
	return fmt.Sprintf("%s:%s:%s:%d", TypeRegression, kind, fp, stateVersion)
}

func (e *Evaluator) ensureBaseline(ctx context.Context, kind string) error {
	if e.baselineDone[kind] {
		return nil
	}
	key := baselineKey(kind)
	done, err := e.store.AlertBaselineDone(ctx, key)
	if err != nil {
		return err
	}
	if done {
		e.baselineDone[kind] = true
		return nil
	}
	fps, err := e.store.AllIssueFingerprints(ctx, kind, e.cfg.IncludeDevBuilds, maxBaseline)
	if err != nil {
		return err
	}
	now := e.now()
	recs := make([]models.AlertRecord, 0, len(fps)+1)
	for _, c := range fps {
		recs = append(recs, models.AlertRecord{AlertKey: newIssueKey(kind, c.Fingerprint), At: now, Status: models.AlertKnown,
			Instance: e.instance, Type: TypeNewIssue, Kind: kind, Fingerprint: c.Fingerprint, AppVersion: c.FirstVersion, Error: "baseline"})
	}
	// The marker last: if an insert fails, the next evaluation repeats the
	// baseline (duplicate known rows are harmless).
	for start := 0; start < len(recs); start += 10000 {
		end := start + 10000
		if end > len(recs) {
			end = len(recs)
		}
		if err := e.store.InsertAlertRecords(ctx, recs[start:end]); err != nil {
			return err
		}
	}
	if err := e.store.InsertAlertRecords(ctx, []models.AlertRecord{{AlertKey: key, At: now, Status: models.AlertKnown,
		Instance: e.instance, Type: typeBaselineMarker, Kind: kind, Error: fmt.Sprintf("%d fingerprints", len(fps))}}); err != nil {
		return err
	}
	e.baselineDone[kind] = true
	e.logger.Info("alert: baseline recorded; existing issues are not reported as new",
		zap.String("kind", kind), zap.Int("fingerprints", len(fps)))
	return nil
}

func (e *Evaluator) detectNewIssues(ctx context.Context, kind string, now time.Time, sweep bool) error {
	from := now.Add(-e.cfg.NewIssueWindow)
	if sweep {
		from = now.Add(-retention)
	}
	cands, err := e.store.NewIssueCandidates(ctx, kind, from, e.cfg.IncludeDevBuilds, maxCandidates)
	if err != nil || len(cands) == 0 {
		return err
	}
	max := e.cfg.NewIssueMaxPerCycle
	if max < 1 {
		max = 1
	}
	var recs []models.AlertRecord
	for i, c := range cands {
		rec := models.AlertRecord{AlertKey: newIssueKey(kind, c.Fingerprint), At: now, Instance: e.instance,
			Type: TypeNewIssue, Kind: kind, Fingerprint: c.Fingerprint, AppVersion: c.FirstVersion}
		if i < max {
			a := newIssueAlert(c, now)
			a.Key = rec.AlertKey
			payload, err := json.Marshal(a)
			if err != nil {
				return err
			}
			rec.Status, rec.Payload = models.AlertPending, string(payload)
		} else {
			rec.Status, rec.Error = models.AlertKnown, "suppressed: in the digest of "+digestKey(kind, cands[max:])
		}
		recs = append(recs, rec)
	}
	if len(cands) > max {
		a := digestAlert(kind, cands[max:], now)
		payload, err := json.Marshal(a)
		if err != nil {
			return err
		}
		recs = append(recs, models.AlertRecord{AlertKey: a.Key, At: now, Status: models.AlertPending, Instance: e.instance,
			Type: TypeNewIssueDigest, Kind: kind, Payload: string(payload)})
	}
	if err := e.store.InsertAlertRecords(ctx, recs); err != nil {
		return err
	}
	e.logger.Info("alert: new issues detected", zap.String("kind", kind), zap.Int("count", len(cands)),
		zap.Int("individual", min(len(cands), max)))
	return nil
}

func digestKey(kind string, rest []models.IssueCandidate) string {
	return TypeNewIssueDigest + ":" + kind + ":" + rest[0].Fingerprint
}

func newIssueAlert(c models.IssueCandidate, now time.Time) Alert {
	what := CleanText(c.Title, maxGameTextInTitle)
	if what == "" {
		what = "(no type)"
	}
	title := "New " + kindLabel(c.Kind) + ": " + what
	if c.Site != "" {
		title += " at " + CleanText(c.Site, maxGameTextInTitle)
	}
	facts := []Fact{
		{"First seen", fmtTime(c.FirstSeen) + " in " + CleanText(c.FirstVersion, 60)},
		{"Versions", versionList(c.Versions, 5)},
		{"Occurrences", fmt.Sprintf("%d in %d sessions (since first seen, as of detection)", c.Count, c.Sessions)},
	}
	msg := c.Message
	if c.Kind == models.IssueKindCrash {
		msg = FirstLine(msg)
	}
	if m := CleanText(msg, maxGameTextInFact); m != "" {
		label := "Message"
		if c.Kind == models.IssueKindCrash {
			label = "Stack"
		}
		facts = append(facts, Fact{label, "\"" + m + "\""})
	}
	facts = append(facts, Fact{"Fingerprint", CleanText(c.Fingerprint, maxFingerprintChars)})
	sev := SeverityWarning
	if c.Kind == models.IssueKindCrash {
		sev = SeverityCritical
	}
	return Alert{
		Type: TypeNewIssue, Severity: sev, Title: title, Kind: c.Kind,
		Fingerprint: CleanText(c.Fingerprint, maxFingerprintChars), AppVersion: CleanText(c.FirstVersion, 60),
		Facts: facts, Metrics: map[string]float64{"count": float64(c.Count), "sessions": float64(c.Sessions)},
		AdminPath: issuePath(c.Kind, c.Fingerprint), DetectedAt: now.UTC(),
	}
}

func digestAlert(kind string, rest []models.IssueCandidate, now time.Time) Alert {
	var examples []string
	for i, c := range rest {
		if i == 5 {
			break
		}
		examples = append(examples, CleanText(c.Title, 60)+" ("+CleanText(c.FirstVersion, 40)+")")
	}
	path := "/exceptions?sort=first_seen"
	if kind == models.IssueKindCrash {
		path = "/crashes"
	}
	return Alert{
		Key: digestKey(kind, rest), Type: TypeNewIssueDigest, Severity: SeverityWarning,
		Title: fmt.Sprintf("%d more new %ss (not reported one by one)", len(rest), kindLabel(kind)),
		Kind:  kind,
		Facts: []Fact{
			{"Examples", strings.Join(examples, "; ")},
			{"Why", fmt.Sprintf("more new %ss than the per-evaluation limit; often a new fingerprint algorithm or a broken build", kindLabel(kind))},
		},
		Metrics:   map[string]float64{"count": float64(len(rest))},
		AdminPath: path, DetectedAt: now.UTC(),
	}
}

// regressedIn returns the newest version among hits that counts as a
// regression of the resolved issue: rows after the resolution in a version
// newer than its threshold (ResolvedInVersion inclusive, else newer than
// LastSeenVersion; any version when both are empty).
func regressedIn(st models.IssueState, hits []models.IssueVersionHit, order VersionOrder) string {
	if st.ResolvedAt == nil {
		return ""
	}
	threshold, inclusive := st.ResolvedInVersion, true
	if threshold == "" {
		threshold, inclusive = st.LastSeenVersion, false
	}
	var qualifying []string
	for _, h := range hits {
		if h.Fingerprint != st.Fingerprint || h.LastSeen.Before(*st.ResolvedAt) {
			continue
		}
		if threshold == "" {
			qualifying = append(qualifying, h.AppVersion)
			continue
		}
		// A version without a known first-seen time counts as seen when its
		// rows here were first seen; the threshold version as seen at the
		// resolution.
		c := order.Compare(h.AppVersion, threshold, h.FirstSeen, *st.ResolvedAt)
		if c > 0 || (inclusive && c == 0) {
			qualifying = append(qualifying, h.AppVersion)
		}
	}
	return order.Newest(qualifying, *st.ResolvedAt)
}

// needsTimeOrder reports whether any hit's version cannot be compared with
// the issue's threshold by name.
func needsTimeOrder(states []models.IssueState, hits []models.IssueVersionHit) bool {
	thresholds := map[string]string{}
	for _, st := range states {
		t := st.ResolvedInVersion
		if t == "" {
			t = st.LastSeenVersion
		}
		thresholds[st.Fingerprint] = t
	}
	for _, h := range hits {
		t := thresholds[h.Fingerprint]
		if t == "" {
			continue
		}
		if _, ok := CompareVersions(h.AppVersion, t); !ok {
			return true
		}
	}
	return false
}

func (e *Evaluator) detectRegressions(ctx context.Context, kind string, now time.Time, sweep bool) error {
	states, err := e.store.ResolvedIssues(ctx, kind, maxResolved)
	if err != nil || len(states) == 0 {
		return err
	}
	earliest := now
	fps := make([]string, 0, len(states))
	for _, st := range states {
		if st.ResolvedAt != nil && st.ResolvedAt.Before(earliest) {
			earliest = *st.ResolvedAt
		}
		fps = append(fps, st.Fingerprint)
	}
	from := now.Add(-e.cfg.NewIssueWindow)
	if sweep {
		from = now.Add(-retention)
	}
	if earliest.After(from) {
		from = earliest
	}
	hits, err := e.store.IssueHits(ctx, kind, fps, from, e.cfg.IncludeDevBuilds)
	if err != nil || len(hits) == 0 {
		return err
	}
	order := VersionOrder{FirstSeen: map[string]time.Time{}}
	if needsTimeOrder(states, hits) {
		if order.FirstSeen, err = e.store.VersionFirstSeen(ctx, e.cfg.IncludeDevBuilds); err != nil {
			return err
		}
	}
	type regression struct {
		st      models.IssueState
		version string
		key     string
	}
	var found []regression
	var keys []string
	for _, st := range states {
		if v := regressedIn(st, hits, order); v != "" {
			key := regressionKey(kind, st.Fingerprint, st.Version)
			found = append(found, regression{st, v, key})
			keys = append(keys, key)
		}
	}
	if len(found) == 0 {
		return nil
	}
	existing, err := e.store.ExistingAlertKeys(ctx, keys)
	if err != nil {
		return err
	}
	for _, r := range found {
		if existing[r.key] {
			continue
		}
		hit, err := e.store.IssueHitSince(ctx, kind, r.st.Fingerprint, r.version, *r.st.ResolvedAt, e.cfg.IncludeDevBuilds)
		if err != nil {
			return err
		}
		a := regressionAlert(r.st, hit, now)
		a.Key = r.key
		payload, err := json.Marshal(a)
		if err != nil {
			return err
		}
		if err := e.store.InsertAlertRecords(ctx, []models.AlertRecord{{AlertKey: r.key, At: now, Status: models.AlertPending,
			Instance: e.instance, Type: TypeRegression, Kind: kind, Fingerprint: r.st.Fingerprint, AppVersion: r.version,
			Payload: string(payload)}}); err != nil {
			return err
		}
		// Reopen the issue (a new row; the resolution stays for the record).
		reopened := r.st
		reopened.Status = models.IssueUnresolved
		reopened.RegressedInVersion = r.version
		first := hit.FirstSeen
		reopened.RegressedAt = &first
		reopened.UpdatedAt = now.UTC()
		reopened.UpdatedBy = "alert"
		reopened.Comment = ""
		reopened.Version = NextStateVersion(now, r.st.Version)
		if err := e.store.InsertIssueState(ctx, reopened); err != nil {
			return err
		}
		e.logger.Warn("alert: regression of a resolved issue", zap.String("kind", kind),
			zap.String("fingerprint", r.st.Fingerprint), zap.String("version", r.version))
	}
	return nil
}

// NextStateVersion is the version of a new issue state row: the time in
// milliseconds, above the previous row's.
func NextStateVersion(now time.Time, previous uint64) uint64 {
	v := uint64(now.UnixMilli())
	if v <= previous {
		v = previous + 1
	}
	return v
}

func regressionAlert(st models.IssueState, hit models.IssueVersionHit, now time.Time) Alert {
	resolved := "resolved " + fmtTime(*st.ResolvedAt)
	if st.ResolvedInVersion != "" {
		resolved += ", fixed in " + CleanText(st.ResolvedInVersion, 60)
	} else if st.LastSeenVersion != "" {
		resolved += ", last seen in " + CleanText(st.LastSeenVersion, 60) + " before"
	}
	return Alert{
		Type: TypeRegression, Severity: SeverityCritical,
		Title: fmt.Sprintf("Regression: resolved %s %s occurs again in %s", kindLabel(st.Kind),
			CleanText(st.Fingerprint, maxFingerprintChars), CleanText(hit.AppVersion, 60)),
		Kind: st.Kind, Fingerprint: CleanText(st.Fingerprint, maxFingerprintChars), AppVersion: CleanText(hit.AppVersion, 60),
		Facts: []Fact{
			{"Resolution", resolved},
			{"Again in", CleanText(hit.AppVersion, 60) + " since " + fmtTime(hit.FirstSeen)},
			{"Occurrences since the resolution", fmt.Sprintf("%d in %d sessions", hit.Count, hit.Sessions)},
			{"Issue", "reopened (unresolved)"},
		},
		Metrics:   map[string]float64{"count": float64(hit.Count), "sessions": float64(hit.Sessions)},
		AdminPath: issuePath(st.Kind, st.Fingerprint), DetectedAt: now.UTC(),
	}
}

// crashRateThreshold is the rate above which a version spikes: at least
// CrashRateMinRate, and CrashRateFactor times the baseline when it is known
// (enough sessions).
func (e *Evaluator) crashRateThreshold() (threshold, baseline float64, known bool) {
	threshold = e.cfg.CrashRateMinRate
	if e.crashBase.sessions >= e.cfg.CrashRateMinSessions && e.crashBase.sessions > 0 {
		baseline = float64(e.crashBase.crashedSessions) / float64(e.crashBase.sessions)
		known = true
		if t := baseline * e.cfg.CrashRateFactor; t > threshold {
			threshold = t
		}
	}
	return threshold, baseline, known
}

func (e *Evaluator) detectCrashRate(ctx context.Context, now time.Time) error {
	windowStart := now.Add(-e.cfg.CrashRateWindow)
	if e.crashBase.at.IsZero() || now.Sub(e.crashBase.at) >= time.Hour {
		base, err := e.store.CrashRates(ctx, windowStart.Add(-e.cfg.CrashRateBaselineWindow), windowStart, e.cfg.IncludeDevBuilds)
		if err != nil {
			return err
		}
		e.crashBase.at, e.crashBase.sessions, e.crashBase.crashedSessions = now, 0, 0
		for _, v := range base {
			e.crashBase.sessions += v.Sessions
			e.crashBase.crashedSessions += v.CrashedSessions
		}
	}
	rates, err := e.store.CrashRates(ctx, windowStart, now, e.cfg.IncludeDevBuilds)
	if err != nil {
		return err
	}
	threshold, baseline, known := e.crashRateThreshold()
	bucket := now.Truncate(e.cfg.CrashRateCheckInterval).Unix()
	for _, v := range rates {
		if v.Sessions < e.cfg.CrashRateMinSessions || v.CrashedSessions < e.cfg.CrashRateMinCrashed {
			continue
		}
		rate := float64(v.CrashedSessions) / float64(v.Sessions)
		if rate < threshold {
			continue
		}
		recent, err := e.store.RecentAlertExists(ctx, TypeCrashRateSpike, v.AppVersion, now.Add(-e.cfg.CrashRateCooldown))
		if err != nil {
			return err
		}
		if recent {
			continue
		}
		key := fmt.Sprintf("%s:%s:%d", TypeCrashRateSpike, v.AppVersion, bucket)
		a := crashRateAlert(v, rate, threshold, baseline, known, e.cfg, now)
		a.Key = key
		payload, err := json.Marshal(a)
		if err != nil {
			return err
		}
		if err := e.store.InsertAlertRecords(ctx, []models.AlertRecord{{AlertKey: key, At: now, Status: models.AlertPending,
			Instance: e.instance, Type: TypeCrashRateSpike, AppVersion: v.AppVersion, Payload: string(payload)}}); err != nil {
			return err
		}
		e.logger.Warn("alert: crash rate spike", zap.String("version", v.AppVersion), zap.Float64("rate", rate),
			zap.Float64("threshold", threshold))
	}
	return nil
}

func crashRateAlert(v models.VersionRate, rate, threshold, baseline float64, known bool, cfg Config, now time.Time) Alert {
	base := "unknown (fewer sessions than the minimum)"
	if known {
		base = fmtPercent(baseline) + " (all versions, previous " + cfg.CrashRateBaselineWindow.String() + ")"
	}
	return Alert{
		Type: TypeCrashRateSpike, Severity: SeverityCritical,
		Title: fmt.Sprintf("Crash rate spike in %s: %s of sessions crashed in the last %s",
			CleanText(v.AppVersion, 60), fmtPercent(rate), cfg.CrashRateWindow),
		AppVersion: CleanText(v.AppVersion, 60),
		Facts: []Fact{
			{"Crashed sessions", fmt.Sprintf("%d of %d (%d crashes)", v.CrashedSessions, v.Sessions, v.Crashes)},
			{"Baseline", base},
			{"Threshold", fmtPercent(threshold)},
		},
		Metrics: map[string]float64{"rate": rate, "threshold": threshold, "baseline": baseline,
			"sessions": float64(v.Sessions), "crashed_sessions": float64(v.CrashedSessions), "crashes": float64(v.Crashes)},
		AdminPath: "/crashes?" + "app_version=" + urlQueryEscape(v.AppVersion), DetectedAt: now.UTC(),
	}
}

func (e *Evaluator) detectStartupRegression(ctx context.Context, now time.Time) error {
	stats, err := e.store.StartupStats(ctx, now.Add(-e.cfg.StartupWindow), e.cfg.IncludeDevBuilds)
	if err != nil {
		return err
	}
	var eligible []models.VersionStartup
	order := VersionOrder{FirstSeen: map[string]time.Time{}}
	for _, s := range stats {
		if s.Samples >= e.cfg.StartupMinSamples && s.P95Ms > 0 {
			eligible = append(eligible, s)
			order.FirstSeen[s.AppVersion] = s.FirstSeen
		}
	}
	if len(eligible) < 2 {
		return nil
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		return order.Compare(eligible[i].AppVersion, eligible[j].AppVersion, now, now) > 0
	})
	cur, prev := eligible[0], eligible[1]
	if cur.P95Ms < prev.P95Ms*(1+e.cfg.StartupIncrease) || cur.P95Ms-prev.P95Ms < e.cfg.StartupMinDeltaMs {
		return nil
	}
	key := TypeStartupP95 + ":" + cur.AppVersion + ":" + prev.AppVersion
	existing, err := e.store.ExistingAlertKeys(ctx, []string{key})
	if err != nil || existing[key] {
		return err
	}
	increase := cur.P95Ms/prev.P95Ms - 1
	a := Alert{
		Key: key, Type: TypeStartupP95, Severity: SeverityWarning,
		Title: fmt.Sprintf("Startup P95 regression: %s is %.0f%% slower than %s",
			CleanText(cur.AppVersion, 60), increase*100, CleanText(prev.AppVersion, 60)),
		AppVersion: CleanText(cur.AppVersion, 60),
		Facts: []Fact{
			{"P95 startup", fmt.Sprintf("%.0f ms (%d startups) vs %.0f ms (%d startups)", cur.P95Ms, cur.Samples, prev.P95Ms, prev.Samples)},
			{"Window", "last " + e.cfg.StartupWindow.String() + "; startup = app launch to time to interactive"},
		},
		Metrics: map[string]float64{"p95_ms": cur.P95Ms, "previous_p95_ms": prev.P95Ms, "increase": increase,
			"samples": float64(cur.Samples), "previous_samples": float64(prev.Samples)},
		AdminPath: "/performance?app_version=" + urlQueryEscape(cur.AppVersion), DetectedAt: now.UTC(),
	}
	payload, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return e.store.InsertAlertRecords(ctx, []models.AlertRecord{{AlertKey: key, At: now, Status: models.AlertPending,
		Instance: e.instance, Type: TypeStartupP95, AppVersion: cur.AppVersion, Payload: string(payload)}})
}

// backoff is the wait after the n-th failed attempt: 1, 2, 4, 8, 16 minutes.
func backoff(failures int) time.Duration {
	if failures < 1 {
		return 0
	}
	d := time.Minute << (failures - 1)
	if d > 30*time.Minute || d <= 0 {
		d = 30 * time.Minute
	}
	return d
}

// nextAttempt decides what to do with an unsent alert: the attempt number to
// claim (0: nothing now) or abandon.
func nextAttempt(o models.AlertOutboxEntry, now time.Time) (attempt int, abandon bool) {
	failures := o.Failed
	lastFailure := o.LastFailedAt
	if o.LastClaim > o.LastDone {
		if now.Sub(o.LastClaimAt) < claimStale {
			return 0, false // being sent by the instance that claimed it
		}
		failures++ // its instance stopped while sending
		if o.LastClaimAt.After(lastFailure) {
			lastFailure = o.LastClaimAt
		}
	}
	made := o.LastClaim
	if o.LastDone > made {
		made = o.LastDone
	}
	if made >= MaxAttempts {
		return 0, true
	}
	if failures > 0 && now.Before(lastFailure.Add(backoff(failures))) {
		return 0, false
	}
	return made + 1, false
}

// claimWinners returns, per key, the instance whose claim of the attempt is
// first (earliest time, then instance name).
func claimWinners(claims []models.AlertClaim, attempts map[string]int) map[string]string {
	best := map[string]models.AlertClaim{}
	for _, c := range claims {
		if attempts[c.AlertKey] != c.Attempt {
			continue
		}
		b, ok := best[c.AlertKey]
		if !ok || c.At.Before(b.At) || (c.At.Equal(b.At) && c.Instance < b.Instance) {
			best[c.AlertKey] = c
		}
	}
	out := map[string]string{}
	for k, c := range best {
		out[k] = c.Instance
	}
	return out
}

func (e *Evaluator) send(ctx context.Context) error {
	if e.sender == nil {
		return nil
	}
	now := e.now()
	entries, err := e.store.AlertsToSend(ctx, now.Add(-sendWindow), maxSendsPerCycle)
	if err != nil || len(entries) == 0 {
		return err
	}
	var claims, abandoned []models.AlertRecord
	attempts := map[string]int{}
	byKey := map[string]models.AlertOutboxEntry{}
	for _, o := range entries {
		n, abandon := nextAttempt(o, now)
		if abandon {
			abandoned = append(abandoned, models.AlertRecord{AlertKey: o.AlertKey, At: now, Status: models.AlertAbandoned,
				Instance: e.instance, Type: o.Type, Error: fmt.Sprintf("gave up after %d attempts", MaxAttempts)})
			e.logger.Error("alert: abandoned after the last failed attempt", zap.String("key", o.AlertKey), zap.Int("attempts", MaxAttempts))
			continue
		}
		if n == 0 {
			continue
		}
		attempts[o.AlertKey] = n
		byKey[o.AlertKey] = o
		claims = append(claims, models.AlertRecord{AlertKey: o.AlertKey, At: now, Status: models.AlertClaimed,
			Attempt: uint16(n), Instance: e.instance, Type: o.Type})
	}
	if err := e.store.InsertAlertRecords(ctx, abandoned); err != nil {
		return err
	}
	if len(claims) == 0 {
		return nil
	}
	if err := e.store.InsertAlertRecords(ctx, claims); err != nil {
		return err
	}
	if err := e.sleep(ctx, claimSettle); err != nil {
		return err
	}
	keys := make([]string, 0, len(attempts))
	for k := range attempts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	all, err := e.store.AlertClaims(ctx, keys)
	if err != nil {
		return err
	}
	winners := claimWinners(all, attempts)
	for _, key := range keys {
		if winners[key] != e.instance {
			continue // another instance sends this attempt
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		o := byKey[key]
		rec := models.AlertRecord{AlertKey: key, Attempt: uint16(attempts[key]), Instance: e.instance, Type: o.Type}
		var a Alert
		if err := json.Unmarshal([]byte(o.Payload), &a); err != nil || a.Title == "" {
			rec.At, rec.Status, rec.Error = e.now(), models.AlertAbandoned, "unreadable payload"
		} else if err := e.sender.Send(ctx, a); err != nil {
			rec.At, rec.Status, rec.Error = e.now(), models.AlertFailed, CleanText(err.Error(), 500)
			e.logger.Warn("alert: webhook attempt failed", zap.String("key", key), zap.Int("attempt", attempts[key]),
				zap.String("error", rec.Error))
		} else {
			rec.At, rec.Status = e.now(), models.AlertSent
			e.logger.Info("alert sent", zap.String("key", key), zap.String("type", o.Type), zap.Int("attempt", attempts[key]))
		}
		// The outcome must be written even when the evaluation's deadline
		// passed meanwhile, or the attempt would count as stale and be retried.
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err := e.store.InsertAlertRecords(wctx, []models.AlertRecord{rec})
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

// TestAlert is the alert of POST /api/alerts/test.
func TestAlert(key, requestedBy string, now time.Time) Alert {
	return Alert{
		Key: key, Type: TypeTest, Severity: SeverityInfo,
		Title: "Test alert: the OZX APM alert webhook works",
		Facts: []Fact{
			{"Requested", fmtTime(now) + " from " + CleanText(requestedBy, 80)},
			{"Note", "sent by the alert evaluator through the outbox, like every alert"},
		},
		AdminPath: "/", DetectedAt: now.UTC(),
	}
}

// NewTestAlertRecord returns the pending outbox row of a test alert.
func NewTestAlertRecord(requestedBy string, now time.Time) (models.AlertRecord, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return models.AlertRecord{}, err
	}
	key := fmt.Sprintf("%s:%d:%s", TypeTest, now.UnixMilli(), hex.EncodeToString(b))
	payload, err := json.Marshal(TestAlert(key, requestedBy, now))
	if err != nil {
		return models.AlertRecord{}, err
	}
	return models.AlertRecord{AlertKey: key, At: now, Status: models.AlertPending, Instance: "admin-api",
		Type: TypeTest, Payload: string(payload)}, nil
}
