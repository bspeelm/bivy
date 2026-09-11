// Package graphics asks the terminal what it can draw, and draws it.
//
// Asking rather than guessing is the point (§2.2). Deciding from $TERM, or by
// trying image commands until one appears to work, is wrong the moment the
// program is run somewhere else — and wrong here means writing bytes a
// terminal prints as rubbish.
//
// It writes escape sequences derived from remote image bytes, which puts it on
// the load-bearing map: nothing a server sent reaches the terminal without
// being decoded as an image first and re-encoded here.
package graphics

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg" // thumbnails arrive as JPEG
	"image/png"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Capability is what the terminal said it could do.
type Capability int

const (
	// None means text only — not a failure: most terminals are this.
	None Capability = iota
	// Kitty means the terminal answered the graphics protocol's own query.
	Kitty
)

func (c Capability) String() string {
	if c == Kitty {
		return "kitty graphics"
	}
	return "text only"
}

// probeID is the image number the query uses. Any number does.
const probeID = 31

// probeWait is how long a terminal gets to answer. One that supports the
// protocol answers immediately; this is long enough for a slow pipe and short
// enough not to be a pause anyone notices.
const probeWait = 250 * time.Millisecond

// Probe asks the terminal whether it speaks the kitty graphics protocol. It
// must already be in raw mode, or the reply is swallowed by line discipline
// and every terminal looks like it cannot draw.
func Probe(in io.Reader, out io.Writer) Capability {
	// A one-pixel image, transmitted but not displayed. A terminal that knows
	// the protocol replies OK; one that does not says nothing at all.
	query := "\x1b_Gi=" + fmt.Sprint(probeID) + ",s=1,v=1,a=q,t=d,f=24;" +
		base64.StdEncoding.EncodeToString([]byte{0, 0, 0}) + "\x1b\\"
	if _, err := io.WriteString(out, query); err != nil {
		return None
	}

	answered := make(chan bool, 1)
	go func() {
		buf := make([]byte, 256)
		n, err := in.Read(buf)
		answered <- err == nil && bytes.Contains(buf[:n], []byte("\x1b_G"))
	}()

	select {
	case ok := <-answered:
		if ok {
			return Kitty
		}
	case <-time.After(probeWait):
		// The read is left running rather than blocking here for as long as a
		// silent terminal feels like being silent.
	}
	return None
}

// Clear removes every image the terminal is holding. Writing text over a
// picture does not remove it, so a frame that draws none has to say so.
const Clear = "\x1b_Ga=d,d=A\x1b\\"

// chunk is the largest base64 payload one escape sequence carries. The
// protocol's own limit.
const chunk = 4096

// Render turns image bytes into the sequences that draw them, scaled into a
// box of cells. Decoded and re-encoded rather than forwarded: the only way to
// be sure remote bytes are an image is to have read them as one.
func Render(data []byte, cols, rows int, cell Cell) (string, error) {
	if cols < 1 || rows < 1 {
		return "", fmt.Errorf("a picture needs room: %dx%d cells", cols, rows)
	}
	if !cell.Known() {
		cell = Assumed
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("that is not an image bivy can read: %w", err)
	}

	// Scaled here rather than by the terminal, which would scale only after
	// the whole image crossed the pseudo-terminal.
	// Scaled to fit the box, then padded to fill it exactly: the protocol
	// stretches whatever it is given across the cells it is told about, and a
	// thumbnail is not always the shape the box was built for.
	img = letterbox(scale(img, cols*cell.Width, rows*cell.Height), cols*cell.Width, rows*cell.Height)

	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		return "", err
	}
	payload := base64.StdEncoding.EncodeToString(encoded.Bytes())

	bounds := img.Bounds()
	var b strings.Builder
	for first := true; len(payload) > 0; first = false {
		piece := payload
		if len(piece) > chunk {
			piece = piece[:chunk]
		}
		payload = payload[len(piece):]

		more := 0
		if len(payload) > 0 {
			more = 1
		}

		b.WriteString("\x1b_G")
		if first {
			// q=2 asks the terminal not to answer: a reply arrives on the
			// same file the keys come from, and is read as keystrokes.
			fmt.Fprintf(&b, "a=T,f=100,q=2,s=%d,v=%d,c=%d,r=%d,", bounds.Dx(), bounds.Dy(), cols, rows)
		}
		fmt.Fprintf(&b, "m=%d;%s\x1b\\", more, piece)
	}
	return b.String(), nil
}

// Cell is a terminal cell in pixels. It belongs to the font, and guessing it
// is what makes a picture the wrong shape: the protocol stretches what it is
// given to fill the cells it is told about.
type Cell struct{ Width, Height int }

// Assumed is the fallback where the terminal will not say.
var Assumed = Cell{Width: 8, Height: 16}

// Known reports whether these are a terminal's own figures.
func (c Cell) Known() bool { return c.Width > 0 && c.Height > 0 }

// CellSize asks the terminal how big one cell is: CSI 16 t, answered with
// CSI 6 ; height ; width t. Silence is handled the way the graphics query
// handles it.
func CellSize(in io.Reader, out io.Writer) Cell {
	if _, err := io.WriteString(out, "\x1b[16t"); err != nil {
		return Cell{}
	}

	answered := make(chan Cell, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := in.Read(buf)
		if err != nil {
			answered <- Cell{}
			return
		}
		answered <- parseCell(string(buf[:n]))
	}()

	select {
	case c := <-answered:
		return c
	case <-time.After(probeWait):
		return Cell{}
	}
}

var cellReply = regexp.MustCompile(`\x1b\[6;([0-9]+);([0-9]+)t`)

// parseCell reads the height and width out of the terminal's answer.
func parseCell(reply string) Cell {
	m := cellReply.FindStringSubmatch(reply)
	if m == nil {
		return Cell{}
	}
	height, _ := strconv.Atoi(m[1])
	width, _ := strconv.Atoi(m[2])
	if width <= 0 || height <= 0 || width > 64 || height > 128 {
		return Cell{}
	}
	return Cell{Width: width, Height: height}
}

// scale shrinks an image to fit a box, keeping its shape. Box-averaged rather
// than nearest-neighbour: dropping pixels to reduce a photograph makes a field
// of speckles, and doing it properly is short enough not to need a library.
func scale(src image.Image, maxW, maxH int) image.Image {
	b := src.Bounds()
	if b.Dx() <= maxW && b.Dy() <= maxH {
		return src
	}

	width, height := b.Dx(), b.Dy()
	if width*maxH > height*maxW {
		height = height * maxW / width
		width = maxW
	} else {
		width = width * maxH / height
		height = maxH
	}
	width, height = max(width, 1), max(height, 1)

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			x0 := b.Min.X + x*b.Dx()/width
			x1 := max(b.Min.X+(x+1)*b.Dx()/width, x0+1)
			y0 := b.Min.Y + y*b.Dy()/height
			y1 := max(b.Min.Y+(y+1)*b.Dy()/height, y0+1)
			dst.Set(x, y, average(src, x0, y0, x1, y1))
		}
	}
	return dst
}

// letterbox centres an image in a box of exactly this size, on black.
func letterbox(src image.Image, width, height int) image.Image {
	b := src.Bounds()
	if b.Dx() == width && b.Dy() == height {
		return src
	}

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	at := image.Rect(
		(width-b.Dx())/2, (height-b.Dy())/2,
		(width-b.Dx())/2+b.Dx(), (height-b.Dy())/2+b.Dy(),
	)
	draw.Draw(dst, at, src, b.Min, draw.Src)
	return dst
}

// average is the mean colour of a rectangle of the source.
func average(src image.Image, x0, y0, x1, y1 int) color.RGBA64 {
	var r, g, b, a, n uint64
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			cr, cg, cb, ca := src.At(x, y).RGBA()
			r, g, b, a, n = r+uint64(cr), g+uint64(cg), b+uint64(cb), a+uint64(ca), n+1
		}
	}
	if n == 0 {
		return color.RGBA64{}
	}
	return color.RGBA64{
		R: uint16(r / n), G: uint16(g / n), B: uint16(b / n), A: uint16(a / n),
	}
}
