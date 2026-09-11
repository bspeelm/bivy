package term

import (
	"os"
	"strings"
	"testing"
)

func TestDecodeKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want Press
		used int
	}{
		{"nothing", "", Press{}, 0},
		{"a letter", "j", Press{Key: KeyRune, Rune: 'j'}, 1},
		{"a capital", "G", Press{Key: KeyRune, Rune: 'G'}, 1},
		{"a space", " ", Press{Key: KeyRune, Rune: ' '}, 1},
		{"a slash", "/", Press{Key: KeyRune, Rune: '/'}, 1},
		{"a multi-byte character", "é", Press{Key: KeyRune, Rune: 'é'}, 2},
		{"a character outside the basic plane", "🌲", Press{Key: KeyRune, Rune: '🌲'}, 4},
		{"return", "\r", Press{Key: KeyEnter}, 1},
		{"newline", "\n", Press{Key: KeyEnter}, 1},
		{"backspace", "\x7f", Press{Key: KeyBackspace}, 1},
		{"the other backspace", "\x08", Press{Key: KeyBackspace}, 1},
		{"ctrl-c", "\x03", Press{Key: KeyInterrupt}, 1},
		{"ctrl-d", "\x04", Press{Key: KeyInterrupt}, 1},
		{"a chord with no meaning here", "\x01", Press{}, 1},
		{"down arrow", "\x1b[B", Press{Key: KeyDown}, 3},
		{"up arrow", "\x1b[A", Press{Key: KeyUp}, 3},
		{"application-mode up", "\x1bOA", Press{Key: KeyUp}, 3},
		{"home", "\x1b[H", Press{Key: KeyHome}, 3},
		{"end", "\x1b[F", Press{Key: KeyEnd}, 3},
		{"home, the long spelling", "\x1b[1~", Press{Key: KeyHome}, 4},
		{"end, the long spelling", "\x1b[4~", Press{Key: KeyEnd}, 4},
		{"a sequence with no meaning here", "\x1b[5~", Press{}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, used := DecodeKey([]byte(tc.in))
			if got != tc.want || used != tc.used {
				t.Errorf("DecodeKey(%q) = (%+v, %d), want (%+v, %d)", tc.in, got, used, tc.want, tc.used)
			}
		})
	}
}

// Escape cancels a search box, so it has to arrive as a keypress — but the
// same byte begins every arrow key. It is reported only once the buffer proves
// nothing followed it.
func TestEscapeIsAKeyButOnlyWhenItIsAlone(t *testing.T) {
	got, used := DecodeKey([]byte("\x1b"))
	if got.Key != KeyEscape || used != 1 {
		t.Errorf("a lone escape = (%+v, %d), want KeyEscape", got, used)
	}

	// Two bytes of a three-byte sequence: the rest has not arrived.
	if _, used := DecodeKey([]byte("\x1b[")); used != 0 {
		t.Errorf("an unfinished arrow key consumed %d bytes, want it to wait", used)
	}
	if _, used := DecodeKey([]byte("\x1b[12")); used != 0 {
		t.Errorf("an unfinished parameterised sequence consumed %d bytes", used)
	}
}

// Keys arrive in whatever chunks the terminal writes them, and one read can
// hold several. Decoding must drain a buffer without losing or inventing one.
func TestABufferOfSeveralKeys(t *testing.T) {
	buf := []byte("hi\x1b[Bk\r\x7f")
	want := []Press{
		{Key: KeyRune, Rune: 'h'},
		{Key: KeyRune, Rune: 'i'},
		{Key: KeyDown},
		{Key: KeyRune, Rune: 'k'},
		{Key: KeyEnter},
		{Key: KeyBackspace},
	}

	var got []Press
	for len(buf) > 0 {
		press, used := DecodeKey(buf)
		if used == 0 {
			t.Fatalf("decoding stalled with %q left", buf)
		}
		buf = buf[used:]
		if press.Key != KeyNone {
			got = append(got, press)
		}
	}

	if len(got) != len(want) {
		t.Fatalf("decoded %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// A character split across two reads must not be reported as two broken ones,
// because a search box would then hold a title nobody typed.
func TestAMultiByteCharacterSplitAcrossReads(t *testing.T) {
	full := []byte("é")
	if len(full) != 2 {
		t.Fatalf("the fixture is %d bytes; it no longer tests a split", len(full))
	}

	if _, used := DecodeKey(full[:1]); used != 0 {
		t.Errorf("half a character consumed %d bytes, want it to wait", used)
	}
	got, used := DecodeKey(full)
	if got.Rune != 'é' || used != 2 {
		t.Errorf("DecodeKey = (%+v, %d), want the whole character", got, used)
	}
}

// The bytes of an unrecognised sequence must be consumed, not passed through
// as characters. A function key would otherwise type "[15~" into a search box.
func TestAnUnknownSequenceIsSwallowedWhole(t *testing.T) {
	buf := []byte("\x1b[15~j")

	got, used := DecodeKey(buf)
	if got.Key != KeyNone {
		t.Errorf("an unknown sequence decoded as %+v", got)
	}
	if used != 5 {
		t.Fatalf("consumed %d bytes of %q, want the whole sequence", used, buf)
	}
	if next, _ := DecodeKey(buf[used:]); !next.IsRune('j') {
		t.Errorf("the key after the sequence decoded as %+v", next)
	}
}

func TestIsRune(t *testing.T) {
	if !(Press{Key: KeyRune, Rune: 'q'}).IsRune('q') {
		t.Error("a character did not match itself")
	}
	if (Press{Key: KeyEnter}).IsRune('q') {
		t.Error("a named key matched a character")
	}
	if (Press{Key: KeyRune, Rune: 'q'}).IsRune('r') {
		t.Error("a character matched a different one")
	}
}

// Being run from a pipe is normal — tests do it, and so does anyone piping the
// output somewhere. It is reported as itself rather than as a failure.
func TestOpeningWhatIsNotATerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	if _, err := Open(r, w); err != ErrNotATerminal {
		t.Errorf("Open on a pipe returned %v, want ErrNotATerminal", err)
	}
}

// The alternate screen is what makes bivy leave no trace on the scrollback,
// and the cursor has to come back whatever happens.
func TestTheEscapeSequencesArePaired(t *testing.T) {
	for _, pair := range [][2]string{
		{enterAltScreen, leaveAltScreen},
		{hideCursor, showCursor},
	} {
		if pair[0] == pair[1] {
			t.Errorf("%q is its own inverse, which it is not", pair[0])
		}
		if !strings.HasPrefix(pair[0], "\x1b[") || !strings.HasPrefix(pair[1], "\x1b[") {
			t.Errorf("%q and %q are not both control sequences", pair[0], pair[1])
		}
	}
}
