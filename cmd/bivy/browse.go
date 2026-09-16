package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/bspeelm/bivy/internal/follow"
	"github.com/bspeelm/bivy/internal/graphics"
	"github.com/bspeelm/bivy/internal/media"
	"github.com/bspeelm/bivy/internal/mpv"
	"github.com/bspeelm/bivy/internal/term"
	"github.com/bspeelm/bivy/internal/tui"
	"github.com/bspeelm/bivy/internal/ytdlp"
)

// queryLimit is what the search box will hold. The extractor refuses more, and
// a box that silently keeps taking characters it will then reject is worse
// than one that stops.
const queryLimit = 200

// player is what the browser needs from mpv, named here so the loop can be
// tested without one. A stand-in for the stand-in: internal/mpv proves the
// protocol against a process, and this proves the loop against a fake.
type player interface {
	Play(v media.Video) error
	Events() <-chan mpv.Event
	Close() error
}

// artist is what the browser needs to put a picture on screen. A terminal that
// cannot draw simply has none.
type artist interface {
	// Fetch returns a video's picture as the bytes a server sent.
	Fetch(ctx context.Context, videoID string) ([]byte, error)
	// Draw turns those bytes into what the terminal understands, for cells of
	// the size the terminal said they are.
	Draw(data []byte, cols, rows int, cell graphics.Cell) (string, error)
}

// pictures is the real artist: feed fetches, graphics draws.
type pictures struct{ feeds fetcher }

func (p pictures) Fetch(ctx context.Context, videoID string) ([]byte, error) {
	return p.feeds.Thumbnail(ctx, videoID)
}

func (p pictures) Draw(data []byte, cols, rows int, cell graphics.Cell) (string, error) {
	return graphics.Render(data, cols, rows, cell)
}

// screen is what the browser needs from a terminal.
type screen interface {
	Graphics() graphics.Capability
	Cell() graphics.Cell
	Size() (width, height int)
	Draw(frame string) error
	DrawArt(row int, art string) error
	Keys() <-chan term.Press
	Resized() <-chan struct{}
	Close() error
}

// searcher is what the browser needs from the extractor, named here so the
// loop is testable without one installed.
type searcher interface {
	Search(ctx context.Context, query string, limit int) ([]media.Video, error)
	Channels(ctx context.Context, query string, limit int) ([]media.Channel, error)
	Uploads(ctx context.Context, channelID string, limit int) (media.Channel, error)
}

// browser is one interactive session: a list, a cursor, and at most one mpv.
type browser struct {
	app    *app
	screen screen

	state   follow.State
	fetched []media.Channel
	// all is every row this screen has and rows is what is on it. They differ
	// only while watched videos are hidden.
	all         []follow.Row
	rows        []follow.Row
	hideWatched bool
	// queued means the list on screen is the saved queue rather than a feed,
	// a search or a channel.
	queued bool
	// through means a video ending should start the next one down.
	through bool
	failed  []string
	// stale means some channels came from the extractor because their feed
	// would not answer, so those rows carry no publish times.
	stale    bool
	selected int
	status   string

	// player is started on the first play and reused after that. Launching
	// mpv takes long enough to notice, and a second video should not pay for
	// it again.
	player player
	// playing is what was last handed to mpv, so that the end-file event can
	// be attributed to something. mpv reports that a file ended, not which.
	playing media.Video

	// art is the picture for the row under the cursor; pictures is what this
	// session has fetched. In memory and nowhere else — a thumbnail cache on
	// disk is a viewing history in image form (ADR-007).
	art       string
	pictures  map[string][]byte
	drawn     string
	drawnCols int
	drawnRows int

	// viewing is the channel whose videos are on screen, empty on every other
	// screen. query is what was searched for, empty on the dashboard;
	// channels means those results are channels.
	// line is what has been typed into the command line, and typing means it
	// has the keyboard.
	// busy means what is on screen is still arriving.
	busy bool
	// more is how to fetch the next page of whatever is on screen, and nil
	// where there is no next page. A closure because every screen loads more
	// of itself differently and the key that asks does not care which.
	more func(ctx context.Context, from int) ([]follow.Row, error)

	// back is what escape returns to, one entry per screen gone into. A stack
	// rather than a slot: a channel opened from a search that came from the
	// dashboard is three screens deep, and escape means the one before this.
	back []view

	viewing  string
	query    string
	channels bool
	line     string
	typing   bool
}

// browse runs the dashboard until the user quits.
func (a *app) browse(ctx context.Context) int {
	sc, err := term.Open(os.Stdin, os.Stdout)
	if errors.Is(err, term.ErrNotATerminal) {
		// Being run from a pipe is normal. The same model renders as a plain
		// listing, which is what the non-interactive dashboard already prints.
		return a.dashboard(ctx)
	}
	if err != nil {
		return a.fail(err)
	}

	if sc.Graphics() == graphics.Kitty {
		a.art = pictures{feeds: a.feeds}
	}

	b := &browser{app: a, screen: sc}
	// Deferred rather than left to the end of the function: every path out of
	// the loop below, including a panic, has to put the terminal back.
	defer b.close()

	return b.run(ctx)
}

func (b *browser) close() {
	if b.player != nil {
		_ = b.player.Close()
	}
	_ = b.screen.Close()
}

func (b *browser) run(ctx context.Context) int {
	if err := b.load(ctx); err != nil {
		b.close()
		return b.app.fail(err)
	}
	if err := b.draw(); err != nil {
		b.close()
		return b.app.fail(err)
	}

	var events <-chan mpv.Event
	for {
		if b.player != nil {
			events = b.player.Events()
		}

		select {
		case <-ctx.Done():
			return 0

		case press, open := <-b.screen.Keys():
			if !open {
				return 0
			}
			if b.handle(ctx, press) {
				return 0
			}
			// Everything already waiting is handled first: drawing one frame
			// per press is what made the cursor travel on after key-up.
			for more := true; more; {
				select {
				case next, open := <-b.screen.Keys():
					if !open {
						return 0
					}
					if b.handle(ctx, next) {
						return 0
					}
				default:
					more = false
				}
			}

		case e, open := <-events:
			if !open {
				// mpv is gone. Closed rather than dropped, because the socket
				// directory is bivy's to remove and this mpv never asked to go.
				_ = b.player.Close()
				b.player, events = nil, nil
				continue
			}
			b.report(ctx, e)

		case <-b.screen.Resized():
		}

		if err := b.draw(); err != nil {
			return b.app.fail(err)
		}
	}
}

// handle applies one keypress, reporting whether the session is over. What a
// character means depends on which screen is in front of the user: the
// terminal reports "j" and this decides whether that is "down" or a letter.
func (b *browser) handle(ctx context.Context, press term.Press) (done bool) {
	if b.typing {
		return b.handleTyping(ctx, press)
	}

	switch press.Key {
	case term.KeyInterrupt:
		return true
	case term.KeyDown:
		b.selected = tui.Move(b.selected, 1, len(b.rows))
	case term.KeyUp:
		b.selected = tui.Move(b.selected, -1, len(b.rows))
	case term.KeyHome:
		b.selected = tui.Move(b.selected, -len(b.rows), len(b.rows))
	case term.KeyEnd:
		b.selected = tui.Move(b.selected, len(b.rows), len(b.rows))
	case term.KeyEnter:
		b.enter(ctx)
	case term.KeyEscape:
		b.leaveResults()
	case term.KeyRune:
		return b.handleRune(ctx, press.Rune)
	}
	return false
}

// handleRune is the list's keys.
//
// Quitting is not among them. It is one keystroke away from every other key on
// this list, and the cost of hitting it by accident is losing the screen you
// were reading — so it costs three keystrokes and lives on the command line
// with the other rare things (ADR-011).
func (b *browser) handleRune(ctx context.Context, r rune) (done bool) {
	switch r {
	case 'j':
		b.selected = tui.Move(b.selected, 1, len(b.rows))
	case 'k':
		b.selected = tui.Move(b.selected, -1, len(b.rows))
	case 'g':
		b.selected = tui.Move(b.selected, -len(b.rows), len(b.rows))
	case 'G':
		b.selected = tui.Move(b.selected, len(b.rows), len(b.rows))
	case ' ':
		b.enter(ctx)
	case 'f':
		b.followRow(ctx)
	case 's':
		b.saveRow()
	case 'p':
		b.playThrough(ctx)
	case 'm':
		b.markRow()
	case 'M':
		b.loadMore(ctx)
	case '/':
		// The one command common enough to deserve a key, opened with its
		// name already in the line so the same surface handles both.
		b.typing, b.line, b.status = true, "search ", ""
	case ':':
		b.typing, b.line, b.status = true, "", ""
	case 'r':
		if b.query != "" {
			// Refreshing a result set means asking the same question again,
			// not throwing the results away for a dashboard.
			b.runSearch(ctx, b.query)
			return false
		}
		b.refresh(ctx)
	}
	return false
}

// handleTyping is the command line, where every printable character is a
// character — including the ones that are commands on the list behind it.
func (b *browser) handleTyping(ctx context.Context, press term.Press) (done bool) {
	switch press.Key {
	case term.KeyInterrupt:
		return true
	case term.KeyEscape:
		b.typing, b.line = false, ""
	case term.KeyTab:
		b.line = tui.Complete(b.line)
	case term.KeyBackspace:
		if r := []rune(b.line); len(r) > 0 {
			b.line = string(r[:len(r)-1])
		}
	case term.KeyEnter:
		b.typing = false
		intent, err := tui.Parse(b.line)
		b.line = ""
		if err != nil {
			b.status = err.Error()
			return false
		}
		return b.act(ctx, intent)
	case term.KeyRune:
		if len(b.line) < queryLimit {
			b.line += string(press.Rune)
		}
	}
	return false
}

// act carries out what the command line asked for.
//
// The parsing is in internal/tui and the doing is here, which is what keeps
// that package renderable in a test: it decides what was meant, and never how
// to bring it about.
func (b *browser) act(ctx context.Context, intent tui.Intent) (done bool) {
	switch v := intent.(type) {
	case nil:
		return false
	case tui.Quit:
		return true
	case tui.ShowHelp:
		b.status = "keys: ↑↓ move · enter play · s save · m watched · / search · :q quit"
	case tui.Refresh:
		b.refresh(ctx)
	case tui.HideWatched:
		b.toggleWatched()
	case tui.ShowQueue:
		b.showQueue()
	case tui.Search:
		b.runSearch(ctx, v.Query)
	case tui.Channels:
		b.runChannelSearch(ctx, v.Query)
	case tui.Open:
		b.openNamed(ctx, v.Target)
	case tui.Follow:
		b.follow(ctx, v.Target)
	case tui.Unfollow:
		b.unfollow(v.Target)
	}
	return false
}

// targetIn works out which channel a command means: everything the command
// line does — a handle, a URL, an identifier — and also a channel name that is
// on screen, because after a search that is what the user is reading.
func targetIn(rows []follow.Row, want string) (id, handle string, err error) {
	if id, handle, err = target(want); err == nil {
		return id, handle, nil
	}
	for _, r := range rows {
		// The row's own identifier, not the video's: a channel row has no
		// video, and a video row carries the channel it came from either way.
		if strings.EqualFold(r.Channel, want) && media.IsChannelID(r.ChannelID) {
			return r.ChannelID, "", nil
		}
	}
	return "", "", fmt.Errorf("no channel called %q here", want)
}

// follow adds a channel from inside the browser.
//
// The target may be a name on screen rather than an identifier, because after
// a search the channel is a column the user is looking at and its identifier
// is not.
func (b *browser) follow(ctx context.Context, target string) {
	id, handle, err := targetIn(b.rows, target)
	if err != nil {
		b.status = err.Error()
		return
	}

	b.status = "following…"
	_ = b.draw()

	if id == "" {
		if id, err = b.app.feeds.Resolve(ctx, handle); err != nil {
			b.status = fmt.Sprintf("could not work out which channel %s is", handle)
			return
		}
	}

	// A row already on screen has been named by the thing that produced it, so
	// there is nothing left to ask a feed. Asking anyway puts every follow
	// behind an endpoint that intermittently refuses, for a name bivy is
	// holding.
	if title := titleIn(b.rows, id); title != "" {
		b.add(ctx, id, title)
		return
	}

	ch, err := b.app.feeds.Fetch(ctx, id)
	if err != nil {
		b.status = "could not reach that channel: " + err.Error()
		return
	}
	b.add(ctx, id, ch.Title)
}

// add puts a channel on the follow list and saves it.
func (b *browser) add(ctx context.Context, id, title string) {
	next, err := b.state.Add(follow.Channel{ID: id, Title: title}, b.app.now())
	if errors.Is(err, follow.ErrAlreadyFollowed) {
		b.status = "already following " + name(title, id)
		return
	}
	if err != nil {
		b.status = err.Error()
		return
	}
	if err := b.app.store.WriteJSON(stateFile, next); err != nil {
		b.status = err.Error()
		return
	}

	b.state = next
	b.status = "following " + name(title, id)

	if b.channels {
		// Stay on the list. Following one channel out of a search is rarely
		// the last thing anyone does with that search.
		for i := range b.all {
			if b.all[i].ChannelID == id {
				b.all[i].Followed = true
			}
		}
		b.reshow()
		return
	}
	b.reload(ctx)
}

// titleIn is the name a row on screen already carries for a channel.
func titleIn(rows []follow.Row, id string) string {
	for _, r := range rows {
		if r.ChannelID == id && r.Channel != "" {
			return r.Channel
		}
	}
	return ""
}

func (b *browser) unfollow(target string) {
	id, _, err := targetIn(b.rows, target)
	if err != nil || id == "" {
		if id = byTitle(b.state, target); id == "" {
			b.status = "not following " + target
			return
		}
	}

	channel, _ := b.state.Find(id)
	next, removed := b.state.Remove(id)
	if !removed {
		b.status = "not following " + target
		return
	}
	if err := b.app.store.WriteJSON(stateFile, next); err != nil {
		b.status = err.Error()
		return
	}

	b.state = next
	b.status = "unfollowed " + name(channel.Title, id)
	b.show(follow.Dashboard(b.state, b.fetched, dashboardRows))
	b.selected = tui.Move(b.selected, 0, len(b.rows))
}

// refresh fetches the followed feeds again.
func (b *browser) refresh(ctx context.Context) {
	b.busy, b.status = true, ""
	_ = b.draw()
	err := b.load(ctx)
	b.busy = false
	if err != nil {
		b.status = err.Error()
	}
}

// reload rebuilds the dashboard from what is already fetched, plus whatever a
// newly followed channel brings, without asking for every feed again.
func (b *browser) reload(ctx context.Context) {
	fetched, failed, stale := b.app.fetchAll(ctx, b.state)
	if len(fetched) > 0 {
		b.fetched, b.failed, b.stale = fetched, failed, stale
	}
	b.query, b.channels, b.viewing, b.queued, b.back = "", false, "", false, nil
	b.show(follow.Dashboard(b.state, b.fetched, dashboardRows))
	b.selected = tui.Move(b.selected, 0, len(b.rows))
}

// followRow follows the channel the row under the cursor belongs to. A key
// rather than a command because it takes no argument and is not rare
// (ADR-011), and a video row answers it the same way a channel row does.
func (b *browser) followRow(ctx context.Context) {
	if b.selected >= len(b.rows) {
		return
	}
	r := b.rows[b.selected]
	if r.ChannelID == "" {
		b.status = "that row does not say which channel it is from"
		return
	}
	b.follow(ctx, r.ChannelID)
}

// runChannelSearch replaces the rows with channels.
func (b *browser) runChannelSearch(ctx context.Context, query string) {
	b.push()
	b.query, b.channels, b.viewing, b.queued = query, true, "", false
	b.show(nil)
	b.selected, b.busy = 0, true
	b.status = ""
	_ = b.draw()

	found, err := b.app.search.Channels(ctx, query, dashboardRows)
	b.busy = false
	if err != nil {
		b.status = searchTrouble(err)
		return
	}

	b.show(follow.ChannelResults(b.state, found))
	b.more = func(ctx context.Context, from int) ([]follow.Row, error) {
		next, err := b.app.search.Channels(ctx, query, from+pageSize)
		if err != nil {
			return nil, err
		}
		return follow.ChannelResults(b.state, next), nil
	}
}

// view is one screen, kept so that escape can put it back.
type view struct {
	all      []follow.Row
	query    string
	channels bool
	queued   bool
	viewing  string
	selected int
}

// push remembers the screen being left.
func (b *browser) push() {
	b.back = append(b.back, view{
		all:      b.all,
		query:    b.query,
		channels: b.channels,
		queued:   b.queued,
		viewing:  b.viewing,
		selected: b.selected,
	})
}

// pop puts the previous screen back, reporting whether there was one.
func (b *browser) pop() bool {
	if len(b.back) == 0 {
		return false
	}
	last := b.back[len(b.back)-1]
	b.back = b.back[:len(b.back)-1]

	b.query, b.channels, b.viewing, b.queued = last.query, last.channels, last.viewing, last.queued
	b.show(last.all)
	b.selected = tui.Move(last.selected, 0, len(b.rows))
	b.status = ""
	return true
}

// openNamed opens a channel the command line named, which may be a name on
// screen, a handle, an identifier or a URL.
func (b *browser) openNamed(ctx context.Context, target string) {
	id, handle, err := targetIn(b.rows, target)
	if err != nil {
		// Not on screen. A channel already followed can be opened by name
		// whatever the screen is showing — including when a feed outage has
		// left it showing nothing.
		if id = byTitle(b.state, target); id == "" {
			b.status = err.Error()
			return
		}
		err = nil
	}
	if id == "" {
		b.status = "resolving " + handle + "…"
		_ = b.draw()
		if id, err = b.app.feeds.Resolve(ctx, handle); err != nil {
			b.status = fmt.Sprintf("could not work out which channel %s is", handle)
			return
		}
	}

	title := titleIn(b.rows, id)
	if title == "" {
		if c, found := b.state.Find(id); found {
			title = c.Title
		}
	}
	if title == "" {
		title = id
	}
	b.open(ctx, id, title)
}

// open shows a channel's videos.
//
// The feed first, because it carries publish times, and the extractor when it
// will not answer — the same order the dashboard uses, for the same reason
// (ADR-003, ADR-013).
func (b *browser) open(ctx context.Context, id, title string) {
	if !media.IsChannelID(id) {
		b.status = "that row does not say which channel it is from"
		return
	}

	b.push()
	b.show(nil)
	b.selected, b.query, b.channels = 0, "", false
	b.viewing, b.busy, b.status = title, true, ""
	_ = b.draw()

	ch, err := b.app.feeds.Fetch(ctx, id)
	if err != nil && b.app.search != nil {
		ch, err = b.app.search.Uploads(ctx, id, dashboardRows)
	}
	b.busy = false
	if err != nil {
		b.pop()
		b.status = "could not open " + title + ": " + err.Error()
		return
	}
	if ch.Title != "" {
		b.viewing = ch.Title
	}

	b.status = ""
	b.show(b.channelRows(id, ch.Videos))
	b.more = func(ctx context.Context, from int) ([]follow.Row, error) {
		// A feed carries what it carries; only the extractor pages.
		if b.app.search == nil {
			return nil, errors.New("that needs yt-dlp")
		}
		next, err := b.app.search.Uploads(ctx, id, from+pageSize)
		if err != nil {
			return nil, err
		}
		return b.channelRows(id, next.Videos), nil
	}
}

// channelRows is a channel's videos as rows. The channel is the screen, so
// every row repeating its name is noise.
func (b *browser) channelRows(id string, videos []media.Video) []follow.Row {
	rows := follow.Results(b.state, videos)
	for i := range rows {
		rows[i].Channel = ""
		rows[i].ChannelID = id
	}
	return rows
}

// runSearch replaces the rows with results, keeping the screen to come back
// to.
func (b *browser) runSearch(ctx context.Context, query string) {
	b.push()
	b.query, b.channels, b.viewing, b.queued = query, false, "", false
	b.show(nil)
	b.selected, b.busy = 0, true
	b.status = ""
	_ = b.draw()

	results, err := b.app.search.Search(ctx, query, dashboardRows)
	b.busy = false
	if err != nil {
		b.status = searchTrouble(err)
		return
	}

	b.show(follow.Results(b.state, results))
	b.more = func(ctx context.Context, from int) ([]follow.Row, error) {
		found, err := b.app.search.Search(ctx, query, from+pageSize)
		if err != nil {
			return nil, err
		}
		return follow.Results(b.state, found), nil
	}
}

// leaveResults goes back one screen.
func (b *browser) leaveResults() { b.pop() }

// searchTrouble says what to do about a search that did not happen.
func searchTrouble(err error) string {
	if errors.Is(err, ytdlp.ErrNotInstalled) {
		return "yt-dlp is not installed, and search needs it — the dashboard does not"
	}
	return "search failed: " + err.Error()
}

// loadMore adds the next page to what is on screen.
//
// A page at a time rather than everything, because everything is a request for
// thirty more rows nobody has scrolled to yet — and the cursor stays where it
// was, so the rows that arrive are below where the reader already is.
func (b *browser) loadMore(ctx context.Context) {
	if b.more == nil {
		b.status = "nothing more to load here"
		return
	}

	was := len(b.all)
	b.busy, b.status = true, ""
	_ = b.draw()

	rows, err := b.more(ctx, was)
	b.busy = false
	if err != nil {
		b.status = "could not load more: " + err.Error()
		return
	}

	added := 0
	for _, r := range rows {
		if !b.showing(r) {
			b.all = append(b.all, r)
			added++
		}
	}
	if added == 0 {
		b.more = nil
		b.status = "that is all of it"
		return
	}
	b.reshow()
	b.status = fmt.Sprintf("%d more", added)
}

// showing reports whether a row is already on screen, so a page that overlaps
// the one before it does not double anything up.
func (b *browser) showing(r follow.Row) bool {
	for _, have := range b.all {
		if r.IsChannel() {
			if have.ChannelID == r.ChannelID {
				return true
			}
			continue
		}
		if have.Video.ID == r.Video.ID {
			return true
		}
	}
	return false
}

// enter is what the row under the cursor is for: a channel opens, a video
// plays. One key, because "the obvious thing" is not two different keys.
func (b *browser) enter(ctx context.Context) {
	if b.selected >= len(b.rows) {
		return
	}
	if r := b.rows[b.selected]; r.IsChannel() {
		b.open(ctx, r.ChannelID, r.Channel)
		return
	}
	b.play(ctx)
}

// play hands the selected row to mpv, starting one if there is not one yet.
func (b *browser) play(ctx context.Context) {
	if b.selected >= len(b.rows) {
		return
	}
	video := b.rows[b.selected].Video

	if b.player == nil {
		b.status = "starting mpv…"
		_ = b.draw()

		p, err := b.app.newPlayer(ctx)
		if err != nil {
			b.status = playerTrouble(err)
			return
		}
		b.player = p
	}

	if err := b.player.Play(video); err != nil {
		b.status = "mpv would not play that: " + err.Error()
		return
	}
	b.playing = video
	b.status = "playing · " + video.Title
}

// report turns an mpv event into what the screen says, and marks a video
// watched when it actually reached its end.
//
// Watched on end rather than on start: a video opened and abandoned after ten
// seconds has not been watched, and a program that says otherwise is keeping a
// record of something that did not happen.
func (b *browser) report(ctx context.Context, e mpv.Event) {
	if e.Name != "end-file" {
		return
	}

	// A refusal is always worth saying. A playback that merely stopped is
	// worth saying only when mpv complained: the user closing the window and
	// the player dying arrive as the same event with the same reason, and the
	// detail alongside is the only thing that tells them apart.
	// Anything but a video reaching its own end stops a run through the list.
	// Closing the window is how you get out of one.
	if !e.Finished() {
		b.through = false
	}
	if e.Failed() || (!e.Finished() && e.Detail != "") {
		b.status = playbackTrouble(e)
		return
	}
	if !e.Finished() {
		b.status = ""
		return
	}

	b.status = "watched · " + b.playing.Title
	b.state = follow.MarkWatched(b.state, b.playing.ID, b.app.now())
	// Watching something to its end takes it out of the queue wherever it was
	// played from, because the queue is what is left to watch.
	b.state = follow.Unsave(b.state, b.playing.ID)
	if err := b.app.store.WriteJSON(stateFile, b.state); err != nil {
		b.status = "could not record that as watched: " + err.Error()
	}
	// Only the dashboard is rebuilt; a tick is not worth the screen it was
	// read on. Every other list keeps its rows and marks the one that played.
	switch {
	case b.queued:
		// The row that just finished has left, so the cursor is already on
		// the one after it.
		b.show(follow.QueueRows(b.state))
		b.selected = tui.Move(b.selected, 0, len(b.rows))
	case b.query == "" && b.viewing == "":
		b.show(follow.Dashboard(b.state, b.fetched, dashboardRows))
	default:
		for i := range b.all {
			if b.all[i].Video.ID == b.playing.ID {
				b.all[i].Watched = true
			}
		}
		b.reshow()
	}
	b.playing = media.Video{}

	if b.through {
		b.playNext(ctx)
	}
}

// playNext carries a run through the list on to the row after the one that
// finished, and stops when there is nothing left rather than wrapping.
func (b *browser) playNext(ctx context.Context) {
	if !b.queued {
		b.selected = tui.Move(b.selected, 1, len(b.rows))
	}
	if b.selected >= len(b.rows) || b.rows[b.selected].IsChannel() {
		b.through = false
		b.status = "that was the last one"
		return
	}
	b.enter(ctx)
}

// showQueue lists what has been saved for later.
func (b *browser) showQueue() {
	b.push()
	b.query, b.channels, b.viewing, b.queued = "", false, "", true
	b.show(follow.QueueRows(b.state))
	b.selected, b.more, b.status = 0, nil, ""
	if len(b.rows) == 0 {
		b.status = "nothing saved yet — s saves the row under the cursor"
	}
}

// saveRow puts the row under the cursor in the queue, or takes it out again.
func (b *browser) saveRow() {
	if b.selected >= len(b.rows) {
		return
	}
	r := b.rows[b.selected]
	if r.IsChannel() {
		b.status = "channels are followed, not saved — f follows this one"
		return
	}

	saved := !b.state.IsQueued(r.Video.ID)
	if saved {
		b.state = follow.Save(b.state, r.Video, b.app.now())
	} else {
		b.state = follow.Unsave(b.state, r.Video.ID)
	}
	if err := b.app.store.WriteJSON(stateFile, b.state); err != nil {
		b.status = "could not record that: " + err.Error()
		return
	}

	if b.queued {
		b.show(follow.QueueRows(b.state))
		b.selected = tui.Move(b.selected, 0, len(b.rows))
	}
	if saved {
		b.status = "saved for later · " + r.Video.Title
		return
	}
	b.status = "no longer saved · " + r.Video.Title
}

// playThrough starts at the cursor and keeps going. Anything but a video
// reaching its own end stops it, so closing a window is how you get out
// rather than something to fight.
func (b *browser) playThrough(ctx context.Context) {
	if b.selected >= len(b.rows) {
		return
	}
	b.through = true
	b.enter(ctx)
}

// markRow toggles the tick on the row under the cursor.
//
// By hand, because bivy's own mark means "played to the end" and that is not
// the only way to be done with something: a video watched elsewhere, or one
// abandoned two minutes in on purpose, are both finished as far as the list is
// concerned.
func (b *browser) markRow() {
	if b.selected >= len(b.rows) {
		return
	}
	r := b.rows[b.selected]
	if r.IsChannel() {
		b.status = "channels are followed, not watched — f follows this one"
		return
	}

	watched := !r.Watched
	if watched {
		b.state = follow.MarkWatched(b.state, r.Video.ID, b.app.now())
		// In the queue, being done with something is what takes it out: the
		// queue is what is still to watch, so a ticked row sitting in it is a
		// row asking to be removed twice.
		if b.queued {
			b.state = follow.Unsave(b.state, r.Video.ID)
		}
	} else {
		b.state = follow.Unwatch(b.state, r.Video.ID)
	}
	if b.queued {
		b.show(follow.QueueRows(b.state))
		b.selected = tui.Move(b.selected, 0, len(b.rows))
	} else {
		for i := range b.all {
			if b.all[i].Video.ID == r.Video.ID {
				b.all[i].Watched = watched
			}
		}
		b.reshow()
	}

	if err := b.app.store.WriteJSON(stateFile, b.state); err != nil {
		b.status = "could not record that: " + err.Error()
		return
	}
	if watched {
		b.status = "marked watched · " + r.Video.Title
		return
	}
	b.status = "no longer watched · " + r.Video.Title
}

// toggleWatched hides what has been watched, or brings it back. For this
// session only: a list that came back filtered on the next launch, with
// nothing on screen saying why, is a bug report about missing videos.
func (b *browser) toggleWatched() {
	b.hideWatched = !b.hideWatched
	b.reshow()

	if !b.hideWatched {
		b.status = "showing every video"
		return
	}
	if hidden := len(b.all) - len(b.rows); hidden > 0 {
		b.status = fmt.Sprintf("hiding %d watched · :watched shows them again", hidden)
		return
	}
	b.status = "hiding watched videos · nothing here is watched yet"
}

// show puts rows on screen. Every screen's rows arrive through here, so the
// filter cannot be on for one list and off for the next.
func (b *browser) show(rows []follow.Row) {
	b.all = rows
	b.rows = visible(rows, b.hideWatched)
}

// reshow rebuilds the list from rows the screen already had, after the filter
// or a row's own state changed under it.
func (b *browser) reshow() {
	b.rows = visible(b.all, b.hideWatched)
	b.selected = tui.Move(b.selected, 0, len(b.rows))
}

// visible drops watched videos when they are hidden. Channels keep their
// place: their tick means followed, which is not something to hide.
func visible(rows []follow.Row, hide bool) []follow.Row {
	if !hide {
		return rows
	}
	out := make([]follow.Row, 0, len(rows))
	for _, r := range rows {
		if r.Watched && !r.IsChannel() {
			continue
		}
		out = append(out, r)
	}
	return out
}

// load fetches every followed channel and rebuilds the list.
func (b *browser) load(ctx context.Context) error {
	state, err := b.app.load()
	if err != nil {
		return err
	}

	fetched, failed, stale := b.app.fetchAll(ctx, state)
	if len(fetched) > 0 {
		state = follow.Visited(follow.Retitle(state, fetched), fetched, b.app.now())
		if err := b.app.store.WriteJSON(stateFile, state); err != nil {
			return err
		}
	}

	b.state, b.fetched, b.failed, b.stale = state, fetched, failed, stale
	b.back = nil
	b.query, b.channels, b.viewing, b.queued = "", false, "", false
	b.show(follow.Dashboard(state, fetched, dashboardRows))
	b.selected = tui.Move(b.selected, 0, len(b.rows))
	// The dashboard shows everything the feeds carried, so there is no next
	// page of it to ask for.
	b.more = nil
	return nil
}

// failedNow reports unreachable channels only where they are relevant. A
// search result set is not short because a feed was down.
func (b *browser) failedNow() []string {
	if b.query != "" {
		return nil
	}
	return b.failed
}

// picture is the drawn thumbnail for the row under the cursor: what is worth
// a request is what somebody is looking at.
func (b *browser) picture(ctx context.Context, cols, rows int, cell graphics.Cell) string {
	if b.app.art == nil || cols < 1 || rows < 1 || b.selected >= len(b.rows) {
		return ""
	}
	id := b.rows[b.selected].Video.ID
	if id == "" {
		return ""
	}
	// Redrawn when the window changes shape: a picture drawn for a box that
	// no longer exists is the wrong size in the one that does.
	if id == b.drawn && cols == b.drawnCols && rows == b.drawnRows {
		return b.art
	}
	b.drawnCols, b.drawnRows = cols, rows

	data, held := b.pictures[id]
	if !held {
		var err error
		if data, err = b.app.art.Fetch(ctx, id); err != nil {
			// Not worth a word on screen: the row says what the video is.
			b.drawn, b.art = id, ""
			return ""
		}
		b.remember(id, data)
	}

	drawn, err := b.app.art.Draw(data, cols, rows, cell)
	if err != nil {
		drawn = ""
	}
	b.drawn, b.art = id, drawn
	return drawn
}

// picturesHeld bounds what a session keeps: "small and short" is not a
// limit.
const picturesHeld = 60

// remember keeps a picture for the session, forgetting an arbitrary one once
// there are too many — arbitrary because each is equally cheap to fetch
// again.
func (b *browser) remember(id string, data []byte) {
	if b.pictures == nil {
		// So a browser never handed one is short of a cache, not of a map.
		b.pictures = map[string][]byte{}
	}
	for len(b.pictures) >= picturesHeld {
		for old := range b.pictures {
			delete(b.pictures, old)
			break
		}
	}
	b.pictures[id] = data
}

func (b *browser) draw() error {
	width, height := b.screen.Size()
	cell := b.screen.Cell()
	cols, rows := tui.ArtBox(width, height, cell)
	model := tui.Dashboard{
		Art:         b.picture(context.Background(), cols, rows, cell),
		Queue:       b.queued,
		Cell:        cell,
		Rows:        b.rows,
		Failed:      b.failedNow(),
		Reached:     len(b.fetched),
		Stale:       b.stale && b.query == "",
		Now:         b.app.now(),
		Width:       width,
		Height:      height,
		Selected:    b.selected,
		Status:      b.status,
		Query:       b.query,
		Channels:    b.channels,
		Viewing:     b.viewing,
		Busy:        b.busy,
		Line:        b.line,
		Typing:      b.typing,
		Interactive: true,
	}

	if err := b.screen.Draw(tui.Render(model)); err != nil {
		return err
	}
	// After the text, and only when it changed. Placed by row rather than
	// written into the frame, so it cannot shift where a line of text lands.
	return b.screen.DrawArt(model.ArtRow(), model.Art)
}

// playbackTrouble explains a video that would not play.
//
// The failure that actually happens is the extractor being refused a stream —
// bivy holds no account (ADR-004), and that is exactly the request a bot check
// declines. Saying nothing, which is what bivy did before
// this existed, looks like the keypress was ignored.
func playbackTrouble(e mpv.Event) string {
	if e.Detail == "" {
		return "could not play that — mpv could not open it"
	}
	return "could not play that — " + tidy(e.Detail)
}

// tidy drops what the extractor writes for its own readers: the identifier of
// the video it failed on, which is the row under the cursor, and the links to
// its own documentation.
func tidy(detail string) string {
	if i := strings.Index(detail, " See "); i > 0 {
		detail = detail[:i]
	}
	// mpv passes the extractor's line through with its level still on the
	// front, which the status line has already implied by existing.
	for _, level := range []string{"ERROR: ", "WARNING: "} {
		detail = strings.TrimPrefix(detail, level)
	}
	// bivy is the reader being told to pass a flag it refuses (ADR-004), and
	// the advice crowds the cause off the end of a status line.
	if i := strings.Index(detail, ". Use --"); i > 0 {
		detail = detail[:i+1]
	}
	if strings.HasPrefix(detail, "[") {
		if i := strings.Index(detail, "] "); i > 0 {
			rest := detail[i+2:]
			if j := strings.Index(rest, ": "); j > 0 && !strings.Contains(rest[:j], " ") {
				detail = rest[j+2:]
			}
		}
	}
	return strings.TrimSpace(detail)
}

// startMPV is the real player. The only place in bivy that launches one.
func startMPV(ctx context.Context) (player, error) {
	return mpv.Start(ctx, mpv.Options{})
}

// playerTrouble turns a failure to start mpv into a line that says what to do.
//
// The overwhelmingly likely cause is that mpv is not installed, and "exec:
// mpv: executable file not found in $PATH" is a true sentence that helps
// nobody.
func playerTrouble(err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Sprintf("mpv is not installed, and bivy plays through it (needs %s or newer)", mpv.Minimum)
	}
	return "could not start mpv: " + err.Error()
}
