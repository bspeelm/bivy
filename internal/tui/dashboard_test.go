package tui

import (
	"flag"
	"fmt"
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

func watchedRow(channel, title string, ago time.Duration) follow.Row {
	r := row(channel, title, ago, true)
	r.Watched = true
	return r
}

func manyRows(n int) []follow.Row {
	var rows []follow.Row
	for i := range n {
		rows = append(rows, row("Aye", fmt.Sprintf("Video number %d", i), time.Duration(i)*time.Hour, i < 3))
	}
	return rows
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
		{"interactive", Dashboard{Now: now, Interactive: true, Selected: 1, Rows: []follow.Row{
			row("Aye", "The newest thing that happened", 2*time.Hour, true),
			row("Bee", "The one under the cursor", 26*time.Hour, true),
			watchedRow("Aye", "One that has been watched", 5*24*time.Hour),
		}}},
		{"playing", Dashboard{Now: now, Interactive: true, Status: "playing · The one under the cursor", Rows: []follow.Row{
			row("Aye", "The one under the cursor", 2*time.Hour, true),
		}}},
		{"scrolled", Dashboard{Now: now, Interactive: true, Height: 10, Selected: 12, Rows: manyRows(30)}},
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

// Watched outranks new. Something already watched is not news, whenever it
// arrived, and a row that claims both says nothing.
func TestWatchedOutranksNew(t *testing.T) {
	both := row("Aye", "Seen it", time.Hour, true)
	both.Watched = true

	if got, want := marker(both), " ✓ "; got != want {
		t.Errorf("marker = %q, want %q", got, want)
	}
	if got, want := marker(row("Aye", "Fresh", time.Hour, true)), " • "; got != want {
		t.Errorf("marker = %q, want %q", got, want)
	}
	if got, want := marker(row("Aye", "Old", time.Hour, false)), "   "; got != want {
		t.Errorf("marker = %q, want %q", got, want)
	}
}

func TestMoveStaysInsideTheList(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		selected, delta, rows int
		want                  int
	}{
		{"down", 0, 1, 5, 1},
		{"up", 3, -1, 5, 2},
		{"down at the bottom stays", 4, 1, 5, 4},
		{"up at the top stays", 0, -1, 5, 0},
		{"a long jump down clamps", 0, 99, 5, 4},
		{"a long jump up clamps", 4, -99, 5, 0},
		{"an empty list has no cursor", 0, 1, 0, 0},
		{"a selection past the end comes back", 9, 0, 5, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Move(tc.selected, tc.delta, tc.rows); got != tc.want {
				t.Errorf("Move(%d, %d, %d) = %d, want %d", tc.selected, tc.delta, tc.rows, got, tc.want)
			}
		})
	}
}

// The cursor stays put and the list moves under it, so a row that was next to
// the cursor before a keypress is still next to it after one.
func TestTheWindowKeepsTheCursorInIt(t *testing.T) {
	const rows, height = 30, 12

	for selected := range rows {
		first, last := window(selected, rows, height)

		if selected < first || selected >= last {
			t.Errorf("row %d is outside the window %d-%d", selected, first, last)
		}
		if first < 0 || last > rows {
			t.Errorf("window %d-%d is outside the list of %d", first, last, rows)
		}
		if got, want := last-first, height-chrome; got != want {
			t.Errorf("window at %d shows %d rows, want %d", selected, got, want)
		}
	}
}

func TestAShortListIsNotWindowed(t *testing.T) {
	first, last := window(0, 3, 40)
	if first != 0 || last != 3 {
		t.Errorf("window = %d-%d, want the whole list", first, last)
	}

	// Height zero is the non-interactive listing: everything, no window.
	if first, last := window(0, 100, 0); first != 0 || last != 100 {
		t.Errorf("window = %d-%d, want the whole list", first, last)
	}
}

// A terminal can be two rows tall. Refusing to draw is worse than drawing one
// row, and a negative slice index is worse than both.
func TestAnAbsurdlyShortTerminalStillDraws(t *testing.T) {
	for height := 1; height <= chrome+1; height++ {
		first, last := window(5, 30, height)
		if first < 0 || last > 30 || first >= last {
			t.Errorf("at height %d the window is %d-%d", height, first, last)
		}
	}

	frame := Render(Dashboard{Now: now, Interactive: true, Height: 2, Rows: manyRows(30)})
	if frame == "" {
		t.Error("a two-row terminal rendered nothing at all")
	}
}
