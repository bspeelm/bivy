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
	"github.com/bspeelm/bivy/internal/ytdlp"
)

// fakeScreen is a terminal that draws into a buffer and takes its keypresses
// from a script. internal/term proves the decoding; this proves the loop.
type fakeScreen struct {
	keys   chan term.Press
	resize chan struct{}

	mu     sync.Mutex
	frames []string
	closed bool
}

// key is one character pressed; named is one of the keys that is not a
// character.
func key(r rune) term.Press       { return term.Press{Key: term.KeyRune, Rune: r} }
func named(k term.Key) term.Press { return term.Press{Key: k} }

func newScreen() *fakeScreen {
	return &fakeScreen{keys: make(chan term.Press, 64), resize: make(chan struct{}, 1)}
}

func (s *fakeScreen) Size() (int, int)         { return 80, 24 }
func (s *fakeScreen) Keys() <-chan term.Press  { return s.keys }
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
	mu      sync.Mutex
	results []media.Video
	err     error
	asked   []string
}

func (s *stubSearch) Search(_ context.Context, query string, _ int) ([]media.Video, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked = append(s.asked, query)
	if s.err != nil {
		return nil, s.err
	}
	return s.results, nil
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
	done   chan int
}

func newBrowser(t *testing.T) *browserHarness {
	t.Helper()

	h := newHarness(t)
	bh := &browserHarness{
		harness: h,
		screen:  newScreen(),
		player:  newPlayer(),
		finder: &stubSearch{results: []media.Video{
			{ID: "sssssssssss", Title: "A Search Result", Author: "Someone", Duration: 89 * time.Second},
			{ID: "ttttttttttt", Title: "Another Result", Author: "Someone Else", Duration: 3 * time.Minute},
		}},
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
	b.screen.press(named(term.KeyEnter), named(term.KeyDown), named(term.KeyEnter))
	b.quit(t)

	if got := len(b.player.watched()); got != 0 {
		t.Errorf("%d videos played from an empty dashboard", got)
	}
}

// The list's own quit key, which the helper does not use because it does not
// work from the search box.
func TestQQuitsFromTheList(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "Nothing to show")
	b.screen.press(key('q'))

	select {
	case <-b.done:
	case <-time.After(5 * time.Second):
		t.Fatal("q did not quit the list")
	}
}

// And must not, from the box.
func TestQDoesNotQuitFromTheSearchBox(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "Nothing to show")
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
	b.eventually(t, "2 results for terminal video")
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
	b.eventually(t, "Nothing to show")
	b.screen.press(key('/'))
	b.screen.typed("cats")
	b.eventually(t, ":search cats")

	b.screen.press(named(term.KeyBackspace), named(term.KeyBackspace))
	b.eventually(t, ":search ca_")

	// Backspacing past the start eats the command name too — it is one line,
	// not a box with a label on it — and then stops rather than crashing.
	for range 20 {
		b.screen.press(named(term.KeyBackspace))
	}
	b.eventually(t, ":_")
	b.quit(t)
}

// A search box that takes characters it will then refuse is worse than one
// that stops.
func TestTheSearchBoxStopsAtItsLimit(t *testing.T) {
	b := newBrowser(t)

	br := b.start(t)
	b.eventually(t, "Nothing to show")
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
	b.eventually(t, "Nothing to show")
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
	b.eventually(t, "Nothing to show")
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
	b.eventually(t, "Nothing to show")
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
	b.eventually(t, "Nothing to show")
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
	b.eventually(t, "Nothing to show")
	b.screen.press(key(':'), key('f'), named(term.KeyTab))
	b.eventually(t, ":follow _")

	// Escape abandons the line rather than leaving it to reappear next time.
	b.screen.press(named(term.KeyEscape))
	b.eventually(t, "Nothing to show")
	b.quit(t)

	if br.line != "" {
		t.Errorf("an abandoned line survived as %q", br.line)
	}
}

func TestAnUnknownCommandIsReportedOnScreen(t *testing.T) {
	b := newBrowser(t)

	b.start(t)
	b.eventually(t, "Nothing to show")
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
	b.eventually(t, "Nothing to show")
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
	b.eventually(t, "Nothing to show")
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
	b.eventually(t, "Nothing to show")
	b.screen.press(key(':'))
	b.screen.typed("follow Nobody At All")
	b.screen.press(named(term.KeyEnter))
	b.eventually(t, "no channel called")
	b.quit(t)

	if asked := b.feeds.asked(); len(asked) != 0 {
		t.Errorf("a name that resolved to nothing still reached the network: %v", asked)
	}
}
