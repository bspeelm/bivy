// Command bivy browses and plays online video from the terminal.
//
// Milestone 1: follow channels, and see what they have posted on launch.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bspeelm/bivy/internal/feed"
	"github.com/bspeelm/bivy/internal/follow"
	"github.com/bspeelm/bivy/internal/media"
	"github.com/bspeelm/bivy/internal/pushback"
	"github.com/bspeelm/bivy/internal/store"
	"github.com/bspeelm/bivy/internal/tui"
	"github.com/bspeelm/bivy/internal/ytdlp"
)

// Version is stamped at build time. A plain string literal, because -X does
// nothing to a variable initialised by a function call and says so silently.
var Version = "dev"

// stateFile is the whole of what bivy remembers.
const stateFile = "follows.json"

// dashboardRows is how far back the dashboard goes, and pageSize is how much
// more of anything arrives at a time. One screenful of scrolling, so that
// asking for more is a decision rather than a habit.
const (
	dashboardRows = 30
	pageSize      = 30
)

// fetcher is what the application needs from the network, named here rather
// than in the package that implements it, so the tests below run without a
// server. That is not only convenience: net/http may be imported by one
// package (§0), and a test helper reaching for httptest in this one would
// break the budget that makes the privacy claim checkable.
type fetcher interface {
	Fetch(ctx context.Context, channelID string) (media.Channel, error)
	Resolve(ctx context.Context, handle string) (string, error)
	Thumbnail(ctx context.Context, videoID string) ([]byte, error)
	WideThumbnail(ctx context.Context, videoID string) ([]byte, error)
}

type app struct {
	store  *store.Store
	feeds  fetcher
	now    func() time.Time
	out    io.Writer
	errOut io.Writer
	width  int

	// newPlayer starts mpv. A field for the same reason fetcher is an
	// interface: the loop that drives a player is worth testing, and mpv is
	// not something a test may require to be installed.
	newPlayer func(context.Context) (player, error)
	// search runs the extractor, which is likewise not something a test may
	// require to be installed.
	search searcher
	// art fetches and draws thumbnails, and is nil where the terminal cannot
	// draw. Nothing else in the program changes when it is.
	art artist

	// gap is the wait between extractor fallbacks, spread the jitter before a
	// feed fetch, settle the pause before a row's picture. Fields so that
	// tests need not sit through any of them.
	gap    time.Duration
	spread time.Duration
	settle time.Duration

	// gate is shut for the rest of the session once the service pushes back.
	// One gate for everything: a refusal to the extractor is a refusal to the
	// feeds as well (ADR-017).
	gate *pushback.Gate
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	s, err := store.Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "bivy:", err)
		os.Exit(1)
	}

	gate := &pushback.Gate{}
	feeds, search := feed.New(), ytdlp.New()
	feeds.Gate, search.Gate = gate, gate

	a := &app{
		store:     s,
		feeds:     feeds,
		search:    search,
		gate:      gate,
		gap:       fallbackGap,
		spread:    feedSpread,
		settle:    thumbnailSettle,
		now:       time.Now,
		out:       os.Stdout,
		errOut:    os.Stderr,
		newPlayer: startMPV,
	}
	os.Exit(a.run(ctx, os.Args[1:]))
}

const usage = `bivy — a terminal browser and player for online video

    bivy                    what the channels you follow have posted
    bivy list-only          the same, printed once, without the cursor
    bivy follow <channel>   follow a channel: @handle, a channel URL, or an id
    bivy unfollow <channel> stop following one
    bivy list               the channels you follow
    bivy version            what this is

Once bivy is open, most of it is keys rather than commands:

    ↑↓ or j k               move
    enter                   play the row in mpv
    /                       search
    f                       follow the channel this row came from
    s                       put the row on the playlist, or take it off
    m                       mark the row watched, or unmark it
    p                       play down the playlist from here, one after another
    M                       load thirty more
    esc                     back to the dashboard
    r                       refresh
    :q                      quit

Playing needs mpv, and searching needs yt-dlp. The dashboard needs neither.
bivy never logs in, holds no account, and downloads nothing.
`

func (a *app) run(ctx context.Context, args []string) int {
	var command, argument string
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}
	if len(args) > 0 {
		argument = args[0]
	}

	switch command {
	case "":
		return a.browse(ctx)
	case "follow":
		return a.follow(ctx, argument)
	case "unfollow":
		return a.unfollow(argument)
	case "list":
		return a.list()
	case "list-only":
		return a.dashboard(ctx)
	case "version":
		fmt.Fprintln(a.out, "bivy", Version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(a.out, usage)
		return 0
	default:
		fmt.Fprintf(a.errOut, "bivy: no command %q\n\n", command)
		fmt.Fprint(a.errOut, usage)
		return 2
	}
}

func (a *app) fail(err error) int {
	fmt.Fprintln(a.errOut, "bivy:", err)
	return 1
}

func (a *app) load() (follow.State, error) {
	var s follow.State
	err := a.store.ReadJSON(stateFile, &s)
	return s, err
}

// dashboard is what launching bivy does.
func (a *app) dashboard(ctx context.Context) int {
	state, err := a.load()
	if err != nil {
		return a.fail(err)
	}

	fetched, failed, _ := a.fetchAll(ctx, state)

	rows := follow.Dashboard(state, fetched, dashboardRows)
	fmt.Fprint(a.out, tui.Render(tui.Dashboard{
		Rows:   rows,
		Failed: failed,
		Status: a.gate.Says(),
		Now:    a.now(),
		Width:  a.width,
	}))

	if len(state.Channels) == 0 {
		return 0
	}

	// Two changes, one save: the visit is recorded and any channel whose name
	// was learned from its feed is retitled. Saving only when something
	// actually changed keeps a read-only launch read-only.
	next := follow.Visited(follow.Retitle(state, fetched), fetched, a.now())
	if len(fetched) == 0 {
		return 0
	}
	if err := a.store.WriteJSON(stateFile, next); err != nil {
		return a.fail(err)
	}
	return 0
}

// fallbackChannels caps how many channels one launch asks the extractor about.
// The rest are reported unreachable, which is what the dashboard already says
// about a channel it could not read.
const fallbackChannels = 3

// fallbackGap is the wait between those, with as much again in jitter, so the
// requests do not arrive as a burst on a fixed rhythm.
const fallbackGap = time.Second

// feedSpread is the most a feed fetch waits first: two at a time is already
// far from a burst, and the jitter keeps them off a fixed rhythm.
const feedSpread = 250 * time.Millisecond

// fetchAll asks every followed channel's feed, two at a time and spread out: a
// follow list is allowed to be long, and a burst of simultaneous requests from
// one address is the shape that gets it scored (ADR-017). Failures are
// collected rather than returned, so one unreachable channel does not cost the
// dashboard.
func (a *app) fetchAll(ctx context.Context, state follow.State) (fetched []media.Channel, failed []string, stale bool) {
	if len(state.Channels) == 0 {
		return nil, nil, false
	}

	results := make([]media.Channel, len(state.Channels))
	errs := make([]error, len(state.Channels))
	viaExtractor := make([]bool, len(state.Channels))

	const parallel = 2
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup

	for i, c := range state.Channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if a.spread > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(rand.N(a.spread)):
				}
			}
			results[i], errs[i] = a.feeds.Fetch(ctx, c.ID)
		}()
	}
	wg.Wait()

	a.fillFromExtractor(ctx, state, results, errs, viaExtractor)

	for i, c := range state.Channels {
		if errs[i] != nil {
			name := c.Title
			if name == "" {
				name = c.ID
			}
			failed = append(failed, name)
			continue
		}
		if viaExtractor[i] {
			stale = true
		}
		fetched = append(fetched, results[i])
	}
	sort.Strings(failed)
	return fetched, failed, stale
}

// fillFromExtractor asks the extractor about the channels whose feed would not
// answer: one at a time, with a pause between, and only the first few. An
// empty dashboard is worse than a stale one (ADR-013); a browser-page fetch
// per channel at every launch is what got an address blocked (ADR-017).
func (a *app) fillFromExtractor(ctx context.Context, state follow.State, results []media.Channel, errs []error, viaExtractor []bool) {
	if a.search == nil {
		return
	}

	asked := 0
	for i, c := range state.Channels {
		if errs[i] == nil {
			continue
		}
		if asked == fallbackChannels || a.gate.Shut() {
			return
		}
		if asked > 0 && a.gap > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(a.gap + rand.N(a.gap)):
			}
		}
		asked++

		if ch, err := a.search.Uploads(ctx, c.ID, dashboardRows); err == nil && len(ch.Videos) > 0 {
			results[i], errs[i], viaExtractor[i] = ch, nil, true
		}
	}
}

func (a *app) follow(ctx context.Context, argument string) int {
	if argument == "" {
		fmt.Fprint(a.errOut, "bivy: follow what? a @handle, a channel URL, or an id\n")
		return 2
	}

	id, handle, err := target(argument)
	if err != nil {
		return a.fail(err)
	}
	if id == "" {
		if id, err = a.feeds.Resolve(ctx, handle); err != nil {
			fmt.Fprintf(a.errOut, "bivy: could not work out which channel %s is: %v\n", handle, err)
			fmt.Fprintf(a.errOut, "      open the channel in a browser and pass the /channel/UC… URL instead\n")
			return 1
		}
	}

	state, err := a.load()
	if err != nil {
		return a.fail(err)
	}

	// Fetched before it is saved, so that a channel that does not exist is
	// refused now rather than appearing on the dashboard as a failure forever.
	ch, err := a.feeds.Fetch(ctx, id)
	if err != nil {
		return a.fail(fmt.Errorf("%s: %w", id, err))
	}

	next, err := state.Add(follow.Channel{ID: id, Title: ch.Title}, a.now())
	if errors.Is(err, follow.ErrAlreadyFollowed) {
		fmt.Fprintf(a.out, "already following %s\n", name(ch.Title, id))
		return 0
	}
	if err != nil {
		return a.fail(err)
	}
	if err := a.store.WriteJSON(stateFile, next); err != nil {
		return a.fail(err)
	}

	fmt.Fprintf(a.out, "following %s\n", name(ch.Title, id))
	return 0
}

func (a *app) unfollow(argument string) int {
	if argument == "" {
		fmt.Fprint(a.errOut, "bivy: unfollow what? see `bivy list`\n")
		return 2
	}

	state, err := a.load()
	if err != nil {
		return a.fail(err)
	}

	id, _, err := target(argument)
	if err != nil || id == "" {
		// Not an identifier, so match on the name shown by `bivy list`.
		// Unfollowing by the thing on the screen is what a person will try.
		if id = byTitle(state, argument); id == "" {
			fmt.Fprintf(a.errOut, "bivy: not following %q\n", argument)
			return 1
		}
	}

	channel, _ := state.Find(id)
	next, removed := state.Remove(id)
	if !removed {
		fmt.Fprintf(a.errOut, "bivy: not following %q\n", argument)
		return 1
	}
	if err := a.store.WriteJSON(stateFile, next); err != nil {
		return a.fail(err)
	}

	fmt.Fprintf(a.out, "unfollowed %s\n", name(channel.Title, id))
	return 0
}

func (a *app) list() int {
	state, err := a.load()
	if err != nil {
		return a.fail(err)
	}
	if len(state.Channels) == 0 {
		fmt.Fprint(a.out, "following nothing yet. try: bivy follow @handle\n")
		return 0
	}
	for _, c := range state.Channels {
		fmt.Fprintf(a.out, "%-24s  %s\n", c.ID, c.Title)
	}
	return 0
}

func byTitle(state follow.State, title string) string {
	for _, c := range state.Channels {
		if strings.EqualFold(c.Title, title) {
			return c.ID
		}
	}
	return ""
}

func name(title, id string) string {
	if title == "" {
		return id
	}
	return title
}

// target works out what the user meant by a channel, returning exactly one of
// id and handle. An unrecognised shape is refused here rather than passed
// along hopefully: this string ends up in a URL now and, in a later milestone,
// near an argv.
func target(s string) (id, handle string, err error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", "", errors.New("nothing to follow")
	case media.IsChannelID(s):
		return s, "", nil
	case strings.HasPrefix(s, "@"):
		return "", s, nil
	case strings.HasPrefix(s, "-"):
		// Refused before it can be mistaken for an option by anything
		// downstream (§7). Nothing bivy accepts begins with a dash.
		return "", "", fmt.Errorf("%q begins with a dash and is not a channel", s)
	}

	u, parseErr := url.Parse(s)
	if parseErr != nil || u.Host == "" {
		return "", "", fmt.Errorf("%q is not a channel, a handle, or a channel URL", s)
	}
	if v := u.Query().Get("channel_id"); media.IsChannelID(v) {
		return v, "", nil
	}
	for _, part := range strings.Split(u.Path, "/") {
		switch {
		case media.IsChannelID(part):
			return part, "", nil
		case strings.HasPrefix(part, "@") && len(part) > 1:
			return "", part, nil
		}
	}
	return "", "", fmt.Errorf("%s names no channel bivy can find", s)
}
