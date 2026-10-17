package processor

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/warriorguo/ozx_apm/server/internal/fingerprint"
	"github.com/warriorguo/ozx_apm/server/internal/models"
)

const embeddedChain = "System.IO.InvalidDataException: Save file 'inventory.json' is corrupt ---> System.FormatException: bad digit 7\n" +
	"  at Ozx.Save.Parser.Parse (System.String s) [0x00000] in <00000000000000000000000000000000>:0 \n" +
	"   --- End of inner exception stack trace ---\n" +
	"  at Ozx.Save.SaveLoader.Load (System.String path) [0x00000] in /Users/builder/ozx/Assets/Save/SaveLoader.cs:40 \n" +
	"  at Ozx.Core.GameEventBus.Publish[T] (T evt) [0x00000] in /Users/builder/ozx/Assets/Core/GameEventBus.cs:48 \n"

func TestApplyFingerprint_WrappedException(t *testing.T) {
	e := models.Exception{
		Fingerprint: "client-fp",
		Message:     "[GameEventBus] Error handling event SaveRequested: " + embeddedChain[:60],
		Stack:       "Ozx.Core.GameEventBus:Publish(SaveRequested) (at /Users/builder/ozx/Assets/Core/GameEventBus.cs:52)\n",
		ExceptionDetails: models.ExceptionDetails{
			LogType:           "error",
			EmbeddedException: embeddedChain,
			Breadcrumbs:       []string{"[10:00:00.000] Scene: Main", "[10:00:01.000] room r-12\n"},
		},
	}
	ApplyFingerprint(&e)
	want := fingerprint.Analyze(fingerprint.Input{LogType: "error", Message: e.Message, Stack: e.Stack, Embedded: embeddedChain})
	d := e.ExceptionDetails
	if e.Fingerprint != want.Fingerprint || d.ClientFingerprint != "client-fp" || d.FingerprintVersion != fingerprint.Version {
		t.Errorf("fingerprint %q client %q version %d", e.Fingerprint, d.ClientFingerprint, d.FingerprintVersion)
	}
	if d.ExceptionType != "System.IO.InvalidDataException" || d.MessageTemplate != "Save file '*' is corrupt" {
		t.Errorf("type %q template %q", d.ExceptionType, d.MessageTemplate)
	}
	if len(d.InnerExceptions) != 1 || d.InnerExceptions[0] != "System.FormatException: bad digit 7" {
		t.Errorf("inner %q", d.InnerExceptions)
	}
	if d.ThrowSite != "Ozx.Save.SaveLoader.Load (at /Users/builder/ozx/Assets/Save/SaveLoader.cs:40)" ||
		d.CallSite != "Ozx.Core.GameEventBus.Publish (at /Users/builder/ozx/Assets/Core/GameEventBus.cs:52)" {
		t.Errorf("throw %q call %q", d.ThrowSite, d.CallSite)
	}
	if len(d.Breadcrumbs) != 2 || d.Breadcrumbs[1] != "[10:00:01.000] room r-12" {
		t.Errorf("breadcrumbs %q", d.Breadcrumbs)
	}
}

// Events from SDKs before APM-4 get the server's fingerprint too, and empty
// (never nil) arrays.
func TestApplyFingerprint_LegacyEvent(t *testing.T) {
	e := models.Exception{Fingerprint: "0123456789abcdef", Message: "NullReferenceException: Object reference not set to an instance of an object",
		Stack: "Ozx.Enemies.EnemyAI.Update () (at Assets/Scripts/Enemies/EnemyAI.cs:31)\n"}
	ApplyFingerprint(&e)
	d := e.ExceptionDetails
	if d.ClientFingerprint != "0123456789abcdef" || e.Fingerprint == "0123456789abcdef" || d.LogType != "" {
		t.Errorf("%+v", e)
	}
	if d.ExceptionType != "NullReferenceException" || d.Breadcrumbs == nil || d.InnerExceptions == nil {
		t.Errorf("%+v", d)
	}
}

// testdata/sdk_events.jsonl are exception events as the SDK writes them
// (ExceptionCollector + JsonSerializer, run on Unity's Mono): a bus error,
// its repeat count, a compacted 60-frame inner chain, a logged exception, a
// plain error with non-ASCII text and an assert. The server must compute the
// fingerprint the SDK computed, or the device and the server group differently.
func TestApplyFingerprint_MatchesTheSDK(t *testing.T) {
	b, err := os.ReadFile("testdata/sdk_events.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e models.Exception
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		ApplyFingerprint(&e)
		if e.Fingerprint != e.ClientFingerprint {
			t.Errorf("server %s, SDK %s: %.120q", e.Fingerprint, e.ClientFingerprint, e.Message)
		}
		n++
	}
	if n != 6 {
		t.Errorf("%d events", n)
	}
}

func TestApplyFingerprint_CapsWhatTheClientSent(t *testing.T) {
	var crumbs []string
	for i := 0; i < 80; i++ {
		crumbs = append(crumbs, strings.Repeat("é", 600)+string(rune('a'+i%26)))
	}
	crumbs[79] = "last"
	e := models.Exception{Fingerprint: "f", Message: "m", ExceptionDetails: models.ExceptionDetails{
		LogType:           "<script>",
		EmbeddedException: strings.Repeat("é", MaxEmbeddedExceptionLength+100),
		Breadcrumbs:       crumbs,
	}}
	ApplyFingerprint(&e)
	d := e.ExceptionDetails
	if d.LogType != "" {
		t.Errorf("log type %q", d.LogType)
	}
	if n := utf8.RuneCountInString(d.EmbeddedException); n != MaxEmbeddedExceptionLength || !utf8.ValidString(d.EmbeddedException) {
		t.Errorf("embedded: %d runes, valid %v", n, utf8.ValidString(d.EmbeddedException))
	}
	if len(d.Breadcrumbs) != MaxBreadcrumbs || d.Breadcrumbs[MaxBreadcrumbs-1] != "last" {
		t.Errorf("breadcrumbs: %d, last %q", len(d.Breadcrumbs), d.Breadcrumbs[len(d.Breadcrumbs)-1])
	}
	for _, b := range d.Breadcrumbs {
		if utf8.RuneCountInString(b) > MaxBreadcrumbLength || !utf8.ValidString(b) {
			t.Fatalf("breadcrumb of %d runes", utf8.RuneCountInString(b))
		}
	}
}
