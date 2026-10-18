package alert

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// fakeStore is an in-memory Store with the semantics of the ClickHouse
// queries (storage/repository_alerts.go): the outbox is a list of rows, the
// issue rows are a list of occurrences.
type fakeStore struct {
	mu       sync.Mutex
	alerts   []models.AlertRecord
	states   []models.IssueState
	rows     map[string][]occ // kind -> occurrences
	startups []models.VersionStartup
	rates    func(from, to time.Time) []models.VersionRate
	first    map[string]time.Time
	failOn   map[string]error // method name -> error
}

type occ struct {
	fp, version, session string
	at                   time.Time
	count                int64
	title, message       string
	dev                  bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: map[string][]occ{}, first: map[string]time.Time{}, failOn: map[string]error{}}
}

func (s *fakeStore) add(kind string, o occ) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if o.count == 0 {
		o.count = 1
	}
	s.rows[kind] = append(s.rows[kind], o)
}

func (s *fakeStore) records(status string) []models.AlertRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []models.AlertRecord
	for _, a := range s.alerts {
		if status == "" || a.Status == status {
			out = append(out, a)
		}
	}
	return out
}

func (s *fakeStore) err(name string) error { return s.failOn[name] }

func (s *fakeStore) AlertBaselineDone(ctx context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.alerts {
		if a.AlertKey == key {
			return true, nil
		}
	}
	return false, s.err("AlertBaselineDone")
}

func (s *fakeStore) known(kind string) map[string]bool {
	k := map[string]bool{}
	for _, a := range s.alerts {
		if a.Type == TypeNewIssue && a.Kind == kind {
			k[a.Fingerprint] = true
		}
	}
	return k
}

func (s *fakeStore) candidates(kind string, from time.Time, includeDev bool, skipKnown bool) []models.IssueCandidate {
	known := s.known(kind)
	by := map[string]*models.IssueCandidate{}
	sessions := map[string]map[string]bool{}
	for _, o := range s.rows[kind] {
		if o.at.Before(from) || o.fp == "" || (o.dev && !includeDev) || (skipKnown && known[o.fp]) {
			continue
		}
		c, ok := by[o.fp]
		if !ok {
			c = &models.IssueCandidate{Kind: kind, Fingerprint: o.fp, FirstSeen: o.at, FirstVersion: o.version}
			by[o.fp] = c
			sessions[o.fp] = map[string]bool{}
		}
		if o.at.Before(c.FirstSeen) {
			c.FirstSeen, c.FirstVersion = o.at, o.version
		}
		found := false
		for _, v := range c.Versions {
			found = found || v == o.version
		}
		if !found {
			c.Versions = append(c.Versions, o.version)
		}
		c.Count += o.count
		sessions[o.fp][o.session] = true
		c.Sessions = int64(len(sessions[o.fp]))
		if o.title != "" {
			c.Title = o.title
		}
		if o.message != "" {
			c.Message = o.message
		}
	}
	var out []models.IssueCandidate
	for _, c := range by {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FirstSeen.Equal(out[j].FirstSeen) {
			return out[i].FirstSeen.Before(out[j].FirstSeen)
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	return out
}

func (s *fakeStore) AllIssueFingerprints(ctx context.Context, kind string, includeDev bool, limit int) ([]models.IssueCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.err("AllIssueFingerprints"); err != nil {
		return nil, err
	}
	c := s.candidates(kind, time.Time{}, includeDev, false)
	if len(c) > limit {
		c = c[:limit]
	}
	return c, nil
}

func (s *fakeStore) NewIssueCandidates(ctx context.Context, kind string, from time.Time, includeDev bool, limit int) ([]models.IssueCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.err("NewIssueCandidates"); err != nil {
		return nil, err
	}
	c := s.candidates(kind, from, includeDev, true)
	if len(c) > limit {
		c = c[:limit]
	}
	return c, nil
}

func (s *fakeStore) latestStates() map[string]models.IssueState {
	latest := map[string]models.IssueState{}
	for _, st := range s.states {
		k := st.Kind + "/" + st.Fingerprint
		if cur, ok := latest[k]; !ok || st.Version >= cur.Version {
			latest[k] = st
		}
	}
	return latest
}

func (s *fakeStore) ResolvedIssues(ctx context.Context, kind string, limit int) ([]models.IssueState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []models.IssueState
	for _, st := range s.latestStates() {
		if st.Kind == kind && st.Status == models.IssueResolved {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out, nil
}

func (s *fakeStore) hits(kind string, fps map[string]bool, version string, from time.Time, includeDev bool) []models.IssueVersionHit {
	type key struct{ fp, v string }
	by := map[key]*models.IssueVersionHit{}
	sessions := map[key]map[string]bool{}
	var order []key
	for _, o := range s.rows[kind] {
		if !fps[o.fp] || o.at.Before(from) || (o.dev && !includeDev) || (version != "" && o.version != version) {
			continue
		}
		k := key{o.fp, o.version}
		h, ok := by[k]
		if !ok {
			h = &models.IssueVersionHit{Fingerprint: o.fp, AppVersion: o.version, FirstSeen: o.at, LastSeen: o.at}
			by[k] = h
			sessions[k] = map[string]bool{}
			order = append(order, k)
		}
		if o.at.Before(h.FirstSeen) {
			h.FirstSeen = o.at
		}
		if o.at.After(h.LastSeen) {
			h.LastSeen = o.at
		}
		h.Count += o.count
		sessions[k][o.session] = true
		h.Sessions = int64(len(sessions[k]))
	}
	var out []models.IssueVersionHit
	for _, k := range order {
		out = append(out, *by[k])
	}
	return out
}

func (s *fakeStore) IssueHits(ctx context.Context, kind string, fingerprints []string, from time.Time, includeDev bool) ([]models.IssueVersionHit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[string]bool{}
	for _, f := range fingerprints {
		set[f] = true
	}
	return s.hits(kind, set, "", from, includeDev), nil
}

func (s *fakeStore) IssueHitSince(ctx context.Context, kind, fingerprint, version string, since time.Time, includeDev bool) (models.IssueVersionHit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.hits(kind, map[string]bool{fingerprint: true}, version, since, includeDev)
	if len(h) == 0 {
		return models.IssueVersionHit{Fingerprint: fingerprint, AppVersion: version}, nil
	}
	return h[0], nil
}

func (s *fakeStore) VersionFirstSeen(ctx context.Context, includeDev bool) (map[string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]time.Time{}
	for k, v := range s.first {
		out[k] = v
	}
	return out, nil
}

func (s *fakeStore) InsertIssueState(ctx context.Context, st models.IssueState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.failOn["InsertIssueState"]; err != nil {
		return err
	}
	s.states = append(s.states, st)
	return nil
}

func (s *fakeStore) CrashRates(ctx context.Context, from, to time.Time, includeDev bool) ([]models.VersionRate, error) {
	if s.rates == nil {
		return nil, nil
	}
	return s.rates(from, to), nil
}

func (s *fakeStore) StartupStats(ctx context.Context, from time.Time, includeDev bool) ([]models.VersionStartup, error) {
	return s.startups, nil
}

func (s *fakeStore) InsertAlertRecords(ctx context.Context, recs []models.AlertRecord) error {
	if err := s.err("InsertAlertRecords"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alerts = append(s.alerts, recs...)
	return nil
}

func (s *fakeStore) ExistingAlertKeys(ctx context.Context, keys []string) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	out := map[string]bool{}
	for _, a := range s.alerts {
		if want[a.AlertKey] {
			out[a.AlertKey] = true
		}
	}
	return out, nil
}

func (s *fakeStore) RecentAlertExists(ctx context.Context, alertType, appVersion string, since time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.alerts {
		if a.Type == alertType && a.AppVersion == appVersion && a.Status == models.AlertPending && !a.At.Before(since) {
			return true, nil
		}
	}
	return false, nil
}

// AlertsToSend mirrors the SQL: keys with a pending row since `since`,
// without a sent or abandoned row, aggregated.
func (s *fakeStore) AlertsToSend(ctx context.Context, since time.Time, limit int) ([]models.AlertOutboxEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	by := map[string]*models.AlertOutboxEntry{}
	done := map[string]bool{}
	recent := map[string]bool{}
	lastDoneAttempt := map[string]uint16{}
	for _, a := range s.alerts {
		if a.Status == models.AlertPending && !a.At.Before(since) {
			recent[a.AlertKey] = true
		}
	}
	for _, a := range s.alerts {
		if !recent[a.AlertKey] {
			continue
		}
		e, ok := by[a.AlertKey]
		if !ok {
			e = &models.AlertOutboxEntry{AlertKey: a.AlertKey, Type: a.Type}
			by[a.AlertKey] = e
		}
		switch a.Status {
		case models.AlertPending:
			if e.Payload == "" {
				e.Payload = a.Payload
			}
			if e.CreatedAt.IsZero() || a.At.Before(e.CreatedAt) {
				e.CreatedAt = a.At
			}
		case models.AlertFailed:
			e.Failed++
			if a.At.After(e.LastFailedAt) {
				e.LastFailedAt = a.At
			}
		case models.AlertClaimed:
			if int(a.Attempt) > e.LastClaim {
				e.LastClaim = int(a.Attempt)
			}
			if a.At.After(e.LastClaimAt) {
				e.LastClaimAt = a.At
			}
		case models.AlertSent, models.AlertAbandoned:
			done[a.AlertKey] = true
		}
		if a.Status == models.AlertSent || a.Status == models.AlertFailed {
			if int(a.Attempt) > e.LastDone {
				e.LastDone = int(a.Attempt)
			}
			if a.Attempt >= lastDoneAttempt[a.AlertKey] {
				lastDoneAttempt[a.AlertKey] = a.Attempt
				e.LastDoneStatus = a.Status
			}
		}
	}
	var out []models.AlertOutboxEntry
	for k, e := range by {
		if !done[k] {
			out = append(out, *e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].AlertKey < out[j].AlertKey
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *fakeStore) AlertClaims(ctx context.Context, keys []string) ([]models.AlertClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	var out []models.AlertClaim
	for _, a := range s.alerts {
		if want[a.AlertKey] && a.Status == models.AlertClaimed {
			out = append(out, models.AlertClaim{AlertKey: a.AlertKey, Attempt: int(a.Attempt), At: a.At, Instance: a.Instance})
		}
	}
	return out, nil
}

// fakeSender records the alerts sent; err (if set) fails every attempt.
type fakeSender struct {
	mu   sync.Mutex
	sent []Alert
	err  error
}

func (f *fakeSender) Send(ctx context.Context, a Alert) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, a)
	return nil
}

func (f *fakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}
