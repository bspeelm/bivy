package feed

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bspeelm/bivy/internal/media"
)

const testChannel = "UCabcdefghijklmnopqrstuv"

// against starts a server and points a Client at it. This is the only package
// in bivy that may do this: PLAN.md §0 allows one package to import net/http,
// and it counts test imports.
func against(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c := New()
	c.FeedBase = srv.URL + "/feeds/videos.xml"
	c.PageBase = srv.URL
	return c
}

func TestFetchReadsAChannel(t *testing.T) {
	var asked string
	c := against(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Query().Get("channel_id")
		fmt.Fprint(w, sampleFeed)
	})

	ch, err := c.Fetch(context.Background(), testChannel)
	if err != nil {
		t.Fatal(err)
	}
	if asked != testChannel {
		t.Errorf("asked for channel_id=%q, want %q", asked, testChannel)
	}
	if got, want := len(ch.Videos), 2; got != want {
		t.Fatalf("%d entries, want %d", got, want)
	}
}

// The request carries nothing that says who is making it. This is the whole of
// the §9 claim, and it is the test that fails if a header is ever added for
// convenience.
func TestFetchSendsNothingIdentifying(t *testing.T) {
	var got http.Header
	var query map[string][]string
	c := against(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		query = r.URL.Query()
		fmt.Fprint(w, sampleFeed)
	})

	if _, err := c.Fetch(context.Background(), testChannel); err != nil {
		t.Fatal(err)
	}

	if len(r(got, "Cookie")) != 0 {
		t.Error("the request carried a cookie")
	}
	if len(r(got, "Authorization")) != 0 {
		t.Error("the request carried an authorization header")
	}
	for name := range query {
		if name != "channel_id" {
			t.Errorf("the request carried a %q parameter; only channel_id belongs there", name)
		}
	}
	if ua := got.Get("User-Agent"); ua != userAgent {
		t.Errorf("User-Agent = %q, want the fixed string %q", ua, userAgent)
	}
}

func r(h http.Header, name string) []string { return h.Values(name) }

// io.ReadAll on a remote response is an invitation phrased as convenience.
func TestFetchRefusesAnOversizedBody(t *testing.T) {
	c := against(t, func(w http.ResponseWriter, _ *http.Request) {
		for written := 0; written <= maxFeedBytes; written += 4096 {
			if _, err := w.Write(make([]byte, 4096)); err != nil {
				return
			}
		}
	})

	_, err := c.Fetch(context.Background(), testChannel)
	if err == nil {
		t.Fatal("an oversized feed was accepted")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %v, want it to say the response was too large", err)
	}
}

func TestFetchReportsAnErrorStatus(t *testing.T) {
	c := against(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})

	if _, err := c.Fetch(context.Background(), testChannel); err == nil {
		t.Fatal("a 404 was treated as a feed")
	}
}

// The identifier is checked before it is interpolated into a URL, so a
// malformed one never becomes a request at all.
func TestFetchRefusesAMalformedIdentifier(t *testing.T) {
	reached := false
	c := against(t, func(http.ResponseWriter, *http.Request) { reached = true })

	for _, bad := range []string{"", "not-an-id", "--exec=touch /tmp/pwned", "UC../../etc/passwd"} {
		if _, err := c.Fetch(context.Background(), bad); err == nil {
			t.Errorf("Fetch(%q) returned no error", bad)
		}
	}
	if reached {
		t.Error("a malformed identifier reached the network")
	}
}

// Fetch believes the request over the response: a feed that files its entries
// under a different channel does not get them onto that channel's dashboard.
func TestFetchAttributesEntriesToTheChannelThatWasAsked(t *testing.T) {
	const lying = `<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns="http://www.w3.org/2005/Atom">
  <yt:channelId>UCzzzzzzzzzzzzzzzzzzzzzz</yt:channelId>
  <entry><yt:videoId>aaaaaaaaaaa</yt:videoId><yt:channelId>UCzzzzzzzzzzzzzzzzzzzzzz</yt:channelId>
  <title>T</title><published>2026-09-01T12:00:00+00:00</published></entry>
</feed>`

	c := against(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, lying) })

	ch, err := c.Fetch(context.Background(), testChannel)
	if err != nil {
		t.Fatal(err)
	}
	if ch.ID != testChannel {
		t.Errorf("channel id = %q, want the one that was asked for", ch.ID)
	}
	if ch.Videos[0].ChannelID != testChannel {
		t.Errorf("entry filed under %q, want the channel that was asked for", ch.Videos[0].ChannelID)
	}
}

func TestResolveFindsTheChannelIdentifier(t *testing.T) {
	for _, tc := range []struct{ name, page string }{
		{"from the feed link", `<link rel="alternate" type="application/rss+xml" href="https://www.youtube.com/feeds/videos.xml?channel_id=` + testChannel + `">`},
		{"from the page data", `{"channelId":"` + testChannel + `","other":1}`},
		{"from a channel path", `<a href="/channel/` + testChannel + `">`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := against(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, tc.page) })

			got, err := c.Resolve(context.Background(), "@someone")
			if err != nil {
				t.Fatal(err)
			}
			if got != testChannel {
				t.Errorf("Resolve = %q, want %q", got, testChannel)
			}
		})
	}
}

// A real channel page carries all three markers, and the ones bivy trusts
// least appear first — attached to whichever channels the page recommends.
// Getting this order wrong follows the wrong channel and looks like it worked.
func TestResolvePrefersTheFeedLinkOverEarlierMarkers(t *testing.T) {
	const page = `{"channelId":"UCzzzzzzzzzzzzzzzzzzzzzz"}` +
		`<a href="/channel/UCyyyyyyyyyyyyyyyyyyyyyy">recommended</a>` +
		`<link rel="alternate" href="https://www.youtube.com/feeds/videos.xml?channel_id=` + testChannel + `">`

	c := against(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, page) })

	got, err := c.Resolve(context.Background(), "@someone")
	if err != nil {
		t.Fatal(err)
	}
	if got != testChannel {
		t.Errorf("Resolve = %q, want the feed link's %q", got, testChannel)
	}
}

// Compression is the transport's business. Asking for it by hand turns off the
// decompression that comes with it, and the failure is a valid 200 full of
// bytes nothing downstream can read.
func TestRequestsDoNotNegotiateTheirOwnCompression(t *testing.T) {
	var asked string
	c := against(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.Header.Get("Accept-Encoding")
		fmt.Fprint(w, sampleFeed)
	})

	if _, err := c.Fetch(context.Background(), testChannel); err != nil {
		t.Fatal(err)
	}
	if asked != "gzip" {
		t.Errorf("Accept-Encoding = %q, want the transport's own %q", asked, "gzip")
	}
}

func TestResolveRefusesSomethingThatIsNotAHandle(t *testing.T) {
	reached := false
	c := against(t, func(http.ResponseWriter, *http.Request) { reached = true })

	for _, bad := range []string{"", "someone", "@", "@with/slash", "@with space", "-@dash"} {
		if _, err := c.Resolve(context.Background(), bad); err == nil {
			t.Errorf("Resolve(%q) returned no error", bad)
		}
	}
	if reached {
		t.Error("something that is not a handle reached the network")
	}
}

// Failing to resolve is a normal outcome, not a crash: the caller tells the
// user to pass an identifier instead.
func TestResolveReportsAPageWithNoIdentifier(t *testing.T) {
	c := against(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "<html><body>a consent wall, or a redesign</body></html>")
	})

	if _, err := c.Resolve(context.Background(), "@someone"); err == nil {
		t.Fatal("a page with no identifier on it resolved anyway")
	}
}

func TestGetStopsFollowingRedirectsEventually(t *testing.T) {
	var hops int
	c := against(t, func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	})

	if _, err := c.Fetch(context.Background(), testChannel); err == nil {
		t.Fatal("a redirect loop was followed to completion")
	}
	if hops > 6 {
		t.Errorf("followed %d hops before giving up", hops)
	}
}

func TestFetchHonoursACancelledContext(t *testing.T) {
	c := against(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, sampleFeed) })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.Fetch(ctx, testChannel); err == nil {
		t.Fatal("a cancelled context still made a request")
	}
}

// The feed endpoint answers 404 or 500 to roughly four requests in ten,
// intermittently, for channels that answer 200 on the next attempt. One
// attempt makes following a channel a coin toss.
func TestATransientFailureIsRetried(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls int
			c := against(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if calls < 3 {
					http.Error(w, "not this time", status)
					return
				}
				fmt.Fprint(w, sampleFeed)
			})

			ch, err := c.Fetch(context.Background(), testChannel)
			if err != nil {
				t.Fatalf("gave up after %d attempts: %v", calls, err)
			}
			if len(ch.Videos) != 2 {
				t.Errorf("%d entries, want the feed that finally arrived", len(ch.Videos))
			}
		})
	}
}

// Retrying is bounded. A channel that genuinely does not exist answers the
// same way every time, and bivy has to stop and say so.
func TestRetryingGivesUp(t *testing.T) {
	var calls int
	c := against(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "gone", http.StatusNotFound)
	})

	if _, err := c.Fetch(context.Background(), testChannel); err == nil {
		t.Fatal("a feed that never arrived reported success")
	}
	if calls != attempts {
		t.Errorf("made %d attempts, want %d", calls, attempts)
	}
}

// A refusal that will not change is not worth repeating.
func TestAPermanentFailureIsNotRetried(t *testing.T) {
	var calls int
	c := against(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "no", http.StatusForbidden)
	})

	if _, err := c.Fetch(context.Background(), testChannel); err == nil {
		t.Fatal("a 403 was treated as a feed")
	}
	if calls != 1 {
		t.Errorf("made %d attempts at a 403, want 1", calls)
	}
}

// A caller who has stopped waiting is not made to wait through the backoff.
func TestRetryingStopsWhenTheContextDoes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls int
	c := against(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		cancel()
		http.Error(w, "later", http.StatusInternalServerError)
	})

	if _, err := c.Fetch(ctx, testChannel); err == nil {
		t.Fatal("a cancelled fetch reported success")
	}
	if calls > 1 {
		t.Errorf("made %d attempts after the caller gave up", calls)
	}
}

// An oversized body is the server behaving, not failing, so it is not retried.
func TestAnOversizedBodyIsNotRetried(t *testing.T) {
	var calls int
	c := against(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		for written := 0; written <= maxFeedBytes; written += 4096 {
			if _, err := w.Write(make([]byte, 4096)); err != nil {
				return
			}
		}
	})

	if _, err := c.Fetch(context.Background(), testChannel); err == nil {
		t.Fatal("an oversized feed was accepted")
	}
	if calls != 1 {
		t.Errorf("made %d attempts at an oversized body, want 1", calls)
	}
}

// A thumbnail is fetched by the one package that may touch the network (§0),
// and its address is derived from the identifier rather than taken from a
// feed.
func TestThumbnail(t *testing.T) {
	var asked string
	c := against(t, func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		_, _ = w.Write([]byte("\xff\xd8\xff pretend jpeg"))
	})
	// The thumbnail host is not one of the bases the test server stands in
	// for, so this asserts what it builds rather than what it fetches.
	if got := media.ThumbnailURL("dQw4w9WgXcQ"); got != "https://i.ytimg.com/vi/dQw4w9WgXcQ/hqdefault.jpg" {
		t.Errorf("thumbnail address = %q", got)
	}
	_ = asked
	_ = c
}

func TestThumbnailRefusesSomethingThatIsNotAVideo(t *testing.T) {
	reached := false
	c := against(t, func(http.ResponseWriter, *http.Request) { reached = true })

	for _, bad := range []string{"", "not-an-id", "--exec=touch /tmp/pwned", "../../etc/passwd"} {
		if _, err := c.Thumbnail(context.Background(), bad); err == nil {
			t.Errorf("Thumbnail(%q) returned no error", bad)
		}
	}
	if reached {
		t.Error("a malformed identifier reached the network")
	}
}
