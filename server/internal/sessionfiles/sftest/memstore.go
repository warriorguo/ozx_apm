// Package sftest has an in-memory sessionfiles.Store for tests.
package sftest

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// MemStore keeps the index rows in memory with the table's semantics: every
// insert is a row, the latest version of an upload wins.
type MemStore struct {
	mu        sync.Mutex
	rows      []models.SessionFile
	PlayerOps map[string][]string // player id -> session ids (ops_events)
	// Fail makes reads and writes fail (set before use; not synchronized).
	Fail    error
	Deletes int
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore { return &MemStore{PlayerOps: map[string][]string{}} }

// Rows returns every row written (all versions).
func (s *MemStore) Rows() []models.SessionFile {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]models.SessionFile(nil), s.rows...)
}

func (s *MemStore) latest() map[string]models.SessionFile {
	out := map[string]models.SessionFile{}
	for _, r := range s.rows {
		if cur, ok := out[r.UploadID]; !ok || r.Version >= cur.Version {
			out[r.UploadID] = r
		}
	}
	return out
}

func (s *MemStore) sorted(match func(models.SessionFile) bool) []models.SessionFile {
	out := []models.SessionFile{}
	for _, f := range s.latest() {
		if match(f) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *MemStore) InsertSessionFile(ctx context.Context, f models.SessionFile) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		return s.Fail
	}
	s.rows = append(s.rows, f)
	return nil
}

func (s *MemStore) GetSessionFile(ctx context.Context, id string) (*models.SessionFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		return nil, s.Fail
	}
	f, ok := s.latest()[id]
	if !ok {
		return nil, nil
	}
	return &f, nil
}

func (s *MemStore) ListSessionFiles(ctx context.Context, f models.SessionFileFilter) ([]models.SessionFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.sorted(func(x models.SessionFile) bool {
		if f.GitCommit != "" && !strings.HasPrefix(strings.ToLower(x.GitCommit), strings.ToLower(f.GitCommit)) {
			return false
		}
		if f.Status == "" && x.Status != models.SessionFileComplete {
			return false
		}
		return f.DeviceID == "" || f.DeviceID == x.DeviceID
	})
	return out, nil
}

func (s *MemStore) SessionFileDeviceUsage(ctx context.Context, device string, since time.Time) (uint64, uint64, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		return 0, 0, 0, s.Fail
	}
	var n, b, open uint64
	for _, f := range s.latest() {
		if f.DeviceID == device && !f.CreatedAt.Before(since) {
			n++
			b += f.SizeBytes
			if f.Status == models.SessionFileUploading {
				open++
			}
		}
	}
	return n, b, open, nil
}

func (s *MemStore) SessionFilesToExpire(ctx context.Context, now, stale time.Time, limit int) ([]models.SessionFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.sorted(func(f models.SessionFile) bool {
		return !f.ExpiresAt.After(now) || (f.Status != models.SessionFileComplete && f.CreatedAt.Before(stale))
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemStore) SessionFilesOfDevice(ctx context.Context, device string) ([]models.SessionFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sorted(func(f models.SessionFile) bool { return f.DeviceID == device }), nil
}

func (s *MemStore) SessionFilesOfSessions(ctx context.Context, ids []string) ([]models.SessionFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return s.sorted(func(f models.SessionFile) bool { return set[f.SessionID] }), nil
}

func (s *MemStore) OpsSessionsOfPlayer(ctx context.Context, player string, limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.PlayerOps[player], nil
}

func (s *MemStore) DeleteSessionFileRows(ctx context.Context, ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Fail != nil {
		return s.Fail
	}
	s.Deletes++
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	kept := s.rows[:0]
	for _, r := range s.rows {
		if !set[r.UploadID] {
			kept = append(kept, r)
		}
	}
	s.rows = kept
	return nil
}

func (s *MemStore) SessionFileAvailability(ctx context.Context, ids []string) ([]models.SessionFileAvailability, error) {
	files, _ := s.SessionFilesOfSessions(ctx, ids)
	out := []models.SessionFileAvailability{}
	for _, f := range files {
		if f.Status == models.SessionFileComplete {
			out = append(out, models.SessionFileAvailability{SessionID: f.SessionID, UploadID: f.UploadID, SizeBytes: f.SizeBytes, CreatedAt: f.CreatedAt})
		}
	}
	return out, nil
}

// ErrStore is a storage failure.
var ErrStore = errors.New("clickhouse down")
