// Package tui renders bivy's screens.
//
// It does no I/O: a function here takes a model and returns text. That is what
// makes a golden-file test of every screen possible, and PLAN.md §0 asserts it
// against this package's import graph rather than trusting it to hold.
//
// There is no interactive layer yet. A dashboard that prints and exits needs a
// renderer, not a terminal library, so the choice of one waits for the
// milestone that has something to press a key on.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/bspeelm/bivy/internal/follow"
	"github.com/bspeelm/bivy/internal/media"
)

// Move returns the row the cursor lands on, clamped to the list. Here so that
// "what does down do at the bottom" is answered by a test.
func Move(selected, delta, rows int) int {
	if rows == 0 {
		return 0
	}
	next := selected + delta
	if next < 0 {
		return 0
	}
	if next >= rows {
		return rows - 1
	}
	return next
}

// Dashboard is what launching bivy shows.
type Dashboard struct {
	Rows []follow.Row
	// Failed names the channels whose feed could not be fetched, and Reached
	// counts those that did. Reported rather than omitted: a dashboard that
	// is quietly short lies.
	Failed  []string
	Reached int
	// Stale means some rows came from the extractor rather than a feed, and
	// so carry no publish time. Said plainly, because a dashboard silently
	// missing the thing it sorts by is a dashboard nobody can trust.
	Stale bool
	// Now anchors the relative timestamps, passed in rather than read so the
	// output is a function of its arguments.
	Now time.Time
	// Width and Height are the terminal's, in cells. Zero means the defaults,
	// and a zero Height also means nothing is pinned to the bottom — which is
	// what a pipe wants.
	Width  int
	Height int
	// Selected is the row under the cursor.
	Selected int
	// Status is the line above the footer: what is playing, or what went
	// wrong with the last thing that was asked for.
	Status string
	// Query is what was searched for. Non-empty means the rows are results
	// rather than the dashboard, and Channels means those results are
	// channels.
	Query    string
	Channels bool
	// Line is what has been typed into the command line, and Typing means it
	// has the keyboard.
	Line   string
	Typing bool
	// Art is the picture for the row under the cursor, already turned into
	// what the terminal draws — sized by ArtBox of this frame's own width and
	// height, so it fits the window it is in.
	Art string
	// Interactive draws the cursor, the frame and the key hints. Without it
	// the same model renders as a plain listing, which is what a pipe gets.
	Interactive bool
}

const (
	defaultWidth  = 80
	defaultHeight = 24
	minWidth      = 40

	// The rows of the frame that are not list rows: the title, two rules, the
	// status line and the footer.
	chromeLines = 5
)

// Three escape sequences are not worth the modules a styling package costs
// (ADR-009), and this package already owns every byte it emits.
const (
	bold    = "\x1b[1m"
	faint   = "\x1b[2m"
	reverse = "\x1b[7m"
	reset   = "\x1b[0m"
)

const (
	keyHints     = "↑↓ move · enter play · / search · : commands · r refresh · :q quit"
	resultHints  = "↑↓ move · enter play · f follow · / search again · esc back · :q quit"
	channelHints = "↑↓ move · f follow · / search · esc back · :q quit"
	emptyHints   = "/ search · : commands · r refresh · :q quit"
	noResults    = "/ search again · esc back · :q quit"
)

// styled wraps text in an attribute. Width is always measured before this is
// applied: an escape sequence occupies no cells, and counting it as one is how
// a rendered row ends up short of the line it should fill.
func styled(attr, text string) string {
	if text == "" {
		return ""
	}
	return attr + text + reset
}

// forward moves the cursor right without writing anything over what is there.
func forward(cells int) string { return fmt.Sprintf("\x1b[%dC", cells) }

// rule is the horizontal line above and below the list.
func rule(width int) string { return strings.Repeat("─", width) }

// fit makes a line exactly width cells. Padding matters as much as truncating:
// a row that stops early is a reversed highlight that stops early.
func fit(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = pad(s, width)
	if n := len([]rune(s)); n < width {
		s += strings.Repeat(" ", width-n)
	}
	return s
}

// Render returns the screen as text, newline-terminated, ready to print.
func Render(d Dashboard) string {
	width, height := d.Width, d.Height
	if width < minWidth {
		width = defaultWidth
	}

	var b strings.Builder
	b.WriteString(styled(bold, fit("bivy · "+heading(d), width)))
	b.WriteString("\n")
	b.WriteString(styled(faint, rule(width)))
	b.WriteString("\n")

	visible := 0
	if d.Interactive {
		if height <= 0 {
			height = defaultHeight
		}
		visible = max(1, height-chrome(d, width))
	}

	b.WriteString(list(d, width, visible))

	b.WriteString(styled(faint, rule(width)))
	b.WriteString("\n")
	b.WriteString(styled(faint, fit(status(d), width)))
	b.WriteString("\n")

	if d.Typing {
		for _, line := range completions(d, width) {
			b.WriteString(styled(faint, fit(line, width)))
			b.WriteString("\n")
		}
	}
	b.WriteString(footer(d, width))
	return b.String()
}

// chrome is how many rows the frame uses for something other than the list.
func chrome(d Dashboard, width int) int {
	n := chromeLines
	if d.Typing {
		n += len(completions(d, width))
	}
	return n
}

// ArtBox is the picture's size in cells for a window of this size, and zero
// where there is no room worth the space. A quarter of the width, bounded: one
// that grows without limit takes the screen from what it illustrates, and one
// that never grows is a stamp on a large display.
func ArtBox(width, height int) (cols, rows int) {
	if width < minWidth+minList || height < minHeight {
		return 0, 0
	}

	cols = width / 4
	cols = min(max(cols, minArtCols), maxArtCols)
	rows = cols * 9 / 16 / 2

	if rows < 3 || height-chromeLines-rows < 2 {
		return 0, 0
	}
	return cols, rows
}

const (
	minArtCols = 16
	maxArtCols = 34
	// minList is how much has to be left for the titles beside the picture.
	minList = 30
	// minHeight is the shortest window a picture is worth drawing in.
	minHeight = 14
)

// artPane is the picture's size for this frame, or zero when it has none.
func (d Dashboard) artPane() (cols, rows int) {
	if d.Art == "" || !d.Interactive {
		return 0, 0
	}
	return ArtBox(d.Width, d.Height)
}

// heading is what this screen is, after the program's name.
func heading(d Dashboard) string {
	var fresh int
	for _, r := range d.Rows {
		// The same precedence the marker uses. A headline that counts a row
		// the list does not mark is a headline nobody can reconcile.
		if r.New && !r.Watched {
			fresh++
		}
	}

	switch {
	case d.Query != "" && d.Channels:
		return fmt.Sprintf("channels · %s for %q", plural(len(d.Rows), "channel", "channels"), d.Query)
	case d.Query != "":
		return fmt.Sprintf("search · %s for %q", plural(len(d.Rows), "result", "results"), d.Query)
	case len(d.Rows) == 0:
		return "nothing followed"
	case fresh == 0:
		return fmt.Sprintf("%s, nothing new", plural(len(d.Rows), "video", "videos"))
	default:
		return fmt.Sprintf("%s since your last visit", plural(fresh, "new video", "new videos"))
	}
}

// list is the rows, padded to fill the space between the rules. Every row is
// indented past the picture's pane, including those below it, so the titles
// form one column rather than stepping left half way down.
func list(d Dashboard, width, visible int) string {
	artCols, artRows := d.artPane()
	indent := 0
	if artCols > 0 {
		indent = artCols + 2
	}

	if len(d.Rows) == 0 {
		return empty(d, width, visible)
	}

	first, last := window(d.Selected, len(d.Rows), visible)

	var b strings.Builder
	for i := first; i < last; i++ {
		var line string
		if artCols > 0 && i-first == 0 {
			// Placed where the cursor already is, without moving it.
			line = d.Art
		}

		cursor := " "
		if d.Interactive && i == d.Selected {
			cursor = ">"
		}
		text := fit(cursor+" "+row(d, d.Rows[i], width-indent-2), width-indent)
		if d.Interactive && i == d.Selected {
			text = styled(reverse, text)
		}
		if indent > 0 {
			// Stepped over rather than written across, so whether text over a
			// picture hides it never has to be answered.
			text = forward(indent) + text
		}

		b.WriteString(line + text)
		b.WriteString("\n")
	}

	drawn := last - first
	// The pane may outlast the list, and the rules sit below both.
	for range max(0, max(visible-drawn, artRows-drawn)) {
		b.WriteString("\n")
	}
	return b.String()
}

// row is one line: what is being chosen on the left, what is known about it on
// the right.
func row(d Dashboard, r follow.Row, width int) string {
	if r.IsChannel() {
		return sides(marker(r)+" "+r.Channel, followers(r.Followers), width)
	}

	left := marker(r) + " " + r.Video.Title
	right := r.Channel
	if w := when(d.Now, r.Video); w != "" {
		if right != "" {
			right += " · "
		}
		right += w
	}
	return sides(left, right, width)
}

// followers is a subscriber count at a glance. Exact figures in the millions
// are noise: the number is for telling similarly named channels apart.
func followers(n int) string {
	switch {
	case n <= 0:
		return ""
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM subscribers", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.0fK subscribers", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d subscribers", n)
	}
}

// sides puts one string at each end. The left yields: a title cut short is
// still the title, a channel name cut short is a different channel.
func sides(left, right string, width int) string {
	const gap = 2

	room := width - len([]rune(right)) - gap
	if room < 12 {
		// Too narrow for both. The right yields entirely rather than eating
		// the thing being chosen.
		return pad(left, width)
	}
	left = pad(left, room)
	spaces := width - len([]rune(left)) - len([]rune(right))
	return left + strings.Repeat(" ", max(1, spaces)) + right
}

// empty is what fills the list when there are no rows.
func empty(d Dashboard, width, visible int) string {
	var say string
	switch {
	case d.Query != "":
		say = "  nothing found for " + d.Query
	case d.Interactive:
		say = "  nothing followed yet — press / to search, or : for commands"
	default:
		// Piped somewhere. Telling a pipe which key to press is advice
		// nobody in that position can take.
		say = "  nothing followed yet — try: bivy follow @handle"
	}

	var b strings.Builder
	b.WriteString(fit(say, width))
	b.WriteString("\n")
	for range max(0, visible-1) {
		b.WriteString("\n")
	}
	return b.String()
}

// status is the line above the footer.
func status(d Dashboard) string {
	if d.Status != "" {
		return d.Status
	}
	// A channel's description of itself, for the row under the cursor. There
	// is no room for it on the row and it is what tells two apart.
	if d.Channels && d.Selected < len(d.Rows) {
		if summary := d.Rows[d.Selected].Summary; summary != "" {
			return summary
		}
	}
	if d.Stale {
		return "some feeds are not answering — those rows came from the extractor, without dates"
	}
	if len(d.Failed) > 0 && d.Query == "" {
		// Naming the channels is right when some got through and some did
		// not. When none did, the channels are not what went wrong, and a
		// list of them reads as four separate problems.
		if d.Reached == 0 && len(d.Failed) > 1 {
			return "no feeds are answering — the service is refusing them, which is usually brief"
		}
		return fmt.Sprintf("%s could not be reached: %s",
			plural(len(d.Failed), "channel", "channels"), strings.Join(d.Failed, ", "))
	}
	return "nothing playing"
}

// footer is the last row: the command line when one is open, and the keys
// otherwise.
func footer(d Dashboard, width int) string {
	if d.Typing {
		// A block where the next character goes, which is what a terminal
		// with its own cursor hidden has to draw for itself.
		return fit(":"+d.Line+"█", width) + "\n"
	}
	if !d.Interactive {
		return ""
	}
	return styled(faint, fit(hints(d), width)) + "\n"
}

// hints names only the keys that would do something. The hint line is the one
// part of the screen a new user reads as instructions.
func hints(d Dashboard) string {
	switch {
	case len(d.Rows) == 0 && d.Query != "":
		return noResults
	case len(d.Rows) == 0:
		return emptyHints
	case d.Channels:
		return channelHints
	case d.Query != "":
		return resultHints
	default:
		return keyHints
	}
}

// completions lists what the line could still become. A command line that does
// not show what it accepts is a guessing game, and six commands are few enough
// to print.
func completions(d Dashboard, width int) []string {
	matches := Matching(d.Line)
	switch len(matches) {
	case 0:
		return []string{"  no command starts with that"}
	case 1:
		return []string{"  " + spelled(matches[0]) + "   " + matches[0].Summary}
	}

	var rows []string
	row := "  "
	for _, c := range matches {
		name := spelled(c)
		if len([]rune(row))+len([]rune(name))+3 > width && row != "  " {
			rows = append(rows, strings.TrimRight(row, " "))
			row = "  "
		}
		row += name + "   "
	}
	return append(rows, strings.TrimRight(row, " "))
}

// spelled is a command as the list writes it. Bare words say nothing about
// which of them need typing after.
func spelled(c Command) string {
	if c.Argument == "" {
		return c.Name
	}
	return c.Name + " <" + c.Argument + ">"
}

// marker is the cell before the title. No colour in it: the one thing a list
// must survive is being read on a terminal that has none. A followed channel
// gets the same tick a watched video does — both mean "you have this one".
func marker(r follow.Row) string {
	switch {
	case r.Watched, r.Followed:
		return "✓"
	case r.New:
		return "•"
	default:
		return " "
	}
}

// window is the slice of rows that fits, kept around the cursor: a row next to
// it before a keypress should be next to it after one.
func window(selected, rows, visible int) (first, last int) {
	if visible <= 0 || visible >= rows {
		return 0, rows
	}

	first = selected - visible/2
	if first < 0 {
		first = 0
	}
	if first+visible > rows {
		first = rows - visible
	}
	return first, first + visible
}

func when(now time.Time, v media.Video) string {
	if !v.Published.IsZero() {
		return Ago(now, v.Published)
	}
	if v.Duration > 0 {
		return Length(v.Duration)
	}
	return ""
}

// Length is a running time.
func Length(d time.Duration) string {
	if d < 0 {
		return ""
	}
	total := int(d.Round(time.Second).Seconds())
	hours, minutes, seconds := total/3600, (total/60)%60, total%60

	switch {
	case hours >= 1000:
		return "999h+"
	case hours >= 10:
		// Seconds stop being information at this length, and the column is
		// seven wide: "12:34:56" is eight and takes a character off every
		// title on the screen.
		return fmt.Sprintf("%dh", hours)
	case hours > 0:
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	default:
		return fmt.Sprintf("%d:%02d", minutes, seconds)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Ago is a duration read at a glance. Coarse on purpose: "3 days ago" and "3
// days and four hours ago" lead to the same decision.
func Ago(now, then time.Time) string {
	d := now.Sub(then)
	switch {
	case d < 0:
		return "soon"
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/(24*365)))
	}
}

// pad truncates to width, marking that it did: a title cut without a mark
// reads as the title the channel chose.
func pad(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width <= 1 {
		return string(r[:width])
	}
	return string(r[:width-1]) + "…"
}
