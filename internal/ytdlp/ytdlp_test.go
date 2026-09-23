package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bspeelm/bivy/internal/pushback"
)

// The extractor has options that run arbitrary shell commands, so what may
// reach its argv is the load-bearing question in this package (PLAN.md §7).
func TestSearchTermIsNeverOptionShaped(t *testing.T) {
	for _, query := range []string{
		"ordinary query",
		"--exec=touch /tmp/pwned",
		"-rf",
		"--cookies-from-browser firefox",
		"; rm -rf /",
		"$(whoami)",
		"`id`",
		"--",
	} {
		term, err := SearchTerm(query, 5)
		if err != nil {
			// Refused outright is the other acceptable answer.
			continue
		}
		if strings.HasPrefix(term, "-") {
			t.Errorf("SearchTerm(%q) = %q, which an argument parser reads as an option", query, term)
		}
		if !strings.HasPrefix(term, "ytsearch") {
			t.Errorf("SearchTerm(%q) = %q, which is not a search term", query, term)
		}
	}
}

// A leading dash is refused rather than merely neutralised by the prefix. The
// prefix is the guarantee; this is the belt for it.
func TestSearchTermRefusesALeadingDash(t *testing.T) {
	for _, query := range []string{"-rf", "--exec=touch /tmp/pwned", "-"} {
		if _, err := SearchTerm(query, 5); err == nil {
			t.Errorf("SearchTerm(%q) returned no error", query)
		}
	}
}

// A query is typed by the user but travels to a subprocess and back to a
// terminal. Control characters are removed on the way in as well as out.
func TestSearchTermStripsControlCharacters(t *testing.T) {
	term, err := SearchTerm("before\x1b[31m after\x00", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range term {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Errorf("SearchTerm kept %U in %q", r, term)
		}
	}
}

func TestSearchTermRefusesNothing(t *testing.T) {
	for _, query := range []string{"", "   ", "\x00\x1b"} {
		if _, err := SearchTerm(query, 5); err == nil {
			t.Errorf("SearchTerm(%q) returned no error", query)
		}
	}
}

func TestSearchTermBoundsTheCount(t *testing.T) {
	for _, tc := range []struct {
		limit int
		want  string
	}{
		{5, "ytsearch5:cats"},
		{1, "ytsearch1:cats"},
		{0, "ytsearch30:cats"},
		{-1, "ytsearch30:cats"},
		{9999, "ytsearch9999:cats"},
	} {
		got, err := SearchTerm("cats", tc.limit)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("SearchTerm(cats, %d) = %q, want %q", tc.limit, got, tc.want)
		}
	}
}

func TestSearchTermRefusesAnEssay(t *testing.T) {
	if _, err := SearchTerm(strings.Repeat("a", 300), 5); err == nil {
		t.Error("a 300-character query was accepted")
	}
}

// The shape the extractor actually emits, taken from a real run: a flat search
// result has a duration and no publish timestamp.
const sampleOutput = `{"id":"kYJ8r4d6gy0","title":"Watch Videos In Your Linux Terminal","channel":"DistroTube","uploader":"DistroTube","channel_id":"UCVls1GmFKf6WlTraIb_IaJg","duration":89,"timestamp":null,"view_count":23065,"_type":"url"}
{"id":"dQw4w9WgXcQ","title":"Another One","channel":"Someone","channel_id":"UCabcdefghijklmnopqrstuv","duration":212.0,"timestamp":1757000000}
`

func TestParseReadsResults(t *testing.T) {
	videos := Parse(strings.NewReader(sampleOutput))

	if got, want := len(videos), 2; got != want {
		t.Fatalf("%d results, want %d", got, want)
	}

	first := videos[0]
	if got, want := first.ID, "kYJ8r4d6gy0"; got != want {
		t.Errorf("id = %q, want %q", got, want)
	}
	if got, want := first.Title, "Watch Videos In Your Linux Terminal"; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
	if got, want := first.Author, "DistroTube"; got != want {
		t.Errorf("author = %q, want %q", got, want)
	}
	if got, want := first.Duration, 89*time.Second; got != want {
		t.Errorf("duration = %s, want %s", got, want)
	}
	// A flat search result carries no publish time, and one is not invented.
	if !first.Published.IsZero() {
		t.Errorf("published = %s, want it left unset", first.Published)
	}
	if got, want := first.Thumbnail, "https://i.ytimg.com/vi/kYJ8r4d6gy0/hqdefault.jpg"; got != want {
		t.Errorf("thumbnail = %q, want %q", got, want)
	}

	if videos[1].Published.IsZero() {
		t.Error("a result that did carry a timestamp lost it")
	}
}

// One bad line costs one row. The extractor is a separate program whose output
// format is not bivy's to guarantee.
func TestParseDropsWhatItCannotUse(t *testing.T) {
	const mixed = `not json at all
{"id":"../../etc/passwd","title":"Path"}
{"id":"kYJ8r4d6gy0","title":"Fine"}
{"id":"","title":"Empty"}
{"id":"toolongtobeanid","title":"Long"}
`
	videos := Parse(strings.NewReader(mixed))
	if got, want := len(videos), 1; got != want {
		t.Fatalf("%d results kept, want %d", got, want)
	}
	if got, want := videos[0].Title, "Fine"; got != want {
		t.Errorf("kept %q, want %q", got, want)
	}
}

// A title from a search result is remote text on its way to a terminal, the
// same as one from a feed.
func TestParseStripsControlCharactersFromResults(t *testing.T) {
	const hostile = `{"id":"kYJ8r4d6gy0","title":"a2Jb","channel":"cd"}`

	videos := Parse(strings.NewReader(hostile))
	if len(videos) != 1 {
		t.Fatalf("%d results, want 1", len(videos))
	}
	for _, s := range []string{videos[0].Title, videos[0].Author} {
		for _, r := range s {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				t.Errorf("%q reached the caller carrying %U", s, r)
			}
		}
	}
}

// A thumbnail address is derived, so a compromised extractor cannot aim the
// fetch a later milestone makes.
func TestParseDerivesTheThumbnailAddress(t *testing.T) {
	const redirect = `{"id":"kYJ8r4d6gy0","title":"T","thumbnails":[{"url":"https://attacker.example/track"}]}`

	videos := Parse(strings.NewReader(redirect))
	if got, want := videos[0].Thumbnail, "https://i.ytimg.com/vi/kYJ8r4d6gy0/hqdefault.jpg"; got != want {
		t.Errorf("thumbnail = %q, want %q", got, want)
	}
}

// A channel identifier that is not one is discarded rather than carried: it
// would otherwise be interpolated into a feed URL by `follow`.
func TestParseDiscardsAChannelIdItDoesNotBelieve(t *testing.T) {
	const lying = `{"id":"kYJ8r4d6gy0","title":"T","channel_id":"--exec=touch /tmp/pwned"}`

	videos := Parse(strings.NewReader(lying))
	if videos[0].ChannelID != "" {
		t.Errorf("channel id = %q, want it discarded", videos[0].ChannelID)
	}
}

func TestParseHandlesNothing(t *testing.T) {
	if got := Parse(strings.NewReader("")); len(got) != 0 {
		t.Errorf("%d results from no output", len(got))
	}
}

// The extractor is absent on a machine that has only ever used the dashboard,
// which never needs it. That is a normal state with a name, not a crash.
func TestSearchReportsAMissingExtractor(t *testing.T) {
	c := &Client{Binary: "no-such-extractor-anywhere"}

	_, err := c.Search(context.Background(), "cats", 5)
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("error = %v, want ErrNotInstalled", err)
	}
}

func TestSearchRefusesABadQueryBeforeRunningAnything(t *testing.T) {
	c := &Client{Binary: "no-such-extractor-anywhere"}

	// Refused by SearchTerm, so the missing binary is never even reached.
	_, err := c.Search(context.Background(), "-rf", 5)
	if err == nil {
		t.Fatal("an option-shaped query returned no error")
	}
	if errors.Is(err, ErrNotInstalled) {
		t.Error("the query reached the process instead of being refused first")
	}
}

func TestSearchHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := &Client{Binary: "no-such-extractor-anywhere"}
	if _, err := c.Search(ctx, "cats", 5); err == nil {
		t.Fatal("a cancelled context still ran a search")
	}
}

// The identifier is checked before it is interpolated into a URL that becomes
// an argv.
func TestUploadsRefusesSomethingThatIsNotAChannel(t *testing.T) {
	c := &Client{Binary: "no-such-extractor-anywhere"}

	for _, bad := range []string{"", "nonsense", "--exec=touch /tmp/pwned", "UC../../etc/passwd"} {
		_, err := c.Uploads(context.Background(), bad, 5)
		if err == nil {
			t.Errorf("Uploads(%q) returned no error", bad)
		}
		if errors.Is(err, ErrNotInstalled) {
			t.Errorf("Uploads(%q) reached the process instead of being refused", bad)
		}
	}
}

// A channel listing carries durations and no publish times, which is what the
// fallback has to live with.
func TestUploadsParsesWhatAChannelListingGives(t *testing.T) {
	const listing = `{"id":"jH2_omQcJGI","title":"Ranking EVERY Pizza","duration":2842,"timestamp":null}
{"id":"yoIObGNixd0","title":"The Horrors Of Burning Man","duration":1900}
`
	videos := Parse(strings.NewReader(listing))
	if len(videos) != 2 {
		t.Fatalf("%d videos, want 2", len(videos))
	}
	if videos[0].Duration == 0 {
		t.Error("the duration was lost")
	}
	if !videos[0].Published.IsZero() {
		t.Error("a publish time was invented")
	}
}

// standInExtractor is a script that reports its own argv and whether the
// cookie it was handed existed when it ran.
func standInExtractor(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "stand-in")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\"\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  if [ \"$1\" = \"--cookies\" ]; then\n" +
		"    [ -f \"$2\" ] && printf 'JAR EXISTS\\n' && cat \"$2\"\n" +
		"  fi\n" +
		"  shift\n" +
		"done\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// Search goes through the extractor too, so it carries the session's visitor
// like everything else (ADR-018). What must never appear here is an account.
func TestEverySearchIsTheSameVisitor(t *testing.T) {
	c := &Client{Binary: standInExtractor(t)}

	var ids []string
	for i := 0; i < 2; i++ {
		out, err := c.run(context.Background(), "--", "ytsearch1:anything")
		if err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(out, "--cookies") {
			t.Fatalf("the extractor was given no cookie: %q", out)
		}
		if !strings.Contains(out, "JAR EXISTS") {
			t.Fatal("the cookie file did not exist while the extractor ran")
		}
		if !strings.Contains(out, "VISITOR_INFO1_LIVE") {
			t.Errorf("the jar holds no visitor identifier: %q", out)
		}
		for _, account := range []string{"SAPISID", "LOGIN_INFO"} {
			if strings.Contains(out, account) {
				t.Errorf("the jar carries %s, which is an account (ADR-004)", account)
			}
		}

		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "VISITOR_INFO1_LIVE") {
				fields := strings.Split(line, "\t")
				ids = append(ids, fields[len(fields)-1])
			}
		}
	}

	if len(ids) != 2 {
		t.Fatalf("read %d identifiers, want 2", len(ids))
	}
	if ids[0] != ids[1] {
		t.Errorf("the searches went out as %q and %q, and a session is one visitor", ids[0], ids[1])
	}
}

// The jar is written outside the two directories §8 allows, so it has to go
// when the search does -- including when the search fails, which is the path
// that forgets.
func TestASearchLeavesNoJarBehind(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	for _, c := range []*Client{
		{Binary: standInExtractor(t)},
		{Binary: "no-such-extractor-anywhere"},
	} {
		_, _ = c.run(context.Background(), "--", "ytsearch1:anything")
	}

	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "bivy-") {
			t.Errorf("a search left %s behind in the temporary directory", e.Name())
		}
	}
}

// The bug this exists to stop coming back: the ceiling was also the size of a
// page, so the first request already reached it and every "thirty more" was
// clamped to the same thirty rows. M could not add a row in any view, and the
// tests did not notice because their stub ignored the limit.
func TestAskingForASecondPageAsksForMoreThanTheFirst(t *testing.T) {
	first, err := SearchTerm("cats", 30)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SearchTerm("cats", 60)
	if err != nil {
		t.Fatal(err)
	}

	if first == second {
		t.Fatalf("both pages asked for %q, so the second can only repeat the first", first)
	}
	if second != "ytsearch60:cats" {
		t.Errorf("the second page asked for %q, want ytsearch60:cats", second)
	}
	// Nothing above one page is clamped at all. Measured against the live
	// service on 2026-09-16: ytsearch300 returns 300 rows in 9s and
	// ytsearch1000 returns 585 in 20s -- the service runs out around six
	// hundred, well inside the 45s any one query is given. A ceiling here
	// could only ever stop paging earlier than that, for no reason anybody
	// could name, and loadMore already stops when a page adds nothing.
	deep, err := SearchTerm("cats", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if deep != "ytsearch5000:cats" {
		t.Errorf("a deep page asked for %q; bivy is imposing a ceiling of its own", deep)
	}
}

// refusingExtractor is a stand-in that fails the way the extractor does when
// the service wants a human: a line on stderr and a non-zero exit.
func refusingExtractor(t *testing.T, complaint string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "stand-in")
	script := "#!/bin/sh\nprintf '%s\\n' " + strconv.Quote(complaint) + " >&2\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// The extractor sees the challenge before bivy does: it asks for the page bivy
// never fetches itself. What it says on stderr is the signal (ADR-017).
func TestAChallengeOnStderrShutsTheGate(t *testing.T) {
	for complaint, want := range map[string]string{
		"ERROR: [youtube] aaaaaaaaaaa: Sign in to confirm you're not a bot":    pushback.BotCheck,
		"ERROR: Unable to download webpage: HTTP Error 429: Too Many Requests": pushback.RateLimited,
	} {
		gate := &pushback.Gate{}
		c := &Client{Binary: refusingExtractor(t, complaint), Gate: gate}

		_, err := c.Search(context.Background(), "anything", 1)
		if !errors.Is(err, pushback.ErrStopped) {
			t.Errorf("%q reported %v, want ErrStopped", complaint, err)
		}
		if reason, _, ok := gate.Tripped(); !ok || reason != want {
			t.Errorf("%q left the gate at (%q, %v), want %q", complaint, reason, ok, want)
		}
	}
}

// An ordinary failure is not push-back. A gate that shut on one would end a
// session because a single video was private.
func TestAnOrdinaryFailureLeavesTheGateOpen(t *testing.T) {
	gate := &pushback.Gate{}
	c := &Client{Binary: refusingExtractor(t, "ERROR: [youtube] aaaaaaaaaaa: Video unavailable"), Gate: gate}

	if _, err := c.Search(context.Background(), "anything", 1); errors.Is(err, pushback.ErrStopped) {
		t.Error("an unavailable video was read as push-back")
	}
	if gate.Shut() {
		t.Error("an unavailable video shut the gate")
	}
}

// Once the gate is shut the extractor is not run at all: not asking again is
// the whole of the remedy.
func TestTheExtractorIsNotRunOnceTheGateIsShut(t *testing.T) {
	gate := &pushback.Gate{}
	gate.Trip(pushback.RateLimited)
	c := &Client{Binary: standInExtractor(t), Gate: gate}

	for _, ask := range []func() error{
		func() error { _, err := c.Search(context.Background(), "anything", 1); return err },
		func() error { _, err := c.Channels(context.Background(), "anything", 1); return err },
		func() error { _, err := c.Uploads(context.Background(), "UCabcdefghijklmnopqrstuv", 1); return err },
	} {
		if err := ask(); !errors.Is(err, pushback.ErrStopped) {
			t.Errorf("a shut gate returned %v, want ErrStopped", err)
		}
	}
}
