package graphics

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"strings"
	"testing"
	"time"
)

// a terminal that answers the query, and one that says nothing.
type replier struct {
	reply string
	quiet bool
}

func (r replier) Read(p []byte) (int, error) {
	if r.quiet {
		// A terminal that does not know the sequence says nothing at all,
		// which from here is indistinguishable from a terminal thinking.
		time.Sleep(2 * probeWait)
		return 0, io.EOF
	}
	return copy(p, r.reply), nil
}

func TestProbeBelievesATerminalThatAnswers(t *testing.T) {
	var sent bytes.Buffer
	got := Probe(replier{reply: "\x1b_Gi=31;OK\x1b\\"}, &sent)

	if got != Kitty {
		t.Errorf("Probe = %v, want %v", got, Kitty)
	}
	if !strings.Contains(sent.String(), "a=q") {
		t.Errorf("the query was %q, want it to ask rather than draw", sent.String())
	}
	// The probe must not put anything on screen. t=d with a=q transmits and
	// asks; nothing is displayed.
	if strings.Contains(sent.String(), "a=T") {
		t.Errorf("the query draws something: %q", sent.String())
	}
}

// A terminal without the protocol ignores the sequence silently, so the probe
// has to give up on its own rather than wait for an answer that is not coming.
func TestProbeGivesUpOnSilence(t *testing.T) {
	began := time.Now()
	got := Probe(replier{quiet: true}, io.Discard)

	if got != None {
		t.Errorf("Probe = %v, want %v", got, None)
	}
	if took := time.Since(began); took > 2*probeWait {
		t.Errorf("waited %s for a terminal that was never going to answer", took)
	}
}

// Something answered, but not this question.
func TestProbeIgnoresAnUnrelatedReply(t *testing.T) {
	if got := Probe(replier{reply: "\x1b[?62;c"}, io.Discard); got != None {
		t.Errorf("Probe = %v on an unrelated reply, want %v", got, None)
	}
}

func TestProbeSurvivesATerminalItCannotWriteTo(t *testing.T) {
	if got := Probe(replier{reply: "\x1b_Gi=31;OK\x1b\\"}, failingWriter{}); got != None {
		t.Errorf("Probe = %v when the terminal could not be written to", got)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// a JPEG of the size a thumbnail actually arrives at.
func thumbnail(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestRenderDrawsAnImage(t *testing.T) {
	out, err := Render(thumbnail(t, 480, 360), 20, 6)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(out, "\x1b_G") {
		t.Errorf("the output does not begin with a graphics sequence: %.40q", out)
	}
	if !strings.HasSuffix(out, "\x1b\\") {
		t.Errorf("the output does not end a sequence properly: %.40q", out[len(out)-40:])
	}
	for _, want := range []string{"a=T", "f=100", "c=20", "r=6", "q=2", "C=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("the sequence does not carry %s", want)
		}
	}
}

// A reply would arrive on the same file the keys come from and be read as
// keystrokes, so the terminal is asked not to send one.
func TestRenderAsksForNoReply(t *testing.T) {
	out, err := Render(thumbnail(t, 64, 64), 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "q=2") {
		t.Error("the draw does not suppress the terminal's reply")
	}
}

// The protocol carries at most 4096 base64 bytes per sequence, and the chunks
// have to say whether another follows.
func TestRenderChunksALargeImage(t *testing.T) {
	out, err := Render(thumbnail(t, 480, 360), 60, 20)
	if err != nil {
		t.Fatal(err)
	}

	pieces := strings.Count(out, "\x1b_G")
	if pieces < 2 {
		t.Fatalf("a large image went out in %d sequence(s); it should be chunked", pieces)
	}
	if got := strings.Count(out, "m=1"); got != pieces-1 {
		t.Errorf("%d chunks say more follows, want %d", got, pieces-1)
	}
	if got := strings.Count(out, "m=0"); got != 1 {
		t.Errorf("%d chunks say they are last, want exactly 1", got)
	}
	// Only the first carries the picture's dimensions.
	if got := strings.Count(out, "a=T"); got != 1 {
		t.Errorf("%d chunks carry the header, want 1", got)
	}

	for _, piece := range strings.Split(out, "\x1b_G")[1:] {
		payload := piece[strings.Index(piece, ";")+1 : len(piece)-2]
		if len(payload) > chunk {
			t.Errorf("a chunk carries %d bytes, and the limit is %d", len(payload), chunk)
		}
	}
}

// What arrives is bytes a remote server chose. The only way to be sure they
// are an image is to have read them as one.
func TestRenderRefusesWhatIsNotAnImage(t *testing.T) {
	for _, data := range [][]byte{
		nil,
		[]byte("not an image"),
		[]byte("\x1b_Ga=T,f=100;pretending to be a graphics sequence\x1b\\"),
		append([]byte("\xff\xd8\xff"), []byte("a truncated jpeg")...),
	} {
		if _, err := Render(data, 10, 4); err == nil {
			t.Errorf("Render(%.20q) drew something", data)
		}
	}
}

func TestRenderNeedsRoom(t *testing.T) {
	for _, size := range [][2]int{{0, 4}, {4, 0}, {-1, 4}, {0, 0}} {
		if _, err := Render(thumbnail(t, 64, 64), size[0], size[1]); err == nil {
			t.Errorf("Render into %dx%d cells drew something", size[0], size[1])
		}
	}
}

// A thumbnail is sent down a pseudo-terminal, so what goes out has to be the
// size of the picture on screen rather than the size it arrived.
func TestRenderShrinksWhatItSends(t *testing.T) {
	small, err := Render(thumbnail(t, 1280, 720), 20, 6)
	if err != nil {
		t.Fatal(err)
	}
	full, err := Render(thumbnail(t, 1280, 720), 200, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(small) >= len(full) {
		t.Errorf("a small picture went out as %d bytes and a large one as %d", len(small), len(full))
	}
}

func TestScaleKeepsTheShape(t *testing.T) {
	for _, tc := range []struct {
		w, h, maxW, maxH int
		wantW, wantH     int
	}{
		{480, 360, 240, 240, 240, 180},
		{360, 480, 240, 240, 180, 240},
		{480, 360, 960, 960, 480, 360}, // never enlarged
		{100, 100, 10, 10, 10, 10},
	} {
		got := scale(image.NewRGBA(image.Rect(0, 0, tc.w, tc.h)), tc.maxW, tc.maxH).Bounds()
		if got.Dx() != tc.wantW || got.Dy() != tc.wantH {
			t.Errorf("scale(%dx%d into %dx%d) = %dx%d, want %dx%d",
				tc.w, tc.h, tc.maxW, tc.maxH, got.Dx(), got.Dy(), tc.wantW, tc.wantH)
		}
	}
}

// Dropping pixels to shrink a photograph makes a field of speckles. Averaging
// them does not, and the difference shows on a picture that is all edges.
func TestScaleAveragesRatherThanSamples(t *testing.T) {
	const size = 64
	src := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := range size {
		for x := range size {
			c := color.RGBA{A: 255}
			if (x+y)%2 == 0 {
				c = color.RGBA{R: 255, G: 255, B: 255, A: 255}
			}
			src.Set(x, y, c)
		}
	}

	// A chequerboard averaged down is grey. Sampled down it is black or white.
	got := scale(src, 8, 8)
	r, _, _, _ := got.At(4, 4).RGBA()
	if r < 0x4000 || r > 0xC000 {
		t.Errorf("a chequerboard scaled down is %04x, which is not the average of its pixels", r)
	}
}

// Clearing is not optional: writing text over a picture does not remove it.
func TestClearIsAGraphicsSequence(t *testing.T) {
	if !strings.HasPrefix(Clear, "\x1b_G") || !strings.HasSuffix(Clear, "\x1b\\") {
		t.Errorf("Clear = %q, which is not a graphics sequence", Clear)
	}
	if !strings.Contains(Clear, "a=d") {
		t.Errorf("Clear = %q, which does not delete anything", Clear)
	}
}

func TestCapabilitySaysWhatItIs(t *testing.T) {
	if got := Kitty.String(); !strings.Contains(got, "kitty") {
		t.Errorf("Kitty.String() = %q", got)
	}
	if got := None.String(); !strings.Contains(got, "text") {
		t.Errorf("None.String() = %q", got)
	}
}

// The picture that goes out is a picture, not the bytes that arrived.
func TestRenderReEncodesRatherThanForwarding(t *testing.T) {
	original := thumbnail(t, 64, 64)
	out, err := Render(original, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, string(original[:16])) {
		t.Error("the original bytes were forwarded to the terminal")
	}

	// And what it sent decodes as a PNG, which is what f=100 promises.
	payload := out[strings.Index(out, ";")+1:]
	payload = payload[:strings.Index(payload, "\x1b\\")]
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := png.Decode(bytes.NewReader(raw)); err != nil && len(raw) > 8 {
		// A chunked image only has its first slice here, so a short read is
		// expected; a wrong magic number is not.
		if !bytes.HasPrefix(raw, []byte("\x89PNG")) {
			t.Errorf("what went out is not a PNG: %.8q", raw)
		}
	}
}

// The picture goes beside the text, not instead of it, so it must not move the
// cursor: the caller writes the rows afterwards and indents them past it.
func TestRenderLeavesTheCursorAlone(t *testing.T) {
	out, err := Render(thumbnail(t, 64, 64), 6, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "C=1") {
		t.Error("the picture moves the cursor, so text after it lands below rather than beside")
	}
}
