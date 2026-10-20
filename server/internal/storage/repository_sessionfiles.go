package storage

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// Session files (APM-13): the index rows of uploaded AiTelemetry session
// directories. The table is defined in schema.go (sessionFilesTableDDL);
// internal/sessionfiles owns the rules.

const sessionFileColumns = `upload_id, version, status, created_at, updated_at, expires_at, device_id, session_id,
	recording_name, app_version, app_build, build_type, platform, device_model, os_version, unity_version,
	sdk_version, git_commit, started_at, scene, data_source, data_pack_version, data_pack_sha256, size_bytes,
	part_bytes, part_count, sha256, file_count, raw_bytes, object_key, session_json, error`

// InsertSessionFile writes a row (a new upload or a new state of one).
func (r *Repository) InsertSessionFile(ctx context.Context, f models.SessionFile) error {
	batch, err := r.client.conn.PrepareBatch(ctx, "INSERT INTO "+SessionFilesTable+" ("+sessionFileColumns+")")
	if err != nil {
		return fmt.Errorf("prepare session file insert: %w", err)
	}
	if err := batch.Append(f.UploadID, f.Version, f.Status, f.CreatedAt, f.UpdatedAt, f.ExpiresAt, f.DeviceID, f.SessionID,
		f.RecordingName, f.AppVersion, f.AppBuild, f.BuildType, f.Platform, f.DeviceModel, f.OSVersion, f.UnityVersion,
		f.SDKVersion, f.GitCommit, f.StartedAt, f.Scene, f.DataSource, f.DataPackVersion, f.DataPackSHA256, f.SizeBytes,
		f.PartBytes, f.PartCount, f.SHA256, f.FileCount, f.RawBytes, f.ObjectKey, f.SessionJSON, f.Error); err != nil {
		return fmt.Errorf("append session file: %w", err)
	}
	return batch.Send()
}

func scanSessionFiles(rows interface {
	Next() bool
	Scan(dest ...interface{}) error
	Err() error
	Close() error
}) ([]models.SessionFile, error) {
	defer rows.Close()
	out := []models.SessionFile{}
	for rows.Next() {
		var f models.SessionFile
		if err := rows.Scan(&f.UploadID, &f.Version, &f.Status, &f.CreatedAt, &f.UpdatedAt, &f.ExpiresAt, &f.DeviceID, &f.SessionID,
			&f.RecordingName, &f.AppVersion, &f.AppBuild, &f.BuildType, &f.Platform, &f.DeviceModel, &f.OSVersion, &f.UnityVersion,
			&f.SDKVersion, &f.GitCommit, &f.StartedAt, &f.Scene, &f.DataSource, &f.DataPackVersion, &f.DataPackSHA256, &f.SizeBytes,
			&f.PartBytes, &f.PartCount, &f.SHA256, &f.FileCount, &f.RawBytes, &f.ObjectKey, &f.SessionJSON, &f.Error); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetSessionFile returns the latest row of an upload, nil when there is none.
func (r *Repository) GetSessionFile(ctx context.Context, uploadID string) (*models.SessionFile, error) {
	rows, err := r.client.conn.Query(ctx, "SELECT "+sessionFileColumns+" FROM "+SessionFilesTable+
		" WHERE upload_id = ? ORDER BY version DESC LIMIT 1", uploadID)
	if err != nil {
		return nil, fmt.Errorf("read session file: %w", err)
	}
	files, err := scanSessionFiles(rows)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	return &files[0], nil
}

// ListSessionFiles returns the latest state of the uploads matching the
// filter, newest first. Every value is bound as a query argument.
func (r *Repository) ListSessionFiles(ctx context.Context, f models.SessionFileFilter) ([]models.SessionFile, error) {
	var where []string
	var args []interface{}
	add := func(cond string, v ...interface{}) {
		where = append(where, cond)
		args = append(args, v...)
	}
	if !f.StartTime.IsZero() {
		add("created_at >= fromUnixTimestamp64Milli(toInt64(?))", f.StartTime.UnixMilli())
	}
	if !f.EndTime.IsZero() {
		add("created_at <= fromUnixTimestamp64Milli(toInt64(?))", f.EndTime.UnixMilli())
	}
	if f.GitCommit != "" {
		add("startsWith(lower(git_commit), ?)", strings.ToLower(f.GitCommit))
	}
	if f.AppVersion != "" {
		add("app_version = ?", f.AppVersion)
	}
	if f.AppBuild != "" {
		add("app_build = ?", f.AppBuild)
	}
	if f.BuildType != "" {
		add("build_type = ?", f.BuildType)
	}
	if f.DeviceID != "" {
		add("device_id = ?", f.DeviceID)
	}
	if len(f.SessionIDs) > 0 {
		add("session_id IN ?", f.SessionIDs)
	}
	if f.RecordingName != "" {
		add("recording_name = ?", f.RecordingName)
	}
	switch f.Status {
	case "all":
	case "":
		add("status = ?", models.SessionFileComplete)
	default:
		add("status = ?", f.Status)
	}
	limit := f.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := "SELECT " + sessionFileColumns + " FROM " + SessionFilesTable + " FINAL"
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += fmt.Sprintf(" ORDER BY created_at DESC, upload_id LIMIT %d", limit)
	rows, err := r.client.conn.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list session files: %w", err)
	}
	return scanSessionFiles(rows)
}

// SessionFileDeviceUsage counts one device's uploads started since a time:
// all of them, their bytes, and those not committed yet.
func (r *Repository) SessionFileDeviceUsage(ctx context.Context, deviceID string, since time.Time) (uploads, bytes, open uint64, err error) {
	err = r.client.conn.QueryRow(ctx, "SELECT count(), sum(size_bytes), countIf(status = ?) FROM "+SessionFilesTable+
		" FINAL WHERE device_id = ? AND created_at >= fromUnixTimestamp64Milli(toInt64(?))", models.SessionFileUploading, deviceID, since.UnixMilli()).Scan(&uploads, &bytes, &open)
	if err != nil {
		err = fmt.Errorf("session file usage: %w", err)
	}
	return
}

// SessionFilesToExpire returns uploads past their expiry, and uploads still
// not committed that were started before staleBefore, oldest first.
func (r *Repository) SessionFilesToExpire(ctx context.Context, now, staleBefore time.Time, limit int) ([]models.SessionFile, error) {
	rows, err := r.client.conn.Query(ctx, "SELECT "+sessionFileColumns+" FROM "+SessionFilesTable+
		" FINAL WHERE expires_at <= toDateTime(toInt64(?)) OR (status != ? AND created_at < fromUnixTimestamp64Milli(toInt64(?))) ORDER BY created_at LIMIT ?",
		now.Unix(), models.SessionFileComplete, staleBefore.UnixMilli(), limit)
	if err != nil {
		return nil, fmt.Errorf("session files to expire: %w", err)
	}
	return scanSessionFiles(rows)
}

// SessionFilesOfDevice returns every upload of a device (privacy deletion).
func (r *Repository) SessionFilesOfDevice(ctx context.Context, deviceID string) ([]models.SessionFile, error) {
	rows, err := r.client.conn.Query(ctx, "SELECT "+sessionFileColumns+" FROM "+SessionFilesTable+
		" FINAL WHERE device_id = ? ORDER BY created_at", deviceID)
	if err != nil {
		return nil, fmt.Errorf("session files of a device: %w", err)
	}
	return scanSessionFiles(rows)
}

// SessionFilesOfSessions returns every upload of the sessions.
func (r *Repository) SessionFilesOfSessions(ctx context.Context, sessionIDs []string) ([]models.SessionFile, error) {
	if len(sessionIDs) == 0 {
		return []models.SessionFile{}, nil
	}
	rows, err := r.client.conn.Query(ctx, "SELECT "+sessionFileColumns+" FROM "+SessionFilesTable+
		" FINAL WHERE session_id IN ? ORDER BY created_at", sessionIDs)
	if err != nil {
		return nil, fmt.Errorf("session files of sessions: %w", err)
	}
	return scanSessionFiles(rows)
}

// OpsSessionsOfPlayer returns the session ids of a player's ops events
// (APM-12): the bridge from a player id to the technical sessions, whose
// recordings privacy deletion by player id removes too.
func (r *Repository) OpsSessionsOfPlayer(ctx context.Context, playerID string, limit int) ([]string, error) {
	rows, err := r.client.conn.Query(ctx, "SELECT DISTINCT session_id FROM ops_events WHERE player_id = ? AND session_id != '' LIMIT ?", playerID, limit)
	if err != nil {
		return nil, fmt.Errorf("sessions of a player: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteSessionFileRows deletes every row of the uploads (a mutation,
// waited for; needs ALTER DELETE, granted for APM-14).
func (r *Repository) DeleteSessionFileRows(ctx context.Context, uploadIDs []string) error {
	if len(uploadIDs) == 0 {
		return nil
	}
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"mutations_sync": 2}))
	if err := r.client.conn.Exec(ctx, "ALTER TABLE "+SessionFilesTable+" DELETE WHERE upload_id IN ?", uploadIDs); err != nil {
		if isAccessDenied(err) {
			return fmt.Errorf("%w (%v)", ErrPrivacyDeleteNotPermitted, err)
		}
		return fmt.Errorf("delete session file rows: %w", err)
	}
	return nil
}

// SessionFileAvailability returns the completed uploads of the sessions
// (no file content, no device): what the session time line and the
// exception page show as "has a recording".
func (r *Repository) SessionFileAvailability(ctx context.Context, sessionIDs []string) ([]models.SessionFileAvailability, error) {
	out := []models.SessionFileAvailability{}
	if len(sessionIDs) == 0 {
		return out, nil
	}
	rows, err := r.client.conn.Query(ctx, "SELECT session_id, upload_id, size_bytes, created_at FROM "+SessionFilesTable+
		" FINAL WHERE session_id IN ? AND status = ? ORDER BY created_at DESC LIMIT 500", sessionIDs, models.SessionFileComplete)
	if err != nil {
		return nil, fmt.Errorf("session file availability: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a models.SessionFileAvailability
		if err := rows.Scan(&a.SessionID, &a.UploadID, &a.SizeBytes, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
