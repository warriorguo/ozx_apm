package models

import "time"

// Session files (APM-13): AiTelemetry session directories recorded by
// development builds, uploaded as a zip, stored in object storage and
// indexed in apm_session_files (one row per upload and state; the latest
// version wins: ReplacingMergeTree(version), read with FINAL).

// Upload states.
const (
	SessionFileUploading = "uploading" // created; parts arrive
	SessionFileFailed    = "failed"    // the commit found the archive wrong (checksum, structure)
	SessionFileComplete  = "complete"  // assembled, verified, downloadable
)

// SessionFileStatusRank orders the states: a row of a later state always
// replaces an earlier one, whatever the clocks of the servers that wrote
// them (it is the high part of the row version).
func SessionFileStatusRank(status string) uint64 {
	switch status {
	case SessionFileUploading:
		return 1
	case SessionFileFailed:
		return 2
	case SessionFileComplete:
		return 3
	}
	return 0
}

// SessionFileVersion is the ReplacingMergeTree version of a row: the
// state's rank, then the write time in milliseconds.
func SessionFileVersion(status string, at time.Time) uint64 {
	return SessionFileStatusRank(status)<<48 | uint64(at.UnixMilli())&(1<<48-1)
}

// SessionFile is one upload (a row of apm_session_files).
type SessionFile struct {
	UploadID  string    `json:"upload_id"`
	Version   uint64    `json:"-"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ExpiresAt time.Time `json:"expires_at"`

	DeviceID      string `json:"device_id"`
	SessionID     string `json:"session_id"`
	RecordingName string `json:"recording_name"`
	AppVersion    string `json:"app_version"`
	AppBuild      string `json:"app_build"`
	BuildType     string `json:"build_type"`
	Platform      string `json:"platform"`
	DeviceModel   string `json:"device_model"`
	OSVersion     string `json:"os_version"`
	UnityVersion  string `json:"unity_version"`
	SDKVersion    string `json:"sdk_version"`
	GitCommit     string `json:"git_commit"`

	// From session.json.
	StartedAt       string `json:"started_at"`
	Scene           string `json:"scene"`
	DataSource      string `json:"data_source"`
	DataPackVersion string `json:"data_pack_version"`
	DataPackSHA256  string `json:"data_pack_sha256"`

	SizeBytes uint64 `json:"size_bytes"`
	PartBytes uint32 `json:"part_bytes"`
	PartCount uint32 `json:"part_count"`
	SHA256    string `json:"sha256"`
	FileCount uint32 `json:"file_count"`
	RawBytes  uint64 `json:"raw_bytes"`

	ObjectKey   string `json:"-"`
	SessionJSON string `json:"session_json,omitempty"`
	Error       string `json:"error,omitempty"`
}

// SessionFileFilter selects uploads for the admin list.
type SessionFileFilter struct {
	GitCommit     string // prefix (at least 4 hex digits) or the full commit
	AppVersion    string
	AppBuild      string
	BuildType     string
	DeviceID      string
	SessionIDs    []string
	RecordingName string
	Status        string // "" = complete; "all"
	StartTime     time.Time
	EndTime       time.Time
	Limit         int
}

// SessionFileAvailability is what the session time line and the exception
// detail page learn about a session's recordings without the admin token.
type SessionFileAvailability struct {
	SessionID string    `json:"session_id"`
	UploadID  string    `json:"upload_id"`
	SizeBytes uint64    `json:"size_bytes"`
	CreatedAt time.Time `json:"created_at"`
}
