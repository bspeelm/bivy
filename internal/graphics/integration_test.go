//go:build integration

// Behind a build tag because it fetches a real thumbnail. The suite must not
// need the network: a test that does passes where it was written and fails
// where the artifact is built.
//
//	go test -tags=integration ./internal/graphics/
package graphics

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/png"
	"strings"
	"testing"

	"github.com/bspeelm/bivy/internal/feed"
)

// The whole path a picture takes, on a real one: fetched by the package that
// may touch the network, decoded from whatever the server sent, scaled, and
// re-encoded as what the protocol promises.
//
// It cannot show that a terminal draws it. It can show that everything up to
// handing the terminal the bytes is right, which is the part bivy owns.
func TestARealThumbnailBecomesARealSequence(t *testing.T) {
	// A video that has been on the service for many years.
	const video = "dQw4w9WgXcQ"

	data, err := feed.New().Thumbnail(context.Background(), video)
	if err != nil {
		t.Skipf("could not fetch a thumbnail: %v", err)
	}
	t.Logf("fetched %d bytes", len(data))
	if len(data) < 1000 {
		t.Fatalf("fetched %d bytes, which is not a picture", len(data))
	}

	out, err := Render(data, 28, 7)
	if err != nil {
		t.Fatalf("a real thumbnail would not render: %v", err)
	}
	t.Logf("rendered %d bytes of escape sequence", len(out))

	if !strings.Contains(out, "c=28,r=7") {
		t.Error("the sequence does not ask for the cells it was given")
	}

	// Reassemble what was sent and read it back as the image it claims to be.
	var payload strings.Builder
	for _, piece := range strings.Split(out, "\x1b_G")[1:] {
		body := piece[strings.Index(piece, ";")+1:]
		payload.WriteString(strings.TrimSuffix(body, "\x1b\\"))
	}

	raw, err := base64.StdEncoding.DecodeString(payload.String())
	if err != nil {
		t.Fatalf("what went to the terminal is not base64: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("what went to the terminal is not the PNG f=100 promises: %v", err)
	}

	bounds := img.Bounds()
	t.Logf("the terminal receives a %dx%d PNG", bounds.Dx(), bounds.Dy())
	if bounds.Dx() > 28*cellWidth || bounds.Dy() > 7*cellHeight {
		t.Errorf("the picture is %dx%d pixels for %dx%d cells; it was not scaled down",
			bounds.Dx(), bounds.Dy(), 28, 7)
	}
	if bounds.Dx() < 32 || bounds.Dy() < 16 {
		t.Errorf("the picture is %dx%d pixels, which is not a thumbnail", bounds.Dx(), bounds.Dy())
	}
}
