package media

import (
	"strings"
	"testing"
)

func TestTextStripsWhatATerminalWouldObey(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"plain", "A normal title", "A normal title"},
		{"keeps non-ascii", "Ökologie — 日本語 · café", "Ökologie — 日本語 · café"},
		{"collapses whitespace", "  spaced \t out \n title  ", "spaced out title"},
		{"strips the escape and leaves the rest inert", "before\x1b[31mafter", "before[31mafter"},
		{"defuses an osc-8 hyperlink", "\x1b]8;;https://elsewhere\x07click\x1b]8;;\x07", "]8;;https://elsewhereclick]8;;"},
		{"drops c1 controls", "abc", "abc"},
		{"drops del", "a\x7fb", "ab"},
		{"drops the replacement char", "a�b", "ab"},
		{"empty", "", ""},
		{"only controls", "\x00\x1b\x07", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.in); got != tc.want {
				t.Errorf("Text(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The point of stripping at the boundary is that nothing downstream has to
// recognise a sequence in order to be safe from it. Whatever a title
// contained, what comes out carries no byte a terminal reads as an
// instruction.
func TestTextLeavesNoControlBytes(t *testing.T) {
	hostile := []string{
		"\x1b[2J\x1b[H wiped your screen",
		"\x1b]0;retitled your window\x07",
		"\x1b]8;;file:///etc/passwd\x07looks harmless\x1b]8;;\x07",
		"line one\nline two\rline three",
		"\x1bPtmux;\x1b\x1b]0;nested\x07\x1b\\",
		"0;a c1 operating system command",
	}
	for _, in := range hostile {
		got := Text(in)
		for _, r := range got {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				t.Errorf("Text(%q) = %q, which still contains %U", in, got, r)
			}
		}
		if strings.ContainsRune(got, 0x1b) {
			t.Errorf("Text(%q) kept an escape", in)
		}
	}
}

func TestIsChannelID(t *testing.T) {
	const valid = "UCabcdefghijklmnopqrstuv"
	if len(valid) != 24 {
		t.Fatalf("the fixture is %d characters; it no longer tests the shape", len(valid))
	}
	if !IsChannelID(valid) {
		t.Errorf("IsChannelID(%q) = false", valid)
	}
	for _, bad := range []string{
		"", "UC", "ucabcdefghijklmnopqrstuv", "UCabcdefghijklmnopqrstu",
		"UCabcdefghijklmnopqrstuvw", "UCabcdefghijklmnopqrst v", "UCabcdefghijklmnopqrst/v",
		"--exec=touch x", "UCabcdefghijklmnopqrst.v",
	} {
		if IsChannelID(bad) {
			t.Errorf("IsChannelID(%q) = true", bad)
		}
	}
}

func TestIsVideoID(t *testing.T) {
	if !IsVideoID("dQw4w9WgXcQ") {
		t.Error("a well-formed video id was rejected")
	}
	for _, bad := range []string{"", "short", "waytoolongforavideoid", "dQw4w9WgXc/", "-Qw4w9WgXcQ!"} {
		if IsVideoID(bad) {
			t.Errorf("IsVideoID(%q) = true", bad)
		}
	}
}

func TestURLIsBuiltFromTheIdentifier(t *testing.T) {
	v := Video{ID: "dQw4w9WgXcQ"}
	if got, want := v.URL(), "https://www.youtube.com/watch?v=dQw4w9WgXcQ"; got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}
}
