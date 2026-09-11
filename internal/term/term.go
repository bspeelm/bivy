// Package term is the interactive layer: raw mode, the alternate screen, key
// decoding and redrawing.
//
// Written here rather than taken from a framework (ADR-009), so this package
// owns every byte bivy sends to the terminal — which matters most for what has
// not been built yet, since a thumbnail grid speaks a graphics protocol
// directly.
//
// It does I/O, which is why it is not internal/tui: that package renders a
// model and returns text, and this is the other half of the split.
package term

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"unicode/utf8"

	"golang.org/x/term"
)

// Key names a keypress that is not a character.
//
// What the characters mean is not decided here. A list reads "j" as down and a
// search box reads it as the letter j, and only the screen knows which it is
// looking at — so this package reports what was pressed and the caller decides
// what it meant.
type Key int

const (
	KeyNone Key = iota
	KeyRune
	KeyUp
	KeyDown
	KeyHome
	KeyEnd
	KeyEnter
	KeyBackspace
	KeyEscape
	KeyInterrupt
)

// Press is one keypress: a named key, or a character in Rune when Key is
// KeyRune.
type Press struct {
	Key  Key
	Rune rune
}

// IsRune reports that this press is the character r.
func (p Press) IsRune(r rune) bool { return p.Key == KeyRune && p.Rune == r }

// Written out rather than pulled from terminfo: bivy sends five of them, and a
// dependency to hold five strings is the trade this package exists to refuse.
const (
	enterAltScreen = "\x1b[?1049h"
	leaveAltScreen = "\x1b[?1049l"
	hideCursor     = "\x1b[?25l"
	showCursor     = "\x1b[?25h"
	cursorHome     = "\x1b[H"
	clearLine      = "\x1b[K"
	clearBelow     = "\x1b[J"
)

// Terminal is a terminal in raw mode, on the alternate screen.
type Terminal struct {
	in      *os.File
	out     *os.File
	state   *term.State
	keys    chan Press
	resized chan struct{}
	closed  chan struct{}
}

// ErrNotATerminal is returned when input or output is not a terminal, which is
// a normal thing to be: bivy is run from a pipe by tests and by anyone
// grepping its output.
var ErrNotATerminal = errors.New("not a terminal")

// Open puts the terminal into raw mode and switches to the alternate screen,
// which is what makes bivy leave no trace on the scrollback: on Close the
// terminal restores whatever was there before.
func Open(in, out *os.File) (*Terminal, error) {
	if !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return nil, ErrNotATerminal
	}

	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return nil, fmt.Errorf("putting the terminal into raw mode: %w", err)
	}

	t := &Terminal{
		in:      in,
		out:     out,
		state:   state,
		keys:    make(chan Press),
		resized: make(chan struct{}, 1),
		closed:  make(chan struct{}),
	}

	if _, err := io.WriteString(out, enterAltScreen+hideCursor); err != nil {
		_ = t.Close()
		return nil, err
	}

	go t.readKeys()
	go t.watchResize()
	return t, nil
}

// Close restores the terminal to exactly how it was found. Safe to call twice,
// and it must be: this runs from a deferred call on the ordinary path and a
// signal handler on the abrupt one, and a terminal left in raw mode is a shell
// nobody can type into.
func (t *Terminal) Close() error {
	select {
	case <-t.closed:
		return nil
	default:
		close(t.closed)
	}

	_, _ = io.WriteString(t.out, showCursor+leaveAltScreen)
	if t.state == nil {
		return nil
	}
	return term.Restore(int(t.in.Fd()), t.state)
}

// Size is the terminal's width and height in cells.
func (t *Terminal) Size() (width, height int) {
	w, h, err := term.GetSize(int(t.out.Fd()))
	if err != nil || w <= 0 || h <= 0 {
		return 80, 24
	}
	return w, h
}

// Draw replaces what is on screen with frame. Each line is cleared as it is
// written, rather than blanking the screen first: blanking shows the empty
// screen for one refresh, which is the flicker that makes a redraw on every
// keypress unpleasant.
func (t *Terminal) Draw(frame string) error {
	var b []byte
	b = append(b, cursorHome...)

	for _, c := range []byte(frame) {
		if c == '\n' {
			// Carriage return as well: in raw mode a newline moves down
			// without moving left, and every row would start further across
			// than the one above it.
			b = append(b, clearLine...)
			b = append(b, '\r', '\n')
			continue
		}
		b = append(b, c)
	}
	b = append(b, clearLine...)
	b = append(b, clearBelow...)

	_, err := t.out.Write(b)
	return err
}

// Keys yields decoded keypresses until the terminal is closed.
func (t *Terminal) Keys() <-chan Press { return t.keys }

// Resized fires when the terminal changes size, holding at most one pending
// notification: a redraw needs the current size, not a queue of stale ones.
func (t *Terminal) Resized() <-chan struct{} { return t.resized }

func (t *Terminal) readKeys() {
	defer close(t.keys)

	var pending []byte
	buf := make([]byte, 32)
	for {
		n, err := t.in.Read(buf)
		if n == 0 || err != nil {
			return
		}
		pending = append(pending, buf[:n]...)

		for len(pending) > 0 {
			press, used := DecodeKey(pending)
			if used == 0 {
				// An incomplete escape sequence. Wait for the rest rather
				// than reporting the escape as a keypress of its own.
				break
			}
			pending = pending[used:]
			if press.Key == KeyNone {
				continue
			}
			select {
			case t.keys <- press:
			case <-t.closed:
				return
			}
		}
	}
}

func (t *Terminal) watchResize() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	defer signal.Stop(ch)

	for {
		select {
		case <-ch:
			select {
			case t.resized <- struct{}{}:
			default:
			}
		case <-t.closed:
			return
		}
	}
}

// DecodeKey reads one keypress from the front of b, returning how many bytes
// it consumed — and zero when b holds the start of an escape sequence but not
// all of it, so the caller reads more rather than deciding a lone escape was
// pressed. Pure, so every sequence bivy understands is tested without a
// terminal.
func DecodeKey(b []byte) (Press, int) {
	if len(b) == 0 {
		return Press{}, 0
	}

	switch b[0] {
	case 0x1b:
		return decodeEscape(b)
	case '\r', '\n':
		return Press{Key: KeyEnter}, 1
	case 0x7f, 0x08:
		return Press{Key: KeyBackspace}, 1
	case 0x03, 0x04: // ctrl-c, ctrl-d
		return Press{Key: KeyInterrupt}, 1
	}

	// Any other control byte is a chord bivy has no use for. Consumed so the
	// stream advances, reported as nothing so it does nothing.
	if b[0] < 0x20 {
		return Press{}, 1
	}

	r, size := utf8.DecodeRune(b)
	if r == utf8.RuneError {
		// Either an invalid byte, or a character whose remaining bytes have
		// not arrived. Waiting is only correct for the second.
		if size <= 1 && !utf8.FullRune(b) {
			return Press{}, 0
		}
		return Press{}, size
	}
	return Press{Key: KeyRune, Rune: r}, size
}

func decodeEscape(b []byte) (Press, int) {
	// Terminals send a sequence in one write, so a short buffer beginning with
	// escape is an incomplete sequence rather than the escape key. A lone
	// escape is reported once the rest of the buffer proves it was alone.
	if len(b) == 1 {
		return Press{Key: KeyEscape}, 1
	}
	if b[1] != '[' && b[1] != 'O' {
		return Press{Key: KeyEscape}, 1
	}
	if len(b) < 3 {
		return Press{}, 0
	}

	switch b[2] {
	case 'A':
		return Press{Key: KeyUp}, 3
	case 'B':
		return Press{Key: KeyDown}, 3
	case 'H':
		return Press{Key: KeyHome}, 3
	case 'F':
		return Press{Key: KeyEnd}, 3
	}

	// A longer sequence: a parameterised CSI, ending at its final byte. Home
	// and End arrive this way on some terminals, and everything else here is
	// consumed so that its trailing bytes are not read as characters.
	if b[1] == '[' {
		for i := 2; i < len(b); i++ {
			if b[i] >= 0x40 && b[i] <= 0x7e {
				if b[i] == '~' && i == 3 {
					switch b[2] {
					case '1', '7':
						return Press{Key: KeyHome}, i + 1
					case '4', '8':
						return Press{Key: KeyEnd}, i + 1
					}
				}
				return Press{}, i + 1
			}
		}
		return Press{}, 0
	}
	return Press{}, 3
}
