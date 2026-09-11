package feed

import (
	"strings"
	"testing"
	"time"
)

// A feed in the shape the real one arrives in, trimmed to what bivy reads.
const sampleFeed = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015"
      xmlns:media="http://search.yahoo.com/mrss/"
      xmlns="http://www.w3.org/2005/Atom">
  <yt:channelId>UCabcdefghijklmnopqrstuv</yt:channelId>
  <title>A Channel</title>
  <entry>
    <id>yt:video:aaaaaaaaaaa</id>
    <yt:videoId>aaaaaaaaaaa</yt:videoId>
    <yt:channelId>UCabcdefghijklmnopqrstuv</yt:channelId>
    <title>The older one</title>
    <author><name>A Channel</name></author>
    <published>2026-09-01T12:00:00+00:00</published>
    <media:group>
      <media:thumbnail url="https://i.ytimg.com/vi/aaaaaaaaaaa/hqdefault.jpg"/>
    </media:group>
  </entry>
  <entry>
    <id>yt:video:bbbbbbbbbbb</id>
    <yt:videoId>bbbbbbbbbbb</yt:videoId>
    <yt:channelId>UCabcdefghijklmnopqrstuv</yt:channelId>
    <title>The newer one</title>
    <author><name>A Channel</name></author>
    <published>2026-09-05T09:30:00+00:00</published>
    <media:group>
      <media:thumbnail url="https://i.ytimg.com/vi/bbbbbbbbbbb/hqdefault.jpg"/>
    </media:group>
  </entry>
</feed>`

func TestParseReadsAFeed(t *testing.T) {
	ch, err := Parse(strings.NewReader(sampleFeed))
	if err != nil {
		t.Fatal(err)
	}

	if got, want := ch.ID, "UCabcdefghijklmnopqrstuv"; got != want {
		t.Errorf("channel id = %q, want %q", got, want)
	}
	if got, want := ch.Title, "A Channel"; got != want {
		t.Errorf("channel title = %q, want %q", got, want)
	}
	if got, want := len(ch.Videos), 2; got != want {
		t.Fatalf("%d entries, want %d", got, want)
	}

	// Newest first, whatever order the server serialised them in.
	if got, want := ch.Videos[0].Title, "The newer one"; got != want {
		t.Errorf("first entry = %q, want %q", got, want)
	}

	v := ch.Videos[0]
	if got, want := v.ID, "bbbbbbbbbbb"; got != want {
		t.Errorf("video id = %q, want %q", got, want)
	}
	if got, want := v.Published, time.Date(2026, 9, 5, 9, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("published = %s, want %s", got, want)
	}
	if got, want := v.Thumbnail, "https://i.ytimg.com/vi/bbbbbbbbbbb/hq720.jpg"; got != want {
		t.Errorf("thumbnail = %q, want %q", got, want)
	}
}

// One bad entry costs one row, not the channel. A feed is remote input and
// rejecting all of it because one field is wrong hands a hostile or merely
// broken server the ability to blank the dashboard.
func TestParseDropsBadEntriesAndKeepsTheRest(t *testing.T) {
	const mixed = `<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns="http://www.w3.org/2005/Atom">
  <yt:channelId>UCabcdefghijklmnopqrstuv</yt:channelId>
  <title>A Channel</title>
  <entry><yt:videoId>../../etc/passwd</yt:videoId><title>Path</title><published>2026-09-01T12:00:00+00:00</published></entry>
  <entry><yt:videoId>aaaaaaaaaaa</yt:videoId><title>Fine</title><published>2026-09-01T12:00:00+00:00</published></entry>
  <entry><yt:videoId>bbbbbbbbbbb</yt:videoId><title>No date</title><published>not a date</published></entry>
  <entry><yt:videoId></yt:videoId><title>Empty</title><published>2026-09-01T12:00:00+00:00</published></entry>
</feed>`

	ch, err := Parse(strings.NewReader(mixed))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(ch.Videos), 1; got != want {
		t.Fatalf("%d entries kept, want %d", got, want)
	}
	if got, want := ch.Videos[0].Title, "Fine"; got != want {
		t.Errorf("kept %q, want %q", got, want)
	}
}

func TestParseStripsControlCharactersFromRemoteText(t *testing.T) {
	// C1 controls, not ESC. XML 1.0 permits these characters, so they reach
	// the parser intact and stripping them is this code's actual job: U+009B
	// is a control sequence introducer on its own, and U+0085 is a line break
	// that unicode.IsSpace would otherwise have turned into a space.
	const hostile = `<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns="http://www.w3.org/2005/Atom">
  <yt:channelId>UCabcdefghijklmnopqrstuv</yt:channelId>
  <title>Chan&#x9b;2Jnel</title>
  <entry><yt:videoId>aaaaaaaaaaa</yt:videoId><title>Ti&#x85;&#x9b;0mtle</title>
  <author><name>Au&#x9b;thor</name></author><published>2026-09-01T12:00:00+00:00</published></entry>
</feed>`

	ch, err := Parse(strings.NewReader(hostile))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{ch.Title, ch.Videos[0].Title, ch.Videos[0].Author} {
		for _, r := range s {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				t.Errorf("%q reached the caller carrying %U", s, r)
			}
		}
	}
}

// Newlines are legal XML and would otherwise let a title write a second row of
// its own onto the dashboard.
func TestParseFlattensMultiLineTitles(t *testing.T) {
	const multiline = `<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns="http://www.w3.org/2005/Atom">
  <yt:channelId>UCabcdefghijklmnopqrstuv</yt:channelId>
  <entry><yt:videoId>aaaaaaaaaaa</yt:videoId><title>first
   second</title><published>2026-09-01T12:00:00+00:00</published></entry>
</feed>`

	ch, err := Parse(strings.NewReader(multiline))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ch.Videos[0].Title, "first second"; got != want {
		t.Errorf("title = %q, want %q", got, want)
	}
}

// The decoder refuses an escape smuggled in as a character reference, which is
// a defence bivy gets for free and does not control. Recorded because it is
// load-bearing in an unusual direction: it costs the whole feed rather than
// one entry, so a feed that does this goes dark instead of going wrong.
func TestAnEscapeCharacterReferenceCostsTheWholeFeed(t *testing.T) {
	const smuggled = `<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns="http://www.w3.org/2005/Atom">
  <yt:channelId>UCabcdefghijklmnopqrstuv</yt:channelId>
  <entry><yt:videoId>aaaaaaaaaaa</yt:videoId><title>a&#x1b;[31mb</title><published>2026-09-01T12:00:00+00:00</published></entry>
</feed>`

	if _, err := Parse(strings.NewReader(smuggled)); err == nil {
		t.Fatal("an escape character reference was accepted")
	}
}

// A thumbnail address is derived from the video identifier rather than taken
// from the feed, so that a hostile feed cannot aim a later fetch at a host of
// its choosing.
func TestParseDerivesTheThumbnailAddress(t *testing.T) {
	const redirect = `<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns:media="http://search.yahoo.com/mrss/" xmlns="http://www.w3.org/2005/Atom">
  <yt:channelId>UCabcdefghijklmnopqrstuv</yt:channelId>
  <entry><yt:videoId>aaaaaaaaaaa</yt:videoId><title>T</title><published>2026-09-01T12:00:00+00:00</published>
  <media:group><media:thumbnail url="https://attacker.example/track?who=me"/></media:group></entry>
</feed>`

	ch, err := Parse(strings.NewReader(redirect))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ch.Videos[0].Thumbnail, "https://i.ytimg.com/vi/aaaaaaaaaaa/hq720.jpg"; got != want {
		t.Errorf("thumbnail = %q, want %q", got, want)
	}
}

// Guessing at an encoding is how mojibake becomes a rendering bug three
// packages away, so a charset bivy cannot decode is a feed bivy does not read.
func TestParseRefusesAnUnknownCharset(t *testing.T) {
	const exotic = `<?xml version="1.0" encoding="Shift_JIS"?><feed><title>t</title></feed>`
	if _, err := Parse(strings.NewReader(exotic)); err == nil {
		t.Fatal("a feed in an undecodable charset was accepted")
	}
}

func TestParseRejectsRubbish(t *testing.T) {
	for _, in := range []string{"", "not xml at all", "<feed><unclosed>"} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("Parse(%q) returned no error", in)
		}
	}
}

// An identifier that is not shaped like one never reaches the caller, because
// it is interpolated into a URL and, in a later milestone, travels towards an
// argv.
func TestParseClearsAChannelIdItDoesNotBelieve(t *testing.T) {
	const lying = `<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns="http://www.w3.org/2005/Atom">
  <yt:channelId>--exec=touch /tmp/pwned</yt:channelId><title>t</title></feed>`

	ch, err := Parse(strings.NewReader(lying))
	if err != nil {
		t.Fatal(err)
	}
	if ch.ID != "" {
		t.Errorf("channel id = %q, want it discarded", ch.ID)
	}
}
