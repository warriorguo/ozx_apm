package models

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// Issue state and alerting (APM-8).
//
// An issue is an exception or crash group (kind + fingerprint). It is
// unresolved unless an admin marked it resolved; a resolved issue that
// occurs again in a newer version is a regression, which reopens it.

const (
	IssueKindException = "exception"
	IssueKindCrash     = "crash"

	IssueUnresolved = "unresolved"
	IssueResolved   = "resolved"

	// MaxFingerprintLength bounds fingerprints accepted by the state API.
	MaxFingerprintLength = 256
	// MaxIssueVersionLength bounds the version given when resolving.
	MaxIssueVersionLength = 128
	// MaxIssueCommentLength bounds the free-text comment of a state change.
	MaxIssueCommentLength = 500
)

// IssueKinds lists the valid kinds.
var IssueKinds = []string{IssueKindException, IssueKindCrash}

// ValidIssueKind reports whether kind is exception or crash.
func ValidIssueKind(kind string) bool {
	return kind == IssueKindException || kind == IssueKindCrash
}

// IssueState is the latest state of one issue (apm_issue_states).
type IssueState struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"` // unresolved | resolved
	// ResolvedInVersion: the version that contains the fix, given by the
	// admin (inclusive: an occurrence in it or a newer version after the
	// resolution is a regression). Empty: "the next version", i.e. any
	// version newer than LastSeenVersion.
	ResolvedInVersion string `json:"resolved_in_version"`
	// LastSeenVersion: the newest version (by the alert package's version
	// order) that had the issue when it was resolved.
	LastSeenVersion string     `json:"last_seen_version"`
	ResolvedAt      *time.Time `json:"resolved_at,omitempty"`
	// RegressedInVersion / RegressedAt: set when the alert evaluator reopened
	// the issue because it occurred again in a newer version.
	RegressedInVersion string     `json:"regressed_in_version,omitempty"`
	RegressedAt        *time.Time `json:"regressed_at,omitempty"`
	UpdatedAt          time.Time  `json:"updated_at"`
	UpdatedBy          string     `json:"updated_by"` // admin | alert
	Comment            string     `json:"comment,omitempty"`
	// Version orders the rows of one issue (ReplacingMergeTree version):
	// UpdatedAt in milliseconds. Not part of the API.
	Version uint64 `json:"-"`
}

// IssueStatusInfo is the state summary added to exception and crash groups.
type IssueStatusInfo struct {
	Status             string     `json:"status,omitempty"` // "" when the states could not be read
	ResolvedInVersion  string     `json:"resolved_in_version,omitempty"`
	ResolvedAt         *time.Time `json:"resolved_at,omitempty"`
	RegressedInVersion string     `json:"regressed_in_version,omitempty"`
}

// StatusInfo returns the summary of the state.
func (s IssueState) StatusInfo() IssueStatusInfo {
	return IssueStatusInfo{
		Status:             s.Status,
		ResolvedInVersion:  s.ResolvedInVersion,
		ResolvedAt:         s.ResolvedAt,
		RegressedInVersion: s.RegressedInVersion,
	}
}

// IssueStateRequest is the body of POST /api/issues/state.
type IssueStateRequest struct {
	Kind              string `json:"kind"`
	Fingerprint       string `json:"fingerprint"`
	Status            string `json:"status"`
	ResolvedInVersion string `json:"resolved_in_version"`
	Comment           string `json:"comment"`
}

// Validate checks the request; it never echoes the values back.
func (r IssueStateRequest) Validate() error {
	if !ValidIssueKind(r.Kind) {
		return fmt.Errorf("kind must be exception or crash")
	}
	if r.Fingerprint == "" || len(r.Fingerprint) > MaxFingerprintLength || !utf8.ValidString(r.Fingerprint) {
		return fmt.Errorf("fingerprint is required (at most %d bytes)", MaxFingerprintLength)
	}
	if r.Status != IssueResolved && r.Status != IssueUnresolved {
		return fmt.Errorf("status must be resolved or unresolved")
	}
	if len(r.ResolvedInVersion) > MaxIssueVersionLength || !utf8.ValidString(r.ResolvedInVersion) {
		return fmt.Errorf("resolved_in_version: at most %d bytes", MaxIssueVersionLength)
	}
	if r.Status == IssueUnresolved && r.ResolvedInVersion != "" {
		return fmt.Errorf("resolved_in_version is only allowed with status resolved")
	}
	if utf8.RuneCountInString(r.Comment) > MaxIssueCommentLength || !utf8.ValidString(r.Comment) {
		return fmt.Errorf("comment: at most %d characters", MaxIssueCommentLength)
	}
	return nil
}

// Alert outbox statuses (apm_alerts.status).
const (
	AlertPending   = "pending"   // detected, to be sent; the row carries the payload
	AlertClaimed   = "claimed"   // an instance is about to send attempt N
	AlertSent      = "sent"      // attempt N was accepted by the webhook (2xx)
	AlertFailed    = "failed"    // attempt N failed (error says why)
	AlertAbandoned = "abandoned" // given up after the last attempt
	AlertKnown     = "known"     // a fingerprint recorded without an alert (baseline, storm suppression)
)

// AlertRecord is one row of the alert outbox (apm_alerts). Rows are only
// appended; the state of an alert is the set of rows of its key.
type AlertRecord struct {
	AlertKey    string    `json:"alert_key"`
	At          time.Time `json:"at"`
	Status      string    `json:"status"`
	Attempt     uint16    `json:"attempt"`
	Instance    string    `json:"instance"`
	Type        string    `json:"type"`
	Kind        string    `json:"kind"`
	Fingerprint string    `json:"fingerprint"`
	AppVersion  string    `json:"app_version"`
	Payload     string    `json:"payload,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// AlertOutboxEntry is the aggregated state of one alert key that still has
// to be sent (storage.Repository.AlertsToSend).
type AlertOutboxEntry struct {
	AlertKey       string
	Type           string
	Payload        string
	CreatedAt      time.Time
	Failed         int
	LastFailedAt   time.Time
	LastClaim      int // highest claimed attempt (0: none)
	LastClaimAt    time.Time
	LastDone       int // highest attempt with an outcome (sent/failed)
	LastDoneStatus string
}

// AlertClaim is one claimed attempt.
type AlertClaim struct {
	AlertKey string
	Attempt  int
	At       time.Time
	Instance string
}

// AlertSummary is one alert for GET /api/alerts: its key and latest row.
type AlertSummary struct {
	AlertKey    string    `json:"alert_key"`
	Type        string    `json:"type"`
	Kind        string    `json:"kind"`
	Fingerprint string    `json:"fingerprint"`
	AppVersion  string    `json:"app_version"`
	CreatedAt   time.Time `json:"created_at"`
	Status      string    `json:"status"`
	UpdatedAt   time.Time `json:"updated_at"`
	Attempts    int       `json:"attempts"`
	Error       string    `json:"error,omitempty"`
	Title       string    `json:"title"`
}

// IssueCandidate is a fingerprint not yet known to the alerting (a new
// issue), with what the alert says about it.
type IssueCandidate struct {
	Kind         string
	Fingerprint  string
	FirstSeen    time.Time
	FirstVersion string
	Versions     []string
	Count        int64
	Sessions     int64
	Title        string // exception type or crash type
	Message      string // the latest message (exceptions) or first stack line (crashes)
	Site         string // throw site (exceptions)
}

// IssueVersionHit is the occurrences of an issue in one version after a
// given time (regression detection).
type IssueVersionHit struct {
	Fingerprint string
	AppVersion  string
	FirstSeen   time.Time
	LastSeen    time.Time
	Count       int64
	Sessions    int64
}

// VersionRate is the crash rate of one version over a window.
type VersionRate struct {
	AppVersion      string
	Sessions        int64
	CrashedSessions int64
	Crashes         int64
}

// VersionStartup is the startup time distribution of one version.
type VersionStartup struct {
	AppVersion string
	Samples    int64
	P95Ms      float64
	FirstSeen  time.Time
}
