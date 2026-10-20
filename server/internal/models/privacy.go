package models

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Privacy deletion (APM-14): delete every stored row of one data subject.

// PrivacyIDTypes maps the id types the deletion API accepts to the column
// that holds them: device_id (the technical tables, APM-14) and player_id
// (the ops tables ops_events, ops_player_days and ops_players, APM-12; the
// SDK's ApmClient.GetPlayerId). The tables are found by column name
// (storage.PrivacyTables). A player's data is under both ids: delete both.
var PrivacyIDTypes = map[string]string{
	"device_id": "device_id",
	"player_id": "player_id",
}

// MaxPrivacyIDLength bounds the id in a deletion request. SDK device ids are
// 32 hex characters; anything near the bound is not an id the SDK sent.
const MaxPrivacyIDLength = 256

// PrivacyRequest is the body of POST /api/privacy/delete and /api/privacy/verify.
type PrivacyRequest struct {
	// IDType is one of PrivacyIDTypes; empty means "device_id".
	IDType string `json:"id_type"`
	ID     string `json:"id"`
}

// Deletion statuses, as stored in the audit table and returned by the API.
const (
	PrivacyStatusRunning    = "running"    // started; the final record follows
	PrivacyStatusDone       = "done"       // no row of the subject is left
	PrivacyStatusIncomplete = "incomplete" // finished, but rows are left (e.g. inserted meanwhile): run it again
	PrivacyStatusFailed     = "failed"     // an error stopped it (Error says which)
)

// PrivacyTableResult is the outcome for one table.
type PrivacyTableResult struct {
	Table      string `json:"table"`
	RowsBefore uint64 `json:"rows_before"`
	RowsAfter  uint64 `json:"rows_after"`
	Error      string `json:"error,omitempty"`
}

// PrivacySessionFiles is what a deletion did to the subject's session file
// recordings (APM-13): their objects in the object store and index rows.
// Found by device id, or for a player id through the sessions of the
// player's ops events.
type PrivacySessionFiles struct {
	UploadsBefore int    `json:"uploads_before"`
	UploadsAfter  int    `json:"uploads_after"`
	Error         string `json:"error,omitempty"`
}

// PrivacyDeletion is the response of POST /api/privacy/delete.
type PrivacyDeletion struct {
	RequestID     string               `json:"request_id"`
	IDType        string               `json:"id_type"`
	IDHash        string               `json:"id_hash"`
	Status        string               `json:"status"`
	StartedAt     time.Time            `json:"started_at"`
	FinishedAt    *time.Time           `json:"finished_at,omitempty"`
	Tables        []PrivacyTableResult `json:"tables"`
	RowsDeleted   uint64               `json:"rows_deleted"`
	RowsRemaining uint64               `json:"rows_remaining"`
	Error         string               `json:"error,omitempty"`
	Note          string               `json:"note,omitempty"`
	// SessionFiles: the recordings (APM-13); nil when this server has no
	// object store configured.
	SessionFiles *PrivacySessionFiles `json:"session_files,omitempty"`
}

// PrivacyAuditRecord is one row of apm_privacy_deletions. A request writes
// a "running" row when it starts and a final row when it ends; both share
// the request id. The subject is only stored as IDHash.
type PrivacyAuditRecord struct {
	RequestID   string    `json:"request_id"`
	At          time.Time `json:"at"`
	IDType      string    `json:"id_type"`
	IDHash      string    `json:"id_hash"`
	Status      string    `json:"status"`
	Tables      []string  `json:"tables"`
	RowsBefore  uint64    `json:"rows_before"`
	RowsAfter   uint64    `json:"rows_after"`
	Error       string    `json:"error,omitempty"`
	RequestedBy string    `json:"requested_by"`
}

// PrivacyVerification is the response of POST /api/privacy/verify.
type PrivacyVerification struct {
	IDType        string               `json:"id_type"`
	IDHash        string               `json:"id_hash"`
	Tables        []PrivacyTableResult `json:"tables"` // RowsAfter: rows stored now
	RowsRemaining uint64               `json:"rows_remaining"`
	Deletions     []PrivacyAuditRecord `json:"deletions"`
	Note          string               `json:"note,omitempty"`
	// SessionFiles: recordings stored now (UploadsAfter); nil without an object store.
	SessionFiles *PrivacySessionFiles `json:"session_files,omitempty"`
}

// PrivacyIDHash is how a subject id is recorded in the audit table and in
// logs: the hex SHA-256 of the id. It identifies a request for someone who
// knows the id, without storing the id itself.
func PrivacyIDHash(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}
