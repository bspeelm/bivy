package mpv

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bspeelm/bivy/internal/media"
)

// The tests run against a stand-in that speaks mpv's IPC protocol, not against
// mpv. The instinct fence is "test the code, not the machine": a test that
// needs mpv installed passes where it was written and fails where the artifact
// is built.
//
// The stand-in is this test binary, re-executed. That needs no build step and
// no fixture binary checked in, and it is the pattern os/exec's own tests use.
const (
	standInEnv = "BIVY_MPV_STAND_IN"
	// Where the stand-in records that it was asked to quit. A file, because
	// the evidence has to cross a process boundary — and the absence of it is
	// what a killed mpv looks like.
	quitMarkerEnv = "BIVY_MPV_QUIT_MARKER"
)

func standIn(t *testing.T, behaviour string) Options {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		Binary:     self,
		Args:       []string{"-test.run=TestStandInProcess", "--"},
		Env:        append(os.Environ(), standInEnv+"="+behaviour),
		RuntimeDir: t.TempDir(),
	}
}

// TestStandInProcess is not a test. It is the stand-in mpv, and it returns
// immediately unless it was started as one.
func TestStandInProcess(t *testing.T) {
	behaviour := os.Getenv(standInEnv)
	if behaviour == "" {
		return
	}
	defer os.Exit(0)

	var socket string
	for _, a := range os.Args {
		if strings.HasPrefix(a, "--input-ipc-server=") {
			socket = strings.TrimPrefix(a, "--input-ipc-server=")
		}
	}

	switch behaviour {
	case "refuse-a-flag":
		// What an mpv too old for one of the flags does: complains and exits
		// without ever creating the socket.
		return
	case "never-listen":
		time.Sleep(2 * time.Second)
		return
	}

	listener, err := net.Listen("unix", socket)
	if err != nil {
		return
	}
	defer listener.Close()

	conn, err := listener.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	encoder := json.NewEncoder(conn)
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var req struct {
			Command   []any `json:"command"`
			RequestID int   `json:"request_id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			continue
		}
		name, _ := req.Command[0].(string)

		switch {
		case name == "loadfile" && behaviour == "refuse-loadfile":
			_ = encoder.Encode(map[string]any{"error": "unsupported format", "request_id": req.RequestID})
			continue
		case name == "loadfile":
			_ = encoder.Encode(map[string]any{"error": "success", "request_id": req.RequestID})
			// Real mpv interleaves events with replies on the same socket.
			_ = encoder.Encode(map[string]any{"event": "start-file"})
			if behaviour == "play-to-the-end" {
				_ = encoder.Encode(map[string]any{"event": "end-file", "reason": "eof"})
			}
			if behaviour == "stopped-early" {
				_ = encoder.Encode(map[string]any{"event": "end-file", "reason": "quit"})
			}
			// A line that is not JSON at all, which must not end the
			// conversation.
			_, _ = conn.Write([]byte("this is not json\n"))
		case name == "quit":
			if marker := os.Getenv(quitMarkerEnv); marker != "" {
				_ = os.WriteFile(marker, []byte("asked"), 0o600)
			}
			_ = encoder.Encode(map[string]any{"error": "success", "request_id": req.RequestID})
			return
		default:
			_ = encoder.Encode(map[string]any{"error": "success", "request_id": req.RequestID})
		}
	}
}

func aVideo() media.Video { return media.Video{ID: "dQw4w9WgXcQ", Title: "A video"} }

func start(t *testing.T, behaviour string) *Player {
	t.Helper()
	p, err := Start(context.Background(), standIn(t, behaviour))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestPlay(t *testing.T) {
	p := start(t, "idle")

	if err := p.Play(aVideo()); err != nil {
		t.Fatal(err)
	}

	if got := waitFor(t, p, "start-file"); !got {
		t.Error("no start-file event arrived")
	}
}

// The difference between watched and closed. Marking a video watched because
// it was opened is a claim about something that did not happen.
func TestPlayingToTheEndIsDistinguishableFromStopping(t *testing.T) {
	for _, tc := range []struct {
		behaviour string
		finished  bool
	}{
		{"play-to-the-end", true},
		{"stopped-early", false},
	} {
		t.Run(tc.behaviour, func(t *testing.T) {
			p := start(t, tc.behaviour)
			if err := p.Play(aVideo()); err != nil {
				t.Fatal(err)
			}

			deadline := time.After(5 * time.Second)
			for {
				select {
				case e := <-p.Events():
					if e.Name != "end-file" {
						continue
					}
					if e.Finished() != tc.finished {
						t.Errorf("Finished() = %v for reason %q, want %v", e.Finished(), e.Reason, tc.finished)
					}
					return
				case <-deadline:
					t.Fatal("no end-file event arrived")
				}
			}
		})
	}
}

// A refusal from mpv is an error the caller sees, not a success with nothing
// on screen.
func TestARefusedCommandIsReported(t *testing.T) {
	p := start(t, "refuse-loadfile")

	err := p.Play(aVideo())
	if err == nil {
		t.Fatal("a refused loadfile reported success")
	}
	if !strings.Contains(err.Error(), "unsupported format") {
		t.Errorf("error = %v, want it to carry what mpv said", err)
	}
}

// What reaches mpv is derived from an identifier bivy has checked, never a
// string that arrived from a feed.
func TestPlayRefusesAVideoThatIsNotOne(t *testing.T) {
	p := start(t, "idle")

	for _, bad := range []string{"", "../../etc/passwd", "--exec=touch /tmp/pwned", "toolongtobeanid"} {
		if err := p.Play(media.Video{ID: bad}); err == nil {
			t.Errorf("Play(%q) returned no error", bad)
		}
	}
}

// The socket carries what the user is watching, and argv is world-readable.
func TestNothingUserControlledReachesTheArgv(t *testing.T) {
	args := flags("/run/somewhere/mpv.sock")

	for _, a := range args {
		if !strings.HasPrefix(a, "--") {
			t.Errorf("%q is a positional argument; mpv is given none", a)
		}
	}
	for _, want := range []string{"--no-config", "--idle=yes", "--terminal=no", "--vo=gpu"} {
		if !contains(args, want) {
			t.Errorf("the flags do not include %s", want)
		}
	}
	if contains(args, "--config-dir") {
		t.Error("bivy points mpv at a configuration directory; ADR-006 says it reads none")
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle || strings.HasPrefix(h, needle+"=") {
			return true
		}
	}
	return false
}

// A promise of no residue includes the socket. It is the one thing bivy writes
// outside the two directories in §8, and it is gone when bivy is.
func TestCloseRemovesTheSocketDirectory(t *testing.T) {
	runtime := t.TempDir()

	opt := standIn(t, "idle")
	opt.RuntimeDir = runtime

	p, err := Start(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("%d entries in the runtime directory, want 1", len(before))
	}
	socketDir := filepath.Join(runtime, before[0].Name())
	if fi, err := os.Stat(socketDir); err != nil {
		t.Fatal(err)
	} else if got := fi.Mode().Perm(); got != dirPerm {
		t.Errorf("socket directory mode = %v, want %v", got, dirPerm)
	}

	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadDir(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Errorf("Close left %d entries behind", len(after))
	}
}

// mpv is asked to quit rather than killed, so it tears down its window and
// releases the audio device the way it means to.
//
// This test exists because Close marked the player closed before sending the
// quit, and commands are refused after that — so the command was never sent
// and every exit was a three-second stall followed by a kill. The stand-in
// exits when its connection closes, which hid it: only asking the stand-in
// what it was actually told shows the difference.
func TestClosingAsksMPVToQuitRatherThanKillingIt(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "quit-received")

	opt := standIn(t, "idle")
	opt.Env = append(opt.Env, quitMarkerEnv+"="+marker)

	p, err := Start(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}

	began := time.Now()
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Error("mpv was never asked to quit; it was killed")
	}
	// A quit that is never sent shows up as the full kill timeout.
	if took := time.Since(began); took >= quitWait {
		t.Errorf("Close took %s, which is the kill timeout, not a clean exit", took)
	}
}

// Close runs from a deferred call on the ordinary path and from a signal
// handler on the abrupt one, so it happens twice.
func TestCloseTwiceIsHarmless(t *testing.T) {
	p, err := Start(context.Background(), standIn(t, "idle"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("the second Close returned %v", err)
	}
}

func TestCommandsAfterCloseAreRefused(t *testing.T) {
	p, err := Start(context.Background(), standIn(t, "idle"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Play(aVideo()); err == nil {
		t.Error("a command after Close reported success")
	}
}

// An mpv too old for one of the flags exits instead of listening. Reported
// now, rather than after the full startup timeout.
func TestAPlayerThatNeverListensIsReportedQuickly(t *testing.T) {
	began := time.Now()
	_, err := Start(context.Background(), standIn(t, "refuse-a-flag"))
	if err == nil {
		t.Fatal("a process that never opened a socket started successfully")
	}
	if took := time.Since(began); took > 5*time.Second {
		t.Errorf("took %s to notice; it should not have waited for the timeout", took)
	}
	if !strings.Contains(err.Error(), "exited") {
		t.Errorf("error = %v, want it to say mpv exited", err)
	}
}

func TestStartReportsAMissingBinary(t *testing.T) {
	_, err := Start(context.Background(), Options{
		Binary:     filepath.Join(t.TempDir(), "no-such-mpv"),
		RuntimeDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("starting a binary that does not exist reported success")
	}
}

// A failed start leaves nothing behind either.
func TestAFailedStartRemovesItsDirectory(t *testing.T) {
	runtime := t.TempDir()

	if _, err := Start(context.Background(), Options{
		Binary:     filepath.Join(t.TempDir(), "no-such-mpv"),
		RuntimeDir: runtime,
	}); err == nil {
		t.Fatal("expected a failure")
	}

	entries, err := os.ReadDir(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed start left %d entries behind", len(entries))
	}
}

func TestStartHonoursACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Start(ctx, standIn(t, "never-listen")); err == nil {
		t.Fatal("a cancelled context still started a player")
	}
}

func TestParseVersion(t *testing.T) {
	for _, tc := range []struct{ banner, want string }{
		{"mpv v0.38.0 Copyright © 2000-2024 mpv/MPlayer/mplayer2 projects", "0.38.0"},
		{"mpv 0.29.1", "0.29.1"},
		{"mpv v0.40.0-dirty", "0.40.0-dirty"},
		{"", ""},
		{"something else entirely", ""},
		{"mpv", ""},
	} {
		if got := ParseVersion(tc.banner); got != tc.want {
			t.Errorf("ParseVersion(%q) = %q, want %q", tc.banner, got, tc.want)
		}
	}
}

func TestOlderThan(t *testing.T) {
	for _, tc := range []struct {
		version string
		older   bool
	}{
		{"0.28.0", true},
		{"0.17.0", true},
		{"0.29.0", false},
		{"0.29.1", false},
		{"0.38.0", false},
		{"1.0.0", false},
		{"0.40.0-dirty", false},
		{"0.29", true},
		// An unreadable version is not treated as too old: refusing to run
		// because a banner changed shape is worse than trying.
		{"", false},
		{"unknown", false},
	} {
		if got := OlderThan(tc.version, Minimum); got != tc.older {
			t.Errorf("OlderThan(%q, %q) = %v, want %v", tc.version, Minimum, got, tc.older)
		}
	}
}

func waitFor(t *testing.T, p *Player, name string) bool {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, open := <-p.Events():
			if !open {
				return false
			}
			if e.Name == name {
				return true
			}
		case <-deadline:
			return false
		}
	}
}
