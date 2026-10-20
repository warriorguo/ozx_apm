// Package sessionfiles receives, stores and serves the AiTelemetry session
// directories that development builds record (APM-13): OZX's
// SessionFolder/<session>/ (session.json, level*.json, the channel JSONL
// files, room subdirectories), zipped on the device with the directory as
// the zip's only top-level entry, so a download unzips to exactly that
// directory and OZX's ai_trace.py / verify-ticket read it unmodified.
//
// Protocol (ingest server, /v1/session-files; stateless across replicas:
// the state is the index row in ClickHouse and the part objects in S3):
//
//  1. POST /v1/session-files            create (or resume) an upload: metadata,
//     size, SHA-256, part size. Answers the parts already received.
//  2. PUT  /v1/session-files/{id}/parts/{n}  one part (at most 4 MiB, its
//     SHA-256 in X-Apm-Part-Sha256), stored as an object of its own.
//  3. POST /v1/session-files/{id}/commit  the server joins the parts into one
//     object (S3 multipart, 8 MiB parts), checks the archive's SHA-256 and
//     structure, marks the upload complete and deletes the parts.
//
// A part is any size the client chose (64 KiB - 4 MiB, below proxies' body
// limits and the ingest server's timeouts on a slow phone network); S3's
// multipart parts must be 5 MiB or more, which is why the parts are objects
// and the server assembles them. Every step is idempotent: a client that
// lost an answer repeats it.
//
// Storage: <prefix>tmp/<upload>/<n> (parts, deleted at commit or after
// IncompleteDays), <prefix>sessions/<yyyy>/<mm>/<dd>/<upload>.zip (the
// recording, deleted after Retention). Index: storage.SessionFilesTable.
package sessionfiles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"go.uber.org/zap"

	"github.com/warriorguo/ozx_apm/server/internal/models"
	"github.com/warriorguo/ozx_apm/server/internal/objstore"
)

// Limits of the protocol.
const (
	MinPartBytes      = 64 << 10
	MaxPartBytes      = 4 << 20
	MaxParts          = 10000
	MinArchiveBytes   = 22 // an empty zip
	MaxSessionJSON    = 64 << 10
	MaxCreateBody     = MaxSessionJSON + 16<<10
	assemblyPartBytes = 8 << 20 // S3 multipart part size of the assembly (>= 5 MiB)
	maxArchiveEntries = 50000
	maxRawPerArchive  = 64 // uncompressed bytes may be at most this many times the archive (zip bombs)
)

// Store is what the service needs from ClickHouse (*storage.Repository).
type Store interface {
	InsertSessionFile(ctx context.Context, f models.SessionFile) error
	GetSessionFile(ctx context.Context, uploadID string) (*models.SessionFile, error)
	ListSessionFiles(ctx context.Context, f models.SessionFileFilter) ([]models.SessionFile, error)
	SessionFileDeviceUsage(ctx context.Context, deviceID string, since time.Time) (uploads, bytes, open uint64, err error)
	SessionFilesToExpire(ctx context.Context, now, staleBefore time.Time, limit int) ([]models.SessionFile, error)
	SessionFilesOfDevice(ctx context.Context, deviceID string) ([]models.SessionFile, error)
	SessionFilesOfSessions(ctx context.Context, sessionIDs []string) ([]models.SessionFile, error)
	OpsSessionsOfPlayer(ctx context.Context, playerID string, limit int) ([]string, error)
	DeleteSessionFileRows(ctx context.Context, uploadIDs []string) error
	SessionFileAvailability(ctx context.Context, sessionIDs []string) ([]models.SessionFileAvailability, error)
}

// Objects is what the service needs from the object store (*objstore.Client).
type Objects interface {
	PutObject(ctx context.Context, key string, data []byte, contentType string) error
	GetObject(ctx context.Context, key string) (io.ReadCloser, int64, error)
	GetRange(ctx context.Context, key string, off, n int64) ([]byte, error)
	DeleteObject(ctx context.Context, key string) error
	ListObjects(ctx context.Context, prefix, startAfter string, max int) ([]objstore.Object, bool, error)
	CreateMultipartUpload(ctx context.Context, key, contentType string) (string, error)
	UploadPart(ctx context.Context, key, uploadID string, n int, data []byte) (string, error)
	CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts []objstore.Part) error
	AbortMultipartUpload(ctx context.Context, key, uploadID string) error
}

// Config of the service (config.SessionFilesConfig and config.S3Config.Prefix).
type Config struct {
	Prefix             string
	Retention          time.Duration
	Incomplete         time.Duration
	MaxArchiveBytes    int64
	MaxUploadsPerDay   int
	MaxBytesPerDay     int64
	MaxOpenUploads     int
	ConcurrentParts    int // parts read and stored at once per process (each up to MaxPartBytes in memory)
	ConcurrentCommits  int // assemblies at once per process (each 8 MiB in memory)
	SlotWait           time.Duration
	MetadataCacheTTL   time.Duration
	MaxMetadataEntries int
}

func (c Config) withDefaults() Config {
	if c.Prefix == "" {
		c.Prefix = "session-files/"
	}
	if !strings.HasSuffix(c.Prefix, "/") {
		c.Prefix += "/"
	}
	if c.Retention <= 0 {
		c.Retention = 30 * 24 * time.Hour
	}
	if c.Incomplete <= 0 {
		c.Incomplete = 7 * 24 * time.Hour
	}
	if c.MaxArchiveBytes <= 0 {
		c.MaxArchiveBytes = 512 << 20
	}
	if c.MaxUploadsPerDay <= 0 {
		c.MaxUploadsPerDay = 100
	}
	if c.MaxBytesPerDay <= 0 {
		c.MaxBytesPerDay = 4 << 30
	}
	if c.MaxOpenUploads <= 0 {
		c.MaxOpenUploads = 4
	}
	if c.ConcurrentParts <= 0 {
		c.ConcurrentParts = 8
	}
	if c.ConcurrentCommits <= 0 {
		c.ConcurrentCommits = 2
	}
	if c.SlotWait <= 0 {
		c.SlotWait = 3 * time.Second
	}
	if c.MetadataCacheTTL <= 0 {
		c.MetadataCacheTTL = 5 * time.Minute
	}
	if c.MaxMetadataEntries <= 0 {
		c.MaxMetadataEntries = 4096
	}
	return c
}

// Error is a refusal with its HTTP status. RejectCode set: the client must
// not retry this upload (the SDK drops it). Retry: try again later.
type Error struct {
	Status     int
	RejectCode string
	Message    string
	RetryAfter int   // seconds, with Retry
	Missing    []int // commit: the parts still missing
}

func (e *Error) Error() string { return e.Message }

func reject(status int, code, format string, args ...interface{}) *Error {
	return &Error{Status: status, RejectCode: code, Message: fmt.Sprintf(format, args...)}
}

func retryLater(status, after int, format string, args ...interface{}) *Error {
	return &Error{Status: status, RetryAfter: after, Message: fmt.Sprintf(format, args...)}
}

func badRequest(format string, args ...interface{}) *Error {
	return reject(http.StatusBadRequest, "invalid_request", format, args...)
}

// Service implements the protocol, the listing, downloads, expiry and
// privacy deletion.
type Service struct {
	store  Store
	obj    Objects
	cfg    Config
	logger *zap.Logger
	now    func() time.Time

	partSlots   chan struct{}
	commitSlots chan struct{}

	mu    sync.Mutex
	cache map[string]cachedUpload
}

type cachedUpload struct {
	f  models.SessionFile
	at time.Time
}

// New returns the service.
func New(store Store, obj Objects, cfg Config, logger *zap.Logger) *Service {
	cfg = cfg.withDefaults()
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{store: store, obj: obj, cfg: cfg, logger: logger, now: time.Now,
		partSlots: make(chan struct{}, cfg.ConcurrentParts), commitSlots: make(chan struct{}, cfg.ConcurrentCommits),
		cache: map[string]cachedUpload{}}
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Config returns the effective configuration.
func (s *Service) Config() Config { return s.cfg }

// TmpPrefix is the prefix of an upload's part objects.
func (s *Service) TmpPrefix(uploadID string) string { return s.cfg.Prefix + "tmp/" + uploadID + "/" }

func (s *Service) partKey(uploadID string, n int) string {
	return fmt.Sprintf("%s%06d", s.TmpPrefix(uploadID), n)
}

func (s *Service) objectKey(uploadID string, created time.Time) string {
	return s.cfg.Prefix + "sessions/" + created.UTC().Format("2006/01/02") + "/" + uploadID + ".zip"
}

// ------------------------------------------------------------------
// Create

// CreateRequest is the body of POST /v1/session-files.
type CreateRequest struct {
	UploadID      string `json:"upload_id"`
	DeviceID      string `json:"device_id"`
	SessionID     string `json:"session_id"`
	RecordingName string `json:"recording_name"`
	AppVersion    string `json:"app_version"`
	AppBuild      string `json:"build"`
	BuildType     string `json:"build_type"`
	Platform      string `json:"platform"`
	DeviceModel   string `json:"device_model"`
	OSVersion     string `json:"os_version"`
	UnityVersion  string `json:"unity_version"`
	SDKVersion    string `json:"sdk_version"`
	GitCommit     string `json:"git_commit"`
	SizeBytes     int64  `json:"size_bytes"`
	SHA256        string `json:"sha256"`
	PartBytes     int64  `json:"part_bytes"`
	PartCount     int64  `json:"part_count"`
	FileCount     int64  `json:"file_count"`
	RawBytes      int64  `json:"raw_bytes"`
	SessionJSON   string `json:"session_json"`
}

// UploadState is the answer to create and commit.
type UploadState struct {
	UploadID  string `json:"upload_id"`
	Status    string `json:"status"`
	PartBytes int64  `json:"part_bytes"`
	PartCount int64  `json:"part_count"`
	Received  []int  `json:"received"`
	FileCount uint32 `json:"file_count,omitempty"`
}

var (
	hex32Re         = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hex64Re         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	idRe            = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
	recordingNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	commitRe        = regexp.MustCompile(`^[A-Za-z0-9._-]{1,80}$`)
)

// ValidUploadID reports whether s is an upload id (32 lower-case hex digits).
func ValidUploadID(s string) bool { return hex32Re.MatchString(s) }

// ValidRecordingName reports whether s can be the session directory's name.
func ValidRecordingName(s string) bool {
	return recordingNameRe.MatchString(s) && !strings.Contains(s, "..")
}

// clean cuts a text field to max bytes (valid UTF-8) and drops control characters.
func clean(s string, max int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// sessionSummary is what the index keeps of session.json.
type sessionSummary struct {
	StartedAt, Scene, GitCommit, UnityVersion, DataSource, DataPackVersion, DataPackSHA256 string
}

func jsonText(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

// summarize reads AiTelemetry's session.json keys (Documents/Shared/AITelemetry.md);
// anything unreadable is left empty, never an error (the file is the game's).
func summarize(sessionJSON string) sessionSummary {
	var m map[string]interface{}
	dec := json.NewDecoder(strings.NewReader(sessionJSON))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		return sessionSummary{}
	}
	return sessionSummary{
		StartedAt:       clean(jsonText(m["startedAt"]), 64),
		Scene:           clean(jsonText(m["scene"]), 128),
		GitCommit:       jsonText(m["gitCommit"]),
		UnityVersion:    clean(jsonText(m["unityVersion"]), 64),
		DataSource:      clean(jsonText(m["dataSource"]), 32),
		DataPackVersion: clean(jsonText(m["dataPackVersion"]), 32),
		DataPackSHA256:  clean(jsonText(m["dataPackSha256"]), 128),
	}
}

// validateCreate checks a create request and returns the row to store.
func (s *Service) validateCreate(req CreateRequest, now time.Time) (models.SessionFile, *Error) {
	var f models.SessionFile
	if !ValidUploadID(req.UploadID) {
		return f, badRequest("upload_id must be 32 lower-case hex digits")
	}
	if !idRe.MatchString(req.DeviceID) {
		return f, badRequest("device_id is required (1-128 of A-Z a-z 0-9 _ . : -)")
	}
	if req.SessionID != "" && !idRe.MatchString(req.SessionID) {
		return f, badRequest("session_id: 1-128 of A-Z a-z 0-9 _ . : -")
	}
	if !ValidRecordingName(req.RecordingName) {
		return f, badRequest("recording_name must be the session directory's name (1-128 of A-Z a-z 0-9 _ . -, not starting with . or -)")
	}
	switch req.BuildType {
	case "dev", "editor":
	default:
		// Release builds never record or upload session files (APM-13);
		// a release build that sends one is a bug: refuse for good.
		return f, reject(http.StatusForbidden, "release_build",
			"session files are accepted from development and editor builds only (build_type dev or editor, got %q)", clean(req.BuildType, 32))
	}
	if req.SizeBytes < MinArchiveBytes {
		return f, badRequest("size_bytes must be at least %d", MinArchiveBytes)
	}
	if req.SizeBytes > s.cfg.MaxArchiveBytes {
		return f, reject(http.StatusRequestEntityTooLarge, "too_large", "the archive is %d bytes; at most %d are accepted",
			req.SizeBytes, s.cfg.MaxArchiveBytes)
	}
	if req.PartBytes < MinPartBytes || req.PartBytes > MaxPartBytes {
		return f, badRequest("part_bytes must be %d-%d", MinPartBytes, MaxPartBytes)
	}
	parts := (req.SizeBytes + req.PartBytes - 1) / req.PartBytes
	if parts > MaxParts {
		return f, badRequest("too many parts (%d): use larger parts", parts)
	}
	if req.PartCount != 0 && req.PartCount != parts {
		return f, badRequest("part_count %d does not match size_bytes / part_bytes (%d)", req.PartCount, parts)
	}
	if !hex64Re.MatchString(req.SHA256) {
		return f, badRequest("sha256 must be 64 lower-case hex digits")
	}
	if len(req.SessionJSON) > MaxSessionJSON {
		return f, badRequest("session_json is longer than %d bytes", MaxSessionJSON)
	}
	if req.FileCount < 0 || req.RawBytes < 0 {
		return f, badRequest("file_count and raw_bytes must not be negative")
	}
	sum := summarize(req.SessionJSON)
	commit := req.GitCommit
	if commit == "" {
		commit = sum.GitCommit
	}
	commit = strings.TrimSpace(commit)
	if commit != "" && !commitRe.MatchString(commit) {
		commit = ""
	}
	unity := clean(req.UnityVersion, 64)
	if unity == "" {
		unity = sum.UnityVersion
	}
	created := now.UTC().Truncate(time.Millisecond)
	f = models.SessionFile{
		UploadID: req.UploadID, Status: models.SessionFileUploading,
		Version: models.SessionFileVersion(models.SessionFileUploading, created), CreatedAt: created, UpdatedAt: created,
		ExpiresAt: created.Add(s.cfg.Retention).Truncate(time.Second),
		DeviceID:  req.DeviceID, SessionID: req.SessionID, RecordingName: req.RecordingName,
		AppVersion: clean(req.AppVersion, 64), AppBuild: clean(req.AppBuild, 64), BuildType: req.BuildType,
		Platform: clean(req.Platform, 32), DeviceModel: clean(req.DeviceModel, 128), OSVersion: clean(req.OSVersion, 128),
		UnityVersion: unity, SDKVersion: clean(req.SDKVersion, 32), GitCommit: commit,
		StartedAt: sum.StartedAt, Scene: sum.Scene, DataSource: sum.DataSource, DataPackVersion: sum.DataPackVersion,
		DataPackSHA256: sum.DataPackSHA256,
		SizeBytes:      uint64(req.SizeBytes), PartBytes: uint32(req.PartBytes), PartCount: uint32(parts), SHA256: req.SHA256,
		FileCount: uint32(minInt64(req.FileCount, 1<<31)), RawBytes: uint64(req.RawBytes),
		SessionJSON: req.SessionJSON,
	}
	if !utf8.ValidString(f.SessionJSON) {
		f.SessionJSON = strings.ToValidUTF8(f.SessionJSON, "�")
	}
	f.ObjectKey = s.objectKey(f.UploadID, created)
	return f, nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func sameUpload(a, b models.SessionFile) bool {
	return a.DeviceID == b.DeviceID && a.SHA256 == b.SHA256 && a.SizeBytes == b.SizeBytes && a.PartBytes == b.PartBytes
}

func storageError(err error) *Error {
	return retryLater(http.StatusServiceUnavailable, 30, "storage error, retry later")
}

// Create starts an upload or resumes it.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*UploadState, error) {
	now := s.now()
	f, verr := s.validateCreate(req, now)
	if verr != nil {
		return nil, verr
	}
	existing, err := s.store.GetSessionFile(ctx, f.UploadID)
	if err != nil {
		s.logger.Error("session files: read upload", zap.String("upload_id", f.UploadID), zap.Error(err))
		return nil, storageError(err)
	}
	if existing != nil {
		if !sameUpload(*existing, f) {
			return nil, reject(http.StatusConflict, "upload_conflict", "upload %s exists with another device, size or checksum", f.UploadID)
		}
		switch existing.Status {
		case models.SessionFileComplete:
			return &UploadState{UploadID: f.UploadID, Status: existing.Status, PartBytes: int64(existing.PartBytes),
				PartCount: int64(existing.PartCount), Received: []int{}, FileCount: existing.FileCount}, nil
		case models.SessionFileFailed:
			return nil, reject(http.StatusConflict, "upload_failed", "upload %s failed: %s", f.UploadID, existing.Error)
		}
		s.remember(*existing)
		received, err := s.receivedParts(ctx, *existing)
		if err != nil {
			s.logger.Error("session files: list parts", zap.String("upload_id", f.UploadID), zap.Error(err))
			return nil, storageError(err)
		}
		return &UploadState{UploadID: f.UploadID, Status: existing.Status, PartBytes: int64(existing.PartBytes),
			PartCount: int64(existing.PartCount), Received: received}, nil
	}

	uploads, bytes, open, err := s.store.SessionFileDeviceUsage(ctx, f.DeviceID, now.Add(-24*time.Hour))
	if err != nil {
		s.logger.Error("session files: device usage", zap.Error(err))
		return nil, storageError(err)
	}
	if int(uploads) >= s.cfg.MaxUploadsPerDay || int64(bytes)+int64(f.SizeBytes) > s.cfg.MaxBytesPerDay {
		return nil, retryLater(http.StatusTooManyRequests, 3600, "this device uploaded %d recordings (%d bytes) in 24 hours; retry later", uploads, bytes)
	}
	if int(open) >= s.cfg.MaxOpenUploads {
		return nil, retryLater(http.StatusTooManyRequests, 600, "this device has %d uploads in progress; finish them first", open)
	}
	if err := s.store.InsertSessionFile(ctx, f); err != nil {
		s.logger.Error("session files: insert upload", zap.String("upload_id", f.UploadID), zap.Error(err))
		return nil, storageError(err)
	}
	s.remember(f)
	s.logger.Info("session files: upload started", zap.String("upload_id", f.UploadID), zap.String("build_type", f.BuildType),
		zap.String("git_commit", f.GitCommit), zap.Uint64("size_bytes", f.SizeBytes), zap.Uint32("parts", f.PartCount))
	return &UploadState{UploadID: f.UploadID, Status: f.Status, PartBytes: int64(f.PartBytes), PartCount: int64(f.PartCount), Received: []int{}}, nil
}

func (s *Service) remember(f models.SessionFile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.cache) >= s.cfg.MaxMetadataEntries {
		s.cache = map[string]cachedUpload{}
	}
	s.cache[f.UploadID] = cachedUpload{f: f, at: s.now()}
}

func (s *Service) forget(uploadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, uploadID)
}

// upload returns the upload's latest row (cached briefly for parts).
func (s *Service) upload(ctx context.Context, uploadID string, cached bool) (*models.SessionFile, error) {
	if cached {
		s.mu.Lock()
		c, ok := s.cache[uploadID]
		s.mu.Unlock()
		if ok && s.now().Sub(c.at) < s.cfg.MetadataCacheTTL {
			f := c.f
			return &f, nil
		}
	}
	f, err := s.store.GetSessionFile(ctx, uploadID)
	if err != nil {
		return nil, err
	}
	if f != nil && f.Status == models.SessionFileUploading {
		s.remember(*f)
	}
	return f, nil
}

// expectedPartSize is the size of part n of an upload.
func expectedPartSize(f models.SessionFile, n int) int64 {
	if n < int(f.PartCount)-1 {
		return int64(f.PartBytes)
	}
	return int64(f.SizeBytes) - int64(f.PartBytes)*int64(f.PartCount-1)
}

// receivedParts lists the parts stored for the upload (with the right size).
func (s *Service) receivedParts(ctx context.Context, f models.SessionFile) ([]int, error) {
	prefix := s.TmpPrefix(f.UploadID)
	got := []int{}
	after := ""
	for {
		objs, more, err := s.obj.ListObjects(ctx, prefix, after, 1000)
		if err != nil {
			return nil, err
		}
		for _, o := range objs {
			n, err := strconv.Atoi(strings.TrimPrefix(o.Key, prefix))
			if err != nil || n < 0 || n >= int(f.PartCount) || o.Size != expectedPartSize(f, n) {
				continue
			}
			got = append(got, n)
		}
		if !more {
			break
		}
		after = objs[len(objs)-1].Key
	}
	sort.Ints(got)
	return got, nil
}

func (s *Service) acquire(ctx context.Context, slots chan struct{}) bool {
	t := time.NewTimer(s.cfg.SlotWait)
	defer t.Stop()
	select {
	case slots <- struct{}{}:
		return true
	case <-t.C:
	case <-ctx.Done():
	}
	return false
}

// ------------------------------------------------------------------
// Parts

// PutPart stores part n of an upload. body is read up to the part's
// expected size (+1, to detect a longer body).
func (s *Service) PutPart(ctx context.Context, uploadID string, n int, deviceID, sha string, body io.Reader) error {
	if !ValidUploadID(uploadID) {
		return badRequest("invalid upload id")
	}
	if !hex64Re.MatchString(sha) {
		return badRequest("X-Apm-Part-Sha256 must be the part's SHA-256 (64 lower-case hex digits)")
	}
	f, err := s.upload(ctx, uploadID, true)
	if err != nil {
		s.logger.Error("session files: read upload", zap.String("upload_id", uploadID), zap.Error(err))
		return storageError(err)
	}
	if err := checkOwner(f, deviceID); err != nil {
		return err
	}
	switch f.Status {
	case models.SessionFileComplete:
		return nil // already joined: the part is not needed any more
	case models.SessionFileFailed:
		return reject(http.StatusConflict, "upload_failed", "upload %s failed: %s", uploadID, f.Error)
	}
	if n < 0 || n >= int(f.PartCount) {
		return badRequest("part %d out of range (0-%d)", n, f.PartCount-1)
	}
	want := expectedPartSize(*f, n)
	if !s.acquire(ctx, s.partSlots) {
		return retryLater(http.StatusServiceUnavailable, 5, "busy, retry")
	}
	defer func() { <-s.partSlots }()
	data, err := io.ReadAll(io.LimitReader(body, want+1))
	if err != nil {
		return retryLater(http.StatusBadRequest, 1, "reading the part failed: %v", err)
	}
	if int64(len(data)) != want {
		return badRequest("part %d is %d bytes, expected %d", n, len(data), want)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != sha {
		// Corrupted in transit (or a proxy changed it): the client sends it again.
		return &Error{Status: http.StatusBadRequest, Message: fmt.Sprintf("part %d: SHA-256 mismatch, send it again", n), RetryAfter: 1}
	}
	if err := s.obj.PutObject(ctx, s.partKey(uploadID, n), data, "application/octet-stream"); err != nil {
		s.logger.Error("session files: store part", zap.String("upload_id", uploadID), zap.Int("part", n), zap.Error(err))
		return retryLater(http.StatusServiceUnavailable, 10, "object store error, retry")
	}
	return nil
}

func checkOwner(f *models.SessionFile, deviceID string) *Error {
	if f == nil {
		// Never created, or abandoned and deleted by the cleaner: the
		// client creates it again (no reject_code: the recording is kept).
		return &Error{Status: http.StatusNotFound, Message: "no such upload (expired or never created): create it again"}
	}
	if deviceID == "" || deviceID != f.DeviceID {
		return reject(http.StatusForbidden, "wrong_device", "the upload belongs to another device")
	}
	return nil
}

// ------------------------------------------------------------------
// Commit

// CommitRequest is the body of POST /v1/session-files/{id}/commit.
type CommitRequest struct {
	DeviceID  string `json:"device_id"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

// Commit joins the parts, verifies the archive and completes the upload.
func (s *Service) Commit(ctx context.Context, uploadID string, req CommitRequest) (*UploadState, error) {
	if !ValidUploadID(uploadID) {
		return nil, badRequest("invalid upload id")
	}
	f, err := s.upload(ctx, uploadID, false)
	if err != nil {
		s.logger.Error("session files: read upload", zap.String("upload_id", uploadID), zap.Error(err))
		return nil, storageError(err)
	}
	if e := checkOwner(f, req.DeviceID); e != nil {
		return nil, e
	}
	if req.SHA256 != f.SHA256 || req.SizeBytes != int64(f.SizeBytes) {
		return nil, reject(http.StatusConflict, "upload_conflict", "size or checksum differ from the upload's")
	}
	switch f.Status {
	case models.SessionFileComplete:
		return &UploadState{UploadID: uploadID, Status: f.Status, PartBytes: int64(f.PartBytes), PartCount: int64(f.PartCount),
			Received: []int{}, FileCount: f.FileCount}, nil
	case models.SessionFileFailed:
		return nil, reject(http.StatusConflict, "upload_failed", "upload %s failed: %s", uploadID, f.Error)
	}
	received, err := s.receivedParts(ctx, *f)
	if err != nil {
		s.logger.Error("session files: list parts", zap.String("upload_id", uploadID), zap.Error(err))
		return nil, storageError(err)
	}
	if len(received) != int(f.PartCount) {
		have := map[int]bool{}
		for _, n := range received {
			have[n] = true
		}
		var missing []int
		for n := 0; n < int(f.PartCount) && len(missing) < 1000; n++ {
			if !have[n] {
				missing = append(missing, n)
			}
		}
		return nil, &Error{Status: http.StatusConflict, Message: fmt.Sprintf("%d parts missing", int(f.PartCount)-len(received)), Missing: missing}
	}
	if !s.acquire(ctx, s.commitSlots) {
		return nil, retryLater(http.StatusServiceUnavailable, 10, "busy, retry")
	}
	defer func() { <-s.commitSlots }()

	digest, err := s.assemble(ctx, *f)
	if err != nil {
		s.logger.Error("session files: assembly failed", zap.String("upload_id", uploadID), zap.Error(err))
		return nil, retryLater(http.StatusServiceUnavailable, 10, "object store error, retry")
	}
	if digest != f.SHA256 {
		s.fail(ctx, *f, "the joined archive's SHA-256 is "+digest)
		return nil, reject(http.StatusUnprocessableEntity, "checksum_mismatch", "the joined archive does not match its SHA-256")
	}
	files, raw, verr := ValidateArchive(newObjectReaderAt(ctx, s.obj, f.ObjectKey), int64(f.SizeBytes), f.RecordingName)
	if verr != nil {
		var se *storeReadError
		if errors.As(verr, &se) {
			s.logger.Error("session files: reading the archive back failed", zap.String("upload_id", uploadID), zap.Error(verr))
			return nil, retryLater(http.StatusServiceUnavailable, 10, "object store error, retry")
		}
		s.fail(ctx, *f, "invalid archive: "+verr.Error())
		return nil, reject(http.StatusUnprocessableEntity, "invalid_archive", "invalid archive: %v", verr)
	}
	done := *f
	now := s.now().UTC()
	done.Status = models.SessionFileComplete
	done.Version = models.SessionFileVersion(done.Status, now)
	done.UpdatedAt = now
	done.FileCount = uint32(files)
	done.RawBytes = raw
	if err := s.store.InsertSessionFile(ctx, done); err != nil {
		s.logger.Error("session files: insert completion", zap.String("upload_id", uploadID), zap.Error(err))
		return nil, storageError(err)
	}
	s.forget(uploadID)
	s.deletePrefix(ctx, s.TmpPrefix(uploadID))
	s.logger.Info("session files: upload complete", zap.String("upload_id", uploadID), zap.String("git_commit", f.GitCommit),
		zap.Uint64("size_bytes", f.SizeBytes), zap.Int("files", files), zap.Uint64("raw_bytes", raw))
	return &UploadState{UploadID: uploadID, Status: done.Status, PartBytes: int64(f.PartBytes), PartCount: int64(f.PartCount),
		Received: []int{}, FileCount: done.FileCount}, nil
}

// fail marks the upload failed and removes what it stored.
func (s *Service) fail(ctx context.Context, f models.SessionFile, reason string) {
	now := s.now().UTC()
	f.Status = models.SessionFileFailed
	f.Version = models.SessionFileVersion(f.Status, now)
	f.UpdatedAt = now
	f.Error = clean(reason, 500)
	if err := s.store.InsertSessionFile(ctx, f); err != nil {
		s.logger.Error("session files: mark failed", zap.String("upload_id", f.UploadID), zap.Error(err))
	}
	s.forget(f.UploadID)
	if err := s.obj.DeleteObject(ctx, f.ObjectKey); err != nil {
		s.logger.Warn("session files: delete failed archive", zap.String("upload_id", f.UploadID), zap.Error(err))
	}
	s.deletePrefix(ctx, s.TmpPrefix(f.UploadID))
	s.logger.Warn("session files: upload failed", zap.String("upload_id", f.UploadID), zap.String("reason", f.Error))
}

// assemble joins the parts into the upload's object and returns the hex
// SHA-256 of what was joined. At most one assembly part (8 MiB) in memory.
func (s *Service) assemble(ctx context.Context, f models.SessionFile) (string, error) {
	h := sha256.New()
	buf := bytes.NewBuffer(make([]byte, 0, minInt64(assemblyPartBytes, int64(f.SizeBytes))))
	appendPart := func(n int) error {
		rc, _, err := s.obj.GetObject(ctx, s.partKey(f.UploadID, n))
		if err != nil {
			return err
		}
		defer rc.Close()
		w, err := io.Copy(io.MultiWriter(buf, h), io.LimitReader(rc, MaxPartBytes+1))
		if err != nil {
			return err
		}
		if w != expectedPartSize(f, n) {
			return fmt.Errorf("part %d is %d bytes, expected %d", n, w, expectedPartSize(f, n))
		}
		return nil
	}
	if int64(f.SizeBytes) <= assemblyPartBytes {
		for n := 0; n < int(f.PartCount); n++ {
			if err := appendPart(n); err != nil {
				return "", err
			}
		}
		if err := s.obj.PutObject(ctx, f.ObjectKey, buf.Bytes(), "application/zip"); err != nil {
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}

	mpu, err := s.obj.CreateMultipartUpload(ctx, f.ObjectKey, "application/zip")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			// Detached: the request's context may be what failed.
			actx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := s.obj.AbortMultipartUpload(actx, f.ObjectKey, mpu); err != nil {
				s.logger.Warn("session files: abort multipart upload", zap.String("upload_id", f.UploadID), zap.Error(err))
			}
		}
	}()
	var parts []objstore.Part
	flush := func() error {
		etag, err := s.obj.UploadPart(ctx, f.ObjectKey, mpu, len(parts)+1, buf.Bytes())
		if err != nil {
			return err
		}
		parts = append(parts, objstore.Part{Number: len(parts) + 1, ETag: etag})
		buf.Reset()
		return nil
	}
	for n := 0; n < int(f.PartCount); n++ {
		if err := appendPart(n); err != nil {
			return "", err
		}
		// Every assembly part but the last is at least 8 MiB (S3: >= 5 MiB).
		if buf.Len() >= assemblyPartBytes && n < int(f.PartCount)-1 {
			if err := flush(); err != nil {
				return "", err
			}
		}
	}
	if buf.Len() > 0 {
		if err := flush(); err != nil {
			return "", err
		}
	}
	if err := s.obj.CompleteMultipartUpload(ctx, f.ObjectKey, mpu, parts); err != nil {
		return "", err
	}
	ok = true
	return hex.EncodeToString(h.Sum(nil)), nil
}

// deletePrefix deletes every object with the prefix (best effort, logged).
func (s *Service) deletePrefix(ctx context.Context, prefix string) int {
	deleted := 0
	for i := 0; i < 20; i++ {
		objs, _, err := s.obj.ListObjects(ctx, prefix, "", 1000)
		if err != nil {
			s.logger.Warn("session files: list for deletion", zap.String("prefix", prefix), zap.Error(err))
			return deleted
		}
		if len(objs) == 0 {
			return deleted
		}
		for _, o := range objs {
			if err := s.obj.DeleteObject(ctx, o.Key); err != nil {
				s.logger.Warn("session files: delete object", zap.String("key", o.Key), zap.Error(err))
				return deleted
			}
			deleted++
		}
	}
	return deleted
}

// ------------------------------------------------------------------
// Retrieval

// List returns uploads matching the filter.
func (s *Service) List(ctx context.Context, f models.SessionFileFilter) ([]models.SessionFile, error) {
	return s.store.ListSessionFiles(ctx, f)
}

// Availability returns the completed recordings of the sessions.
func (s *Service) Availability(ctx context.Context, sessionIDs []string) ([]models.SessionFileAvailability, error) {
	return s.store.SessionFileAvailability(ctx, sessionIDs)
}

// Get returns an upload's latest row (nil if unknown).
func (s *Service) Get(ctx context.Context, uploadID string) (*models.SessionFile, error) {
	if !ValidUploadID(uploadID) {
		return nil, nil
	}
	return s.store.GetSessionFile(ctx, uploadID)
}

// Open opens a completed upload's archive.
func (s *Service) Open(ctx context.Context, f models.SessionFile) (io.ReadCloser, int64, error) {
	return s.obj.GetObject(ctx, f.ObjectKey)
}
