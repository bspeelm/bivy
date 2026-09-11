package feed

import (
	"strings"
	"testing"

	"github.com/bspeelm/bivy/internal/media"
)

// FuzzParse is the fuzzing PLAN.md §10 asks for, aimed at the one place in
// bivy where bytes chosen by a remote server are parsed.
//
// The property is not "no error". A feed is allowed to be rubbish and Parse is
// allowed to say so. The property is that Parse never panics, and that nothing
// which survives it has skipped the checks the rest of the program relies on:
// every identifier is shaped like one, and no text carries a byte a terminal
// would obey.
func FuzzParse(f *testing.F) {
	f.Add(sampleFeed)
	f.Add(`<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns="http://www.w3.org/2005/Atom">
  <yt:channelId>UCabcdefghijklmnopqrstuv</yt:channelId><title>t</title></feed>`)
	f.Add("<feed></feed>")
	f.Add("")
	f.Add("not xml")
	f.Add(`<?xml version="1.0" encoding="Shift_JIS"?><feed/>`)

	f.Fuzz(func(t *testing.T, in string) {
		ch, err := Parse(strings.NewReader(in))
		if err != nil {
			return
		}

		if ch.ID != "" && !media.IsChannelID(ch.ID) {
			t.Fatalf("kept a channel id that is not one: %q", ch.ID)
		}
		assertClean(t, "channel title", ch.Title)

		for _, v := range ch.Videos {
			if !media.IsVideoID(v.ID) {
				t.Fatalf("kept a video id that is not one: %q", v.ID)
			}
			if v.Published.IsZero() {
				t.Fatalf("kept an entry with no publish time: %q", v.ID)
			}
			assertClean(t, "title", v.Title)
			assertClean(t, "author", v.Author)

			// A thumbnail address is derived, so it can only ever name the one
			// host. A fuzzed feed that changes that has found a way to aim a
			// later fetch.
			if v.Thumbnail != "" && !strings.HasPrefix(v.Thumbnail, "https://i.ytimg.com/vi/") {
				t.Fatalf("thumbnail points at %q", v.Thumbnail)
			}
		}

		// Newest first, always. The dashboard reads this order and does not
		// re-establish it per channel.
		for i := 1; i < len(ch.Videos); i++ {
			if ch.Videos[i-1].Published.Before(ch.Videos[i].Published) {
				t.Fatalf("entries came back out of order at %d", i)
			}
		}
	})
}

func assertClean(t *testing.T, what, s string) {
	t.Helper()
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("%s %q carries %U", what, s, r)
		}
	}
}
