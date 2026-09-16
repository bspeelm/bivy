package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bspeelm/bivy/internal/follow"
	"github.com/bspeelm/bivy/internal/graphics"
	"github.com/bspeelm/bivy/internal/media"
	"github.com/bspeelm/bivy/internal/mpv"
	"github.com/bspeelm/bivy/internal/term"
	"github.com/bspeelm/bivy/internal/tui"
	"github.com/bspeelm/bivy/internal/ytdlp"
)

// fakeScreen is a terminal that draws into a buffer and takes its keypresses
// from a script. internal/term proves the decoding; this proves the loop.
type fakeScreen struct {
	draws    graphics.Capability
	keys     chan term.Press
	resizes  chan struct{}
	w, h     int
	art      string
	artRow   int
	artSends int

	mu     sync.Mutex
	frames []string
	closed bool
}

// key is one character pressed; named is one of the keys that is not a
// character.
func key(r rune) term.Press       { return term.Press{Key: term.KeyRune, Rune: r} }
func named(k term.Key) term.Press { return term.Press{Key: k} }

func newScreen() *fakeScreen {
	return &fakeScreen{keys: make(chan term.Press, 64), resizes: make(chan struct{}, 1)}
}

func (s *fakeScreen) Graphics() graphics.Capability { return s.draws }
func (s *fakeScreen) Cell() graphics.Cell           { return graphics.Assumed }
func (s *fakeScreen) Keys() <-chan term.Press       { return s.keys }
func (s *fakeScreen) Resized() <-chan struct{}      { return s.resizes }

func (s *fakeScreen) Size() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w == 0 {
		return 80, 24
	}
	return s.w, s.h
}

func (s *fakeScreen) width() int { w, _ := s.Size(); return w }

// resize changes the window and says so, the way a terminal does.
func (s *fakeScreen) resize(w, h int) {
	s.mu.Lock()
	s.w, s.h = w, h
	s.mu.Unlock()
	select {
	case s.resizes <- struct{}{}:
	default:
	}
}

// DrawArt records the picture the way the terminal keeps it: set once and
// left alone until it changes.
func (s *fakeScreen) DrawArt(row int, art string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if art != s.art || row != s.artRow {
		s.artSends++
	}
	s.art, s.artRow = art, row
	return nil
}

func (s *fakeScreen) picture() (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.artRow, s.art
}

func (s *fakeScreen) pictureSends() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.artSends
}

func (s *fakeScreen) Draw(frame string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames = append(s.frames, frame)
	return nil
}

func (s *fakeScreen) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *fakeScreen) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.frames) == 0 {
		return ""
	}
	return s.frames[len(s.frames)-1]
}

func (s *fakeScreen) frameCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.frames)
}

func (s *fakeScreen) wasClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *fakeScreen) press(keys ...term.Press) {
	for _, k := range keys {
		s.keys <- k
	}
}

// typed is what a person typing into the search box produces.
func (s *fakeScreen) typed(text string) {
	for _, r := range text {
		s.keys <- term.Press{Key: term.KeyRune, Rune: r}
	}
}

// fakePlayer is an mpv that records what it was asked to play and emits
// whatever events the test wants.
type fakePlayer struct {
	events chan mpv.Event
	refuse error
	mu     sync.Mutex
	played []media.Video
	closed bool
}

func newPlayer() *fakePlayer {
	return &fakePlayer{events: make(chan mpv.Event, 8)}
}

func (p *fakePlayer) Play(v media.Video) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refuse != nil {
		return p.refuse
	}
	p.played = append(p.played, v)
	return nil
}

// fakeArt stands in for the network and the graphics protocol together.
type fakeArt struct {
	mu      sync.Mutex
	fetched []string
	fail    error
}

func (a *fakeArt) Fetch(_ context.Context, videoID string) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.fetched = append(a.fetched, videoID)
	if a.fail != nil {
		return nil, a.fail
	}
	return []byte("picture of " + videoID), nil
}

func (a *fakeArt) Draw(data []byte, cols, rows int, cell graphics.Cell) (string, error) {
	return fmt.Sprintf("<art %dx%d %s>", cols, rows, data), nil
}

func (a *fakeArt) asked() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.fetched...)
}

func (p *fakePlayer) Events() <-chan mpv.Event { return p.events }

func (p *fakePlayer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

func (p *fakePlayer) watched() []media.Video {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]media.Video(nil), p.played...)
}

func (p *fakePlayer) wasClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// stubSearch stands in for the extractor, which a test may not require to be
// installed.
type stubSearch struct {
	mu       sync.Mutex
	results  []media.Video
	channels []media.Channel
	uploads  map[string]media.Channel
	err      error
	// before runs at the start of a search, so a test can hold one open.
	before func()
	asked  []string
}

func (s *stubSearch) Search(_ context.Context, query string, limit int) ([]media.Video, error) {
	s.mu.Lock()
	hold := s.before
	s.mu.Unlock()
	if hold != nil {
		hold()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, query)
	if s.err != nil {
		return nil, s.err
	}
	if limit < len(s.results) {
		return s.results[:limit], nil
	}
	return s.results, nil
}

func (s *stubSearch) Channels(_ context.Context, query string, _ int) ([]media.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, "channels:"+query)
	if s.err != nil {
		return nil, s.err
	}
	return s.channels, nil
}

func (s *stubSearch) Uploads(_ context.Context, channelID string, _ int) (media.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, "uploads:"+channelID)
	if s.uploads == nil {
		return media.Channel{}, errors.New("no listing for that channel")
	}
	return s.uploads[channelID], nil
}

func (s *stubSearch) queries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

func (s *stubSearch) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// browserHarness is a session with both ends faked.
type browserHarness struct {
	*harness
	screen *fakeScreen
	player *fakePlayer
	finder *stubSearch
	art    *fakeArt
	done   chan int
}

func newBrowser(t *testing.T) *browserHarness {
	t.Helper()

	h := newHarness(t)
	bh := &browserHarness{
		harness: h,
		screen:  newScreen(),
		player:  newPlayer(),
		finder: &stubSearch{
			results: []media.Video{
				{ID: "sssssssssss", Title: "A Search Result", Author: "Someone", Duration: 89 * time.Second},
				{ID: "ttttttttttt", Title: "Another Result", Author: "Someone Else", Duration: 3 * time.Minute},
			},
			channels: []media.Channel{
				{ID: chanA, Title: "Aye", Description: "the first one", Followers: 3_590_000},
				{ID: chanB, Title: "Bee", Description: "the other one", Followers: 12_400},
			},
		},
		art:  &fakeArt{},
		done: make(chan int, 1),
	}
	h.app.newPlayer = func(context.Context) (player, error) { return bh.player, nil }
	h.app.search = bh.finder
	return bh
}

// start runs the loop in the background and returns the browser it is driving.
func (b *browserHarness) start(t *testing.T) *browser {
	t.Helper()

	br := &browser{app: b.app, screen: b.screen}
	go func() {
		code := br.run(context.Background())
		br.close()
		b.done <- code
	}()
	return br
}

// quit ends the session with ctrl-c, which works from the list and from the
// search box alike. "q" is a letter in the box, and that is the point of it.
func (b *browserHarness) quit(t *testing.T) {
	t.Helper()
	b.screen.press(named(term.KeyInterrupt))
	select {
	case <-b.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser did not stop when quit was pressed")
	}
}

// eventuallyArt waits for the picture on screen to be something.
func (b *browserHarness) eventuallyArt(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if _, art := b.screen.picture(); strings.Contains(art, want) {
			return
		}
		select {
		case <-deadline:
			_, art := b.screen.picture()
			t.Fatalf("the picture never became %q; it is %q", want, art)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// eventually waits for the screen to say something, because the loop redraws
// on its own schedule.
func (b *browserHarness) eventually(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if strings.Contains(b.screen.last(), want) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the screen never said %q. Last frame:\n%s", want, b.screen.last())
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestBrowseDrawsTheDashboard(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.eventually(t, "enter play")
	b.quit(t)

	if !b.screen.wasClosed() {
		t.Error("the terminal was not put back")
	}
}

func TestEnterPlaysTheSelectedRow(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")

	// The second row, so this proves the cursor is consulted rather than the
	// first row being played whatever is selected.
	b.screen.press(named(term.KeyDown), named(term.KeyEnter))
	b.eventually(t, "playing · An older thing")

	played := b.player.watched()
	if len(played) != 1 {
		t.Fatalf("%d videos played, want 1", len(played))
	}
	if got, want := played[0].ID, "ccccccccccc"; got != want {
		t.Errorf("played %q, want %q", got, want)
	}
	b.quit(t)

	if !b.player.wasClosed() {
		t.Error("quitting bivy did not stop mpv (§7)")
	}
}

// Watched on end, not on start. A video opened and abandoned after ten seconds
// has not been watched, and recording it as watched is a claim about something
// that did not happen.
func TestAVideoIsMarkedWatchedWhenItReachesItsEnd(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")

	// Still not watched: it is only playing.
	if strings.Contains(b.screen.last(), "✓") {
		t.Error("a video was marked watched as soon as it started")
	}

	b.player.events <- mpv.Event{Name: "end-file", Reason: "eof"}
	b.eventually(t, "watched · The newest thing")
	b.eventually(t, "✓")
	b.quit(t)

	if !br.state.HasWatched("aaaaaaaaaaa") {
		t.Error("the video was not recorded as watched")
	}

	// And it survives the session, because the point of recording it is the
	// next launch.
	var saved follow.State
	if err := b.store.ReadJSON(stateFile, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.HasWatched("aaaaaaaaaaa") {
		t.Error("the watch was not written to disk")
	}
}

// The complaint the user actually gets: press enter, and nothing happens and
// nothing is said. mpv dying and a window being closed are the same event, so
// bivy showed the same nothing for both.
func TestAPlayerThatDiesSaysSoOnScreen(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")

	b.player.events <- mpv.Event{
		Name:   "end-file",
		Reason: "quit",
		Detail: "Error occurred on the display fd",
	}
	b.eventually(t, "could not play that")
	b.eventually(t, "display fd")
	b.quit(t)
}

func TestAVideoThatWasStoppedIsNotMarkedWatched(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")

	b.player.events <- mpv.Event{Name: "end-file", Reason: "quit"}
	b.eventually(t, "The newest thing")
	b.quit(t)

	if br.state.HasWatched("aaaaaaaaaaa") {
		t.Error("a video that was closed early was recorded as watched")
	}
}

// One mpv per session. Launching it takes long enough to notice, and the IPC
// socket exists so a second video does not pay for it again.
func TestASecondVideoReusesTheSamePlayer(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	var starts int
	b.app.newPlayer = func(context.Context) (player, error) {
		starts++
		return b.player, nil
	}

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")
	b.screen.press(named(term.KeyDown), named(term.KeyEnter))
	b.eventually(t, "playing · An older thing")
	b.quit(t)

	if starts != 1 {
		t.Errorf("mpv was started %d times, want 1", starts)
	}
	if got := len(b.player.watched()); got != 2 {
		t.Errorf("%d videos played, want 2", got)
	}
}

// The overwhelmingly likely failure is that mpv is not installed, and the
// message has to say what to do about it rather than quoting exec.
func TestAMissingPlayerSaysWhatIsWrong(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)
	b.app.newPlayer = func(context.Context) (player, error) {
		return nil, &exec.Error{Name: "mpv", Err: exec.ErrNotFound}
	}

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "mpv is not installed")
	b.eventually(t, mpv.Minimum)
	b.quit(t)
}

func TestARefusedPlayIsReportedOnScreen(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)
	b.player.refuse = errors.New("unsupported format")

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "unsupported format")
	b.quit(t)
}

// mpv can be closed from its own window. The next play starts a new one rather
// than writing to a socket nobody is listening on.
func TestAPlayerThatDiesIsNotReusedAfterwards(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	var starts int
	players := []*fakePlayer{newPlayer(), newPlayer()}
	b.app.newPlayer = func(context.Context) (player, error) {
		p := players[starts]
		starts++
		return p, nil
	}

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")

	close(players[0].events)
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")
	b.quit(t)

	if starts != 2 {
		t.Errorf("mpv was started %d times, want 2", starts)
	}
	if got := len(players[1].watched()); got != 1 {
		t.Errorf("the replacement played %d videos, want 1", got)
	}
}

// An mpv that exits on its own leaves a socket directory behind, and removing
// it is bivy's job rather than the dead process's. Closing a video by its own
// window is the ordinary way to reach this, so the directories accumulate one
// per dismissed video — and §8 says the write set is two directories.
func TestAPlayerThatDiesIsClosedSoItsSocketDirectoryGoes(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	players := []*fakePlayer{newPlayer(), newPlayer()}
	var starts int
	b.app.newPlayer = func(context.Context) (player, error) {
		p := players[starts]
		starts++
		return p, nil
	}

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")

	close(players[0].events)
	// Played again so the assertion runs after the loop has handled the close
	// rather than racing it.
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")
	b.quit(t)

	if !players[0].wasClosed() {
		t.Error("the player that died was dropped without being closed, so its socket directory is still there")
	}
}

// Saying nothing, which is what bivy did before this test existed, looks like
// the keypress was ignored. The failure that actually happens is the extractor
// being refused a stream: bivy holds no account and sends no cookie (ADR-004),
// which is exactly the request a bot check declines.
func TestAVideoThatWouldNotPlaySaysSo(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")

	b.player.events <- mpv.Event{
		Name:   "end-file",
		Reason: "error",
		Detail: "Failed to open https://www.youtube.com/watch?v=aaaaaaaaaaa.",
	}
	b.eventually(t, "could not play that")
	b.eventually(t, "Failed to open")
	b.quit(t)

	if br.state.HasWatched("aaaaaaaaaaa") {
		t.Error("a video that failed to play was recorded as watched")
	}
}

// mpv does not always say why.
func TestAFailureWithNoDetailStillSaysSomething(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")

	b.player.events <- mpv.Event{Name: "end-file", Reason: "error"}
	b.eventually(t, "could not play that")
	b.quit(t)
}

func TestTheCursorStopsAtTheEnds(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")

	b.screen.press(named(term.KeyUp), named(term.KeyUp), named(term.KeyUp))
	b.eventually(t, "> ")
	b.screen.press(named(term.KeyEnd), named(term.KeyDown), named(term.KeyDown))
	b.eventually(t, "> ")
	b.quit(t)

	if br.selected != len(br.rows)-1 {
		t.Errorf("the cursor is at %d of %d rows", br.selected, len(br.rows))
	}
}

func TestRefreshRefetches(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")

	b.feeds.setChannel(chanA, media.Channel{ID: chanA, Title: "Aye", Videos: []media.Video{
		{ID: "ddddddddddd", Title: "Posted while bivy was open", Published: at(11)},
	}})

	b.screen.press(key('r'))
	b.eventually(t, "Posted while bivy was open")
	b.quit(t)
}

func TestResizeRedraws(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")

	before := b.screen.frameCount()
	b.screen.resize(90, 30)

	deadline := time.After(5 * time.Second)
	for b.screen.frameCount() <= before {
		select {
		case <-deadline:
			t.Fatal("a resize did not redraw")
		case <-time.After(5 * time.Millisecond):
		}
	}
	b.quit(t)
}

// Pressing enter with nothing on screen must not index into an empty list.
func TestPlayingWithAnEmptyDashboard(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(named(term.KeyEnter), named(term.KeyDown), named(term.KeyEnter))
	b.quit(t)

	if got := len(b.player.watched()); got != 0 {
		t.Errorf("%d videos played from an empty dashboard", got)
	}
}

// Quitting is not a key. It is one keystroke away from every other key on the
// list, and the cost of hitting it by accident is losing the screen you were
// reading.
func TestQDoesNotQuitTheList(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key('q'))

	select {
	case <-b.done:
		t.Fatal("q quit the list; it is supposed to cost :q")
	case <-time.After(300 * time.Millisecond):
	}
	b.quit(t)
}

// :q, the spelling anyone who has used a modal editor will try first.
func TestColonQQuits(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key(':'))
	b.screen.typed("q")
	b.screen.press(named(term.KeyEnter))

	select {
	case <-b.done:
	case <-time.After(5 * time.Second):
		t.Fatal(":q did not quit")
	}
}

// And must not, from the box.
func TestQDoesNotQuitFromTheSearchBox(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key('/'))
	b.screen.typed("q")
	b.eventually(t, ":search q")

	select {
	case <-b.done:
		t.Fatal("typing q into the search box quit the program")
	case <-time.After(200 * time.Millisecond):
	}
	b.quit(t)
}

func TestSearchReplacesTheListAndPlaysTheSameWay(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")

	b.screen.press(key('/'))
	b.eventually(t, ":search ")
	b.screen.typed("terminal video")
	b.eventually(t, ":search terminal video")
	b.screen.press(named(term.KeyEnter))

	b.eventually(t, "A Search Result")
	b.eventually(t, `2 results for "terminal video"`)
	if strings.Contains(b.screen.last(), "The newest thing") {
		t.Error("the dashboard rows are still on screen behind the results")
	}
	if got := b.finder.queries(); len(got) != 1 || got[0] != "terminal video" {
		t.Errorf("searched for %v, want one query for \"terminal video\"", got)
	}

	// A result plays exactly like a dashboard row: that is the milestone.
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing · A Search Result")
	b.quit(t)

	played := b.player.watched()
	if len(played) != 1 || played[0].ID != "sssssssssss" {
		t.Errorf("played %+v, want the selected result", played)
	}
}

// The keys that drive the list are letters, and in the box they have to be
// letters. Typing "jkqr" must not move a cursor or quit.
func TestTypingACommandLetterTypesIt(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key('/'))
	b.eventually(t, ":search ")

	b.screen.typed("jkqrg")
	b.eventually(t, ":search jkqrg")

	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "A Search Result")
	b.quit(t)

	if got := b.finder.queries(); len(got) != 1 || got[0] != "jkqrg" {
		t.Errorf("searched for %v, want the letters as typed", got)
	}
}

func TestBackspaceInTheSearchBox(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key('/'))
	b.screen.typed("cats")
	b.eventually(t, ":search cats")

	b.screen.press(named(term.KeyBackspace), named(term.KeyBackspace))
	b.eventually(t, ":search ca█")

	// Backspacing past the start eats the command name too — it is one line,
	// not a box with a label on it — and then stops rather than crashing.
	for range 20 {
		b.screen.press(named(term.KeyBackspace))
	}
	b.eventually(t, ":█")
	b.quit(t)
}

// A search box that takes characters it will then refuse is worse than one
// that stops.
func TestTheSearchBoxStopsAtItsLimit(t *testing.T) {
	b := newBrowser(t)

	br := b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key('/'))
	b.screen.typed(strings.Repeat("a", queryLimit+20))
	b.eventually(t, ":search "+strings.Repeat("a", 40))
	b.quit(t)

	if len(br.line) > queryLimit {
		t.Errorf("the line holds %d characters, and the limit is %d", len(br.line), queryLimit)
	}
}

// Escape from the box leaves the dashboard exactly as it was, without
// refetching every feed to rebuild it.
func TestEscapeFromTheSearchBoxChangesNothing(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")

	before := len(b.feeds.asked())
	b.screen.press(key('/'))
	b.screen.typed("cats")
	b.eventually(t, ":search cats")
	b.screen.press(named(term.KeyEscape))

	b.eventually(t, "The newest thing")
	b.quit(t)

	if got := b.finder.queries(); len(got) != 0 {
		t.Errorf("a cancelled box still searched for %v", got)
	}
	if after := len(b.feeds.asked()); after != before {
		t.Errorf("escaping refetched %d feeds", after-before)
	}
}

// Escape from the results puts the dashboard back, also without refetching.
func TestEscapeFromResultsRestoresTheDashboard(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	before := len(b.feeds.asked())

	b.screen.press(key('/'))
	b.screen.typed("cats")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "A Search Result")

	b.screen.press(named(term.KeyEscape))
	b.eventually(t, "The newest thing")
	b.quit(t)

	if strings.Contains(b.screen.last(), "A Search Result") {
		t.Error("the results are still on screen")
	}
	if after := len(b.feeds.asked()); after != before {
		t.Errorf("coming back from results refetched %d feeds", after-before)
	}
}

// An empty query is a way out of the box, not a search for nothing.
func TestAnEmptySearchIsNotASearch(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key('/'), named(term.KeyEnter))
	b.eventually(t, "The newest thing")
	b.quit(t)

	if got := b.finder.queries(); len(got) != 0 {
		t.Errorf("an empty box searched for %v", got)
	}
}

// The dashboard never needs the extractor, so a user can go a long time
// without discovering they have not got it. The message says which half of the
// program is affected.
func TestAMissingExtractorSaysWhatIsAffected(t *testing.T) {
	b := newBrowser(t)
	b.finder.fail(ytdlp.ErrNotInstalled)

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key('/'))
	b.screen.typed("cats")
	b.screen.press(named(term.KeyEnter))

	b.eventually(t, "yt-dlp is not installed")
	b.eventually(t, "the dashboard does not")
	b.quit(t)
}

func TestAFailedSearchIsReportedOnScreen(t *testing.T) {
	b := newBrowser(t)
	b.finder.fail(errors.New("the search failed: Sign in to confirm you are not a bot"))

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key('/'))
	b.screen.typed("cats")
	b.screen.press(named(term.KeyEnter))

	b.eventually(t, "not a bot")
	b.quit(t)
}

// Watched is true however you found the video: a result you have already seen
// is marked, even though it was never on the dashboard.
func TestAResultAlreadyWatchedIsMarked(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key('/'))
	b.screen.typed("cats")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "A Search Result")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")

	b.player.events <- mpv.Event{Name: "end-file", Reason: "eof"}
	b.eventually(t, "watched · A Search Result")
	b.quit(t)

	if !br.state.HasWatched("sssssssssss") {
		t.Error("a result played to its end was not recorded as watched")
	}
}

// Refreshing a result set asks the same question again rather than throwing
// the results away for a dashboard.
func TestRefreshOnResultsSearchesAgain(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key('/'))
	b.screen.typed("cats")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "A Search Result")

	b.screen.press(key('r'))
	b.eventually(t, "A Search Result")
	b.quit(t)

	if got := b.finder.queries(); len(got) != 2 {
		t.Errorf("searched %v, want the same query twice", got)
	}
}

// The reason there is a command line at all: after a search, the channel is a
// column the user is reading and its identifier is not. Following it should
// not mean quitting, typing a command, and starting again.
func TestFollowAChannelByTheNameOnScreen(t *testing.T) {
	b := newBrowser(t)
	b.finder.results = []media.Video{
		{ID: "sssssssssss", Title: "A Search Result", Author: "Aye", ChannelID: chanA},
	}

	br := b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key('/'))
	b.screen.typed("something")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "A Search Result")

	b.screen.press(key(':'))
	b.eventually(t, "follow")
	b.screen.typed("follow Aye")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "following Aye")
	b.quit(t)

	if _, found := br.state.Find(chanA); !found {
		t.Error("the channel was not followed")
	}

	// And it survives, because the point of following is the next launch.
	var saved follow.State
	if err := b.store.ReadJSON(stateFile, &saved); err != nil {
		t.Fatal(err)
	}
	if _, found := saved.Find(chanA); !found {
		t.Error("the follow was not written to disk")
	}
}

func TestFollowByHandleFromTheCommandLine(t *testing.T) {
	b := newBrowser(t)

	br := b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key(':'))
	b.screen.typed("follow @aye")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "following Aye")
	b.quit(t)

	if _, found := br.state.Find(chanA); !found {
		t.Error("the handle was not resolved and followed")
	}
}

func TestUnfollowFromTheCommandLine(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key(':'))
	b.screen.typed("unfollow Aye")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "unfollowed Aye")
	b.quit(t)

	if _, found := br.state.Find(chanA); found {
		t.Error("the channel is still followed")
	}
}

// Tab completes as far as the matches agree, so the line can be driven without
// remembering how anything is spelled.
func TestTabCompletesTheCommandLine(t *testing.T) {
	b := newBrowser(t)

	br := b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key(':'), key('f'), named(term.KeyTab))
	b.eventually(t, ":follow █")

	// Escape abandons the line rather than leaving it to reappear next time.
	b.screen.press(named(term.KeyEscape))
	b.eventually(t, "nothing followed yet")
	b.quit(t)

	if br.line != "" {
		t.Errorf("an abandoned line survived as %q", br.line)
	}
}

func TestAnUnknownCommandIsReportedOnScreen(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key(':'))
	b.screen.typed("serach cats")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "did you mean search")
	b.quit(t)
}

// :quit is a command because it is rare, and it has to actually work.
func TestQuitFromTheCommandLine(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key(':'))
	b.screen.typed("quit")
	b.screen.press(named(term.KeyEnter))

	select {
	case <-b.done:
	case <-time.After(5 * time.Second):
		t.Fatal(":quit did not end the session")
	}
}

// :search is the same surface the / key opens, so it has to behave the same.
func TestSearchFromTheCommandLine(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key(':'))
	b.screen.typed("search cats")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "A Search Result")
	b.quit(t)

	if got := b.finder.queries(); len(got) != 1 || got[0] != "cats" {
		t.Errorf("searched for %v, want one query for cats", got)
	}
}

// Following something that is not on screen and is not an identifier says so
// rather than reaching the network with it.
func TestFollowingSomethingThatIsNotHere(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key(':'))
	b.screen.typed("follow Nobody At All")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "no channel called")
	b.quit(t)

	if asked := b.feeds.asked(); len(asked) != 0 {
		t.Errorf("a name that resolved to nothing still reached the network: %v", asked)
	}
}

// The ask: search for a channel, then press f next to it to follow it.
func TestChannelSearchThenFollowWithF(t *testing.T) {
	b := newBrowser(t)

	br := b.start(t)
	b.eventually(t, "nothing followed yet")

	b.screen.press(key(':'))
	b.screen.typed("channels papa meat")
	b.screen.press(named(term.KeyEnter))

	b.eventually(t, "2 channels for")
	b.eventually(t, "Aye")
	b.eventually(t, "3.6M subscribers")
	// The channel's own description, for the row under the cursor.
	b.eventually(t, "the first one")

	// f on the second row follows that one, not the first.
	b.screen.press(named(term.KeyDown), key('f'))
	b.eventually(t, "following Bee")
	b.quit(t)

	if _, found := br.state.Find(chanB); !found {
		t.Error("f did not follow the channel under the cursor")
	}
	if _, found := br.state.Find(chanA); found {
		t.Error("f followed the wrong row")
	}
}

// Following one channel out of a list is rarely the last thing anyone does
// with that list, so the list stays and the row is marked.
func TestFollowingFromAChannelListKeepsTheList(t *testing.T) {
	b := newBrowser(t)

	br := b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key(':'))
	b.screen.typed("ch papa meat")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "2 channels for")

	b.screen.press(key('f'))
	b.eventually(t, "following Aye")
	b.eventually(t, "✓")
	b.quit(t)

	if !br.channels {
		t.Error("following a channel threw the channel list away")
	}
}

// f on a video row follows the channel that video came from, which is the
// other half of the same question.
func TestFollowWithFFromAVideoRow(t *testing.T) {
	b := newBrowser(t)
	b.finder.results = []media.Video{
		{ID: "sssssssssss", Title: "A Search Result", Author: "Aye", ChannelID: chanA},
	}

	br := b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key('/'))
	b.screen.typed("something")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "A Search Result")

	b.screen.press(key('f'))
	b.eventually(t, "following Aye")
	b.quit(t)

	if _, found := br.state.Find(chanA); !found {
		t.Error("f on a video row did not follow its channel")
	}
}

// A channel already followed is marked when the list is built, not only after
// following one from it.
func TestAChannelAlreadyFollowedIsMarkedInTheList(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key(':'))
	b.screen.typed("channels papa meat")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "2 channels for")
	b.eventually(t, "✓")
	b.quit(t)
}

// f with nothing under the cursor is not a crash.
func TestFollowWithFOnAnEmptyList(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key('f'))
	b.quit(t)

	if asked := b.feeds.asked(); len(asked) != 0 {
		t.Errorf("f on an empty list reached the network: %v", asked)
	}
}

// A row on screen has been named by whatever produced it, so following it asks
// no feed. Asking anyway would put every follow behind an endpoint that
// intermittently refuses, for a name bivy is already holding.
func TestFollowingARowOnScreenAsksNoFeed(t *testing.T) {
	b := newBrowser(t)
	b.finder.channels = []media.Channel{{ID: chanA, Title: "Aye", Followers: 10}}

	br := b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key(':'))
	b.screen.typed("channels whatever")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "1 channel for")

	before := len(b.feeds.asked())
	b.screen.press(key('f'))
	b.eventually(t, "following Aye")
	b.quit(t)

	if after := len(b.feeds.asked()); after != before {
		t.Errorf("following a named row made %d feed requests", after-before)
	}
	if _, found := br.state.Find(chanA); !found {
		t.Error("the channel was not followed")
	}
}

// And it still works when the feed is refusing, which is the case that made
// this worth doing.
func TestFollowingARowWorksWhileFeedsAreDown(t *testing.T) {
	b := newBrowser(t)
	b.finder.channels = []media.Channel{{ID: chanA, Title: "Aye", Followers: 10}}
	b.feeds.fail(errors.New("404 Not Found"))

	br := b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key(':'))
	b.screen.typed("channels whatever")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "1 channel for")

	b.screen.press(key('f'))
	b.eventually(t, "following Aye")
	b.quit(t)

	if _, found := br.state.Find(chanA); !found {
		t.Error("a channel could not be followed while its feed was down")
	}
}

// A handle has no name attached until something resolves it, so that path
// still asks.
func TestFollowingAHandleStillAsksTheFeed(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")
	before := len(b.feeds.asked())

	b.screen.press(key(':'))
	b.screen.typed("follow @aye")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "following Aye")
	b.quit(t)

	if after := len(b.feeds.asked()); after == before {
		t.Error("following a handle made no request, so the name came from nowhere")
	}
}

// A picture is fetched for the row somebody is looking at, and for no other.
// A list of thirty rows is thirty pictures nobody asked for.
func TestOnlyTheRowUnderTheCursorGetsAPicture(t *testing.T) {
	b := newBrowser(t)
	b.screen.draws = graphics.Kitty
	b.app.art = b.art
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventuallyArt(t, "picture of aaaaaaaaaaa")

	if got := b.art.asked(); len(got) != 1 || got[0] != "aaaaaaaaaaa" {
		t.Errorf("fetched %v, want only the row under the cursor", got)
	}

	b.screen.press(named(term.KeyDown))
	b.eventuallyArt(t, "picture of ccccccccccc")
	b.quit(t)

	if got := len(b.art.asked()); got != 2 {
		t.Errorf("%d pictures fetched after moving one row, want 2", got)
	}
}

// Moving back to a row already seen costs nothing: the session keeps what it
// fetched, in memory and nowhere else (ADR-007).
func TestAPictureIsFetchedOnce(t *testing.T) {
	b := newBrowser(t)
	b.screen.draws = graphics.Kitty
	b.app.art = b.art
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventuallyArt(t, "picture of aaaaaaaaaaa")
	b.screen.press(named(term.KeyDown))
	b.eventuallyArt(t, "picture of ccccccccccc")
	b.screen.press(named(term.KeyUp))
	b.eventuallyArt(t, "picture of aaaaaaaaaaa")
	b.quit(t)

	if got := len(b.art.asked()); got != 2 {
		t.Errorf("%d fetches for two rows visited twice, want 2", got)
	}
}

// A terminal that cannot draw is the ordinary case, and nothing else about the
// program changes.
func TestATerminalThatCannotDrawGetsNoPictures(t *testing.T) {
	b := newBrowser(t)
	b.screen.draws = graphics.None
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.quit(t)

	if got := b.art.asked(); len(got) != 0 {
		t.Errorf("a terminal with no graphics fetched %v", got)
	}
	if strings.Contains(b.screen.last(), "\x1b_G") {
		t.Error("a graphics sequence was sent to a terminal that cannot read one")
	}
}

// A picture that will not arrive is not worth a word on screen: the row says
// what the video is, and the thumbnail is a convenience.
func TestAMissingPictureIsNotAnError(t *testing.T) {
	b := newBrowser(t)
	b.screen.draws = graphics.Kitty
	b.art.fail = errors.New("404 Not Found")
	b.app.art = b.art
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.quit(t)

	if strings.Contains(b.screen.last(), "404") {
		t.Errorf("a missing picture put an error on screen:\n%s", b.screen.last())
	}
}

// A channel row has no video, so there is nothing to draw and nothing to ask
// for.
func TestAChannelRowHasNoPicture(t *testing.T) {
	b := newBrowser(t)
	b.screen.draws = graphics.Kitty
	b.app.art = b.art

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key(':'))
	b.screen.typed("channels whatever")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "2 channels for")
	b.quit(t)

	if got := b.art.asked(); len(got) != 0 {
		t.Errorf("a list of channels fetched %v", got)
	}
}

// The picture is drawn for the window it is in, so a window that changes shape
// gets a picture that fits it rather than the one that fitted before.
func TestThePictureIsRedrawnWhenTheWindowChanges(t *testing.T) {
	b := newBrowser(t)
	b.screen.draws = graphics.Kitty
	b.app.art = b.art
	b.run(t, "follow", chanA)

	b.start(t)
	cols, _ := tui.ArtBox(b.screen.width(), 24, graphics.Assumed)
	b.eventuallyArt(t, fmt.Sprintf("<art %dx", cols))

	b.screen.resize(140, 40)
	wider, _ := tui.ArtBox(140, 40, graphics.Assumed)
	if wider == cols {
		t.Skip("the two window sizes ask for the same picture")
	}
	b.eventuallyArt(t, fmt.Sprintf("<art %dx", wider))
	b.quit(t)

	// Re-drawn, not re-fetched: the bytes did not change, only their size.
	if got := len(b.art.asked()); got != 1 {
		t.Errorf("%d fetches for one video at two sizes, want 1", got)
	}
}

// The dashboard is the whole program, and a feed service that will not answer
// leaves it empty. The extractor can list a channel's recent uploads, at the
// cost of the publish times (ADR-013).
func TestAFeedThatWillNotAnswerFallsBackToTheExtractor(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.feeds.fail(errors.New("404 Not Found"))
	b.finder.uploads = map[string]media.Channel{
		chanA: {ID: chanA, Videos: []media.Video{
			{ID: "uuuuuuuuuuu", Title: "Listed by the extractor", Duration: 90 * time.Second},
		}},
	}

	b.start(t)
	b.eventually(t, "Listed by the extractor")
	b.eventually(t, "some feeds are not answering")
	b.quit(t)

	if asked := b.finder.queries(); len(asked) == 0 || !strings.HasPrefix(asked[0], "uploads:") {
		t.Errorf("the extractor was asked %v, want a channel listing", asked)
	}
}

// The feed is still how bivy learns what a channel posted. The extractor is
// not asked when the feed answers.
func TestTheExtractorIsNotAskedWhenTheFeedAnswers(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.quit(t)

	if asked := b.finder.queries(); len(asked) != 0 {
		t.Errorf("the extractor was asked %v while the feed was answering", asked)
	}
	if strings.Contains(b.screen.last(), "not answering") {
		t.Error("a working dashboard claimed the feeds were down")
	}
}

// A channel whose feed fails and whose listing also fails is reported, rather
// than quietly missing.
func TestAChannelThatFailsBothWaysIsReported(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.feeds.fail(errors.New("404 Not Found"))
	b.finder.uploads = nil

	b.start(t)
	b.eventually(t, "could not be reached")
	b.quit(t)
}

// Rows from the extractor carry no publish time, so nothing is marked new on
// the strength of a date bivy does not have.
func TestFallbackRowsAreNotMarkedNew(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.feeds.fail(errors.New("404 Not Found"))
	b.finder.uploads = map[string]media.Channel{
		chanA: {ID: chanA, Videos: []media.Video{
			{ID: "uuuuuuuuuuu", Title: "Listed by the extractor", Duration: 90 * time.Second},
		}},
	}

	b.start(t)
	b.eventually(t, "Listed by the extractor")
	b.quit(t)

	if strings.Contains(b.screen.last(), "•") {
		t.Errorf("a row with no date was marked new:\n%s", b.screen.last())

	}
}

// Visiting a channel: find it, press enter, and its videos are the list.
func TestEnterOnAChannelOpensIt(t *testing.T) {
	b := newBrowser(t)
	b.finder.channels = []media.Channel{{ID: chanA, Title: "Aye", Followers: 10}}

	br := b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key(':'))
	b.screen.typed("channels aye")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "1 channel for")

	// Enter on a channel row opens it; enter on a video row plays it.
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "Aye · 2 videos")
	b.eventually(t, "The newest thing")
	b.quit(t)

	if br.viewing == "" {
		t.Error("the browser is not showing a channel")
	}
}

// Escape goes back one screen, however many deep the screens are.
func TestEscapeGoesBackOneScreenAtATime(t *testing.T) {
	b := newBrowser(t)
	b.finder.channels = []media.Channel{{ID: chanA, Title: "Aye", Followers: 10}}
	b.run(t, "follow", chanB)

	b.start(t)
	b.eventually(t, "From the other one")

	b.screen.press(key(':'))
	b.screen.typed("channels aye")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "1 channel for")

	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "Aye · 2 videos")

	// Back to the channel list, then back to the dashboard.
	b.screen.press(named(term.KeyEscape))
	b.eventually(t, "1 channel for")
	b.screen.press(named(term.KeyEscape))
	b.eventually(t, "From the other one")
	b.quit(t)
}

// Enter on a video inside a channel plays it, the same as anywhere else.
func TestEnterInsideAChannelPlays(t *testing.T) {
	b := newBrowser(t)
	b.finder.channels = []media.Channel{{ID: chanA, Title: "Aye", Followers: 10}}

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key(':'))
	b.screen.typed("ch aye")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "1 channel for")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "Aye · 2 videos")

	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")
	b.quit(t)

	if got := len(b.player.watched()); got != 1 {
		t.Errorf("%d videos played from inside a channel, want 1", got)
	}
}

// :open takes a name on screen, so a channel on the dashboard can be opened
// without finding it again.
func TestOpenCommandByNameOnScreen(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key(':'))
	b.screen.typed("open Aye")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "Aye · 2 videos")
	b.quit(t)

	if br.viewing != "Aye" {
		t.Errorf("viewing %q, want Aye", br.viewing)
	}
}

// A channel that will not open leaves the screen it was opened from, rather
// than an empty one.
func TestAChannelThatWillNotOpenLeavesTheScreenAlone(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)
	b.feeds.fail(errors.New("404 Not Found"))
	b.finder.uploads = nil

	b.start(t)
	b.eventually(t, "could not be reached")

	b.screen.press(key(':'))
	b.screen.typed("open Aye")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "could not open Aye")
	b.quit(t)
}

// The channel is the screen, so repeating it on every row is noise.
func TestRowsInsideAChannelDoNotRepeatTheChannel(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key(':'))
	b.screen.typed("open Aye")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "Aye · 2 videos")
	b.quit(t)

	for _, r := range br.rows {
		if r.Channel != "" {
			t.Errorf("a row inside a channel still names it: %q", r.Channel)
		}
		if r.ChannelID != chanA {
			t.Errorf("a row inside a channel lost which channel it is from")
		}
	}
}

// A channel already followed can be opened by name whatever the screen is
// showing, including a list it does not appear in.
func TestOpenAFollowedChannelNotOnScreen(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")

	// A search replaces the list with rows from somebody else entirely.
	b.screen.press(key('/'))
	b.screen.typed("unrelated")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "A Search Result")

	b.screen.press(key(':'))
	b.screen.typed("open Aye")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "Aye · 2 videos")
	b.quit(t)

	if br.viewing != "Aye" {
		t.Errorf("viewing %q, want Aye", br.viewing)
	}
}

// Thirty at a time, and the rows that arrive go below where the reader
// already is.
func TestLoadingMore(t *testing.T) {
	b := newBrowser(t)

	var many []media.Video
	for i := range 60 {
		many = append(many, media.Video{ID: fmt.Sprintf("vid%08d", i), Title: fmt.Sprintf("Result %d", i), Author: "Someone"})
	}
	b.finder.results = many[:30]

	br := b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key('/'))
	b.screen.typed("lots")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "30 results")

	b.finder.results = many
	b.screen.press(key('M'))
	b.eventually(t, "30 more")
	b.eventually(t, "60 results")
	b.quit(t)

	if got := len(br.rows); got != 60 {
		t.Errorf("%d rows after loading more, want 60", got)
	}
	if br.selected != 0 {
		t.Errorf("the cursor moved to %d when more arrived", br.selected)
	}
}

// A page that repeats what is already there adds nothing twice.
func TestLoadingMoreDoesNotDuplicate(t *testing.T) {
	b := newBrowser(t)

	var many []media.Video
	for i := range 30 {
		many = append(many, media.Video{ID: fmt.Sprintf("vid%08d", i), Title: fmt.Sprintf("Result %d", i), Author: "Someone"})
	}
	b.finder.results = many

	br := b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key('/'))
	b.screen.typed("lots")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "30 results")

	// The same page again: nothing new in it.
	b.screen.press(key('M'))
	b.eventually(t, "that is all of it")
	b.quit(t)

	if got := len(br.rows); got != 30 {
		t.Errorf("%d rows after a page with nothing new, want 30", got)
	}
	if br.more != nil {
		t.Error("a screen with nothing more to give still offers more")
	}
}

// The dashboard shows what the feeds carried, so there is no next page of it.
func TestTheDashboardHasNoNextPage(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key('M'))
	b.eventually(t, "nothing more to load here")
	b.quit(t)
}

// "0 results" and "not finished looking" are different answers, and the second
// reads as the first.
func TestASearchInFlightSaysSo(t *testing.T) {
	b := newBrowser(t)

	// A search that does not return until told to.
	release := make(chan struct{})
	b.finder.before = func() { <-release }

	b.start(t)
	b.eventually(t, "nothing followed yet")
	b.screen.press(key('/'))
	b.screen.typed("slow")
	b.screen.press(named(term.KeyEnter))

	b.eventually(t, `searching for "slow"…`)
	if strings.Contains(b.screen.last(), "0 results") {
		t.Errorf("a search in flight reported no results:\n%s", b.screen.last())
	}

	close(release)
	b.eventually(t, "A Search Result")
	b.quit(t)
}

// An unchanged picture is not sent again. Re-sending one costs tens of
// kilobytes of escape sequence on every keypress, which a multiplexer between
// bivy and the terminal has to parse and keep in step with.
func TestAnUnchangedPictureIsNotSentAgain(t *testing.T) {
	b := newBrowser(t)
	b.screen.draws = graphics.Kitty
	b.app.art = b.art
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventuallyArt(t, "picture of aaaaaaaaaaa")
	sends := b.screen.pictureSends()

	// Keys that redraw the frame without changing which row is selected.
	for range 6 {
		b.screen.press(named(term.KeyUp))
	}
	b.eventuallyArt(t, "picture of aaaaaaaaaaa")
	b.quit(t)

	if got := b.screen.pictureSends(); got != sends {
		t.Errorf("the picture was sent %d more times while it had not changed", got-sends)
	}
}

// And a picture that does change is sent.
func TestAChangedPictureIsSent(t *testing.T) {
	b := newBrowser(t)
	b.screen.draws = graphics.Kitty
	b.app.art = b.art
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventuallyArt(t, "picture of aaaaaaaaaaa")
	b.screen.press(named(term.KeyDown))
	b.eventuallyArt(t, "picture of ccccccccccc")
	b.quit(t)

	if got := b.screen.pictureSends(); got < 2 {
		t.Errorf("the picture was sent %d times across two rows", got)
	}
}

// The tick bivy sets itself means "played to its end", and that is not the
// only way to be done with a video. One watched elsewhere, or abandoned two
// minutes in on purpose, is finished as far as the list is concerned.
func TestMarkingARowWatchedByHand(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")
	if strings.Contains(b.screen.last(), "✓") {
		t.Fatal("the fixture already has something marked, so this proves nothing")
	}

	b.screen.press(key('m'))
	b.eventually(t, "marked watched · The newest thing")
	b.eventually(t, "✓")
	b.quit(t)

	if !br.state.HasWatched("aaaaaaaaaaa") {
		t.Error("the mark never reached the state")
	}

	var saved follow.State
	if err := b.store.ReadJSON(stateFile, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.HasWatched("aaaaaaaaaaa") {
		t.Error("the mark was not written, so the next launch forgets it")
	}
}

// One keystroke to set and nothing to undo is a trap. The same key takes it
// back, and the status line says which way it went.
func TestMarkingTwiceTakesTheMarkBack(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")

	b.screen.press(key('m'))
	b.eventually(t, "marked watched · The newest thing")
	b.screen.press(key('m'))
	b.eventually(t, "no longer watched · The newest thing")
	b.quit(t)

	if br.state.HasWatched("aaaaaaaaaaa") {
		t.Error("the video is still watched after being unmarked")
	}

	var saved follow.State
	if err := b.store.ReadJSON(stateFile, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.HasWatched("aaaaaaaaaaa") {
		t.Error("the unmark was not written, so the next launch remembers a mark that was taken back")
	}
}

// A channel row carries a tick too, and it means followed rather than watched.
// Marking one is a keypress on the wrong row, and saying which key it wanted
// is cheaper than doing nothing.
func TestMarkingAChannelRowSaysWhichKeyItWanted(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")

	b.screen.press(key(':'))
	b.screen.typed("channels papa meat")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "2 channels for")

	b.screen.press(key('m'))
	b.eventually(t, "channels are followed, not watched")
	b.quit(t)
}

// Hiding what you have seen is what makes a dashboard a list of what is left
// rather than a list of everything.
func TestHidingWatchedVideos(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.eventually(t, "An older thing")

	// Mark the top row, then hide.
	b.screen.press(key('m'))
	b.eventually(t, "marked watched · The newest thing")

	b.screen.press(key(':'))
	b.screen.typed("watched")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "hiding 1 watched")

	if got := b.screen.last(); strings.Contains(got, "The newest thing") {
		t.Error("a watched video is still listed while watched videos are hidden")
	}
	b.eventually(t, "An older thing")
	b.quit(t)
}

// Anything hidden has to be reachable again, from the same command, or the
// list has quietly lost rows with no way back.
func TestShowingWatchedVideosAgain(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key('m'))
	b.eventually(t, "marked watched")

	b.screen.press(key(':'))
	b.screen.typed("watched")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "hiding 1 watched")

	b.screen.press(key(':'))
	b.screen.typed("watched")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "showing every video")
	b.eventually(t, "The newest thing")
	b.quit(t)
}

// Marking while hiding: the row goes, because the filter is a property of the
// list rather than of the moment it was last built.
func TestMarkingWhileHidingRemovesTheRow(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")

	b.screen.press(key(':'))
	b.screen.typed("watched")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "hiding watched videos · nothing here is watched yet")

	b.screen.press(key('m'))
	b.eventually(t, "marked watched · The newest thing")

	// Once, on the status line that just announced it — and not a second time
	// as a row, which is what hiding it means.
	if got := strings.Count(b.screen.last(), "The newest thing"); got != 1 {
		t.Errorf("the title appears %d times; want 1, the status line only", got)
	}
	b.eventually(t, "An older thing")
	b.quit(t)
}

// Every screen's rows go through one place, so the filter cannot be on for the
// dashboard and off for a search.
func TestHidingAppliesToSearchResultsToo(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")

	b.screen.press(key('/'))
	b.screen.typed("anything")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "A Search Result")
	b.eventually(t, "Another Result")

	b.screen.press(key('m'))
	b.eventually(t, "marked watched · A Search Result")

	b.screen.press(key(':'))
	b.screen.typed("watched")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "hiding 1 watched")

	if got := b.screen.last(); strings.Contains(got, "A Search Result") {
		t.Error("a watched search result is still listed while watched videos are hidden")
	}
	b.eventually(t, "Another Result")
	b.quit(t)
}

// The message that actually reaches people: the service refusing the
// extractor. It arrives wrapped in the extractor's own conventions — the video
// it failed on, and two links to its documentation — none of which belongs on
// a status line beside the row it failed for.
func TestTheRefusalReachesTheScreenInWordsAnyoneCanRead(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")

	b.player.events <- mpv.Event{
		Name:   "end-file",
		Reason: "error",
		Detail: "ERROR: [youtube] aaaaaaaaaaa: Sign in to confirm you're not a bot. " +
			"Use --cookies for the authentication. See  https://example.invalid/faq  " +
			"for how to pass cookies. Also see  https://example.invalid/tips  for tips",
	}
	b.eventually(t, "Sign in to confirm you're not a bot")
	b.quit(t)

	got := b.screen.last()
	if strings.Contains(got, "https://") {
		t.Error("the extractor's documentation links reached the status line")
	}
	if strings.Contains(got, "[youtube]") {
		t.Error("the extractor's own prefix reached the status line")
	}
}

func TestTidy(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"a bare message", "loading failed", "loading failed"},
		{
			"the extractor's prefix",
			"ERROR: [youtube] aaaaaaaaaaa: Sign in to confirm you're not a bot.",
			"Sign in to confirm you're not a bot.",
		},
		{
			"advice bivy will not take",
			"Sign in to confirm you're not a bot. Use --cookies for the authentication.",
			"Sign in to confirm you're not a bot.",
		},
		{
			"its documentation",
			"Something went wrong. See  https://example.invalid/faq  for more",
			"Something went wrong.",
		},
		{
			"a prefix that is not one",
			"[stream] Failed to open a file with spaces: it went badly",
			"[stream] Failed to open a file with spaces: it went badly",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tidy(tc.in); got != tc.want {
				t.Errorf("tidy(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A video finishing used to rebuild the dashboard over whatever was on screen,
// so watching something from a search threw the search away -- for a tick that
// could have been set on the row already there.
func TestFinishingAVideoDoesNotThrowAwayTheSearch(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "nothing followed yet")

	b.screen.press(key('/'))
	b.screen.typed("anything")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "A Search Result")

	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")
	b.player.events <- mpv.Event{Name: "end-file", Reason: "eof"}
	b.eventually(t, "watched · A Search Result")

	// Still the search: the results are there and the heading still names it.
	b.eventually(t, "Another Result")
	got := b.screen.last()
	if !strings.Contains(got, "A Search Result") {
		t.Error("the results went away when the video finished")
	}
	if !strings.Contains(got, "anything") {
		t.Errorf("the heading no longer names the search:\n%s", got)
	}
	// And the tick landed on the row that was played.
	if !strings.Contains(got, "✓") {
		t.Error("the watched row carries no tick")
	}
	b.quit(t)
}

// Holding a key queues presses faster than frames can be drawn. Drawing one
// frame each is what made the cursor go on travelling after the key came up,
// and no test could see it: the fake screen's channel is buffered and its
// picture source returns instantly, so production's back-pressure never
// appeared here.
func TestABurstOfPressesDrawsFewerFramesThanPresses(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	before := b.screen.frameCount()

	const presses = 20
	burst := make([]term.Press, 0, presses)
	for i := 0; i < presses; i++ {
		burst = append(burst, named(term.KeyDown))
	}
	b.screen.press(burst...)

	// Settled when the frame count has stopped moving, not after a fixed
	// wait: the point is what the loop did, not how fast this machine is.
	last, still := -1, 0
	deadline := time.After(5 * time.Second)
	for still < 10 {
		select {
		case <-deadline:
			t.Fatal("the screen never stopped redrawing")
		case <-time.After(5 * time.Millisecond):
		}
		if n := b.screen.frameCount(); n == last {
			still++
			continue
		} else {
			last, still = n, 0
		}
	}
	b.quit(t)

	if drawn := last - before; drawn >= presses {
		t.Errorf("%d presses drew %d frames; nothing is draining the queue", presses, drawn)
	}
}

// The whole feature, end to end: save a row, open the queue, find it there.
func TestSavingARowAndFindingItInTheQueue(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key('s'))
	b.eventually(t, "saved for later · The newest thing")

	b.screen.press(key(':'))
	b.screen.typed("queue")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "queue · 1 video saved")
	b.eventually(t, "The newest thing")
	b.quit(t)

	if !br.state.IsQueued("aaaaaaaaaaa") {
		t.Error("the video is not in the saved state")
	}
	var saved follow.State
	if err := b.store.ReadJSON(stateFile, &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.IsQueued("aaaaaaaaaaa") {
		t.Error("the queue was not written, so the next launch forgets it")
	}
}

// s is a toggle, so changing your mind costs the same keystroke as the choice.
func TestSavingTwiceTakesItBackOut(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key('s'))
	b.eventually(t, "saved for later")
	b.screen.press(key('s'))
	b.eventually(t, "no longer saved · The newest thing")
	b.quit(t)

	if br.state.IsQueued("aaaaaaaaaaa") {
		t.Error("the video is still saved after being unsaved")
	}
}

// In the queue, m means done: it marks the row watched and takes it out,
// because a ticked row sitting in a list of things to watch asks to be
// removed twice.
func TestMarkingInTheQueueTakesTheRowOut(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key('s'))
	b.eventually(t, "saved for later")

	b.screen.press(key(':'))
	b.screen.typed("queue")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "queue · 1 video saved")

	b.screen.press(key('m'))
	b.eventually(t, "queue · 0 videos saved")
	b.quit(t)

	if br.state.IsQueued("aaaaaaaaaaa") {
		t.Error("the row is still queued after being marked done")
	}
	if !br.state.HasWatched("aaaaaaaaaaa") {
		t.Error("the row was removed without being marked watched")
	}
}

// Watching to the end takes it out wherever it was played from: the queue is
// what is left to watch.
func TestFinishingAVideoTakesItOutOfTheQueue(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key('s'))
	b.eventually(t, "saved for later")

	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "playing ·")
	b.player.events <- mpv.Event{Name: "end-file", Reason: "eof"}
	b.eventually(t, "watched · The newest thing")
	b.quit(t)

	if br.state.IsQueued("aaaaaaaaaaa") {
		t.Error("finishing the video left it in the queue")
	}
}

// p plays down the list: a video reaching its end starts the next one.
func TestPlayingThroughTheQueue(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key('s'))
	b.eventually(t, "saved for later · The newest thing")
	b.screen.press(named(term.KeyDown), key('s'))
	b.eventually(t, "saved for later · An older thing")

	b.screen.press(key(':'))
	b.screen.typed("queue")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "queue · 2 videos saved")

	b.screen.press(key('p'))
	b.eventually(t, "playing · The newest thing")

	// The first reaching its end starts the second without another keystroke.
	b.player.events <- mpv.Event{Name: "end-file", Reason: "eof"}
	b.eventually(t, "playing · An older thing")

	b.player.events <- mpv.Event{Name: "end-file", Reason: "eof"}
	b.eventually(t, "that was the last one")
	b.quit(t)

	if got := len(b.player.watched()); got != 2 {
		t.Errorf("played %d videos, want both", got)
	}
}

// Closing the window is how you get out of a run through the list, so it must
// not start the next one. Otherwise leaving is a race against bivy.
func TestClosingAWindowStopsThePlayThrough(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(key('s'))
	b.eventually(t, "saved for later")
	b.screen.press(named(term.KeyDown), key('s'))
	b.eventually(t, "saved for later · An older thing")

	b.screen.press(key(':'))
	b.screen.typed("queue")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "queue · 2 videos saved")

	b.screen.press(key('p'))
	b.eventually(t, "playing · The newest thing")

	// What mpv reports when its window is closed.
	b.player.events <- mpv.Event{Name: "end-file", Reason: "quit"}
	b.eventually(t, "queue · 2 videos saved")
	b.quit(t)

	if got := len(b.player.watched()); got != 1 {
		t.Errorf("played %d videos; closing the window should have stopped at one", got)
	}
}
