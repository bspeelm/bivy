// Package feed fetches what channels publish.
//
// It is the only package in bivy that imports net/http, and PLAN.md §0 holds
// it to that with a command. Everything bivy says on the wire is written here,
// which makes the claim in §9 checkable by reading one directory.
package feed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bspeelm/bivy/internal/media"
	"github.com/bspeelm/bivy/internal/pushback"
)

const (
	// A channel feed is a few kilobytes. Generous, and still bounded, which
	// an unbounded read from a hostile server is not (ADR-001).
	maxFeedBytes = 1 << 20

	// A channel page is HTML meant for a browser and is genuinely large.
	maxPageBytes = 8 << 20

	// A thumbnail is tens of kilobytes. The cap is what stops a picture being
	// a way to make bivy hold as much memory as a server cares to send.
	maxImageBytes = 2 << 20

	// bivy identifies the program, never the person. There is no version of
	// this string that varies per install, per machine, or per run.
	userAgent = "bivy (+https://github.com/bspeelm/bivy)"

	// The feed endpoint answers 5xx intermittently for channels that answer
	// 200 next time. One retry, and only for that: a third ask is bivy
	// insisting (ADR-017).
	attempts = 2
	// Waited before the retry, plus as much again in jitter, so a refresh of
	// many channels does not ask them all again in the same instant.
	backoff = 500 * time.Millisecond
	// A Retry-After past this is longer than anyone waits at a launch.
	maxRetryAfter = 5 * time.Second
)

// Client fetches feeds. The zero value is not usable; call New.
type Client struct {
	http *http.Client

	// Gate records the service having pushed back; a nil gate never shuts.
	Gate *pushback.Gate

	// Where requests go. Fields rather than constants so the tests can point
	// them at a local server without a seam in the production path.
	FeedBase  string
	PageBase  string
	ThumbBase string
}

// New returns a Client with the timeouts bivy is willing to wait.
func New() *Client {
	return &Client{
		http: &http.Client{
			Timeout: 20 * time.Second,
			// Redirects are followed, but not indefinitely, and never off the
			// scheme they started on.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				if req.URL.Scheme != "https" {
					return fmt.Errorf("refusing a redirect to %s", req.URL.Scheme)
				}
				return nil
			},
		},
		FeedBase: "https://www.youtube.com/feeds/videos.xml",
		PageBase: "https://www.youtube.com",
	}
}

// Fetch returns what a channel's feed currently carries. This runs at launch,
// once per followed channel, and carries no credential, no cookie and no
// parameter that identifies anyone — only the channel asked about (ADR-003).
func (c *Client) Fetch(ctx context.Context, channelID string) (media.Channel, error) {
	if !media.IsChannelID(channelID) {
		return media.Channel{}, fmt.Errorf("%q is not a channel identifier", channelID)
	}

	body, err := c.get(ctx, c.FeedBase+"?channel_id="+url.QueryEscape(channelID), maxFeedBytes)
	if err != nil {
		return media.Channel{}, err
	}
	ch, err := Parse(strings.NewReader(body))
	// A challenge arrives as a 200 carrying a page rather than a feed, so the
	// body is read for one only once it has failed to be a feed: scanning
	// every body would shut the session over a video titled "sign in to
	// confirm" (ADR-017).
	if !isFeed(ch, err) {
		if reason, pushing := pushback.Detect(body); pushing {
			c.Gate.Trip(reason)
			return media.Channel{}, fmt.Errorf("channel %s: %w", channelID, pushback.ErrStopped)
		}
	}
	if err != nil {
		return media.Channel{}, fmt.Errorf("channel %s: %w", channelID, err)
	}

	// The feed states which channel it is. Believing the request over the
	// response keeps a redirected or substituted feed from filing its entries
	// under a channel the user follows.
	ch.ID = channelID
	for i := range ch.Videos {
		ch.Videos[i].ChannelID = channelID
	}
	return ch, nil
}

// isFeed reports whether what parsed is really a channel feed. A page served
// where a feed was asked for can be well-formed XML and decode into nothing,
// so the parse error alone does not answer it. An empty channel names itself.
func isFeed(ch media.Channel, err error) bool {
	return err == nil && (ch.ID != "" || len(ch.Videos) > 0)
}

// after reads a Retry-After header. Only its seconds form is honoured: the
// date form would have bivy waiting on the difference between two clocks.
func after(header string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || secs <= 0 {
		return 0
	}
	return min(time.Duration(secs)*time.Second, maxRetryAfter)
}

// wait is what the server asked to be left alone for, else backoff and jitter.
func wait(err error) time.Duration {
	var r retryable
	if errors.As(err, &r) && r.after > 0 {
		return r.after
	}
	return backoff + rand.N(backoff)
}

// channelIDIn finds a channel identifier in a page, in the order the markers
// are worth trusting. The page's own link to its feed comes first because it
// is the exact thing bivy is trying to learn; the others appear earlier in the
// document and belong to whichever channels the page happens to recommend.
var channelIDIn = []*regexp.Regexp{
	regexp.MustCompile(`channel_id=(UC[A-Za-z0-9_-]{22})`),
	regexp.MustCompile(`"channelId":"(UC[A-Za-z0-9_-]{22})"`),
	regexp.MustCompile(`/channel/(UC[A-Za-z0-9_-]{22})`),
}

var handleShape = regexp.MustCompile(`^@[A-Za-z0-9._-]{1,60}$`)

// Thumbnail fetches a video's picture. Here rather than in the package that
// draws it, because this is the only package that may import net/http (§0).
// The address is derived from the identifier, never taken from a feed, so a
// hostile feed cannot aim this request.
func (c *Client) Thumbnail(ctx context.Context, videoID string) ([]byte, error) {
	targets := media.ThumbnailURLs(videoID)
	if len(targets) == 0 {
		return nil, fmt.Errorf("%q is not a video identifier", videoID)
	}

	// One attempt each, unlike everything else here: a picture is cosmetic,
	// and the first address 404s by design when the larger size was never made.
	var err error
	for _, target := range targets {
		if c.ThumbBase != "" {
			target = c.ThumbBase + strings.TrimPrefix(target, media.ThumbnailHost)
		}
		var body string
		if body, err = c.attempt(ctx, target, maxImageBytes); err == nil {
			return []byte(body), nil
		}
	}
	return nil, err
}

// Resolve turns a handle into the channel identifier its feed is keyed by. The
// one request bivy makes for a page meant for a browser (ADR-008): it happens
// when a channel is followed, never at launch, and a failure is not fatal.
func (c *Client) Resolve(ctx context.Context, handle string) (string, error) {
	if !handleShape.MatchString(handle) {
		return "", fmt.Errorf("%q is not a handle", handle)
	}

	body, err := c.get(ctx, c.PageBase+"/"+url.PathEscape(handle), maxPageBytes)
	if err != nil {
		return "", err
	}
	for _, re := range channelIDIn {
		if m := re.FindStringSubmatch(body); m != nil {
			return m[1], nil
		}
	}
	if reason, pushing := pushback.Detect(body); pushing {
		c.Gate.Trip(reason)
		return "", pushback.ErrStopped
	}
	return "", fmt.Errorf("no channel identifier on the page for %s", handle)
}

// get performs the request, retrying what the server is likely to answer
// differently the next time.
func (c *Client) get(ctx context.Context, target string, limit int64) (string, error) {
	var err error
	for attempt := range attempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(wait(err)):
			}
		}

		var body string
		body, err = c.attempt(ctx, target, limit)
		if err == nil {
			return body, nil
		}
		if !worthRetrying(err) {
			return "", err
		}
	}
	return "", err
}

// retryable marks a failure the server may well answer differently next time,
// carrying however long it asked to be left alone for.
type retryable struct {
	error
	after time.Duration
}

// worthRetrying reports whether another attempt is worth making. A cancelled
// context never is: the caller has stopped waiting.
func worthRetrying(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var r retryable
	return errors.As(err, &r)
}

// attempt is one request, reading at most limit bytes of the response. The
// body length is chosen by the server, and io.ReadAll on a remote response is
// an invitation phrased as convenience (ADR-001).
func (c *Client) attempt(ctx context.Context, target string, limit int64) (string, error) {
	if err := c.Gate.Err(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	// Compression is left to the transport, which asks for gzip and unwraps
	// the reply. Setting the header here turns that off and hands back the
	// compressed bytes, silently: the response is a valid 200 that nothing
	// downstream can read.

	resp, err := c.http.Do(req)
	if err != nil {
		// A connection that failed is worth another go; a request that could
		// not be built is not, and never reaches here.
		return "", retryable{err, 0}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("%s: %s", target, resp.Status)
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			// Never retried: asking again turns a throttle into a block.
			c.Gate.Trip(pushback.RateLimited)
		case resp.StatusCode >= 500:
			return "", retryable{err, after(resp.Header.Get("Retry-After"))}
		}
		return "", err
	}

	// One byte past the limit, so hitting it is distinguishable from a body
	// that happens to be exactly that long.
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > limit {
		return "", fmt.Errorf("%s: response exceeds %d bytes", target, limit)
	}
	return string(b), nil
}
