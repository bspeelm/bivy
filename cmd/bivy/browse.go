package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/bspeelm/bivy/internal/follow"
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

// screen is what the browser needs from a terminal.
type screen interface {
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

	// query is what was searched for, empty on the dashboard. typing means
	// the search box has the keyboard.
	query  string
	typing bool
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

func (b *browser) handleRune(ctx context.Context, r rune) (done bool) {
	switch r {
	case 'q':
		return true
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
	case '/':
		b.typing, b.query, b.status = true, "", ""
	case 'r':
		if b.query != "" {
			// Refreshing a result set means asking the same question again,
			// not throwing the results away for a dashboard.
			b.runSearch(ctx, b.query)
			return false
		}
		b.status = "refreshing…"
		_ = b.draw()
		if err := b.load(ctx); err != nil {
			b.status = err.Error()
		}
	}
	return false
}

// handleTyping is the search box, where every printable character is a
// character — including the ones that are commands on the list behind it.
func (b *browser) handleTyping(ctx context.Context, press term.Press) (done bool) {
	switch press.Key {
	case term.KeyInterrupt:
		return true
	case term.KeyEscape:
		b.typing = false
		b.query = ""
	case term.KeyBackspace:
		if r := []rune(b.query); len(r) > 0 {
			b.query = string(r[:len(r)-1])
		}
	case term.KeyEnter:
		b.typing = false
		if strings.TrimSpace(b.query) == "" {
			b.leaveResults()
			return false
		}
		b.runSearch(ctx, b.query)
	case term.KeyRune:
		if len(b.query) < queryLimit {
			b.query += string(press.Rune)
		}
	}
	return false
}

// runSearch replaces the rows with results, keeping the dashboard to come back
// to.
func (b *browser) runSearch(ctx context.Context, query string) {
	if b.dashboard == nil {
		b.dashboard = b.rows
	}
	b.query = query
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
	b.query, b.status = "", ""
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
	b.query = ""
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

func (b *browser) draw() error {
	width, height := b.screen.Size()
	return b.screen.Draw(tui.Render(tui.Dashboard{
		Rows:        b.rows,
		Failed:      b.failedNow(),
		Now:         b.app.now(),
		Width:       width,
		Height:      height,
		Selected:    b.selected,
		Status:      b.status,
		Query:       b.query,
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
