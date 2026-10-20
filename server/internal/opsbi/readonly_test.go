package opsbi

import (
	"encoding/xml"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/warriorguo/ozx_apm/server/internal/storage"
)

// The read-only account's users.d snippet grants SELECT on exactly the ops
// tables (no technical table), is read-only and bounded, and every preset
// question reads only those tables.
func TestReadonlyUser_GrantsExactlyTheOpsTables(t *testing.T) {
	b, err := os.ReadFile("../../deployments/clickhouse/users.d/ozx_ops_readonly.xml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Profiles struct {
			P struct {
				Readonly           int   `xml:"readonly"`
				MaxExecutionTime   int   `xml:"max_execution_time"`
				MaxMemoryUsage     int64 `xml:"max_memory_usage"`
				MaxRowsToRead      int64 `xml:"max_rows_to_read"`
				MaxConcurrentQuery int   `xml:"max_concurrent_queries_for_user"`
			} `xml:"ozx_ops_readonly"`
		} `xml:"profiles"`
		Users struct {
			U struct {
				Profile string   `xml:"profile"`
				Grants  []string `xml:"grants>query"`
			} `xml:"ozx_ops_ro"`
		} `xml:"users"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	p := doc.Profiles.P
	if p.Readonly != 2 || p.MaxExecutionTime <= 0 || p.MaxMemoryUsage <= 0 || p.MaxRowsToRead <= 0 || p.MaxConcurrentQuery <= 0 {
		t.Errorf("profile %+v", p)
	}
	if doc.Users.U.Profile != "ozx_ops_readonly" {
		t.Errorf("profile of the user %q", doc.Users.U.Profile)
	}
	var granted []string
	for _, g := range doc.Users.U.Grants {
		if !strings.HasPrefix(g, "GRANT SELECT ON ozx_apm.") {
			t.Errorf("grant %q", g)
		}
		granted = append(granted, strings.TrimPrefix(g, "GRANT SELECT ON ozx_apm."))
	}
	want := storage.OpsTableNames()
	sort.Strings(granted)
	sort.Strings(want)
	if strings.Join(granted, ",") != strings.Join(want, ",") {
		t.Errorf("granted %v, want %v", granted, want)
	}

	tables := regexp.MustCompile(`(?i)\b(?:FROM|JOIN)\s+([a-z_][a-z0-9_]*)`)
	ops := map[string]bool{}
	for _, n := range want {
		ops[n] = true
	}
	for _, c := range Cards {
		for _, m := range tables.FindAllStringSubmatch(strings.ReplaceAll(c.SQL, "ARRAY JOIN", ""), -1) {
			if !ops[m[1]] {
				t.Errorf("%s reads %s, which the BI account may not", c.Key, m[1])
			}
		}
	}
}
