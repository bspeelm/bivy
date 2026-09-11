package tui

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

func videoRow(channel, title string, ago time.Duration, fresh bool) follow.Row {
	return follow.Row{
		Video:   media.Video{ID: "aaaaaaaaaaa", Title: title, Published: now.Add(-ago)},
		Channel: channel,
		New:     fresh,
	}
}

func watchedRow(channel, title string, ago time.Duration) follow.Row {
	r := videoRow(channel, title, ago, true)
	r.Watched = true
	return r
}

// A search result: a duration and no publish time, which is what the
// extractor actually returns.
func resultRow(channel, title string, length time.Duration) follow.Row {
	return follow.Row{
		Video:   media.Video{ID: "aaaaaaaaaaa", Title: title, Duration: length},
		Channel: channel,
	}
}

// A channel row: no video, a subscriber count and a description.
func channelRow(name, summary string, followers int, followed bool) follow.Row {
	return follow.Row{
		Channel:   name,
		ChannelID: "UCaaaaaaaaaaaaaaaaaaaaaa",
		Summary:   summary,
		Followers: followers,
		Followed:  followed,
	}
}

func manyRows(n int) []follow.Row {
	var rows []follow.Row
	for i := range n {
		rows = append(rows, videoRow("Aye", fmt.Sprintf("Video number %d", i), time.Duration(i)*time.Hour, i < 3))
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
			videoRow("Aye", "The newest thing that happened", 2*time.Hour, true),
			videoRow("Bee", "Something from yesterday", 26*time.Hour, true),
			videoRow("Aye", "Older, and already seen", 5*24*time.Hour, false),
		}}},
		{"nothing new", Dashboard{Now: now, Rows: []follow.Row{
			videoRow("Aye", "All of this was here last time", 9*24*time.Hour, false),
		}}},
		{"one new", Dashboard{Now: now, Rows: []follow.Row{
			videoRow("Aye", "Just the one", 30*time.Minute, true),
		}}},
		{"a channel could not be reached", Dashboard{Now: now, Failed: []string{"Bee"}, Rows: []follow.Row{
			videoRow("Aye", "This one arrived", time.Hour, true),
		}}},
		{"everything failed", Dashboard{Now: now, Failed: []string{"Aye", "Bee"}}},
		{"long names are truncated", Dashboard{Now: now, Rows: []follow.Row{
			videoRow("A Channel With A Very Long Name Indeed",
				"A title that runs well past the width a terminal gives it and keeps going",
				time.Hour, true),
		}}},
		{"narrow", Dashboard{Now: now, Width: 46, Rows: []follow.Row{
			videoRow("Aye", "A title that has to be cut to fit", time.Hour, true),
		}}},
		{"interactive", Dashboard{Now: now, Interactive: true, Selected: 1, Rows: []follow.Row{
			videoRow("Aye", "The newest thing that happened", 2*time.Hour, true),
			videoRow("Bee", "The one under the cursor", 26*time.Hour, true),
			watchedRow("Aye", "One that has been watched", 5*24*time.Hour),
		}}},
		{"playing", Dashboard{Now: now, Interactive: true, Status: "playing · The one under the cursor", Rows: []follow.Row{
			videoRow("Aye", "The one under the cursor", 2*time.Hour, true),
		}}},
		{"scrolled", Dashboard{Now: now, Interactive: true, Height: 10, Selected: 12, Rows: manyRows(30)}},
		{"typing a search", Dashboard{Now: now, Interactive: true, Typing: true, Line: "search terminal video", Rows: manyRows(5)}},
		{"the command line empty", Dashboard{Now: now, Interactive: true, Typing: true, Rows: manyRows(5)}},
		{"the command line narrowed", Dashboard{Now: now, Interactive: true, Typing: true, Line: "f", Rows: manyRows(5)}},
		{"the command line matching nothing", Dashboard{Now: now, Interactive: true, Typing: true, Line: "xyzzy"}},
		{"search results", Dashboard{Now: now, Interactive: true, Query: "terminal video", Rows: []follow.Row{
			resultRow("DistroTube", "Watch Videos In Your Linux Terminal", 89*time.Second),
			resultRow("Someone Else", "A much longer one", 2*time.Hour+5*time.Minute+9*time.Second),
		}}},
		{"search found nothing", Dashboard{Now: now, Interactive: true, Query: "asdfghjkl"}},
		{"channel results", Dashboard{Now: now, Interactive: true, Query: "papa meat", Channels: true, Height: 12, Rows: []follow.Row{
			channelRow("Papa Meat", "I make cartoons on my main account", 3_590_000, false),
			channelRow("MeatCanyon", "Oh hi i make cartoons", 9_130_000, true),
			channelRow("Meaty Magic", "The channel for all your magic", 266_000, false),
			channelRow("A Small One", "just starting out", 412, false),
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
			videoRow("A Channel With A Very Long Name Indeed",
				"A title that runs well past the width a terminal gives it and then keeps on going for a while",
				time.Hour, true),
			videoRow("Bee", "Short", 3*time.Hour, false),
		}}

		limit := width
		if limit < minWidth {
			limit = defaultWidth
		}
		// Measured with the attributes taken off. An escape sequence occupies
		// no cells, and counting it as though it did would make this test pass
		// by accident on a line that overflows.
		for _, line := range strings.Split(Render(d), "\n") {
			if n := cells(line); n > limit {
				t.Errorf("at width %d a line is %d wide:\n%s", width, n, plain(line))
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
		if got := Ago(now, now.Add(-d)); len([]rune(got)) > 7 {
			t.Errorf("Ago(-%s) = %q, which is %d wide; it shares a line with a title", d, got, len([]rune(got)))
		}
	}
}

// Watched outranks new. Something already watched is not news, whenever it
// arrived, and a row that claims both says nothing.
func TestWatchedOutranksNew(t *testing.T) {
	both := videoRow("Aye", "Seen it", time.Hour, true)
	both.Watched = true

	if got, want := marker(both), "✓"; got != want {
		t.Errorf("marker = %q, want %q", got, want)
	}
	if got, want := marker(videoRow("Aye", "Fresh", time.Hour, true)), "•"; got != want {
		t.Errorf("marker = %q, want %q", got, want)
	}
	if got, want := marker(videoRow("Aye", "Old", time.Hour, false)), " "; got != want {
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
	const rows, visible = 30, 8

	for selected := range rows {
		first, last := window(selected, rows, visible)

		if selected < first || selected >= last {
			t.Errorf("row %d is outside the window %d-%d", selected, first, last)
		}
		if first < 0 || last > rows {
			t.Errorf("window %d-%d is outside the list of %d", first, last, rows)
		}
		if got, want := last-first, visible; got != want {
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
	for visible := 1; visible <= 6; visible++ {
		first, last := window(5, 30, visible)
		if first < 0 || last > 30 || first >= last {
			t.Errorf("with %d visible rows the window is %d-%d", visible, first, last)
		}
	}

	frame := Render(Dashboard{Now: now, Interactive: true, Height: 2, Rows: manyRows(30)})
	if frame == "" {
		t.Error("a two-row terminal rendered nothing at all")
	}
}

// A feed entry has a publish time and no duration; a search result has the
// reverse. The column shows whichever is known rather than inventing the
// other — a search result with no date would otherwise read as fifty years
// old, which is the kind of wrong that looks like data.
func TestTheSecondColumnShowsWhateverIsKnown(t *testing.T) {
	published := media.Video{Published: now.Add(-2 * time.Hour)}
	if got, want := when(now, published), "2h ago"; got != want {
		t.Errorf("a feed entry showed %q, want %q", got, want)
	}

	result := media.Video{Duration: 89 * time.Second}
	if got, want := when(now, result), "1:29"; got != want {
		t.Errorf("a search result showed %q, want %q", got, want)
	}

	if got := when(now, media.Video{}); got != "" {
		t.Errorf("a video with neither showed %q, want nothing", got)
	}

	// A publish time wins where both are somehow present: the dashboard is
	// about when things arrived.
	both := media.Video{Published: now.Add(-2 * time.Hour), Duration: 89 * time.Second}
	if got, want := when(now, both), "2h ago"; got != want {
		t.Errorf("a video with both showed %q, want %q", got, want)
	}
}

func TestLength(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0:00"},
		{9 * time.Second, "0:09"},
		{89 * time.Second, "1:29"},
		{10 * time.Minute, "10:00"},
		{59*time.Minute + 59*time.Second, "59:59"},
		{time.Hour, "1:00:00"},
		{2*time.Hour + 5*time.Minute + 9*time.Second, "2:05:09"},
		{9*time.Hour + 59*time.Minute + 59*time.Second, "9:59:59"},
		// Seconds stop being information at this length, and the column is
		// narrower than "12:34:56".
		{12*time.Hour + 34*time.Minute + 56*time.Second, "12h"},
		{1500 * time.Hour, "999h+"},
		{-time.Second, ""},
	} {
		if got := Length(tc.d); got != tc.want {
			t.Errorf("Length(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// The second column is a fixed width, so a long value would push every title
// on that row out of alignment.
func TestLengthFitsItsColumn(t *testing.T) {
	for _, d := range []time.Duration{
		0, time.Second, 59 * time.Second, 59*time.Minute + 59*time.Second,
		time.Hour, 12*time.Hour + 34*time.Minute + 56*time.Second,
		400 * time.Hour,
	} {
		if got := Length(d); len([]rune(got)) > 7 {
			t.Errorf("Length(%s) = %q, which is %d wide; it shares a line with a title", d, got, len([]rune(got)))
		}
	}
}

// The search box has to show what has been typed, including nothing.
func TestTheSearchPromptShowsTheQuery(t *testing.T) {
	frame := plain(Render(Dashboard{Now: now, Interactive: true, Typing: true, Line: "search cats"}))
	if !strings.Contains(frame, ":search cats█") {
		t.Errorf("the command line does not show what was typed:\n%s", frame)
	}

	empty := plain(Render(Dashboard{Now: now, Interactive: true, Typing: true}))
	if !strings.Contains(empty, ":█") {
		t.Errorf("an empty command line does not show its cursor:\n%s", empty)
	}
}

// The rows stay on screen while the command line is open. A command acts on
// what is in front of the user, and `:follow` in particular is about a channel
// they are looking at.
func TestTypingKeepsTheRowsVisible(t *testing.T) {
	frame := plain(Render(Dashboard{Now: now, Interactive: true, Height: 20, Typing: true, Line: "search x", Rows: manyRows(5)}))
	if !strings.Contains(frame, "Video number") {
		t.Errorf("the rows vanished when the command line opened:\n%s", frame)
	}
}

// A search that found nothing says so, rather than showing the empty-dashboard
// advice to follow a channel.
func TestASearchThatFoundNothingSaysSo(t *testing.T) {
	frame := plain(Render(Dashboard{Now: now, Interactive: true, Query: "asdfghjkl"}))
	if !strings.Contains(frame, "nothing found for asdfghjkl") {
		t.Errorf("a fruitless search does not say so:\n%s", frame)
	}
	if strings.Contains(frame, "nothing followed yet") {
		t.Errorf("a fruitless search offered the empty-dashboard advice:\n%s", frame)
	}
}

// Offering "enter play" with nothing to play is a small lie, and the hint line
// is the one part of the screen a new user reads as instructions.
func TestTheHintsOfferOnlyKeysThatWouldDoSomething(t *testing.T) {
	for _, tc := range []struct {
		name    string
		d       Dashboard
		absent  string
		present string
	}{
		{"an empty dashboard", Dashboard{Interactive: true}, "enter play", "/ search"},
		{"a search with no results", Dashboard{Interactive: true, Query: "x"}, "enter play", "esc back"},
		{"results", Dashboard{Interactive: true, Query: "x", Rows: manyRows(2)}, "r refresh", "enter play"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := hints(tc.d)
			if strings.Contains(got, tc.absent) {
				t.Errorf("hints = %q, which offers %q with nothing to use it on", got, tc.absent)
			}
			if !strings.Contains(got, tc.present) {
				t.Errorf("hints = %q, which does not offer %q", got, tc.present)
			}
		})
	}
}

// "showing 1-0 of 5" is arithmetic rather than information.
func TestNoWindowCountWhenNoRowsAreDrawn(t *testing.T) {
	frame := plain(Render(Dashboard{Now: now, Interactive: true, Typing: true, Line: "search x", Rows: manyRows(5)}))
	if strings.Contains(frame, "showing") {
		t.Errorf("a window count was drawn with no window:\n%s", frame)
	}
}

// plain is a rendered frame with its attributes removed, for the tests that
// are about what it says rather than how it looks.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func plain(frame string) string { return ansi.ReplaceAllString(frame, "") }

// cells is what a rendered line occupies on screen: its printable characters
// plus whatever the cursor was moved past without writing.
var cursorForward = regexp.MustCompile(`\x1b\[([0-9]+)C`)

func cells(line string) int {
	n := len([]rune(plain(line)))
	for _, m := range cursorForward.FindAllStringSubmatch(line, -1) {
		skipped, _ := strconv.Atoi(m[1])
		n += skipped
	}
	return n
}

// The completion list is the half of the bargain that makes a command line
// bearable: it shows what exists rather than asking anyone to remember.
func TestTheCommandLineShowsWhatExists(t *testing.T) {
	frame := plain(Render(Dashboard{Now: now, Interactive: true, Typing: true}))
	for _, c := range Commands {
		if !strings.Contains(frame, c.Name) {
			t.Errorf("the empty command line does not list %q:\n%s", c.Name, frame)
		}
	}

	// Narrowed to one, it says what that command does — the ones taking no
	// argument would otherwise never explain themselves at any point.
	one := plain(Render(Dashboard{Now: now, Interactive: true, Typing: true, Line: "f"}))
	if !strings.Contains(one, "follow") {
		t.Errorf("narrowing to \"f\" lost follow:\n%s", one)
	}
	if !strings.Contains(one, "add a channel") {
		t.Errorf("narrowed to one command, it does not say what it does:\n%s", one)
	}
	if strings.Contains(one, "quit") {
		t.Errorf("narrowing to \"f\" still offers quit:\n%s", one)
	}
}

func TestACommandLineWithNoMatchesSaysSo(t *testing.T) {
	frame := plain(Render(Dashboard{Now: now, Interactive: true, Typing: true, Line: "xyzzy"}))
	if !strings.Contains(frame, "no command starts with that") {
		t.Errorf("a line matching nothing does not say so:\n%s", frame)
	}
}

// A channel row shows what tells one channel from another with a similar name.
func TestAChannelRowShowsItsSize(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{
		{3_590_000, "3.6M subscribers"},
		{9_130_000, "9.1M subscribers"},
		{266_000, "266K subscribers"},
		{1_200, "1K subscribers"},
		{412, "412 subscribers"},
		{0, ""},
		{-1, ""},
	} {
		if got := followers(tc.n); got != tc.want {
			t.Errorf("followers(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// A channel already followed is marked, because the whole point of the list is
// deciding which to follow.
func TestAFollowedChannelIsMarked(t *testing.T) {
	if got, want := marker(channelRow("Aye", "", 100, true)), "✓"; got != want {
		t.Errorf("a followed channel is marked %q, want %q", got, want)
	}
	if got, want := marker(channelRow("Aye", "", 100, false)), " "; got != want {
		t.Errorf("an unfollowed channel is marked %q, want %q", got, want)
	}
}

// The hints on a channel list offer f and not enter: there is nothing to play.
func TestTheChannelListOffersFollowNotPlay(t *testing.T) {
	got := hints(Dashboard{Interactive: true, Query: "x", Channels: true, Rows: []follow.Row{channelRow("Aye", "", 1, false)}})
	if !strings.Contains(got, "f follow") {
		t.Errorf("hints = %q, want them to offer f", got)
	}
	if strings.Contains(got, "enter play") {
		t.Errorf("hints = %q, which offers play on a list of channels", got)
	}
}

// The picture scales with the window: a stamp on a large display is a waste,
// and one that grows without limit takes the screen away from the titles it is
// illustrating.
func TestThePictureScalesWithTheWindow(t *testing.T) {
	var previous int
	for _, width := range []int{80, 120, 160, 200} {
		cols, rows := ArtBox(width, 40)
		if cols == 0 {
			t.Fatalf("no picture at width %d", width)
		}
		if cols < previous {
			t.Errorf("at width %d the picture is %d cells and at %d it was %d", width, cols, width-40, previous)
		}
		previous = cols

		if cols > maxArtCols {
			t.Errorf("at width %d the picture is %d cells, past the cap of %d", width, cols, maxArtCols)
		}
		if width-cols < minList {
			t.Errorf("at width %d the picture leaves %d cells for titles", width, width-cols)
		}
		// Sixteen by nine, in cells about twice as tall as they are wide.
		if want := cols * 9 / 16 / 2; rows != want {
			t.Errorf("a %d-cell picture is %d rows, want %d", cols, rows, want)
		}
	}
}

// A window with no room for one has none, rather than a picture squeezing the
// titles out.
func TestNoPictureWhereThereIsNoRoom(t *testing.T) {
	for _, size := range [][2]int{{40, 24}, {80, 8}, {30, 30}, {0, 0}} {
		if cols, rows := ArtBox(size[0], size[1]); cols != 0 || rows != 0 {
			t.Errorf("a %dx%d window got a %dx%d picture", size[0], size[1], cols, rows)
		}
	}
}

// The titles run beside the picture, in one column — including the rows below
// it, so they do not step left half way down the list.
func TestTitlesFormOneColumnBesideThePicture(t *testing.T) {
	d := Dashboard{
		Rows: manyRows(12), Now: now, Width: 100, Height: 24,
		Interactive: true, Art: "<art>",
	}
	frame := Render(d)

	var indents []string
	for _, line := range strings.Split(frame, "\n") {
		if !strings.Contains(line, "Video number") {
			continue
		}
		m := cursorForward.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("a row does not step past the picture:\n%q", line)
			continue
		}
		indents = append(indents, m[1])
	}
	if len(indents) < 6 {
		t.Fatalf("only %d rows drawn; the frame has stopped matching", len(indents))
	}
	for _, got := range indents[1:] {
		if got != indents[0] {
			t.Errorf("rows are indented %s and %s; the titles do not line up", indents[0], got)
		}
	}
}

// Only the highlighted row has a picture, and there is only ever one.
func TestThereIsOnePicture(t *testing.T) {
	d := Dashboard{
		Rows: manyRows(12), Now: now, Width: 100, Height: 24,
		Interactive: true, Selected: 3, Art: "<PIC>",
	}
	if got := strings.Count(Render(d), "<PIC>"); got != 1 {
		t.Errorf("the frame draws %d pictures, want 1", got)
	}
}

// A row with no picture is not indented into empty space.
func TestWithoutAPictureNothingIsIndented(t *testing.T) {
	d := Dashboard{Rows: manyRows(3), Now: now, Width: 100, Height: 24, Interactive: true}
	if cursorForward.MatchString(Render(d)) {
		t.Error("rows were indented with no picture to indent past")
	}
}

// Naming the channels is right when some got through. When none did, they are
// not what went wrong, and a list of them reads as four separate problems
// rather than one.
func TestWhenNoFeedAnswersItSaysSoOnce(t *testing.T) {
	none := status(Dashboard{Failed: []string{"Aye", "Bee", "Cee"}, Reached: 0})
	if strings.Contains(none, "Aye") {
		t.Errorf("with nothing reached it blamed the channels: %q", none)
	}
	if !strings.Contains(none, "no feeds are answering") {
		t.Errorf("status = %q, want it to say the service is refusing", none)
	}

	some := status(Dashboard{Failed: []string{"Bee"}, Reached: 2})
	if !strings.Contains(some, "Bee") {
		t.Errorf("with some reached it did not name the one that failed: %q", some)
	}

	// One channel followed and one failure is not evidence about the service.
	only := status(Dashboard{Failed: []string{"Aye"}, Reached: 0})
	if !strings.Contains(only, "Aye") {
		t.Errorf("a single followed channel failing should name it: %q", only)
	}
}
