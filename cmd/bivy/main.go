// Command bivy browses and plays online video from the terminal.
//
// Milestone 1: follow channels, and see what they have posted on launch.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/bspeelm/bivy/internal/store"
	"github.com/bspeelm/bivy/internal/tui"
)

// Version is stamped at build time. A plain string literal, because -X does
// nothing to a variable initialised by a function call and says so silently.
var Version = "dev"

// stateFile is the whole of what bivy remembers.
const stateFile = "follows.json"

// dashboardRows is how far back the dashboard goes. Two screens' worth: enough
// to scroll through what is new, short of being an archive to browse.
const dashboardRows = 30

// fetcher is what the application needs from the network, named here rather
// than in the package that implements it, so the tests below run without a
// server. That is not only convenience: net/http may be imported by one
// package (§0), and a test helper reaching for httptest in this one would
// break the budget that makes the privacy claim checkable.
type fetcher interface {
	Fetch(ctx context.Context, channelID string) (media.Channel, error)
	Resolve(ctx context.Context, handle string) (string, error)
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
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	s, err := store.Open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "bivy:", err)
		os.Exit(1)
	}

	a := &app{
		store:     s,
		feeds:     feed.New(),
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

bivy never logs in, reads no cookies, and downloads nothing.
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

	fetched, failed := a.fetchAll(ctx, state)

	rows := follow.Dashboard(state, fetched, dashboardRows)
	fmt.Fprint(a.out, tui.Render(tui.Dashboard{
		Rows:   rows,
		Failed: failed,
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

// fetchAll asks every followed channel's feed, at most four at a time: a
// follow list is allowed to be long, and forty simultaneous requests is a
// different program's network behaviour. Failures are collected rather than
// returned, so one unreachable channel does not cost the dashboard.
func (a *app) fetchAll(ctx context.Context, state follow.State) (fetched []media.Channel, failed []string) {
	if len(state.Channels) == 0 {
		return nil, nil
	}

	results := make([]media.Channel, len(state.Channels))
	errs := make([]error, len(state.Channels))

	const parallel = 4
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup

	for i, c := range state.Channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i], errs[i] = a.feeds.Fetch(ctx, c.ID)
		}()
	}
	wg.Wait()

	for i, c := range state.Channels {
		if errs[i] != nil {
			name := c.Title
			if name == "" {
				name = c.ID
			}
			failed = append(failed, name)
			continue
		}
		fetched = append(fetched, results[i])
	}
	sort.Strings(failed)
	return fetched, failed
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
