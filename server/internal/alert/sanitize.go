package alert

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Game-provided text (exception messages and types, throw sites, crash
// stacks, version names, fingerprints) is untrusted: a player's input can end
// up in an exception message. Before it goes into a webhook message it is
// made inert for the chat tools such a webhook posts to (Slack, Feishu/Lark,
// generic JSON consumers that may render Markdown):
//
//   - one line: control characters, line and paragraph separators become
//     spaces; invisible format characters (bidi overrides, zero-width) are
//     removed; runs of spaces collapse;
//   - no mentions: "@" is followed by a zero-width space, so "@all",
//     "@here", "@channel" and "@everyone" never notify anyone; "<" and ">"
//     become "‹" and "›", so Slack's "<!channel>" / "<@U123>" / "<url|text>"
//     and Feishu's "<at user_id=\"all\">" cannot be formed; "&" becomes "＆"
//     (no entity tricks);
//   - no Markdown: "`", "*", "~", "[", "]", "|" become look-alikes, "://"
//     gets a zero-width space so URLs in game text are not linked;
//   - bounded: at most max characters, cut with "…".

const zwsp = "​"

var inertReplacer = strings.NewReplacer(
	"@", "@"+zwsp,
	"<", "‹",
	">", "›",
	"&", "＆",
	"`", "'",
	"*", "∗",
	"~", "∼",
	"[", "(",
	"]", ")",
	"|", "¦",
	"://", ":"+zwsp+"//",
)

// CleanText returns s as one inert line of at most max characters.
func CleanText(s string, max int) string {
	if s == "" {
		return ""
	}
	s = strings.ToValidUTF8(s, "�")
	var sb strings.Builder
	sb.Grow(len(s))
	space := false
	for _, r := range s {
		switch {
		case unicode.IsControl(r) || unicode.IsSpace(r) || r == ' ' || r == ' ':
			if !space && sb.Len() > 0 {
				sb.WriteByte(' ')
			}
			space = true
			continue
		case unicode.Is(unicode.Cf, r): // format characters: bidi controls, zero-width
			continue
		}
		sb.WriteRune(r)
		space = false
	}
	out := inertReplacer.Replace(strings.TrimRight(sb.String(), " "))
	return truncateRunes(out, max)
}

// truncateRunes cuts s to at most max characters, ending with "…" when cut.
func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	n := 0
	for i := range s {
		if n == max-1 {
			return strings.TrimRight(s[:i], " "+zwsp) + "…"
		}
		n++
	}
	return s
}

// FirstLine returns the first non-empty line of s (for a crash stack).
func FirstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}
