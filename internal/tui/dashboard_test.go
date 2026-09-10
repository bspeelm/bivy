package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bspeelm/bivy/internal/follow"
	"github.com/bspeelm/bivy/internal/media"
)

// Golden files rather than inline strings: a screen is read as a whole, and a
// diff of one is the only form in which a layout change is reviewable.
//
//	go test ./internal/tui -update
var update = flag.Bool("update", false, "rewrite the golden files")

var now = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")

	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/tui -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s does not match.\n\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func row(channel, title string, ago time.Duration, fresh bool) follow.Row {
	return follow.Row{
		Video:   media.Video{ID: "aaaaaaaaaaa", Title: title, Published: now.Add(-ago)},
		Channel: channel,
		New:     fresh,
	}
}

func TestRenderDashboard(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    Dashboard
	}{
		{"empty", Dashboard{Now: now}},
		{"mixed", Dashboard{Now: now, Rows: []follow.Row{
			row("Aye", "The newest thing that happened", 2*time.Hour, true),
			row("Bee", "Something from yesterday", 26*time.Hour, true),
			row("Aye", "Older, and already seen", 5*24*time.Hour, false),
		}}},
		{"nothing new", Dashboard{Now: now, Rows: []follow.Row{
			row("Aye", "All of this was here last time", 9*24*time.Hour, false),
		}}},
		{"one new", Dashboard{Now: now, Rows: []follow.Row{
			row("Aye", "Just the one", 30*time.Minute, true),
		}}},
		{"a channel could not be reached", Dashboard{Now: now, Failed: []string{"Bee"}, Rows: []follow.Row{
			row("Aye", "This one arrived", time.Hour, true),
		}}},
		{"everything failed", Dashboard{Now: now, Failed: []string{"Aye", "Bee"}}},
		{"long names are truncated", Dashboard{Now: now, Rows: []follow.Row{
			row("A Channel With A Very Long Name Indeed",
				"A title that runs well past the width a terminal gives it and keeps going",
				time.Hour, true),
		}}},
		{"narrow", Dashboard{Now: now, Width: 46, Rows: []follow.Row{
			row("Aye", "A title that has to be cut to fit", time.Hour, true),
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			golden(t, strings.ReplaceAll(tc.name, " ", "-"), Render(tc.d))
		})
	}
}

// Every rendered line stays inside the width it was given. A row that wraps
// turns the dashboard into two ragged columns, and it is the kind of thing
// only a long channel name in real data ever finds.
func TestRenderStaysInsideItsWidth(t *testing.T) {
	for _, width := range []int{0, 46, 60, 80, 120} {
		d := Dashboard{Now: now, Width: width, Failed: []string{"A Channel That Was Unreachable"}, Rows: []follow.Row{
			row("A Channel With A Very Long Name Indeed",
				"A title that runs well past the width a terminal gives it and then keeps on going for a while",
				time.Hour, true),
			row("Bee", "Short", 3*time.Hour, false),
		}}

		limit := width
		if limit < minWidth {
			limit = defaultWidth
		}
		for _, line := range strings.Split(Render(d), "\n") {
			if n := len([]rune(line)); n > limit {
				t.Errorf("at width %d a line is %d wide:\n%s", width, n, line)
			}
		}
	}
}

// A title arrives from a feed, and the feed does not know how wide the
// terminal is. Truncation counts runes, because cutting a multi-byte character
// in half produces a byte no terminal can draw.
func TestTruncationCountsRunesNotBytes(t *testing.T) {
	const title = "日本語のタイトルはとても長いのでどこかで切る必要があります"
	got := pad(title, 10)

	if n := len([]rune(got)); n != 10 {
		t.Errorf("pad returned %d runes, want 10", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("pad(%q) = %q, want it marked as truncated", title, got)
	}
	if !strings.ContainsRune(got, '日') {
		t.Errorf("pad(%q) = %q, which is not the start of the title", title, got)
	}
}

func TestPadLeavesShortStringsAlone(t *testing.T) {
	if got := pad("short", 20); got != "short" {
		t.Errorf("pad = %q, want it unchanged", got)
	}
}

func TestAgo(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{-time.Hour, "soon"},
		{10 * time.Second, "now"},
		{5 * time.Minute, "5m ago"},
		{90 * time.Minute, "1h ago"},
		{25 * time.Hour, "1d ago"},
		{6 * 24 * time.Hour, "6d ago"},
		{8 * 24 * time.Hour, "1w ago"},
		{400 * 24 * time.Hour, "1y ago"},
	} {
		if got := Ago(now, now.Add(-tc.d)); got != tc.want {
			t.Errorf("Ago(-%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// The age column is a fixed width, so a long one would push every title on
// that row out of alignment.
func TestAgoFitsItsColumn(t *testing.T) {
	for _, d := range []time.Duration{
		-time.Hour, time.Second, 59 * time.Minute, 23 * time.Hour,
		6 * 24 * time.Hour, 51 * 7 * 24 * time.Hour, 4000 * 24 * time.Hour,
	} {
		if got := Ago(now, now.Add(-d)); len(got) > agoWidth {
			t.Errorf("Ago(-%s) = %q, which is %d wide and the column is %d", d, got, len(got), agoWidth)
		}
	}
}
