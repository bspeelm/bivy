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
	// Draw turns those bytes into what the terminal understands.
	Draw(data []byte, cols, rows int) (string, error)
}

// pictures is the real artist: feed fetches, graphics draws.
type pictures struct{ feeds fetcher }

func (p pictures) Fetch(ctx context.Context, videoID string) ([]byte, error) {
	return p.feeds.Thumbnail(ctx, videoID)
}

func (p pictures) Draw(data []byte, cols, rows int) (string, error) {
	return graphics.Render(data, cols, rows)
}

// screen is what the browser needs from a terminal.
type screen interface {
	Graphics() graphics.Capability
	Size() (width, height int)
	Draw(frame string) error
	Keys() <-chan term.Press
	Resized() <-chan struct{}
	Close() error
}

// searcher is what the browser needs from the extractor, named here so the
// loop is testable without one installed.
type searcher interface {
	Search(ctx context.Context, query string, limit int) ([]media.Video, error)
	Channels(ctx context.Context, query string, limit int) ([]media.Channel, error)
}

// browser is one interactive session: a list, a cursor, and at most one mpv.
type browser struct {
	app    *app
	screen screen

	state    follow.State
	fetched  []media.Channel
	rows     []follow.Row
	failed   []string
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
	art      string
	pictures map[string][]byte
	drawn    string

	// query is what was searched for, empty on the dashboard; channels means
	// those results are channels. line is what has been typed into the
	// command line, and typing means it has the keyboard.
	query    string
	channels bool
	line     string
	typing   bool
	// dashboard keeps the rows the search replaced, so escape can put them
	// back without fetching every feed again.
	dashboard []follow.Row
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

		case e, open := <-events:
			if !open {
				// mpv is gone. The next play starts a new one rather than
				// talking to a socket nobody is listening on.
				b.player, events = nil, nil
				continue
			}
			b.report(e)

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
		b.play(ctx)
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
		b.play(ctx)
	case 'f':
		b.followRow(ctx)
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
		b.status = "keys: ↑↓ move · enter play · / search · : commands · esc back · q quit"
	case tui.Refresh:
		b.refresh(ctx)
	case tui.Search:
		b.runSearch(ctx, v.Query)
	case tui.Channels:
		b.runChannelSearch(ctx, v.Query)
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
		if strings.EqualFold(r.Channel, want) && media.IsChannelID(r.Video.ChannelID) {
			return r.Video.ChannelID, "", nil
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
		for i := range b.rows {
			if b.rows[i].ChannelID == id {
				b.rows[i].Followed = true
			}
		}
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
	b.rows = follow.Dashboard(b.state, b.fetched, dashboardRows)
	b.selected = tui.Move(b.selected, 0, len(b.rows))
}

// refresh fetches the followed feeds again.
func (b *browser) refresh(ctx context.Context) {
	b.status = "refreshing…"
	_ = b.draw()
	if err := b.load(ctx); err != nil {
		b.status = err.Error()
	}
}

// reload rebuilds the dashboard from what is already fetched, plus whatever a
// newly followed channel brings, without asking for every feed again.
func (b *browser) reload(ctx context.Context) {
	fetched, failed := b.app.fetchAll(ctx, b.state)
	if len(fetched) > 0 {
		b.fetched, b.failed = fetched, failed
	}
	b.query, b.channels, b.dashboard = "", false, nil
	b.rows = follow.Dashboard(b.state, b.fetched, dashboardRows)
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
	if b.dashboard == nil {
		b.dashboard = b.rows
	}
	b.query, b.channels = query, true
	b.status = "searching…"
	b.rows, b.selected = nil, 0
	_ = b.draw()

	found, err := b.app.search.Channels(ctx, query, dashboardRows)
	if err != nil {
		b.status = searchTrouble(err)
		return
	}

	b.status = ""
	b.rows = follow.ChannelResults(b.state, found)
}

// runSearch replaces the rows with results, keeping the dashboard to come back
// to.
func (b *browser) runSearch(ctx context.Context, query string) {
	if b.dashboard == nil {
		b.dashboard = b.rows
	}
	b.query, b.channels = query, false
	b.status = "searching…"
	b.rows, b.selected = nil, 0
	_ = b.draw()

	results, err := b.app.search.Search(ctx, query, dashboardRows)
	if err != nil {
		b.status = searchTrouble(err)
		return
	}

	b.status = ""
	b.rows = follow.Results(b.state, results)
}

// leaveResults puts the dashboard back.
func (b *browser) leaveResults() {
	if b.query == "" {
		return
	}
	b.query, b.channels, b.status = "", false, ""
	b.rows, b.dashboard = b.dashboard, nil
	b.selected = tui.Move(0, 0, len(b.rows))
}

// searchTrouble says what to do about a search that did not happen.
func searchTrouble(err error) string {
	if errors.Is(err, ytdlp.ErrNotInstalled) {
		return "yt-dlp is not installed, and search needs it — the dashboard does not"
	}
	return "search failed: " + err.Error()
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
func (b *browser) report(e mpv.Event) {
	if e.Name != "end-file" {
		return
	}

	// A refusal is always worth saying. A playback that merely stopped is
	// worth saying only when mpv complained: the user closing the window and
	// the player dying arrive as the same event with the same reason, and the
	// detail alongside is the only thing that tells them apart.
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
	if err := b.app.store.WriteJSON(stateFile, b.state); err != nil {
		b.status = "could not record that as watched: " + err.Error()
	}
	b.rows = follow.Dashboard(b.state, b.fetched, dashboardRows)
	b.playing = media.Video{}
}

// load fetches every followed channel and rebuilds the list.
func (b *browser) load(ctx context.Context) error {
	state, err := b.app.load()
	if err != nil {
		return err
	}

	fetched, failed := b.app.fetchAll(ctx, state)
	if len(fetched) > 0 {
		state = follow.Visited(follow.Retitle(state, fetched), fetched, b.app.now())
		if err := b.app.store.WriteJSON(stateFile, state); err != nil {
			return err
		}
	}

	b.state, b.fetched, b.failed = state, fetched, failed
	b.dashboard = nil
	b.query, b.channels = "", false
	b.rows = follow.Dashboard(state, fetched, dashboardRows)
	b.selected = tui.Move(b.selected, 0, len(b.rows))
	if b.status == "refreshing…" {
		b.status = ""
	}
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

// The box a thumbnail is drawn into, in cells: wide enough to be a picture
// rather than a stamp, short enough to leave a list.
const (
	artCols = 28
	artRows = 7
)

// picture is the drawn thumbnail for the row under the cursor, or nothing.
// What is worth a request is what somebody is looking at: a list of thirty
// rows is thirty pictures nobody asked for.
func (b *browser) picture(ctx context.Context) string {
	if b.app.art == nil || b.selected >= len(b.rows) {
		return ""
	}
	id := b.rows[b.selected].Video.ID
	if id == "" {
		return ""
	}
	if id == b.drawn {
		return b.art
	}

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

	drawn, err := b.app.art.Draw(data, artCols, artRows)
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
	art := b.picture(context.Background())
	return b.screen.Draw(tui.Render(tui.Dashboard{
		Art:         art,
		ArtRows:     artRows,
		Rows:        b.rows,
		Failed:      b.failedNow(),
		Reached:     len(b.fetched),
		Now:         b.app.now(),
		Width:       width,
		Height:      height,
		Selected:    b.selected,
		Status:      b.status,
		Query:       b.query,
		Channels:    b.channels,
		Line:        b.line,
		Typing:      b.typing,
		Interactive: true,
	}))
}

// playbackTrouble explains a video that would not play.
//
// The failure that actually happens is the extractor being refused a stream —
// bivy holds no account and sends no cookie (ADR-004), and that is exactly the
// request a bot check declines. Saying nothing, which is what bivy did before
// this existed, looks like the keypress was ignored.
func playbackTrouble(e mpv.Event) string {
	if e.Detail == "" {
		return "could not play that — mpv could not open it"
	}
	return "could not play that — " + e.Detail
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
