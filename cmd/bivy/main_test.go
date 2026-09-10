package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bspeelm/bivy/internal/media"
	"github.com/bspeelm/bivy/internal/store"
)

const (
	chanA = "UCaaaaaaaaaaaaaaaaaaaaaa"
	chanB = "UCbbbbbbbbbbbbbbbbbbbbbb"
)

func at(day int) time.Time { return time.Date(2026, 9, day, 12, 0, 0, 0, time.UTC) }

// stubFeeds stands in for the network.
//
// A fake rather than a test server, and not only for speed: net/http may be
// imported by one package (PLAN.md §0), and reaching for httptest here would
// break the budget that makes the privacy claim checkable.
type stubFeeds struct {
	// Guarded because bivy fetches feeds concurrently, which is the whole
	// point of fetchAll. A test double for a concurrent caller is itself
	// concurrent code.
	mu       sync.Mutex
	channels map[string]media.Channel
	handles  map[string]string
	fetched  []string
	failWith error
}

func (s *stubFeeds) Fetch(_ context.Context, id string) (media.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.fetched = append(s.fetched, id)
	if s.failWith != nil {
		return media.Channel{}, s.failWith
	}
	ch, ok := s.channels[id]
	if !ok {
		return media.Channel{}, errors.New("no such channel")
	}
	return ch, nil
}

// setChannel replaces a channel's feed while bivy may be reading it.
func (s *stubFeeds) setChannel(id string, ch media.Channel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels[id] = ch
}

func (s *stubFeeds) removeChannel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.channels, id)
}

func (s *stubFeeds) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failWith = err
}

func (s *stubFeeds) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.fetched...)
}

func (s *stubFeeds) Resolve(_ context.Context, handle string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, ok := s.handles[handle]
	if !ok {
		return "", errors.New("no identifier on the page")
	}
	return id, nil
}

type harness struct {
	app   *app
	feeds *stubFeeds
	out   *bytes.Buffer
	errs  *bytes.Buffer
	home  string
	store *store.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))

	s, err := store.Open()
	if err != nil {
		t.Fatal(err)
	}

	feeds := &stubFeeds{
		channels: map[string]media.Channel{
			chanA: {ID: chanA, Title: "Aye", Videos: []media.Video{
				{ID: "aaaaaaaaaaa", Title: "The newest thing", Published: at(9)},
				{ID: "ccccccccccc", Title: "An older thing", Published: at(3)},
			}},
			chanB: {ID: chanB, Title: "Bee", Videos: []media.Video{
				{ID: "bbbbbbbbbbb", Title: "From the other one", Published: at(8)},
			}},
		},
		handles: map[string]string{"@aye": chanA},
	}

	h := &harness{
		feeds: feeds,
		out:   &bytes.Buffer{},
		errs:  &bytes.Buffer{},
		home:  home,
		store: s,
	}
	h.app = &app{
		store:  s,
		feeds:  feeds,
		now:    func() time.Time { return at(10) },
		out:    h.out,
		errOut: h.errs,
		width:  80,
	}
	return h
}

func (h *harness) run(t *testing.T, args ...string) string {
	t.Helper()
	h.out.Reset()
	h.errs.Reset()
	if code := h.app.run(context.Background(), args); code != 0 {
		t.Fatalf("bivy %v exited %d: %s", args, code, h.errs)
	}
	return h.out.String()
}

func (h *harness) code(args ...string) (int, string) {
	h.out.Reset()
	h.errs.Reset()
	code := h.app.run(context.Background(), args)
	return code, h.out.String() + h.errs.String()
}

func TestFollowThenDashboard(t *testing.T) {
	h := newHarness(t)

	if got := h.run(t, "follow", chanA); !strings.Contains(got, "following Aye") {
		t.Errorf("follow said %q", got)
	}

	// Followed at day 10, so nothing the feed already carries is new.
	got := h.run(t)
	if !strings.Contains(got, "The newest thing") {
		t.Errorf("the dashboard does not list the channel's entries:\n%s", got)
	}
	if strings.Contains(got, "•") {
		t.Errorf("entries that existed before the follow are marked new:\n%s", got)
	}

	// A later entry is new the next time.
	h.feeds.setChannel(chanA, media.Channel{ID: chanA, Title: "Aye", Videos: []media.Video{
		{ID: "ddddddddddd", Title: "Posted since", Published: at(11)},
	}})
	h.app.now = func() time.Time { return at(12) }

	got = h.run(t)
	if !strings.Contains(got, "1 new video") {
		t.Errorf("the new entry was not marked:\n%s", got)
	}

	// And not new the time after that.
	if got := h.run(t); !strings.Contains(got, "nothing new") {
		t.Errorf("the entry is still new on a second look:\n%s", got)
	}
}

func TestFollowResolvesAHandle(t *testing.T) {
	h := newHarness(t)

	if got := h.run(t, "follow", "@aye"); !strings.Contains(got, "following Aye") {
		t.Errorf("follow said %q", got)
	}
	if got := h.run(t, "list"); !strings.Contains(got, chanA) {
		t.Errorf("list said %q", got)
	}
}

// Failing to resolve a handle is a normal outcome and the message says what to
// do instead. It is the first thing a person will hit.
func TestFollowSaysWhatToDoWhenAHandleCannotBeResolved(t *testing.T) {
	h := newHarness(t)

	code, out := h.code("follow", "@unknown")
	if code == 0 {
		t.Fatal("an unresolvable handle reported success")
	}
	if !strings.Contains(out, "/channel/") {
		t.Errorf("the message does not say what to do instead:\n%s", out)
	}
}

func TestFollowRefusesWhatIsNotAChannel(t *testing.T) {
	h := newHarness(t)

	for _, bad := range []string{"nonsense", "--exec=touch /tmp/pwned", "-rf", "https://example.com/nothing"} {
		if code, out := h.code("follow", bad); code == 0 {
			t.Errorf("follow %q reported success: %s", bad, out)
		}
	}
	if asked := h.feeds.asked(); len(asked) != 0 {
		t.Errorf("a refused argument still reached the network: %v", asked)
	}
}

// A channel that cannot be fetched is refused now rather than sitting on the
// dashboard as a failure forever.
func TestFollowRefusesAChannelThatCannotBeFetched(t *testing.T) {
	h := newHarness(t)

	if code, _ := h.code("follow", "UCzzzzzzzzzzzzzzzzzzzzzz"); code == 0 {
		t.Fatal("a channel that does not exist was followed")
	}
	if got := h.run(t, "list"); !strings.Contains(got, "following nothing") {
		t.Errorf("it was saved anyway: %q", got)
	}
}

func TestFollowingTwiceSaysSo(t *testing.T) {
	h := newHarness(t)
	h.run(t, "follow", chanA)

	got := h.run(t, "follow", chanA)
	if !strings.Contains(got, "already following") {
		t.Errorf("the second follow said %q", got)
	}
}

func TestUnfollow(t *testing.T) {
	h := newHarness(t)
	h.run(t, "follow", chanA)
	h.run(t, "follow", chanB)

	if got := h.run(t, "unfollow", chanA); !strings.Contains(got, "unfollowed Aye") {
		t.Errorf("unfollow said %q", got)
	}
	list := h.run(t, "list")
	if strings.Contains(list, chanA) {
		t.Errorf("it is still followed:\n%s", list)
	}
	if !strings.Contains(list, chanB) {
		t.Errorf("the other channel went too:\n%s", list)
	}
}

// Unfollowing by the name on the screen is what a person will try, and the
// identifier is not on the dashboard to copy.
func TestUnfollowByName(t *testing.T) {
	h := newHarness(t)
	h.run(t, "follow", chanA)

	if got := h.run(t, "unfollow", "aye"); !strings.Contains(got, "unfollowed Aye") {
		t.Errorf("unfollow said %q", got)
	}
}

func TestUnfollowSomethingNotFollowed(t *testing.T) {
	h := newHarness(t)

	if code, out := h.code("unfollow", "whatever"); code == 0 {
		t.Errorf("unfollowing something not followed reported success: %s", out)
	}
}

// One unreachable channel does not cost the dashboard, and the failure is
// reported rather than leaving the list quietly short.
func TestOneUnreachableChannelDoesNotCostTheDashboard(t *testing.T) {
	h := newHarness(t)
	h.run(t, "follow", chanA)
	h.run(t, "follow", chanB)

	h.feeds.removeChannel(chanB)

	got := h.run(t)
	if !strings.Contains(got, "The newest thing") {
		t.Errorf("the reachable channel's entries are missing:\n%s", got)
	}
	if !strings.Contains(got, "could not be reached") {
		t.Errorf("the failure was not reported:\n%s", got)
	}
	if !strings.Contains(got, "Bee") {
		t.Errorf("the failure does not name the channel:\n%s", got)
	}
}

// A network blip must not silently consume the thing the user launched bivy to
// see.
func TestAFailedFetchDoesNotConsumeWhatIsNew(t *testing.T) {
	h := newHarness(t)
	h.run(t, "follow", chanA)

	h.feeds.setChannel(chanA, media.Channel{ID: chanA, Title: "Aye", Videos: []media.Video{
		{ID: "ddddddddddd", Title: "Posted since", Published: at(11)},
	}})
	h.app.now = func() time.Time { return at(12) }

	h.feeds.fail(errors.New("the network is down"))
	if _, out := h.code(); !strings.Contains(out, "could not be reached") {
		t.Errorf("the outage was not reported:\n%s", out)
	}

	h.feeds.fail(nil)
	if got := h.run(t); !strings.Contains(got, "1 new video") {
		t.Errorf("the entry stopped being new while the network was down:\n%s", got)
	}
}

func TestDashboardWithNothingFollowed(t *testing.T) {
	h := newHarness(t)

	got := h.run(t)
	if !strings.Contains(got, "bivy follow") {
		t.Errorf("the empty dashboard does not say what to do:\n%s", got)
	}
}

func TestUnknownCommand(t *testing.T) {
	h := newHarness(t)

	code, out := h.code("wat")
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(out, "bivy follow") {
		t.Errorf("the usage was not shown:\n%s", out)
	}
}

func TestHelpAndVersion(t *testing.T) {
	h := newHarness(t)

	if got := h.run(t, "help"); !strings.Contains(got, "bivy follow") {
		t.Errorf("help said %q", got)
	}
	if got := h.run(t, "version"); !strings.Contains(got, Version) {
		t.Errorf("version said %q", got)
	}
}

// Version must stay a plain string literal: -X does nothing to a variable
// initialised by a function call, and says so silently while four builders
// stamp the same flag.
func TestVersionStaysStampable(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "var Version = \"") {
		t.Error("Version is not a plain string literal; -X will quietly stop working")
	}
}

// The isolation test PLAN.md §8 asks for.
//
// A first launch against a scratch home directory, doing everything bivy can
// currently do, and then every path it left behind is compared against the two
// directories it is allowed to write. "Removable without residue" is a promise
// worth keeping, and this is the command that disagrees when it stops being
// true.
func TestFirstLaunchWritesOnlyTheAllowedDirectories(t *testing.T) {
	h := newHarness(t)

	h.run(t, "follow", chanA)
	h.run(t, "follow", "@aye")
	h.run(t)
	h.run(t, "list")
	h.run(t, "unfollow", chanA)
	h.run(t)

	allowed := h.store.Dirs()
	var strays []string
	err := filepath.WalkDir(h.home, func(path string, d os.DirEntry, err error) error {
		if err != nil || path == h.home {
			return err
		}
		for _, dir := range allowed {
			// The directory itself, its parents, or something inside it.
			if path == dir || strings.HasPrefix(path, dir+string(filepath.Separator)) ||
				strings.HasPrefix(dir, path+string(filepath.Separator)) {
				return nil
			}
		}
		strays = append(strays, strings.TrimPrefix(path, h.home))
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(strays) > 0 {
		t.Errorf("bivy wrote outside the two directories §8 allows: %v", strays)
	}

	// No cache directory, on any platform (ADR-007).
	for _, forbidden := range []string{".cache", "Library/Caches"} {
		if _, err := os.Stat(filepath.Join(h.home, forbidden)); err == nil {
			t.Errorf("bivy created %s", forbidden)
		}
	}

	// And nothing half-written left in the directory it does own.
	entries, err := os.ReadDir(h.store.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != stateFile {
			t.Errorf("the data directory holds %q, which bivy does not clean up", e.Name())
		}
	}
}

func TestTarget(t *testing.T) {
	for _, tc := range []struct {
		in, id, handle string
		wantErr        bool
	}{
		{in: chanA, id: chanA},
		{in: "  " + chanA + "  ", id: chanA},
		{in: "@someone", handle: "@someone"},
		{in: "https://www.youtube.com/channel/" + chanA, id: chanA},
		{in: "https://www.youtube.com/channel/" + chanA + "/videos", id: chanA},
		{in: "https://www.youtube.com/@someone", handle: "@someone"},
		{in: "https://www.youtube.com/@someone/videos", handle: "@someone"},
		{in: "https://www.youtube.com/feeds/videos.xml?channel_id=" + chanA, id: chanA},
		{in: "", wantErr: true},
		{in: "nonsense", wantErr: true},
		{in: "-rf", wantErr: true},
		{in: "--exec=touch /tmp/pwned", wantErr: true},
		{in: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", wantErr: true},
		{in: "https://example.com/", wantErr: true},
	} {
		id, handle, err := target(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("target(%q) = (%q, %q), want an error", tc.in, id, handle)
			}
			continue
		}
		if err != nil {
			t.Errorf("target(%q): %v", tc.in, err)
			continue
		}
		if id != tc.id || handle != tc.handle {
			t.Errorf("target(%q) = (%q, %q), want (%q, %q)", tc.in, id, handle, tc.id, tc.handle)
		}
		if (id == "") == (handle == "") {
			t.Errorf("target(%q) returned both or neither: (%q, %q)", tc.in, id, handle)
		}
	}
}

// Nothing bivy accepts begins with a dash, and refusing here is what keeps an
// option-shaped string away from the argv a later milestone builds (§7).
func TestTargetRefusesOptionShapedInput(t *testing.T) {
	for _, bad := range []string{"-rf", "--exec=touch /tmp/pwned", "-@handle", "--"} {
		if _, _, err := target(bad); err == nil {
			t.Errorf("target(%q) returned no error", bad)
		}
	}
}
