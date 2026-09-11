//go:build integration

// The tests in this file drive a real mpv. They are behind a build tag because
// the suite must not require mpv to be installed: a test that asks whether
// something is present passes where it was written and fails where the
// artifact is built.
//
// What they cover is the half a stand-in cannot. The stand-in agrees with
// bivy's idea of the protocol by construction — it was written from the same
// understanding. Only mpv itself can disagree about whether the flags in
// flags() exist, whether the socket appears where bivy expects it, and whether
// a real end-file arrives with the reason bivy reads.
//
//	go test -tags=integration ./internal/mpv/
package mpv

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/bspeelm/bivy/internal/media"
)

func realMPV(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv is not installed")
	}
}

// The flags bivy passes are the thing most likely to be wrong against a
// version nobody tried, and the failure is total: mpv exits and nothing plays.
func TestRealMPVAcceptsOurFlags(t *testing.T) {
	realMPV(t)

	version, err := Version(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("mpv %s", version)
	if OlderThan(version, Minimum) {
		t.Skipf("mpv %s is older than the %s bivy supports", version, Minimum)
	}

	p, err := Start(context.Background(), Options{Env: os.Environ()})
	if err != nil {
		t.Fatalf("real mpv would not start with bivy's flags: %v", err)
	}
	defer p.Close()

	// A round trip proves the socket is bivy's idea of a socket and the
	// request/reply framing matches.
	data, err := p.command("get_property", "mpv-version")
	if err != nil {
		t.Fatalf("mpv did not answer a property request: %v", err)
	}
	if len(data) == 0 {
		t.Error("mpv answered with nothing")
	}
	t.Logf("over the socket: %s", data)
}

// Closing must leave nothing behind, against the real process rather than one
// that exits when asked politely because a test told it to.
func TestRealMPVIsCleanedUpAfterwards(t *testing.T) {
	realMPV(t)

	runtime := t.TempDir()
	p, err := Start(context.Background(), Options{Env: os.Environ(), RuntimeDir: runtime})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("Close left %v behind", entries)
	}
}

// The end-to-end claim: a real video, resolved by mpv's own extractor hook
// (ADR-010), reaching its own end and reporting the reason bivy reads as
// "watched".
//
// Behind an environment variable as well as the build tag, because it opens a
// window, needs the network, and needs yt-dlp. Set BIVY_PLAY_TEST to a video
// identifier to run it.
func TestRealMPVPlaysAVideoToItsEnd(t *testing.T) {
	realMPV(t)

	id := os.Getenv("BIVY_PLAY_TEST")
	if id == "" {
		t.Skip("set BIVY_PLAY_TEST=<video id> to play something for real")
	}
	if _, err := exec.LookPath("yt-dlp"); err != nil {
		t.Skip("yt-dlp is not installed, and mpv resolves through it (ADR-010)")
	}

	p, err := Start(context.Background(), Options{Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if err := p.Play(media.Video{ID: id}); err != nil {
		t.Fatalf("mpv refused to play %s: %v", id, err)
	}

	var started bool
	deadline := time.After(3 * time.Minute)
	for {
		select {
		case e, open := <-p.Events():
			if !open {
				t.Fatal("mpv went away mid-playback")
			}
			t.Logf("event: %s %s", e.Name, e.Reason)

			if e.Name == "start-file" {
				started = true
			}
			if e.Name != "end-file" {
				continue
			}
			if !started {
				t.Error("the file ended without ever starting")
			}
			if !e.Finished() {
				t.Errorf("end-file reason was %q; bivy reads that as not watched", e.Reason)
			}
			return

		case <-deadline:
			t.Fatal("nothing ended within three minutes")
		}
	}
}
