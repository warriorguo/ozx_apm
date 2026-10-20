package sessionfiles

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/objstore"
)

// Expiry (no bucket lifecycle rule is assumed: the server's key may not set
// one). Every CleanerInterval each server process with the object store
// configured:
//
//  1. deletes the uploads past expires_at (complete or not) and the uploads
//     never committed within Incomplete: their objects first (the archive,
//     the parts), then their index rows (ALTER DELETE). A failure leaves the
//     row, so the next run tries again; nothing is ever unreferenced
//     because of a failed run.
//  2. once a day sweeps the bucket for what step 1 cannot see: part
//     objects older than Incomplete + 1 day, archives older than Retention +
//     1 day whose row is gone (the table's TTL removed it a month after
//     expiry, or a privacy deletion's object step failed) or expired.
//
// Two processes (apps 36 and 38) may run it at the same time: every step is
// idempotent (deleting a deleted object or row is a no-op), and the start is
// spread by a random delay.

// RunCleaner runs the cleaner until ctx ends.
func (s *Service) RunCleaner(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	delay := time.Duration(rand.Int63n(int64(minDuration(interval, 10*time.Minute)))) + time.Minute
	var lastSweep time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = interval
		n, err := s.ExpireOnce(ctx)
		if err != nil {
			s.logger.Error("session files: expiry failed (retried at the next run)", zap.Error(err))
		} else if n > 0 {
			s.logger.Info("session files: expired uploads deleted", zap.Int("uploads", n))
		}
		if s.now().Sub(lastSweep) >= 24*time.Hour {
			lastSweep = s.now()
			if n, err := s.SweepOnce(ctx); err != nil {
				s.logger.Error("session files: bucket sweep failed", zap.Error(err))
			} else if n > 0 {
				s.logger.Info("session files: orphaned objects deleted", zap.Int("objects", n))
			}
		}
	}
}

// multipartLister is implemented by *objstore.Client.
type multipartLister interface {
	ListMultipartUploads(ctx context.Context, prefix string) ([]objstore.MultipartUpload, error)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// ExpireOnce deletes expired and abandoned uploads; returns how many.
func (s *Service) ExpireOnce(ctx context.Context) (int, error) {
	total := 0
	for round := 0; round < 20; round++ {
		now := s.now()
		files, err := s.store.SessionFilesToExpire(ctx, now, now.Add(-s.cfg.Incomplete), 200)
		if err != nil {
			return total, err
		}
		if len(files) == 0 {
			return total, nil
		}
		ids, err := s.deleteObjects(ctx, files)
		if len(ids) > 0 {
			if derr := s.store.DeleteSessionFileRows(ctx, ids); derr != nil {
				return total, derr
			}
			total += len(ids)
		}
		if err != nil {
			return total, err
		}
		if len(files) < 200 {
			return total, nil
		}
	}
	return total, nil
}

// deleteObjects deletes the archive and parts of each upload; returns the
// uploads whose objects are all gone (their rows may be deleted).
func (s *Service) deleteObjects(ctx context.Context, files []models.SessionFile) ([]string, error) {
	var done []string
	for _, f := range files {
		// By the stored key, also when the prefix was changed since.
		if f.ObjectKey != "" {
			if err := s.obj.DeleteObject(ctx, f.ObjectKey); err != nil {
				return done, fmt.Errorf("delete %s: %w", f.UploadID, err)
			}
		}
		prefix := s.TmpPrefix(f.UploadID)
		for i := 0; i < 20; i++ {
			objs, _, err := s.obj.ListObjects(ctx, prefix, "", 1000)
			if err != nil {
				return done, fmt.Errorf("list parts of %s: %w", f.UploadID, err)
			}
			if len(objs) == 0 {
				break
			}
			for _, o := range objs {
				if err := s.obj.DeleteObject(ctx, o.Key); err != nil {
					return done, fmt.Errorf("delete part of %s: %w", f.UploadID, err)
				}
			}
		}
		s.forget(f.UploadID)
		done = append(done, f.UploadID)
	}
	return done, nil
}

// SweepOnce deletes objects no index row accounts for; returns how many.
func (s *Service) SweepOnce(ctx context.Context) (int, error) {
	now := s.now()
	deleted := 0
	// Parts of uploads never committed (or whose commit was interrupted).
	partCutoff := now.Add(-s.cfg.Incomplete - 24*time.Hour)
	if n, err := s.sweep(ctx, s.cfg.Prefix+"tmp/", func(key string, modified time.Time) (bool, error) {
		return modified.Before(partCutoff), nil
	}); err != nil {
		return deleted + n, err
	} else {
		deleted += n
	}
	// Multipart uploads an assembly left open (the process stopped during
	// a commit): S3 keeps their parts until they are aborted.
	if ml, ok := s.obj.(multipartLister); ok {
		ups, err := ml.ListMultipartUploads(ctx, s.cfg.Prefix+"sessions/")
		if err != nil {
			s.logger.Warn("session files: listing open multipart uploads failed (skipped)", zap.Error(err))
		}
		for _, u := range ups {
			if !u.Initiated.IsZero() && u.Initiated.Before(now.Add(-24*time.Hour)) {
				if err := s.obj.AbortMultipartUpload(ctx, u.Key, u.UploadID); err != nil {
					return deleted, err
				}
				deleted++
			}
		}
	}
	// Archives: kept while their row says so.
	cutoff := now.Add(-24 * time.Hour)
	n, err := s.sweep(ctx, s.cfg.Prefix+"sessions/", func(key string, modified time.Time) (bool, error) {
		if !modified.Before(cutoff) {
			return false, nil
		}
		id := strings.TrimSuffix(key[strings.LastIndex(key, "/")+1:], ".zip")
		if !ValidUploadID(id) {
			return false, nil // not ours
		}
		f, err := s.store.GetSessionFile(ctx, id)
		if err != nil {
			return false, err
		}
		switch {
		case f == nil, f.ObjectKey != key, f.Status == models.SessionFileFailed, !f.ExpiresAt.After(now):
			return true, nil // no row refers to it any more, or it is expired
		}
		return false, nil // complete, or a commit in progress (step 1 handles abandoned uploads)
	})
	return deleted + n, err
}

func (s *Service) sweep(ctx context.Context, prefix string, del func(key string, modified time.Time) (bool, error)) (int, error) {
	deleted := 0
	after := ""
	for page := 0; page < 1000; page++ {
		objs, more, err := s.obj.ListObjects(ctx, prefix, after, 1000)
		if err != nil {
			return deleted, err
		}
		for _, o := range objs {
			ok, err := del(o.Key, o.LastModified)
			if err != nil {
				return deleted, err
			}
			if ok {
				if err := s.obj.DeleteObject(ctx, o.Key); err != nil {
					return deleted, err
				}
				deleted++
			}
		}
		if !more {
			break
		}
		after = objs[len(objs)-1].Key
	}
	return deleted, nil
}

// ------------------------------------------------------------------
// Privacy deletion (APM-14)

// PurgeResult is what a privacy deletion did to the session files.
type PurgeResult = models.PrivacySessionFiles

// uploadsOf finds a subject's uploads: by device id directly; by player id
// through the sessions of the player's ops events (APM-12; the session id
// is shared by the technical and the ops data).
func (s *Service) uploadsOf(ctx context.Context, idType, id string) ([]models.SessionFile, error) {
	switch idType {
	case "device_id":
		return s.store.SessionFilesOfDevice(ctx, id)
	case "player_id":
		sessions, err := s.store.OpsSessionsOfPlayer(ctx, id, 100000)
		if err != nil {
			return nil, err
		}
		var out []models.SessionFile
		for i := 0; i < len(sessions); i += 1000 {
			end := i + 1000
			if end > len(sessions) {
				end = len(sessions)
			}
			files, err := s.store.SessionFilesOfSessions(ctx, sessions[i:end])
			if err != nil {
				return nil, err
			}
			out = append(out, files...)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown id type %q", idType)
}

// Count returns how many uploads of the subject are stored.
func (s *Service) Count(ctx context.Context, idType, id string) (int, error) {
	files, err := s.uploadsOf(ctx, idType, id)
	return len(files), err
}

// Purge deletes the subject's recordings: objects first, then index rows.
func (s *Service) Purge(ctx context.Context, idType, id string) PurgeResult {
	files, err := s.uploadsOf(ctx, idType, id)
	if err != nil {
		return PurgeResult{Error: "find session files: " + err.Error()}
	}
	res := PurgeResult{UploadsBefore: len(files), UploadsAfter: len(files)}
	if len(files) == 0 {
		return res
	}
	ids, derr := s.deleteObjects(ctx, files)
	if len(ids) > 0 {
		if err := s.store.DeleteSessionFileRows(ctx, ids); err != nil {
			res.Error = "delete session file rows: " + err.Error()
			return res
		}
	}
	if derr != nil {
		res.Error = "delete session file objects: " + derr.Error()
	}
	if after, err := s.uploadsOf(ctx, idType, id); err == nil {
		res.UploadsAfter = len(after)
	} else if res.Error == "" {
		res.Error = "count session files: " + err.Error()
	}
	return res
}
