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
)

// Move returns the row the cursor lands on, clamped to the list. Here rather
// than in the loop that reads keys, because "what does down do at the bottom"
// is answered by a test rather than by pressing it.
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
	// Failed names the channels whose feed could not be fetched. Reported
	// rather than omitted: a dashboard that is quietly short lies.
	Failed []string
	// Now anchors the relative timestamps, passed in rather than read so the
	// output is a function of its arguments.
	Now time.Time
	// Width is the terminal width to lay out for. Zero means the default.
	Width int
	// Height is how many rows the terminal has. Zero means show everything,
	// which is what the non-interactive listing does.
	Height int
	// Selected is the row under the cursor.
	Selected int
	// Status is a line shown under the list: what is playing, or what went
	// wrong with the last thing that was asked for.
	Status string
	// Interactive draws the cursor and the key hints. Without it the same
	// model renders as a plain listing, which is what a pipe gets.
	Interactive bool
}

const (
	defaultWidth = 80
	minWidth     = 40
	channelWidth = 20
	agoWidth     = 7

	// What the rows have to share the screen with: the headline, the blank
	// line under it, and the footer.
	chrome = 4
)

const keyHints = "  ↑↓ move · enter play · r refresh · q quit"

// Render returns the dashboard as text, newline-terminated, ready to print.
func Render(d Dashboard) string {
	var b strings.Builder

	width := d.Width
	if width < minWidth {
		width = defaultWidth
	}

	var newCount int
	for _, r := range d.Rows {
		// The same precedence the marker uses. A headline that counts a row
		// the list does not mark is a headline nobody can reconcile.
		if r.New && !r.Watched {
			newCount++
		}
	}

	b.WriteString(headline(len(d.Rows), newCount))
	b.WriteString("\n\n")

	if len(d.Rows) == 0 && len(d.Failed) == 0 {
		b.WriteString("  Nothing to show. Follow a channel:\n\n")
		b.WriteString("      bivy follow @handle\n")
		return b.String()
	}

	chanWidth, titleWidth := columns(width)
	first, last := window(d.Selected, len(d.Rows), d.Height)

	for i := first; i < last; i++ {
		r := d.Rows[i]

		cursor := " "
		if d.Interactive && i == d.Selected {
			cursor = ">"
		}
		if chanWidth == 0 {
			fmt.Fprintf(&b, "%s%s %-*s %s\n",
				cursor, marker(r), agoWidth, Ago(d.Now, r.Video.Published),
				pad(r.Video.Title, titleWidth))
			continue
		}
		fmt.Fprintf(&b, "%s%s %-*s %-*s %s\n",
			cursor, marker(r),
			agoWidth, Ago(d.Now, r.Video.Published),
			chanWidth, pad(r.Channel, chanWidth),
			pad(r.Video.Title, titleWidth),
		)
	}

	b.WriteString(footer(d, width, first, last))
	return b.String()
}

// columns divides the space the fixed parts leave. The channel column yields
// first and then disappears: the title is the thing being chosen between, and
// a channel name that has eaten it has made the dashboard useless in order to
// stay tidy.
func columns(width int) (channel, title int) {
	// One space, the three-cell marker, a space, the age, a space, then the
	// channel and title columns with a space between them.
	const fixed = 1 + 3 + 1 + agoWidth + 1

	available := width - fixed - 1
	channel = channelWidth
	if scaled := available / 3; scaled < channel {
		channel = scaled
	}
	title = available - channel

	// Below this the channel column is too narrow to name anything, so the
	// row gives the space to the title instead.
	if channel < 6 || title < 8 {
		return 0, width - fixed
	}
	return channel, title
}

// marker is the three cells before the age. Watched outranks new, and there is
// no colour in it: the one thing a dashboard must survive is being read on a
// terminal that has none.
func marker(r follow.Row) string {
	switch {
	case r.Watched:
		return " ✓ "
	case r.New:
		return " • "
	default:
		return "   "
	}
}

// window is the slice of rows that fits, kept around the cursor. The list
// moves under the cursor rather than jumping by a page: a row that was next to
// the cursor before a keypress should be next to it after one.
func window(selected, rows, height int) (first, last int) {
	visible := height - chrome
	if height <= 0 || visible >= rows {
		return 0, rows
	}
	if visible < 1 {
		visible = 1
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

func footer(d Dashboard, width, first, last int) string {
	var lines []string
	if first > 0 || last < len(d.Rows) {
		lines = append(lines, fmt.Sprintf("  showing %d-%d of %d", first+1, last, len(d.Rows)))
	}
	if len(d.Failed) > 0 {
		lines = append(lines, "  "+fmt.Sprintf("%s could not be reached: %s",
			plural(len(d.Failed), "channel", "channels"),
			strings.Join(d.Failed, ", ")))
	}
	if d.Status != "" {
		lines = append(lines, "  "+d.Status)
	}
	if d.Interactive {
		lines = append(lines, keyHints)
	}
	if len(lines) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n")
	for _, line := range lines {
		b.WriteString(pad(line, width))
		b.WriteString("\n")
	}
	return b.String()
}

func headline(rows, fresh int) string {
	switch {
	case rows == 0:
		return "bivy"
	case fresh == 0:
		return fmt.Sprintf("bivy · %s, nothing new since your last visit",
			plural(rows, "video", "videos"))
	default:
		return fmt.Sprintf("bivy · %s since your last visit", plural(fresh, "new video", "new videos"))
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Ago is a duration a person reads at a glance, in a fixed width. Coarse on
// purpose: "3 days ago" and "3 days and four hours ago" lead to the same
// decision, and the second costs a column the title wants.
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

// pad truncates to width, counting runes, and marks that it did: a title cut
// without a mark reads as the title the channel chose.
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
