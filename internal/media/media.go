// Package media holds the things bivy shows: a video, and the channel it came
// from. Nothing here does I/O.
//
// It exists so that the packages which render and reason about videos never
// have to import the package that fetches them. A renderer that can reach a
// network client eventually does.
package media

import (
	"strings"
	"time"
	"unicode"
)

// Video is one entry from a channel's feed.
type Video struct {
	ID        string
	Title     string
	Author    string
	ChannelID string
	Published time.Time
	Thumbnail string
}

// URL is derived rather than stored: a URL that arrived from a feed is a URL a
// feed chose, and this one is built from an identifier that has been checked.
func (v Video) URL() string { return "https://www.youtube.com/watch?v=" + v.ID }

// Channel is a followed channel and the entries its feed currently carries.
type Channel struct {
	ID     string
	Title  string
	Videos []Video
}

// Text is remote text made safe to print.
//
// Control characters are removed at the boundary rather than left to whatever
// draws the screen. A renderer that discards sequences it does not recognise
// is mitigation by accident: it still honours the ones it does recognise, and
// a hyperlink escape in a title points wherever the feed wants it to.
//
// What is left of a sequence — the "[31m" after a stripped escape — stays, as
// inert text. Removing sequences whole would mean writing the parser this
// function exists so that nothing has to depend on.
//
// Runs of whitespace collapse to one space, because a title is one line.
func Text(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	space := false
	for _, r := range s {
		switch {
		// C0, DEL and the C1 range, before the whitespace case on purpose:
		// U+0085 is a control character unicode.IsSpace claims, and one that
		// becomes a space has been let through rather than removed.
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			continue
		case r == unicode.ReplacementChar:
			continue
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case !unicode.IsPrint(r):
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// IsChannelID reports whether s is shaped like a channel identifier: two
// leading letters and 22 characters of base64url.
//
// Checked rather than trusted because this string is interpolated into a URL
// and, in a later milestone, travels towards an argv.
func IsChannelID(s string) bool {
	const width = 24
	if len(s) != width || !strings.HasPrefix(s, "UC") {
		return false
	}
	for _, r := range s[2:] {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// IsVideoID reports whether s is shaped like a video identifier. Same reasoning
// as IsChannelID, and the same treatment.
func IsVideoID(s string) bool {
	const width = 11
	if len(s) != width {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
