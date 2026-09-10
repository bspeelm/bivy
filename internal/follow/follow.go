// Package follow holds the follow list and decides what is new.
//
// Pure functions over data: nothing here reads a file, makes a request or
// looks at a clock. This is where a program like this accumulates its
// strangest bugs — "why is that marked new", "why did the list reorder" — and
// pure code is where those are cheapest to pin down.
//
// State values are immutable. The functions that change one return a new
// value, so a caller cannot half-apply a change and then fail.
package follow

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/bspeelm/bivy/internal/media"
)

// Channel is one followed channel.
type Channel struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// FollowedAt is recorded rather than used: the backlog is already bounded
	// by how many entries a feed carries, and a second bound would hide rows
	// the user has just asked to see.
	FollowedAt time.Time `json:"followed_at"`
	// LastVisit is when the dashboard last showed this channel's entries.
	// Anything published after it is new.
	LastVisit time.Time `json:"last_visit"`
}

// State is everything bivy remembers, and the whole of what a stolen state
// file would give an attacker (ADR-001).
type State struct {
	Channels []Channel `json:"channels"`
}

// ErrAlreadyFollowed is returned rather than silently doing nothing, because
// "follow" reporting success on a channel already followed reads as a fresh
// subscription that was not made.
var ErrAlreadyFollowed = errors.New("already followed")

// Find returns the followed channel with this identifier.
func (s State) Find(id string) (Channel, bool) {
	for _, c := range s.Channels {
		if c.ID == id {
			return c, true
		}
	}
	return Channel{}, false
}

// Add follows a channel as of now.
func (s State) Add(c Channel, now time.Time) (State, error) {
	if !media.IsChannelID(c.ID) {
		return s, fmt.Errorf("%q is not a channel identifier", c.ID)
	}
	if _, found := s.Find(c.ID); found {
		return s, ErrAlreadyFollowed
	}
	c.FollowedAt = now.UTC()
	// The entries a feed already carries are backlog, not news: listed on the
	// first dashboard, none of them marked.
	c.LastVisit = now.UTC()

	next := State{Channels: append(append([]Channel(nil), s.Channels...), c)}
	next.sort()
	return next, nil
}

// Remove unfollows a channel, reporting whether it was followed at all.
func (s State) Remove(id string) (State, bool) {
	next := State{}
	for _, c := range s.Channels {
		if c.ID != id {
			next.Channels = append(next.Channels, c)
		}
	}
	return next, len(next.Channels) != len(s.Channels)
}

// sort keeps the list in a stable order so that the file on disk does not
// churn between saves and a diff of it means something.
func (s *State) sort() {
	sort.SliceStable(s.Channels, func(i, j int) bool {
		if s.Channels[i].Title != s.Channels[j].Title {
			return s.Channels[i].Title < s.Channels[j].Title
		}
		return s.Channels[i].ID < s.Channels[j].ID
	})
}

// Row is one line of the dashboard.
type Row struct {
	Video media.Video
	// Channel is the followed channel's title, which is the name the user
	// chose to see, not whatever a given entry claims its author is.
	Channel string
	// New reports that this was published since the last visit.
	New bool
}

// Dashboard is what to show on launch: what the followed channels' feeds
// currently carry, newest first, with anything published since that channel's
// last visit marked new.
//
// The follow list decides what appears, not what arrived: a fetch is allowed
// to return more than was asked for.
func Dashboard(s State, fetched []media.Channel, limit int) []Row {
	var rows []Row
	for _, ch := range fetched {
		followed, ok := s.Find(ch.ID)
		if !ok {
			continue
		}
		name := followed.Title
		if name == "" {
			name = ch.Title
		}
		for _, v := range ch.Videos {
			rows = append(rows, Row{
				Video:   v,
				Channel: name,
				New:     v.Published.After(followed.LastVisit),
			})
		}
	}

	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].Video, rows[j].Video
		if a.Published.Equal(b.Published) {
			return a.ID < b.ID
		}
		return a.Published.After(b.Published)
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// Visited records that the dashboard has been shown, so its entries are not
// new next time. Only channels that were actually fetched are advanced: a
// network blip must not silently consume what the user launched bivy to see.
func Visited(s State, fetched []media.Channel, now time.Time) State {
	seen := make(map[string]bool, len(fetched))
	for _, ch := range fetched {
		seen[ch.ID] = true
	}

	next := State{Channels: append([]Channel(nil), s.Channels...)}
	for i := range next.Channels {
		if seen[next.Channels[i].ID] {
			next.Channels[i].LastVisit = now.UTC()
		}
	}
	return next
}

// Retitle records the channel's own name, learned from its feed, for a channel
// that was followed by identifier before anything was known about it.
func Retitle(s State, fetched []media.Channel) State {
	titles := make(map[string]string, len(fetched))
	for _, ch := range fetched {
		if ch.Title != "" {
			titles[ch.ID] = ch.Title
		}
	}

	next := State{Channels: append([]Channel(nil), s.Channels...)}
	for i := range next.Channels {
		if t, ok := titles[next.Channels[i].ID]; ok {
			next.Channels[i].Title = t
		}
	}
	next.sort()
	return next
}
