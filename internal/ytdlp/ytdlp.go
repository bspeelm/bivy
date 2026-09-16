// Package ytdlp runs the extractor as a subprocess, and never as anything
// else. It makes no network request of its own; §0 holds the program to one
// package that does, and this is not it.
//
// The extractor has options that run arbitrary commands, so every argument
// built here is a literal bivy wrote or a value that has been checked (§7).
package ytdlp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/bspeelm/bivy/internal/media"
	"github.com/bspeelm/bivy/internal/visitor"
)

// searchWait bounds a query: the extractor talks to a service bivy does not
// control, and a longer silence is a failure to report rather than wait out.
const searchWait = 45 * time.Second

// DefaultResults is one page, and only what a nonsense limit gets: the service
// stops yielding long before any ceiling here would.
const DefaultResults = 30

// Client runs the extractor.
type Client struct {
	// Binary is the extractor to run. Empty means "yt-dlp", found on PATH.
	Binary string
}

func New() *Client { return &Client{} }

// ErrNotInstalled is a normal state, not a fault: the dashboard never needs
// the extractor, so a user can go a long time without noticing its absence.
var ErrNotInstalled = errors.New("yt-dlp is not installed")

// Search asks the extractor for videos matching a query.
func (c *Client) Search(ctx context.Context, query string, limit int) ([]media.Video, error) {
	term, err := SearchTerm(query, limit)
	if err != nil {
		return nil, err
	}

	out, err := c.run(ctx, "--", term)
	if err != nil {
		return nil, err
	}
	return Parse(strings.NewReader(out)), nil
}

// run executes the extractor and returns what it printed.
//
// Every argument before the caller's is a literal written here; the caller
// supplies only what follows, already checked.
func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, searchWait)
	defer cancel()

	binary := c.Binary
	if binary == "" {
		binary = "yt-dlp"
	}

	// A visitor for this search and no other, gone when it returns (ADR-016).
	dir, err := os.MkdirTemp("", "bivy-")
	if err != nil {
		return "", fmt.Errorf("making a place for the visitor cookie: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	cookies, err := visitor.Write(dir)
	if err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, binary, append([]string{
		"--flat-playlist",
		"--dump-json",
		"--no-warnings",
		"--ignore-config",
		"--no-playlist",
		"--cookies", cookies,
	}, args...)...)

	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		var notFound *exec.Error
		if errors.As(err, &notFound) && errors.Is(notFound.Err, exec.ErrNotFound) {
			return "", ErrNotInstalled
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("the search took longer than %s", searchWait)
		}
		return "", fmt.Errorf("the search failed: %s", firstLine(stderr.String(), err))
	}
	return string(out), nil
}

// SearchTerm builds the one argument that varies. The result always begins
// with "ytsearch", so what reaches the argv cannot be read as an option
// however the query begins — and a leading dash is refused as well, because
// that property is worth having rather than relying on.
func SearchTerm(query string, limit int) (string, error) {
	query = strings.TrimSpace(media.Text(query))
	switch {
	case query == "":
		return "", errors.New("nothing to search for")
	case strings.HasPrefix(query, "-"):
		return "", fmt.Errorf("%q begins with a dash, and nothing bivy passes on may", query)
	case len(query) > 200:
		return "", errors.New("that search is too long")
	}

	if limit < 1 {
		limit = DefaultResults
	}
	return "ytsearch" + strconv.Itoa(limit) + ":" + query, nil
}

// The extractor's output shape, named here rather than implied, so that a
// change to what it happens to emit does not reach the rest of the program.
type entry struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Channel   string  `json:"channel"`
	Uploader  string  `json:"uploader"`
	ChannelID string  `json:"channel_id"`
	Duration  float64 `json:"duration"`
	Timestamp int64   `json:"timestamp"`
	// Extractor names which of the extractor's own readers produced the line,
	// which is how a channel is told from a video.
	Extractor   string `json:"ie_key"`
	Description string `json:"description"`
	Followers   int    `json:"channel_follower_count"`
}

// Parse reads the extractor's output, one JSON object per line. A malformed
// entry, or one carrying an identifier that is not one, is dropped rather than
// failing the search: one bad result should cost one row.
func Parse(r *strings.Reader) []media.Video {
	var videos []media.Video

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	for scanner.Scan() {
		var e entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if !media.IsVideoID(e.ID) {
			continue
		}

		author := e.Channel
		if author == "" {
			author = e.Uploader
		}
		channelID := e.ChannelID
		if !media.IsChannelID(channelID) {
			channelID = ""
		}

		v := media.Video{
			ID:        e.ID,
			Title:     media.Text(e.Title),
			Author:    media.Text(author),
			ChannelID: channelID,
			Thumbnail: media.ThumbnailURL(e.ID),
		}
		if e.Duration > 0 {
			v.Duration = time.Duration(e.Duration) * time.Second
		}
		if e.Timestamp > 0 {
			v.Published = time.Unix(e.Timestamp, 0).UTC()
		}
		videos = append(videos, v)
	}
	return videos
}

// firstLine is what the extractor said, rather than what exec said about it.
func firstLine(stderr string, fallback error) string {
	for _, line := range strings.Split(stderr, "\n") {
		if line = strings.TrimSpace(media.Text(line)); line != "" {
			return line
		}
	}
	return fallback.Error()
}

// channelFilter is the search-results parameter that asks for channels rather
// than videos. An opaque constant of the service's own, kept here as the one
// place it is written down.
const channelFilter = "EgIQAg%3D%3D"

// Channels asks the extractor for channels matching a query. The argument is a
// URL bivy builds, so what reaches the argv begins with https:// and cannot be
// read as an option however the query begins.
func (c *Client) Channels(ctx context.Context, query string, limit int) ([]media.Channel, error) {
	query = strings.TrimSpace(media.Text(query))
	if query == "" {
		return nil, errors.New("nothing to search for")
	}
	if len(query) > 200 {
		return nil, errors.New("that search is too long")
	}
	if limit < 1 {
		limit = DefaultResults
	}

	target := "https://www.youtube.com/results?search_query=" +
		url.QueryEscape(query) + "&sp=" + channelFilter

	out, err := c.run(ctx, "--playlist-end", strconv.Itoa(limit), "--", target)
	if err != nil {
		return nil, err
	}
	return ParseChannels(strings.NewReader(out)), nil
}

// Uploads lists a channel's recent videos through the extractor.
//
// The feed is how bivy learns what a channel has posted (ADR-003). This is
// what it does when the feed service will not answer, and ADR-013 is the
// record of that: the argument is only that an empty dashboard is worse.
//
// What comes back has no publish times. The extractor does not provide them
// without a request per video, and thirty of those is not a launch.
func (c *Client) Uploads(ctx context.Context, channelID string, limit int) (media.Channel, error) {
	if !media.IsChannelID(channelID) {
		return media.Channel{}, fmt.Errorf("%q is not a channel identifier", channelID)
	}
	if limit < 1 {
		limit = DefaultResults
	}

	out, err := c.run(ctx, "--playlist-end", strconv.Itoa(limit),
		"--", "https://www.youtube.com/channel/"+channelID+"/videos")
	if err != nil {
		return media.Channel{}, err
	}

	ch := media.Channel{ID: channelID, Videos: Parse(strings.NewReader(out))}
	for i := range ch.Videos {
		ch.Videos[i].ChannelID = channelID
	}
	return ch, nil
}

// ParseChannels reads channel entries out of the extractor's output. Videos
// and channels arrive through the same command, told apart by which of the
// extractor's own readers produced each line.
func ParseChannels(r *strings.Reader) []media.Channel {
	var channels []media.Channel

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)

	for scanner.Scan() {
		var e entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if e.Extractor != "YoutubeTab" || !media.IsChannelID(e.ID) {
			continue
		}

		title := e.Title
		if title == "" {
			title = e.Channel
		}
		channels = append(channels, media.Channel{
			ID:          e.ID,
			Title:       media.Text(title),
			Description: media.Text(e.Description),
			Followers:   e.Followers,
		})
	}
	return channels
}
