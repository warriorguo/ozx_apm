package opsbi

import (
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every card declares exactly the variables its SQL uses (Metabase rejects a
// tag that is not declared; a declared tag that is not used would show a
// filter that does nothing), renders with its defaults, and reads
// ops_events only with FINAL.
func TestCards_VariablesAndDedup(t *testing.T) {
	keys := map[string]bool{}
	names := map[string]bool{}
	finalRE := regexp.MustCompile(`FROM ops_events(\s+FINAL)?`)
	for _, c := range Cards {
		if keys[c.Key] || names[c.Name()] {
			t.Errorf("duplicate card %s", c.Key)
		}
		keys[c.Key], names[c.Name()] = true, true
		var declared []string
		for _, v := range c.Vars {
			declared = append(declared, v.Name)
			if v.Type != VarNumber && v.Type != VarText {
				t.Errorf("%s: %s has type %q", c.Key, v.Name, v.Type)
			}
		}
		sort.Strings(declared)
		if strings.Join(declared, ",") != strings.Join(c.Tags(), ",") {
			t.Errorf("%s: declared %v, used %v", c.Key, declared, c.Tags())
		}
		sql, err := c.Render(nil)
		if err != nil {
			t.Errorf("%s: %v", c.Key, err)
		}
		if strings.Contains(sql, "{{") || strings.Contains(sql, "}}") {
			t.Errorf("%s: unrendered variable", c.Key)
		}
		for _, m := range finalRE.FindAllStringSubmatch(sql, -1) {
			if m[1] == "" {
				t.Errorf("%s reads ops_events without FINAL", c.Key)
			}
		}
		if strings.Contains(sql, "today()") || strings.Contains(sql, "yesterday()") {
			t.Errorf("%s: use UTC days (toDate(now('UTC'))), not the server's time zone", c.Key)
		}
		if c.Title == "" || c.Description == "" || c.Display == "" {
			t.Errorf("%s: title, description and display are required", c.Key)
		}
	}
	for _, d := range Dashboards {
		if len(d.Cards) == 0 {
			t.Errorf("dashboard %s is empty", d.Key)
		}
		for _, k := range d.Cards {
			if _, ok := CardByKey(k); !ok {
				t.Errorf("dashboard %s: unknown card %s", d.Key, k)
			}
		}
	}
}

func TestRender(t *testing.T) {
	c := Card{Key: "x", SQL: "SELECT {{a}}, {{ b }}, {{a}}", Vars: []Var{{"a", "", VarNumber, "30"}, {"b", "", VarText, "it's"}}}
	got, err := c.Render(nil)
	if err != nil || got != `SELECT 30, 'it\'s', 30` {
		t.Errorf("%q %v", got, err)
	}
	got, err = c.Render(map[string]string{"a": "7", "b": `x\'; DROP`})
	if err != nil || got != `SELECT 7, 'x\\\'; DROP', 7` {
		t.Errorf("%q %v", got, err)
	}
	if _, err := c.Render(map[string]string{"a": "7; DROP TABLE x"}); err == nil {
		t.Error("a number variable must be a number")
	}
	if _, err := (Card{Key: "y", SQL: "{{z}}"}).Render(nil); err == nil {
		t.Error("undeclared variable")
	}
}

// The dashboards' filters: every variable of their cards once.
func TestDashboardVars(t *testing.T) {
	for _, d := range Dashboards {
		seen := map[string]bool{}
		for _, v := range d.VarsOf() {
			if seen[v.Name] {
				t.Errorf("%s: %s twice", d.Key, v.Name)
			}
			seen[v.Name] = true
		}
		if !seen["days"] || !seen["build"] {
			t.Errorf("%s: filters %v", d.Key, seen)
		}
	}
}
