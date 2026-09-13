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
	"github.com/bspeelm/bivy/internal/visitor"
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
		RuntimeDir: shortTempDir(t),
	}
}

// shortTempDir is t.TempDir with the test's name left out of the path.
//
// t.TempDir spells the test name into the directory, and a unix socket path
// may not exceed about a hundred bytes. On a Mac the temporary directory is
// fifty of those before anything is added, so a descriptively named test
// cannot open a socket at all — which arrives as mpv exiting for no stated
// reason, nowhere near the cause.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
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
			if behaviour == "refused-by-the-service" {
				// A real refusal, in the order a real one arrives: the cause,
				// then two consequences, then a file_error that names a
				// category rather than a reason.
				for _, line := range []string{
					"ERROR: [youtube] aaaaaaaaaaa: Sign in to confirm you're not a bot. Use --cookies for the authentication. See  https://example.invalid/faq  for how to pass cookies",
					"youtube-dl failed: unexpected error occurred",
					"Failed to open https://www.youtube.com/watch?v=aaaaaaaaaaa.",
				} {
					_ = encoder.Encode(map[string]any{
						"event": "log-message", "prefix": "ytdl_hook",
						"level": "error", "text": line + "\n",
					})
				}
				_ = encoder.Encode(map[string]any{
					"event": "end-file", "reason": "error", "file_error": "loading failed",
				})
			}
			// A line that is not JSON at all, which must not end the
			// conversation.
			_, _ = conn.Write([]byte("this is not json\n"))
		case name == "request_log_messages":
			_ = encoder.Encode(map[string]any{"error": "success", "request_id": req.RequestID})
			if behaviour == "dies-mid-playback" {
				// mpv complains down the socket and then goes, which is what
				// a player losing its display actually does.
				_ = encoder.Encode(map[string]any{
					"event": "log-message", "prefix": "vo/gpu/wayland",
					"level": "fatal", "text": "Error occurred on the display fd\n",
				})
				_ = encoder.Encode(map[string]any{"event": "end-file", "reason": "quit"})
			}
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

// A player that dies and a window the user closed arrive as the same event
// with the same reason. Only what mpv said alongside tells them apart, and
// --terminal=no means the socket is the only place it can say it.
func TestAPlayerThatDiesExplainsItself(t *testing.T) {
	p := start(t, "dies-mid-playback")

	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, open := <-p.Events():
			if !open {
				t.Fatal("the player went away without an end-file")
			}
			if e.Name != "end-file" {
				continue
			}
			if e.Finished() {
				t.Fatal("a death was reported as reaching the end")
			}
			if e.Detail == "" {
				t.Fatal("playback stopped with nothing to show for it")
			}
			if !strings.Contains(e.Detail, "display fd") {
				t.Errorf("detail = %q, want what mpv actually said", e.Detail)
			}
			return
		case <-deadline:
			t.Fatal("no end-file arrived")
		}
	}
}

// A window the user closed says nothing, because there is nothing wrong.
func TestAStoppedPlaybackWithNoComplaintStaysQuiet(t *testing.T) {
	p := start(t, "stopped-early")

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
			if e.Detail != "" {
				t.Errorf("an ordinary stop carried %q", e.Detail)
			}
			return
		case <-deadline:
			t.Fatal("no end-file arrived")
		}
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
	args := flags("/run/somewhere/mpv.sock", "/run/somewhere/cookies.txt")

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

// An idle mpv must not put a window up.
//
// --force-window is the obvious way to answer "did pressing enter do
// anything", and it is the wrong one: bivy answers that on its own status
// line, and a forced window then sits there for the rest of the session,
// empty, including after every video ends. Three of them were left on a
// desktop before this test existed — and on a compositor that offers no
// decorations they have no title bar to close them by.
func TestAnIdlePlayerPutsNoWindowUp(t *testing.T) {
	if contains(flags("/run/somewhere/mpv.sock", "/run/somewhere/cookies.txt"), "--force-window") {
		t.Error("bivy forces a window, which means an empty one whenever nothing is playing")
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
	runtime := shortTempDir(t)

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
	marker := filepath.Join(shortTempDir(t), "quit-received")

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

// A socket path too long to bind arrives as mpv exiting without saying why,
// and the cause is nowhere near the symptom. It is checked before the process
// is started so the message names the real problem.
func TestAnOverlongSocketPathIsReportedAsItself(t *testing.T) {
	deep := filepath.Join(shortTempDir(t), strings.Repeat("d", 120))
	if err := os.MkdirAll(deep, dirPerm); err != nil {
		t.Skipf("this filesystem will not make a path that long: %v", err)
	}

	opt := standIn(t, "idle")
	opt.RuntimeDir = deep

	_, err := Start(context.Background(), opt)
	if err == nil {
		t.Fatal("an unusable socket path started a player")
	}
	if !strings.Contains(err.Error(), "unix socket") {
		t.Errorf("error = %v, want it to name the socket path limit", err)
	}
	if strings.Contains(err.Error(), "exited") {
		t.Errorf("error = %v, which blames mpv for a path bivy chose", err)
	}
}

func TestStartReportsAMissingBinary(t *testing.T) {
	_, err := Start(context.Background(), Options{
		Binary:     filepath.Join(t.TempDir(), "no-such-mpv"),
		RuntimeDir: shortTempDir(t),
	})
	if err == nil {
		t.Fatal("starting a binary that does not exist reported success")
	}
}

// A failed start leaves nothing behind either.
func TestAFailedStartRemovesItsDirectory(t *testing.T) {
	runtime := shortTempDir(t)

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

// One failure arrives as a cascade, and mpv's own file_error is the least
// informative thing in it: "loading failed" is what it says whether the
// service refused, the network went, or the file was never a video. The cause
// is the first line, and the two after it are consequences of it.
func TestTheCauseOutranksTheCategoryAndTheConsequences(t *testing.T) {
	p := start(t, "refused-by-the-service")
	if err := p.Play(aVideo()); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case e, open := <-p.Events():
			if !open {
				t.Fatal("the player went away without an end-file")
			}
			if e.Name != "end-file" {
				continue
			}
			if e.Detail == "loading failed" {
				t.Fatal("the file_error won, so the screen says nothing anyone can act on")
			}
			if strings.Contains(e.Detail, "Failed to open") {
				t.Errorf("the last line of the cascade won: %q", e.Detail)
			}
			if !strings.Contains(e.Detail, "not a bot") {
				t.Errorf("detail = %q, want the reason the service gave", e.Detail)
			}
			return
		case <-deadline:
			t.Fatal("no end-file arrived")
		}
	}
}

// jarValue is the visitor identifier currently in a player's jar.
func jarValue(t *testing.T, p *Player) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(p.dir, visitor.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, "VISITOR_INFO1_LIVE") {
			fields := strings.Split(line, "\t")
			return fields[len(fields)-1]
		}
	}
	t.Fatalf("no visitor identifier in %q", b)
	return ""
}

// One mpv serves a whole session, so the jar is the only thing that can change
// between videos. If it did not, every video in a session would be the same
// stranger and the session itself would be the identifier (ADR-016).
func TestEachVideoIsANewVisitor(t *testing.T) {
	p := start(t, "play-to-the-end")

	if err := p.Play(aVideo()); err != nil {
		t.Fatal(err)
	}
	first := jarValue(t, p)

	if err := p.Play(aVideo()); err != nil {
		t.Fatal(err)
	}
	if second := jarValue(t, p); second == first {
		t.Errorf("both videos went out as %q", second)
	}
}

// The service writes its own cookies into the jar while a video resolves --
// including one that lasts six months. Keeping them would turn a per-video
// identifier into a durable one within a single session.
func TestWhatTheServiceSentDoesNotOutliveTheVideo(t *testing.T) {
	p := start(t, "play-to-the-end")
	if err := p.Play(aVideo()); err != nil {
		t.Fatal(err)
	}

	jar := filepath.Join(p.dir, visitor.FileName)
	b, err := os.ReadFile(jar)
	if err != nil {
		t.Fatal(err)
	}
	returned := string(b) + ".youtube.com\tTRUE\t/\tTRUE\t1804822338\t__Secure-YNID\t21.YT=opaque\n"
	if err := os.WriteFile(jar, []byte(returned), visitor.Perm); err != nil {
		t.Fatal(err)
	}

	if err := p.Play(aVideo()); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(jar)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), "__Secure-YNID") {
		t.Errorf("the service's own cookie survived into the next video: %q", after)
	}
}

// The jar is the one thing bivy writes that could recognise a session, so it
// goes when the session does -- which it gets for free by living beside the
// socket, and this is the test that notices if it stops.
func TestTheJarGoesWithTheSocketDirectory(t *testing.T) {
	p := start(t, "play-to-the-end")
	jar := filepath.Join(p.dir, visitor.FileName)
	if _, err := os.Stat(jar); err != nil {
		t.Fatalf("no jar beside the socket: %v", err)
	}

	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jar); !os.IsNotExist(err) {
		t.Errorf("the jar outlived the session: %v", err)
	}
}

// The cookie mpv is pointed at must be the one bivy owns and replaces, not a
// path it inherited from somewhere.
func TestTheCookieFlagPointsAtTheJarBivyWrote(t *testing.T) {
	args := flags("/run/somewhere/mpv.sock", "/run/somewhere/"+visitor.FileName)

	var found bool
	for _, a := range args {
		if strings.HasPrefix(a, "--ytdl-raw-options=cookies=") {
			found = true
			if !strings.HasSuffix(a, "/"+visitor.FileName) {
				t.Errorf("%q does not point at the jar", a)
			}
		}
	}
	if !found {
		t.Error("mpv is given no cookie, and the service refuses an extractor without one")
	}
}
