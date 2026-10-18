package alert

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

var t0 = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func testConfig() Config {
	return Config{
		Interval: time.Minute, NewIssueMaxPerCycle: 10, NewIssueWindow: 26 * time.Hour, SweepInterval: 6 * time.Hour,
		CrashRateWindow: time.Hour, CrashRateBaselineWindow: 24 * time.Hour, CrashRateCheckInterval: 5 * time.Minute,
		CrashRateMinSessions: 50, CrashRateMinCrashed: 5, CrashRateMinRate: 0.005, CrashRateFactor: 2, CrashRateCooldown: 6 * time.Hour,
		StartupEnabled: true, StartupWindow: 7 * 24 * time.Hour, StartupCheckInterval: time.Hour,
		StartupMinSamples: 100, StartupIncrease: 0.2, StartupMinDeltaMs: 100,
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestEvaluator(store Store, sender Sender, c *clock, instance string) *Evaluator {
	e := NewEvaluator(store, sender, testConfig(), zap.NewNop())
	e.now = c.now
	e.sleep = func(context.Context, time.Duration) error { return nil }
	e.instance = instance
	return e
}

func sentTypes(s *fakeSender) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, a := range s.sent {
		out = append(out, a.Type)
	}
	return out
}

// Enabling alerting on a deployment with data reports nothing as new; a
// fingerprint that appears afterwards (a new version) is sent once.
func TestNewIssue_BaselineThenNewFingerprintSentOnce(t *testing.T) {
	store := newFakeStore()
	c := &clock{t0}
	store.add(models.IssueKindException, occ{fp: "old", version: "1.0.0", session: "s1", at: t0.Add(-48 * time.Hour), title: "OldError"})
	store.add(models.IssueKindException, occ{fp: "recent", version: "1.0.0", session: "s1", at: t0.Add(-time.Hour), title: "RecentError"})
	store.add(models.IssueKindCrash, occ{fp: "c-old", version: "1.0.0", session: "s1", at: t0.Add(-time.Hour)})
	sender := &fakeSender{}
	e := newTestEvaluator(store, sender, c, "a")

	e.Cycle(context.Background())
	if n := sender.count(); n != 0 {
		t.Fatalf("baseline cycle sent %d alerts", n)
	}
	if got := len(store.records(models.AlertKnown)); got != 3+2 { // 3 fingerprints + 2 markers
		t.Fatalf("known rows %d, want 5", got)
	}

	c.t = t0.Add(time.Minute)
	store.add(models.IssueKindException, occ{fp: "fresh", version: "2.0.0", session: "s2", at: c.t.Add(-10 * time.Second),
		title: "NullReferenceException", message: "Object reference not set @all <!channel>\nline2"})
	store.add(models.IssueKindException, occ{fp: "fresh", version: "2.0.0", session: "s3", at: c.t.Add(-5 * time.Second), count: 3})
	e.Cycle(context.Background())
	if n := sender.count(); n != 1 {
		t.Fatalf("sent %d, want 1: %v", n, sentTypes(sender))
	}
	a := sender.sent[0]
	if a.Type != TypeNewIssue || a.Kind != "exception" || a.Fingerprint != "fresh" || a.AppVersion != "2.0.0" {
		t.Fatalf("alert %+v", a)
	}
	if !strings.Contains(a.Title, "NullReferenceException") || a.AdminPath != "/exceptions/detail?fingerprint=fresh" {
		t.Errorf("title %q path %q", a.Title, a.AdminPath)
	}
	text := a.Text("")
	if strings.Contains(text, "@all") || strings.Contains(text, "<!channel>") || strings.Contains(text, "line2\n") {
		t.Errorf("game text not made inert: %q", text)
	}
	if a.Metrics["count"] != 4 || a.Metrics["sessions"] != 2 {
		t.Errorf("metrics %v", a.Metrics)
	}

	// Later evaluations, a restart (new evaluator, same store) and another
	// instance send nothing more.
	c.t = c.t.Add(time.Minute)
	e.Cycle(context.Background())
	e2 := newTestEvaluator(store, sender, c, "b")
	e2.Cycle(context.Background())
	if n := sender.count(); n != 1 {
		t.Fatalf("sent %d after restart, want 1", n)
	}
}

// Dev and editor builds are ignored by default; their fingerprint is new
// when it first shows up in a release build.
func TestNewIssue_DevBuildsExcluded(t *testing.T) {
	store := newFakeStore()
	c := &clock{t0}
	sender := &fakeSender{}
	e := newTestEvaluator(store, sender, c, "a")
	e.Cycle(context.Background()) // empty baseline
	store.add(models.IssueKindException, occ{fp: "devonly", version: "2.0.0", session: "s", at: t0.Add(-time.Second), dev: true})
	c.t = t0.Add(time.Minute)
	e.Cycle(context.Background())
	if sender.count() != 0 {
		t.Fatalf("a dev build's fingerprint was reported")
	}
	store.add(models.IssueKindException, occ{fp: "devonly", version: "2.0.0", session: "s", at: c.t})
	c.t = c.t.Add(time.Minute)
	e.Cycle(context.Background())
	if sender.count() != 1 {
		t.Fatalf("its first release occurrence was not reported (%d)", sender.count())
	}
}

// More new fingerprints than the per-evaluation limit: the first ones one by
// one, the rest in one digest, all recorded as known.
func TestNewIssue_StormDigest(t *testing.T) {
	store := newFakeStore()
	c := &clock{t0}
	sender := &fakeSender{}
	e := newTestEvaluator(store, sender, c, "a")
	e.Cycle(context.Background())
	for i := 0; i < 25; i++ {
		store.add(models.IssueKindException, occ{fp: fmt.Sprintf("fp%02d", i), version: "3.0.0", session: "s",
			at: t0.Add(time.Duration(i) * time.Second), title: "E"})
	}
	c.t = t0.Add(time.Minute)
	e.Cycle(context.Background())
	types := sentTypes(sender)
	if len(types) != 11 {
		t.Fatalf("sent %d (%v), want 10 + 1 digest", len(types), types)
	}
	digests := 0
	for _, ty := range types {
		if ty == TypeNewIssueDigest {
			digests++
		}
	}
	if digests != 1 {
		t.Fatalf("digests %d", digests)
	}
	for _, a := range sender.sent {
		if a.Type == TypeNewIssueDigest && a.Metrics["count"] != 15 {
			t.Errorf("digest count %v", a.Metrics["count"])
		}
	}
	c.t = c.t.Add(time.Minute)
	e.Cycle(context.Background())
	if sender.count() != 11 {
		t.Fatalf("the suppressed fingerprints were reported later (%d)", sender.count())
	}
}

// A late upload: rows older than the fast window are found by the sweep.
func TestNewIssue_SweepFindsLateUploads(t *testing.T) {
	store := newFakeStore()
	c := &clock{t0}
	sender := &fakeSender{}
	e := newTestEvaluator(store, sender, c, "a")
	e.Cycle(context.Background())
	store.add(models.IssueKindException, occ{fp: "late", version: "2.0.0", session: "s", at: t0.Add(-3 * 24 * time.Hour)})
	c.t = t0.Add(time.Minute)
	e.Cycle(context.Background())
	if sender.count() != 0 {
		t.Fatalf("found outside the fast window before the sweep")
	}
	c.t = t0.Add(6*time.Hour + time.Minute)
	e.Cycle(context.Background())
	if sender.count() != 1 {
		t.Fatalf("the sweep did not find the late upload")
	}
}

// barrierStore makes two instances read the outbox before either claims
// (the race the claim comparison exists for).
type barrierStore struct {
	*fakeStore
	read *sync.WaitGroup
}

func (b barrierStore) AlertsToSend(ctx context.Context, since time.Time, limit int) ([]models.AlertOutboxEntry, error) {
	out, err := b.fakeStore.AlertsToSend(ctx, since, limit)
	b.read.Done()
	b.read.Wait()
	return out, err
}

// Two instances sending at the same time send each alert once: both claim
// attempt 1, and after the settle wait both see both claims and agree on
// the first one.
func TestSend_TwoInstancesSendOnce(t *testing.T) {
	for _, skew := range []time.Duration{0, time.Millisecond, -time.Millisecond} {
		store := newFakeStore()
		rec, _ := NewTestAlertRecord("x", t0)
		store.InsertAlertRecords(context.Background(), []models.AlertRecord{rec})
		var read, claimed sync.WaitGroup
		read.Add(2)
		claimed.Add(2)
		senders := []*fakeSender{{}, {}}
		var wg sync.WaitGroup
		for i, name := range []string{"b", "a"} {
			c := &clock{t0.Add(time.Minute + time.Duration(i)*skew)}
			e := newTestEvaluator(barrierStore{store, &read}, senders[i], c, name)
			e.sleep = func(context.Context, time.Duration) error { // the settle wait
				claimed.Done()
				claimed.Wait()
				return nil
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				e.send(context.Background())
			}()
		}
		wg.Wait()
		if n := senders[0].count() + senders[1].count(); n != 1 {
			t.Fatalf("skew %v: sent %d times (b %d, a %d)", skew, n, senders[0].count(), senders[1].count())
		}
		if len(store.records(models.AlertClaimed)) != 2 || len(store.records(models.AlertSent)) != 1 {
			t.Errorf("skew %v: claims %d, sent rows %d", skew, len(store.records(models.AlertClaimed)), len(store.records(models.AlertSent)))
		}
	}
}

// A failing webhook is retried with backoff and abandoned after the last
// attempt; nothing is sent twice.
func TestSend_RetryBackoffAbandon(t *testing.T) {
	store := newFakeStore()
	c := &clock{t0}
	sender := &fakeSender{err: errors.New("webhook example.com answered 500")}
	e := newTestEvaluator(store, sender, c, "a")
	e.Cycle(context.Background())
	store.add(models.IssueKindException, occ{fp: "x", version: "2.0.0", session: "s", at: t0})
	c.t = t0.Add(time.Minute)
	e.Cycle(context.Background())
	if n := len(store.records(models.AlertFailed)); n != 1 {
		t.Fatalf("failed rows %d", n)
	}
	// Within the backoff (1 min after the first failure): no attempt.
	c.t = c.t.Add(30 * time.Second)
	e.Cycle(context.Background())
	if n := len(store.records(models.AlertClaimed)); n != 1 {
		t.Fatalf("claimed during backoff: %d", n)
	}
	// Recovered: the next attempt after the backoff succeeds.
	sender.err = nil
	c.t = c.t.Add(time.Minute)
	e.Cycle(context.Background())
	if sender.count() != 1 || len(store.records(models.AlertSent)) != 1 {
		t.Fatalf("sent %d, sent rows %d", sender.count(), len(store.records(models.AlertSent)))
	}
	if got := store.records(models.AlertSent)[0].Attempt; got != 2 {
		t.Errorf("sent on attempt %d, want 2", got)
	}

	// Another alert that always fails: abandoned after MaxAttempts.
	sender.err = errors.New("timeout")
	store.add(models.IssueKindException, occ{fp: "y", version: "2.0.0", session: "s", at: c.t})
	for i := 0; i < 40; i++ {
		c.t = c.t.Add(5 * time.Minute)
		e.Cycle(context.Background())
	}
	var failedY, abandonedY int
	for _, r := range store.records("") {
		if strings.Contains(r.AlertKey, ":y") {
			switch r.Status {
			case models.AlertFailed:
				failedY++
			case models.AlertAbandoned:
				abandonedY++
			}
		}
	}
	if failedY != MaxAttempts || abandonedY != 1 {
		t.Fatalf("failed %d abandoned %d, want %d and 1", failedY, abandonedY, MaxAttempts)
	}
}

// A claim whose instance stopped before writing the outcome is retried by
// another instance after claimStale.
func TestSend_StaleClaimRetried(t *testing.T) {
	store := newFakeStore()
	c := &clock{t0}
	sender := &fakeSender{}
	e := newTestEvaluator(store, sender, c, "b")
	rec, err := NewTestAlertRecord("10.0.0.1:1234", t0)
	if err != nil {
		t.Fatal(err)
	}
	store.InsertAlertRecords(context.Background(), []models.AlertRecord{rec,
		{AlertKey: rec.AlertKey, At: t0, Status: models.AlertClaimed, Attempt: 1, Instance: "dead", Type: TypeTest}})
	c.t = t0.Add(time.Minute)
	e.send(context.Background())
	if sender.count() != 0 {
		t.Fatalf("sent while another instance's claim is fresh")
	}
	c.t = t0.Add(claimStale + time.Minute + time.Second) // stale claim + backoff of one failure
	e.send(context.Background())
	if sender.count() != 1 || sender.sent[0].Type != TypeTest {
		t.Fatalf("stale claim not retried: %d", sender.count())
	}
}

func resolve(store *fakeStore, kind, fp, fixedIn, lastSeen string, at time.Time) models.IssueState {
	st := models.IssueState{Kind: kind, Fingerprint: fp, Status: models.IssueResolved, ResolvedInVersion: fixedIn,
		LastSeenVersion: lastSeen, ResolvedAt: &at, UpdatedAt: at, UpdatedBy: "admin", Version: uint64(at.UnixMilli())}
	store.InsertIssueState(context.Background(), st)
	return st
}

// Regression: occurrences after the resolution in a version older than the
// fix are not one; in the fix version or newer they are, once, and the
// issue is reopened.
func TestRegression_FixedInVersion(t *testing.T) {
	store := newFakeStore()
	c := &clock{t0}
	sender := &fakeSender{}
	e := newTestEvaluator(store, sender, c, "a")
	store.add(models.IssueKindException, occ{fp: "bug", version: "1.4.2", session: "s", at: t0.Add(-2 * time.Hour)})
	e.Cycle(context.Background()) // baseline
	resolve(store, models.IssueKindException, "bug", "1.5.0", "1.4.2", t0.Add(-time.Hour))

	// Old clients still running 1.4.2 after the resolution: not a regression.
	store.add(models.IssueKindException, occ{fp: "bug", version: "1.4.2", session: "s2", at: t0.Add(-30 * time.Minute)})
	c.t = t0.Add(time.Minute)
	e.Cycle(context.Background())
	if sender.count() != 0 {
		t.Fatalf("an older version's occurrence was reported as a regression")
	}

	store.add(models.IssueKindException, occ{fp: "bug", version: "1.5.0", session: "s3", at: c.t, count: 2})
	c.t = c.t.Add(time.Minute)
	e.Cycle(context.Background())
	if sender.count() != 1 || sender.sent[0].Type != TypeRegression || sender.sent[0].AppVersion != "1.5.0" {
		t.Fatalf("regression not sent: %v", sentTypes(sender))
	}
	if sender.sent[0].Metrics["count"] != 2 {
		t.Errorf("count since the resolution %v", sender.sent[0].Metrics)
	}
	st := store.latestStates()["exception/bug"]
	if st.Status != models.IssueUnresolved || st.RegressedInVersion != "1.5.0" || st.UpdatedBy != "alert" || st.ResolvedInVersion != "1.5.0" {
		t.Fatalf("state after regression %+v", st)
	}
	c.t = c.t.Add(time.Minute)
	e.Cycle(context.Background())
	if sender.count() != 1 {
		t.Fatalf("regression sent again")
	}

	// Resolved again, and it comes back in a newer version: a new alert.
	resolve(store, models.IssueKindException, "bug", "1.6.0", "1.5.0", c.t)
	store.add(models.IssueKindException, occ{fp: "bug", version: "1.6.1", session: "s4", at: c.t.Add(time.Second)})
	c.t = c.t.Add(time.Minute)
	e.Cycle(context.Background())
	if sender.count() != 2 {
		t.Fatalf("second regression not sent (%d)", sender.count())
	}
}

// Without a fix version, any version newer than the newest one that had the
// issue when it was resolved; names that cannot be compared are ordered by
// when the version was first seen.
func TestRegression_NextVersionAndTimeFallback(t *testing.T) {
	store := newFakeStore()
	c := &clock{t0}
	sender := &fakeSender{}
	e := newTestEvaluator(store, sender, c, "a")
	store.add(models.IssueKindCrash, occ{fp: "crash1", version: "apmtest-2.0.0", session: "s", at: t0.Add(-2 * time.Hour)})
	e.Cycle(context.Background())
	resolve(store, models.IssueKindCrash, "crash1", "", "apmtest-2.0.0", t0.Add(-time.Hour))
	store.first["apmtest-2.0.0"] = t0.Add(-10 * 24 * time.Hour)
	store.first["2.1.0"] = t0.Add(-20 * time.Minute) // first seen after the resolution

	store.add(models.IssueKindCrash, occ{fp: "crash1", version: "apmtest-2.0.0", session: "s2", at: t0.Add(-10 * time.Minute)})
	c.t = t0.Add(time.Minute)
	e.Cycle(context.Background())
	if sender.count() != 0 {
		t.Fatalf("the same version after the resolution is not a regression")
	}
	store.add(models.IssueKindCrash, occ{fp: "crash1", version: "2.1.0", session: "s3", at: t0.Add(-5 * time.Minute)})
	c.t = c.t.Add(time.Minute)
	e.Cycle(context.Background())
	if sender.count() != 1 || sender.sent[0].Kind != "crash" || sender.sent[0].AdminPath != "/crashes?fingerprint=crash1" {
		t.Fatalf("regression in a newer (by time) version: %d %+v", sender.count(), sender.sent)
	}
	if sender.sent[0].Severity != SeverityCritical {
		t.Errorf("severity %s", sender.sent[0].Severity)
	}
}

func TestCrashRateSpike(t *testing.T) {
	store := newFakeStore()
	c := &clock{t0}
	sender := &fakeSender{}
	e := newTestEvaluator(store, sender, c, "a")
	store.rates = func(from, to time.Time) []models.VersionRate {
		if to.Sub(from) > 2*time.Hour { // baseline: 0.5%
			return []models.VersionRate{{AppVersion: "1.0.0", Sessions: 10000, CrashedSessions: 50}}
		}
		return []models.VersionRate{
			{AppVersion: "1.0.0", Sessions: 1000, CrashedSessions: 6, Crashes: 6}, // 0.6%: below 2x baseline
			{AppVersion: "2.0.0", Sessions: 400, CrashedSessions: 12, Crashes: 15}, // 3%
			{AppVersion: "2.1.0", Sessions: 30, CrashedSessions: 10, Crashes: 10},  // too few sessions
			{AppVersion: "2.2.0", Sessions: 1000, CrashedSessions: 3, Crashes: 3},  // too few crashes
		}
	}
	e.Cycle(context.Background())
	if sender.count() != 1 || sender.sent[0].Type != TypeCrashRateSpike || sender.sent[0].AppVersion != "2.0.0" {
		t.Fatalf("sent %v", sentTypes(sender))
	}
	if r := sender.sent[0].Metrics["rate"]; r != 0.03 {
		t.Errorf("rate %v", r)
	}
	// Cooldown: the next checks within 6 h stay quiet.
	for i := 1; i <= 12; i++ {
		c.t = t0.Add(time.Duration(i) * 5 * time.Minute)
		e.Cycle(context.Background())
	}
	if sender.count() != 1 {
		t.Fatalf("cooldown not applied: %d", sender.count())
	}
	c.t = t0.Add(6*time.Hour + 5*time.Minute)
	e.Cycle(context.Background())
	if sender.count() != 2 {
		t.Fatalf("not alerted again after the cooldown: %d", sender.count())
	}
}

func TestStartupP95Regression(t *testing.T) {
	store := newFakeStore()
	c := &clock{t0}
	sender := &fakeSender{}
	e := newTestEvaluator(store, sender, c, "a")
	store.startups = []models.VersionStartup{
		{AppVersion: "1.9.0", Samples: 500, P95Ms: 2000, FirstSeen: t0.Add(-6 * 24 * time.Hour)},
		{AppVersion: "2.0.0", Samples: 300, P95Ms: 2600, FirstSeen: t0.Add(-2 * 24 * time.Hour)},
		{AppVersion: "2.0.1", Samples: 20, P95Ms: 9000, FirstSeen: t0.Add(-time.Hour)}, // too few
	}
	e.Cycle(context.Background())
	if sender.count() != 1 || sender.sent[0].Type != TypeStartupP95 || sender.sent[0].AppVersion != "2.0.0" {
		t.Fatalf("sent %+v", sender.sent)
	}
	c.t = t0.Add(2 * time.Hour)
	e.Cycle(context.Background())
	if sender.count() != 1 {
		t.Fatalf("startup regression sent twice")
	}

	// 10% slower: no alert.
	store2 := newFakeStore()
	sender2 := &fakeSender{}
	e2 := newTestEvaluator(store2, sender2, &clock{t0}, "a")
	store2.startups = []models.VersionStartup{
		{AppVersion: "1.9.0", Samples: 500, P95Ms: 2000},
		{AppVersion: "2.0.0", Samples: 500, P95Ms: 2200},
	}
	e2.Cycle(context.Background())
	if sender2.count() != 0 {
		t.Fatalf("10%% slower alerted")
	}
}

// An evaluation step that fails does not stop the others, and the baseline
// is retried.
func TestCycle_ErrorsAreContained(t *testing.T) {
	store := newFakeStore()
	store.failOn["AllIssueFingerprints"] = errors.New("boom")
	sender := &fakeSender{}
	c := &clock{t0}
	e := newTestEvaluator(store, sender, c, "a")
	rec, _ := NewTestAlertRecord("x", t0)
	store.InsertAlertRecords(context.Background(), []models.AlertRecord{rec})
	e.Cycle(context.Background())
	if sender.count() != 1 {
		t.Fatalf("the test alert was not sent despite the failing baseline")
	}
	delete(store.failOn, "AllIssueFingerprints")
	c.t = t0.Add(time.Minute)
	e.Cycle(context.Background())
	if len(store.records(models.AlertKnown)) != 2 {
		t.Fatalf("baseline not retried")
	}
}

func TestNextAttempt(t *testing.T) {
	now := t0
	cases := []struct {
		name    string
		o       models.AlertOutboxEntry
		attempt int
		abandon bool
	}{
		{"new", models.AlertOutboxEntry{}, 1, false},
		{"claimed, fresh", models.AlertOutboxEntry{LastClaim: 1, LastClaimAt: now.Add(-time.Minute)}, 0, false},
		{"claimed, just stale", models.AlertOutboxEntry{LastClaim: 1, LastClaimAt: now.Add(-150 * time.Second)}, 2, false},
		{"claimed, stale after failures, in backoff", models.AlertOutboxEntry{LastClaim: 3, LastDone: 2, Failed: 2,
			LastFailedAt: now.Add(-10 * time.Minute), LastClaimAt: now.Add(-3 * time.Minute)}, 0, false},
		{"claimed, stale", models.AlertOutboxEntry{LastClaim: 1, LastClaimAt: now.Add(-4 * time.Minute)}, 2, false},
		{"failed once, in backoff", models.AlertOutboxEntry{LastClaim: 1, LastDone: 1, Failed: 1, LastFailedAt: now.Add(-30 * time.Second)}, 0, false},
		{"failed once", models.AlertOutboxEntry{LastClaim: 1, LastDone: 1, Failed: 1, LastFailedAt: now.Add(-61 * time.Second)}, 2, false},
		{"failed 3, 3 min ago", models.AlertOutboxEntry{LastClaim: 3, LastDone: 3, Failed: 3, LastFailedAt: now.Add(-3 * time.Minute)}, 0, false},
		{"failed 3, 5 min ago", models.AlertOutboxEntry{LastClaim: 3, LastDone: 3, Failed: 3, LastFailedAt: now.Add(-5 * time.Minute)}, 4, false},
		{"all attempts made", models.AlertOutboxEntry{LastClaim: MaxAttempts, LastDone: MaxAttempts, Failed: MaxAttempts, LastFailedAt: now.Add(-time.Hour)}, 0, true},
	}
	for _, tc := range cases {
		a, ab := nextAttempt(tc.o, now)
		if a != tc.attempt || ab != tc.abandon {
			t.Errorf("%s: got %d %v, want %d %v", tc.name, a, ab, tc.attempt, tc.abandon)
		}
	}
}

func TestClaimWinners(t *testing.T) {
	claims := []models.AlertClaim{
		{AlertKey: "k", Attempt: 1, At: t0, Instance: "b"},
		{AlertKey: "k", Attempt: 2, At: t0.Add(time.Minute), Instance: "c"},
		{AlertKey: "k", Attempt: 2, At: t0.Add(time.Minute), Instance: "a"},
		{AlertKey: "k", Attempt: 2, At: t0.Add(2 * time.Minute), Instance: "0"},
		{AlertKey: "j", Attempt: 1, At: t0.Add(time.Second), Instance: "x"},
		{AlertKey: "j", Attempt: 1, At: t0, Instance: "y"},
	}
	w := claimWinners(claims, map[string]int{"k": 2, "j": 1})
	if w["k"] != "a" || w["j"] != "y" {
		t.Fatalf("winners %v", w)
	}
}

// The stored payload decodes back into the alert that is sent.
func TestPayloadRoundTrip(t *testing.T) {
	a := newIssueAlert(models.IssueCandidate{Kind: "exception", Fingerprint: "fp", FirstSeen: t0, FirstVersion: "2.0.0",
		Versions: []string{"2.0.0"}, Count: 3, Sessions: 2, Title: "E", Message: "m", Site: "A.B"}, t0)
	b, _ := json.Marshal(a)
	var back Alert
	if err := json.Unmarshal(b, &back); err != nil || back.Title != a.Title || len(back.Facts) != len(a.Facts) {
		t.Fatalf("round trip %v %+v", err, back)
	}
}
