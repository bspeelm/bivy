// Package follow holds the follow list and decides what is new.
//
// Pure functions over data: nothing here reads a file, makes a request or
// looks at a clock. This is where a program like this accumulates its
// strangest bugs — "why is that marked new", "why did the list reorder" — and
// pure code is where those are cheapest to pin down. State values are
// immutable, so a caller cannot half-apply a change and then fail.
package follow

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
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
	// Watched is when each video was watched to its end. A map so that asking
	// about one is a lookup rather than a scan of everything ever seen.
	Watched map[string]time.Time `json:"watched,omitempty"`
}

// WatchedCap bounds the history: unbounded, this is the one part of bivy that
// grows for the life of the install. The oldest entries go, so a video watched
// long ago can appear unwatched again — the right way round, because the
// alternative is a permanent record of everything its user ever watched.
const WatchedCap = 2000

// MarkWatched records that a video was watched to its end.
func MarkWatched(s State, videoID string, now time.Time) State {
	if !media.IsVideoID(videoID) {
		return s
	}

	watched := make(map[string]time.Time, len(s.Watched)+1)
	maps.Copy(watched, s.Watched)
	watched[videoID] = now.UTC()

	next := State{Channels: append([]Channel(nil), s.Channels...), Watched: watched}
	next.trimWatched()
	return next
}

// HasWatched reports whether a video has been watched to its end.
func (s State) HasWatched(videoID string) bool {
	_, ok := s.Watched[videoID]
	return ok
}

// trimWatched drops the oldest entries once the history is over its cap.
func (s *State) trimWatched() {
	if len(s.Watched) <= WatchedCap {
		return
	}

	// Oldest first, and by identifier where two share a timestamp, so which
	// entries survive does not depend on map ordering.
	ids := slices.Collect(maps.Keys(s.Watched))
	slices.SortFunc(ids, func(a, b string) int {
		if c := s.Watched[a].Compare(s.Watched[b]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	for _, id := range ids[:len(s.Watched)-WatchedCap] {
		delete(s.Watched, id)
	}
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

	next := State{Channels: append(append([]Channel(nil), s.Channels...), c), Watched: s.Watched}
	next.sort()
	return next, nil
}

// Remove unfollows a channel, reporting whether it was followed at all.
func (s State) Remove(id string) (State, bool) {
	next := State{Watched: s.Watched}
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
	// ChannelID is the channel this row is, or the one it came from: what the
	// follow key acts on, whichever kind of row it is.
	ChannelID string
	// New reports that this was published since the last visit.
	New bool
	// Watched reports that it was played to its end. It outranks New on
	// screen: something already watched is not news, whenever it arrived.
	Watched bool

	// Followers and Summary are what a channel search knows. They are unset
	// on a video row.
	Followers int
	Summary   string
	// Followed is set on a channel row that is already on the follow list.
	Followed bool
}

// IsChannel reports that this row is a channel rather than a video.
func (r Row) IsChannel() bool { return r.Video.ID == "" && r.ChannelID != "" }

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
			watched := s.HasWatched(v.ID)
			rows = append(rows, Row{
				Video:     v,
				Channel:   name,
				ChannelID: ch.ID,
				New:       !watched && v.Published.After(followed.LastVisit),
				Watched:   watched,
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

// Results turns search results into rows.
//
// Nothing is new here: "new" means "since you last looked at this channel",
// and a search is not a channel. Watched still applies, because whether you
// have seen something is true however you found it.
func Results(s State, videos []media.Video) []Row {
	rows := make([]Row, 0, len(videos))
	for _, v := range videos {
		rows = append(rows, Row{
			Video:     v,
			Channel:   v.Author,
			ChannelID: v.ChannelID,
			Watched:   s.HasWatched(v.ID),
		})
	}
	return rows
}

// ChannelResults turns a channel search into rows. One already followed is
// marked: the list is for deciding which to follow, and "you have this one" is
// the first thing worth knowing.
func ChannelResults(s State, channels []media.Channel) []Row {
	rows := make([]Row, 0, len(channels))
	for _, c := range channels {
		if !media.IsChannelID(c.ID) {
			continue
		}
		_, followed := s.Find(c.ID)
		rows = append(rows, Row{
			Channel:   c.Title,
			ChannelID: c.ID,
			Followers: c.Followers,
			Summary:   c.Description,
			Followed:  followed,
		})
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

	next := State{Channels: append([]Channel(nil), s.Channels...), Watched: s.Watched}
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

	next := State{Channels: append([]Channel(nil), s.Channels...), Watched: s.Watched}
	for i := range next.Channels {
		if t, ok := titles[next.Channels[i].ID]; ok {
			next.Channels[i].Title = t
		}
	}
	next.sort()
	return next
}
