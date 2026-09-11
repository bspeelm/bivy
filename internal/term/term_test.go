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
		want Key
		used int
	}{
		{"nothing", "", KeyNone, 0},
		{"j", "j", KeyDown, 1},
		{"k", "k", KeyUp, 1},
		{"g", "g", KeyTop, 1},
		{"G", "G", KeyBottom, 1},
		{"r", "r", KeyRefresh, 1},
		{"q", "q", KeyQuit, 1},
		{"ctrl-c", "\x03", KeyQuit, 1},
		{"ctrl-d", "\x04", KeyQuit, 1},
		{"return", "\r", KeyEnter, 1},
		{"newline", "\n", KeyEnter, 1},
		{"space", " ", KeyEnter, 1},
		{"down arrow", "\x1b[B", KeyDown, 3},
		{"up arrow", "\x1b[A", KeyUp, 3},
		{"application-mode up", "\x1bOA", KeyUp, 3},
		{"home", "\x1b[H", KeyTop, 3},
		{"end", "\x1b[F", KeyBottom, 3},
		{"home, the long spelling", "\x1b[1~", KeyTop, 4},
		{"end, the long spelling", "\x1b[4~", KeyBottom, 4},
		{"a key with no meaning here", "z", KeyNone, 1},
		{"a sequence with no meaning here", "\x1b[5~", KeyNone, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, used := DecodeKey([]byte(tc.in))
			if key != tc.want || used != tc.used {
				t.Errorf("DecodeKey(%q) = (%v, %d), want (%v, %d)", tc.in, key, used, tc.want, tc.used)
			}
		})
	}
}

// A sequence that arrives in pieces must not be read as an escape keypress
// followed by two letters. Reporting zero consumed is how the reader knows to
// wait for the rest.
func TestAnIncompleteSequenceIsNotAKeypress(t *testing.T) {
	for _, partial := range []string{"\x1b", "\x1b["} {
		key, used := DecodeKey([]byte(partial))
		if used != 0 {
			t.Errorf("DecodeKey(%q) consumed %d bytes, want it to wait", partial, used)
		}
		if key != KeyNone {
			t.Errorf("DecodeKey(%q) = %v, want nothing yet", partial, key)
		}
	}

	// A parameterised sequence whose final byte has not arrived either.
	if _, used := DecodeKey([]byte("\x1b[12")); used != 0 {
		t.Errorf("an unfinished parameterised sequence consumed %d bytes", used)
	}
}

// Keys arrive in whatever chunks the terminal writes them, and one read can
// hold several. Decoding must drain a buffer without losing or inventing one.
func TestABufferOfSeveralKeys(t *testing.T) {
	buf := []byte("jj\x1b[Bk\rq")
	want := []Key{KeyDown, KeyDown, KeyDown, KeyUp, KeyEnter, KeyQuit}

	var got []Key
	for len(buf) > 0 {
		key, used := DecodeKey(buf)
		if used == 0 {
			t.Fatalf("decoding stalled with %q left", buf)
		}
		buf = buf[used:]
		if key != KeyNone {
			got = append(got, key)
		}
	}

	if len(got) != len(want) {
		t.Fatalf("decoded %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// The bytes of an unrecognised sequence must be consumed, not passed through
// as letters. A function key would otherwise arrive as "[", "1", "5" and move
// the cursor on its own.
func TestAnUnknownSequenceIsSwallowedWhole(t *testing.T) {
	buf := []byte("\x1b[15~j")

	key, used := DecodeKey(buf)
	if key != KeyNone {
		t.Errorf("an unknown sequence decoded as %v", key)
	}
	if used != 5 {
		t.Fatalf("consumed %d bytes of %q, want the whole sequence", used, buf)
	}
	if next, _ := DecodeKey(buf[used:]); next != KeyDown {
		t.Errorf("the key after the sequence decoded as %v, want KeyDown", next)
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
