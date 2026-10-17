package admin

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

func TestParseAdminFilter_Defaults(t *testing.T) {
	f, err := parseAdminFilter(url.Values{}, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if f.BuildType != models.BuildFilterRelease || len(f.Context) != 0 || f.AppVersion != "" || f.Platform != "" {
		t.Errorf("%+v", f)
	}
	if d := f.EndTime.Sub(f.StartTime); d != 7*24*time.Hour {
		t.Errorf("range %v", d)
	}
}

func TestParseAdminFilter_BuildTypes(t *testing.T) {
	for _, bt := range []string{"release", "dev", "editor", "unknown", "all"} {
		f, err := parseAdminFilter(url.Values{"build_type": {bt}}, time.Hour)
		if err != nil || f.BuildType != bt {
			t.Errorf("%s: %+v %v", bt, f, err)
		}
	}
	for _, bt := range []string{"Release", "debug", "release'"} {
		if _, err := parseAdminFilter(url.Values{"build_type": {bt}}, time.Hour); err == nil {
			t.Errorf("%q accepted", bt)
		}
	}
}

func TestParseAdminFilter_Context(t *testing.T) {
	f, err := parseAdminFilter(url.Values{"ctx": {"levelId:3", "roomId:r:1", "levelId:4", "panel:"}}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := []models.ContextFilter{
		{Key: "levelId", Values: []string{"3", "4"}},
		{Key: "roomId", Values: []string{"r:1"}}, // only the first ':' separates
		{Key: "panel", Values: []string{""}},
	}
	if len(f.Context) != len(want) {
		t.Fatalf("%+v", f.Context)
	}
	for i := range want {
		if f.Context[i].Key != want[i].Key || strings.Join(f.Context[i].Values, "|") != strings.Join(want[i].Values, "|") {
			t.Errorf("filter %d: %+v, want %+v", i, f.Context[i], want[i])
		}
	}

	bad := [][]string{
		{"noseparator"},
		{":value"},
		{"bad key:1"},
		{"k'ey:1"},
		{strings.Repeat("k", 65) + ":1"},
		{"k:" + strings.Repeat("v", 257)},
		{"a:1", "b:1", "c:1", "d:1", "e:1", "f:1", "g:1", "h:1", "i:1", "j:1", "k:1"},
	}
	for _, b := range bad {
		if _, err := parseAdminFilter(url.Values{"ctx": b}, time.Hour); err == nil {
			t.Errorf("%q accepted", b)
		}
	}
}
