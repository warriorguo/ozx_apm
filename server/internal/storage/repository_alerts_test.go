package storage

import (
	"strings"
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

// APM-8: the status filter applies to the exception list only, as a bound
// subquery on the issue states read with FINAL.
func TestTableWhere_IssueStatus(t *testing.T) {
	f := models.AdminFilter{StartTime: time.Unix(0, 0), EndTime: time.Unix(1, 0), BuildType: "all", IssueStatus: "resolved"}
	where, args := tableWhere(TableExceptions.Name, f)
	if !strings.HasSuffix(where, " AND fingerprint IN (SELECT fingerprint FROM apm_issue_states FINAL WHERE kind = ? AND status = 'resolved')") {
		t.Fatalf("resolved: %s", where)
	}
	if strings.Count(where, "?") != len(args) || args[len(args)-1] != "exception" {
		t.Errorf("args %#v", args)
	}
	f.IssueStatus = "unresolved"
	if where, _ := tableWhere(TableExceptions.Name, f); !strings.Contains(where, "fingerprint NOT IN (SELECT") {
		t.Errorf("unresolved: %s", where)
	}
	if where, _ := tableWhere(TableCrashes.Name, f); strings.Contains(where, "apm_issue_states") {
		t.Errorf("applied to another table: %s", where)
	}
	f.IssueStatus = "bogus"
	if where, _ := tableWhere(TableExceptions.Name, f); strings.Contains(where, "apm_issue_states") {
		t.Errorf("unknown status applied: %s", where)
	}
}

func TestAlertTablesDDL(t *testing.T) {
	// State: ReplacingMergeTree on a version column, one row per issue key;
	// nothing that needs ALTER UPDATE / MODIFY / ADD INDEX.
	if !strings.Contains(issueStatesTableDDL, "ENGINE = ReplacingMergeTree(version)") ||
		!strings.Contains(issueStatesTableDDL, "ORDER BY (kind, fingerprint)") {
		t.Errorf("issue states DDL: %s", issueStatesTableDDL)
	}
	// The outbox has no TTL: its new_issue rows are the record of known
	// fingerprints.
	if strings.Contains(alertsTableDDL, "TTL") || !strings.Contains(alertsTableDDL, "ORDER BY (alert_key, at)") {
		t.Errorf("alerts DDL: %s", alertsTableDDL)
	}
	sql := SchemaSQL()
	for _, ddl := range []string{issueStatesTableDDL, alertsTableDDL} {
		if !strings.Contains(sql, ddl+";") {
			t.Errorf("SchemaSQL lacks %s", strings.SplitN(ddl, "\n", 2)[0])
		}
	}
}

func TestIssueTableAndBuildClause(t *testing.T) {
	if tb, err := issueTable("exception"); err != nil || tb != "apm_exceptions" {
		t.Error(tb, err)
	}
	if tb, err := issueTable("crash"); err != nil || tb != "apm_crashes" {
		t.Error(tb, err)
	}
	if _, err := issueTable("apm_exceptions; DROP"); err == nil {
		t.Error("unknown kind accepted")
	}
	if BuildClause(false) != "build_type IN ('release', '')" || BuildClause(true) != "1" {
		t.Error("build clause")
	}
	if optTime(time.UnixMilli(0)) != nil || optTime(time.Time{}) != nil || optTime(time.UnixMilli(5)) == nil {
		t.Error("optTime")
	}
}
