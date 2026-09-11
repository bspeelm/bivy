package feed

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/bspeelm/bivy/internal/media"
)

// The wire shape, kept separate from media.Channel so that every field
// crossing the boundary is named here rather than implied.
type atomFeed struct {
	ChannelID string      `xml:"http://www.youtube.com/xml/schemas/2015 channelId"`
	Title     string      `xml:"title"`
	Entries   []atomEntry `xml:"entry"`
}

type atomEntry struct {
	VideoID   string `xml:"http://www.youtube.com/xml/schemas/2015 videoId"`
	ChannelID string `xml:"http://www.youtube.com/xml/schemas/2015 channelId"`
	Title     string `xml:"title"`
	Published string `xml:"published"`
	Author    struct {
		Name string `xml:"name"`
	} `xml:"author"`
	Group struct {
		Thumbnail struct {
			URL string `xml:"url,attr"`
		} `xml:"http://search.yahoo.com/mrss/ thumbnail"`
	} `xml:"http://search.yahoo.com/mrss/ group"`
}

// Parse reads a channel feed. Every string that leaves here has been through
// media.Text and every entry keeping its place has an identifier of the right
// shape; one that fails is dropped rather than rejecting the whole feed,
// because a malformed entry should cost one row and not the channel.
//
// The reader is expected to be capped by the caller.
func Parse(r io.Reader) (media.Channel, error) {
	var f atomFeed
	dec := xml.NewDecoder(r)
	// Guessing at an encoding is how mojibake becomes a rendering bug three
	// packages away.
	dec.CharsetReader = func(charset string, _ io.Reader) (io.Reader, error) {
		return nil, fmt.Errorf("unsupported charset %q", charset)
	}
	if err := dec.Decode(&f); err != nil {
		return media.Channel{}, fmt.Errorf("parsing feed: %w", err)
	}

	ch := media.Channel{
		ID:    f.ChannelID,
		Title: media.Text(f.Title),
	}
	if !media.IsChannelID(ch.ID) {
		ch.ID = ""
	}

	for _, e := range f.Entries {
		if !media.IsVideoID(e.VideoID) {
			continue
		}
		published, err := time.Parse(time.RFC3339, e.Published)
		if err != nil {
			continue
		}
		ch.Videos = append(ch.Videos, media.Video{
			ID:        e.VideoID,
			Title:     media.Text(e.Title),
			Author:    media.Text(e.Author.Name),
			ChannelID: e.ChannelID,
			Published: published.UTC(),
			Thumbnail: thumbnailURL(e.Group.Thumbnail.URL, e.VideoID),
		})
	}

	// Newest first, and by identifier where two share a timestamp, so the
	// order does not depend on how the server happened to serialise them.
	sort.SliceStable(ch.Videos, func(i, j int) bool {
		a, b := ch.Videos[i], ch.Videos[j]
		if a.Published.Equal(b.Published) {
			return a.ID < b.ID
		}
		return a.Published.After(b.Published)
	})
	return ch, nil
}

// thumbnailURL rebuilds the address from the video identifier. The supplied
// URL is a string a remote server chose and is destined for a fetch a later
// milestone makes, so deriving it means a hostile feed cannot aim that fetch.
// The feed's value is read only as a statement that a thumbnail exists.
func thumbnailURL(supplied, videoID string) string {
	const host = "https://i.ytimg.com/vi/"
	if supplied == "" {
		return ""
	}
	return host + videoID + "/hqdefault.jpg"
}
