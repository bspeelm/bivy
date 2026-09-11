package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bspeelm/bivy/internal/follow"
	"github.com/bspeelm/bivy/internal/media"
	"github.com/bspeelm/bivy/internal/mpv"
	"github.com/bspeelm/bivy/internal/term"
)

// fakeScreen is a terminal that draws into a buffer and takes its keypresses
// from a script. internal/term proves the decoding; this proves the loop.
type fakeScreen struct {
	keys   chan term.Key
	resize chan struct{}

	mu     sync.Mutex
	frames []string
	closed bool
}

func newScreen() *fakeScreen {
	return &fakeScreen{keys: make(chan term.Key, 16), resize: make(chan struct{}, 1)}
}

func (s *fakeScreen) Size() (int, int)         { return 80, 24 }
func (s *fakeScreen) Keys() <-chan term.Key    { return s.keys }
func (s *fakeScreen) Resized() <-chan struct{} { return s.resize }

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

func (s *fakeScreen) press(keys ...term.Key) {
	for _, k := range keys {
		s.keys <- k
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

// browserHarness is a session with both ends faked.
type browserHarness struct {
	*harness
	screen *fakeScreen
	player *fakePlayer
	done   chan int
}

func newBrowser(t *testing.T) *browserHarness {
	t.Helper()

	h := newHarness(t)
	bh := &browserHarness{
		harness: h,
		screen:  newScreen(),
		player:  newPlayer(),
		done:    make(chan int, 1),
	}
	h.app.newPlayer = func(context.Context) (player, error) { return bh.player, nil }
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

func (b *browserHarness) quit(t *testing.T) {
	t.Helper()
	b.screen.press(term.KeyQuit)
	select {
	case <-b.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser did not stop when quit was pressed")
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
	b.screen.press(term.KeyDown, term.KeyEnter)
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
	b.screen.press(term.KeyEnter)
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

func TestAVideoThatWasStoppedIsNotMarkedWatched(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	br := b.start(t)
	b.eventually(t, "The newest thing")
	b.screen.press(term.KeyEnter)
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
	b.screen.press(term.KeyEnter)
	b.eventually(t, "playing ·")
	b.screen.press(term.KeyDown, term.KeyEnter)
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
	b.screen.press(term.KeyEnter)
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
	b.screen.press(term.KeyEnter)
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
	b.screen.press(term.KeyEnter)
	b.eventually(t, "playing ·")

	close(players[0].events)
	b.screen.press(term.KeyEnter)
	b.eventually(t, "playing ·")
	b.quit(t)

	if starts != 2 {
		t.Errorf("mpv was started %d times, want 2", starts)
	}
	if got := len(players[1].watched()); got != 1 {
		t.Errorf("the replacement played %d videos, want 1", got)
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
	b.screen.press(term.KeyEnter)
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
	b.screen.press(term.KeyEnter)
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

	b.screen.press(term.KeyUp, term.KeyUp, term.KeyUp)
	b.eventually(t, "> ")
	b.screen.press(term.KeyBottom, term.KeyDown, term.KeyDown)
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

	b.screen.press(term.KeyRefresh)
	b.eventually(t, "Posted while bivy was open")
	b.quit(t)
}

func TestResizeRedraws(t *testing.T) {
	b := newBrowser(t)
	b.run(t, "follow", chanA)

	b.start(t)
	b.eventually(t, "The newest thing")

	before := b.screen.frameCount()
	b.screen.resize <- struct{}{}

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
	b.eventually(t, "Nothing to show")
	b.screen.press(term.KeyEnter, term.KeyDown, term.KeyEnter)
	b.quit(t)

	if got := len(b.player.watched()); got != 0 {
		t.Errorf("%d videos played from an empty dashboard", got)
	}
}
