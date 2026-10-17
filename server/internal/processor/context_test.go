package processor

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/warriorguo/ozx_apm/server/internal/models"
)

func normalize(raw, buildType, platform string) (models.EventContext, ContextStats) {
	c := models.EventContext{BuildType: buildType, RawContext: json.RawMessage(raw)}
	stats := NormalizeEventContext(&c, platform)
	return c, stats
}

func TestNormalizeEventContext_KeepsValidEntries(t *testing.T) {
	c, stats := normalize(`{"levelId":"3","room.id":"r-12","stage_type":"boss","seed":123456789012,"elite":true,"panel":"","gone":null}`, "release", "Android")
	want := map[string]string{"levelId": "3", "room.id": "r-12", "stage_type": "boss", "seed": "123456789012", "elite": "true"}
	if fmt.Sprint(c.Context) != fmt.Sprint(want) {
		t.Errorf("context %v, want %v", c.Context, want)
	}
	if stats != (ContextStats{}) {
		t.Errorf("stats %+v", stats)
	}
	if c.RawContext != nil {
		t.Error("RawContext should be released after parsing")
	}
}

func TestNormalizeEventContext_DropsInvalidKeysAndShapes(t *testing.T) {
	long := strings.Repeat("k", 65)
	c, stats := normalize(`{"ok":"1","bad key":"x","quo'te":"x","":"x","`+long+`":"x","obj":{"a":1},"arr":[1],"ünï":"x"}`, "", "Android")
	if len(c.Context) != 1 || c.Context["ok"] != "1" {
		t.Errorf("context %v", c.Context)
	}
	if stats.Dropped != 7 || stats.Truncated != 0 {
		t.Errorf("stats %+v, want 7 dropped", stats)
	}
}

func TestNormalizeEventContext_NotAnObjectIsDroppedNotFatal(t *testing.T) {
	for _, raw := range []string{`"levelId=3"`, `[1,2]`, `42`, `{"unterminated":`} {
		c, stats := normalize(raw, "dev", "Android")
		if c.Context == nil || len(c.Context) != 0 || stats.Dropped != 1 {
			t.Errorf("%s: context %v stats %+v", raw, c.Context, stats)
		}
		if c.BuildType != "dev" {
			t.Errorf("%s: build type %q", raw, c.BuildType)
		}
	}
	for _, raw := range []string{``, `null`, ` `} {
		c, stats := normalize(raw, "", "Android")
		if c.Context == nil || len(c.Context) != 0 || stats != (ContextStats{}) {
			t.Errorf("%q: context %v stats %+v", raw, c.Context, stats)
		}
	}
}

func TestNormalizeEventContext_TruncatesLongValuesOnRuneBoundary(t *testing.T) {
	value := strings.Repeat("界", 300) // 3 bytes per rune
	c, stats := normalize(`{"weapons":"`+value+`","short":"ok"}`, "", "Android")
	got := c.Context["weapons"]
	if len([]rune(got)) != models.MaxContextValueLength || !strings.HasPrefix(value, got) {
		t.Errorf("truncated to %d runes", len([]rune(got)))
	}
	if stats.Truncated != 1 || stats.Dropped != 0 || c.Context["short"] != "ok" {
		t.Errorf("stats %+v context %v", stats, c.Context)
	}
}

func TestNormalizeEventContext_CapsTheNumberOfKeys(t *testing.T) {
	fields := make([]string, 70)
	for i := range fields {
		fields[i] = fmt.Sprintf(`"k%02d":"v"`, i)
	}
	c, stats := normalize("{"+strings.Join(fields, ",")+"}", "", "Android")
	if len(c.Context) != models.MaxContextKeys || stats.Dropped != 6 {
		t.Fatalf("%d keys kept, stats %+v", len(c.Context), stats)
	}
	// The first 64 in key order are kept, so the choice is deterministic.
	if _, ok := c.Context["k63"]; !ok {
		t.Error("k63 should be kept")
	}
	if _, ok := c.Context["k64"]; ok {
		t.Error("k64 should be dropped")
	}
}

func TestNormalizeEventContext_EscapesSurviveRoundTrip(t *testing.T) {
	c, _ := normalize(`{"msg":"a\"b\\c\nd\u0001 é 😀"}`, "", "Android")
	if c.Context["msg"] != "a\"b\\c\nd\x01 é 😀" {
		t.Errorf("value %q", c.Context["msg"])
	}
}

func TestNormalizeEventContext_BuildType(t *testing.T) {
	cases := []struct{ buildType, platform, want string }{
		{"release", "Android", "release"},
		{" Dev ", "Android", "dev"},
		{"EDITOR", "Windows", "editor"},
		{"", "Android", ""},             // old SDK: unknown
		{"debug", "Android", ""},        // not one of the three
		{"", "WindowsEditor", "editor"}, // the editor survives platform folding
		{"", "OSXEditor", "editor"},
		{"release", "OSXEditor", "release"}, // what the client says wins
	}
	for _, tc := range cases {
		c, _ := normalize("", tc.buildType, tc.platform)
		if c.BuildType != tc.want {
			t.Errorf("build_type %q platform %q: got %q, want %q", tc.buildType, tc.platform, c.BuildType, tc.want)
		}
	}
}

func TestNormalizeEventContext_DeviceFields(t *testing.T) {
	c := models.EventContext{GPU: "  " + strings.Repeat("G", 200) + " ", RAMClass: " 8GB ", NetType: "wifi"}
	NormalizeEventContext(&c, "Android")
	if len(c.GPU) != 128 || c.RAMClass != "8GB" || c.NetType != "wifi" {
		t.Errorf("%q %q %q", c.GPU, c.RAMClass, c.NetType)
	}
}

func TestValidContextKey(t *testing.T) {
	for _, k := range []string{"a", "levelId", "room.id", "stage-type", "x_1", strings.Repeat("k", 64)} {
		if !models.ValidContextKey(k) {
			t.Errorf("%q should be valid", k)
		}
	}
	for _, k := range []string{"", " a", "a b", "a'b", "a=b", "a:b", "ä", strings.Repeat("k", 65), "a\x00"} {
		if models.ValidContextKey(k) {
			t.Errorf("%q should be invalid", k)
		}
	}
}
