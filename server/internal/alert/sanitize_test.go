package alert

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCleanText_Inert(t *testing.T) {
	hostile := "Boom @all @here <!channel> <@U123> <at user_id=\"all\"></at> <https://evil.example|click> " +
		"[click](https://evil.example) `code` *bold* ~strike~ a|b & &lt;!here&gt;\nsecond line\r\n\tthird fourth" +
		"‮evil​hidden\x00\x07"
	got := CleanText(hostile, 1000)
	for _, bad := range []string{"@all", "@here", "<", ">", "](", "`", "*", "~", "|", "&", "\n", "\r", "\t", " ",
		"‮", "\x00", "\x07", "https://"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q survived in %q", bad, got)
		}
	}
	if !strings.Contains(got, "Boom @") || !strings.Contains(got, "second line third fourth") {
		t.Errorf("readable text lost: %q", got)
	}
	if !utf8.ValidString(CleanText("bad \xff\xfe utf8", 100)) {
		t.Error("invalid UTF-8 not replaced")
	}
}

func TestCleanText_Bounded(t *testing.T) {
	long := strings.Repeat("é", 500)
	got := CleanText(long, 100)
	if n := utf8.RuneCountInString(got); n != 100 || !strings.HasSuffix(got, "…") {
		t.Errorf("length %d, %q", n, got[len(got)-10:])
	}
	if CleanText("short", 100) != "short" {
		t.Error("short text changed")
	}
	if CleanText("", 10) != "" || CleanText("   \n ", 10) != "" {
		t.Error("blank text not empty")
	}
	// A cut never leaves the zero-width space of a broken mention dangling.
	if got := CleanText("abc@@@@@@@@", 6); utf8.RuneCountInString(got) > 6 {
		t.Errorf("cut %q", got)
	}
}

func TestFirstLine(t *testing.T) {
	if FirstLine("\n\n  SIGSEGV at 0x0\nframe 1") != "SIGSEGV at 0x0" {
		t.Error("first line")
	}
}
