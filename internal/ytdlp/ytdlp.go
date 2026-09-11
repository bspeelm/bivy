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
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/bspeelm/bivy/internal/media"
)

// searchWait bounds a query: the extractor talks to a service bivy does not
// control, and a longer silence is a failure to report rather than wait out.
const searchWait = 45 * time.Second

// MaxResults is the most a single query will ask for. A search is a way to
// find one video, not a way to page through a service.
const MaxResults = 30

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

	ctx, cancel := context.WithTimeout(ctx, searchWait)
	defer cancel()

	binary := c.Binary
	if binary == "" {
		binary = "yt-dlp"
	}

	// Every argument before the separator is a literal written here. The one
	// after it is the only value that varies, and SearchTerm has already
	// refused anything it would not recognise.
	cmd := exec.CommandContext(ctx, binary,
		"--flat-playlist",
		"--dump-json",
		"--no-warnings",
		"--ignore-config",
		"--no-playlist",
		"--",
		term,
	)

	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		var notFound *exec.Error
		if errors.As(err, &notFound) && errors.Is(notFound.Err, exec.ErrNotFound) {
			return nil, ErrNotInstalled
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("the search took longer than %s", searchWait)
		}
		return nil, fmt.Errorf("the search failed: %s", firstLine(stderr.String(), err))
	}
	return Parse(strings.NewReader(string(out))), nil
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

	if limit < 1 || limit > MaxResults {
		limit = MaxResults
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
