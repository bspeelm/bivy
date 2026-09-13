// Package mpv launches mpv and drives it over an IPC socket.
//
// Started idle and given work down the socket rather than handed a URL and
// abandoned: that buys knowing when playback actually ended, and lets a second
// video reuse the process already open. mpv receives no positional argument at
// all (§7).
package mpv

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bspeelm/bivy/internal/media"
	"github.com/bspeelm/bivy/internal/visitor"
)

// Minimum is the oldest mpv bivy drives: --input-ipc-server arrived in 0.17.0
// and --vo=gpu in 0.29.0, and the later of the two is the floor.
const Minimum = "0.29.0"

const (
	dirPerm   fs.FileMode = 0o700
	startWait             = 10 * time.Second
	quitWait              = 3 * time.Second

	// How recent an error has to be to explain a playback that stopped. Long
	// enough to cover a slow teardown, short enough that a complaint from an
	// earlier video does not get blamed on this one.
	troubleWindow = 30 * time.Second

	// sun_path is 108 bytes on Linux and 104 on macOS, and the limit belongs
	// to that struct rather than to the filesystem. A Mac's temporary
	// directory is already fifty characters before bivy adds anything.
	maxSocketPath = 100
)

// Event is something mpv reported.
type Event struct {
	Name string
	// Reason is set on end-file: "eof" when the video played to its end,
	// "error" when it could not be played, and something else when it was
	// stopped, skipped or quit.
	Reason string
	// Detail is what mpv said went wrong, on an end-file that failed.
	Detail string
}

// Failed reports that playback ended because it could not happen, rather than
// because it finished or was stopped.
func (e Event) Failed() bool { return e.Name == "end-file" && e.Reason == "error" }

// Finished distinguishes a video reaching its own end from one being stopped:
// the difference between watched and closed.
func (e Event) Finished() bool { return e.Name == "end-file" && e.Reason == "eof" }

// Options configures a player. The zero value uses mpv from PATH and puts its
// socket under the runtime directory.
type Options struct {
	// Binary is the mpv to run. Empty means "mpv", found on PATH.
	Binary string
	// Args are passed to the process before bivy's own flags. Used by tests to
	// steer a stand-in; nothing in bivy sets it.
	Args []string
	// Env replaces the child's environment when non-nil.
	Env []string
	// RuntimeDir is where the socket directory is created. Empty means
	// XDG_RUNTIME_DIR, or the system temporary directory where that is unset —
	// which is every Mac.
	RuntimeDir string
}

// Player is a running mpv.
type Player struct {
	cmd    *exec.Cmd
	conn   net.Conn
	dir    string
	events chan Event

	// exited is closed when the process is gone. Closed rather than sent to,
	// because cmd.Wait may be called only once and more than one place needs
	// to know: a value would be consumed by whichever asked first, and the
	// next reader would wait for a process that had already exited.
	exited <-chan struct{}

	mu      sync.Mutex
	nextID  int
	waiting map[int]chan reply
	// trouble is the last thing mpv complained about, and when. Kept because
	// an end-file says only that playback stopped: mpv dying and the user
	// closing the window are the same event with the same reason, and this is
	// what tells them apart.
	trouble   string
	troubleAt time.Time

	closeOnce sync.Once
	closed    chan struct{}
}

type request struct {
	Command   []any `json:"command"`
	RequestID int   `json:"request_id"`
}

type reply struct {
	Error     string          `json:"error"`
	Data      json.RawMessage `json:"data"`
	RequestID int             `json:"request_id"`
	Event     string          `json:"event"`
	Reason    string          `json:"reason"`
	FileError string          `json:"file_error"`
	Level     string          `json:"level"`
	Prefix    string          `json:"prefix"`
	Text      string          `json:"text"`
}

// Start launches mpv and connects to it. The socket lives in a 0700 directory
// of bivy's own making, removed on Close — a socket rather than an argument
// because argv is world-readable, and what travels over it is what the user is
// watching.
func Start(ctx context.Context, opt Options) (*Player, error) {
	binary := opt.Binary
	if binary == "" {
		binary = "mpv"
	}

	dir, err := os.MkdirTemp(runtimeDir(opt.RuntimeDir), "bivy-")
	if err != nil {
		return nil, fmt.Errorf("making a place for the mpv socket: %w", err)
	}
	if err := os.Chmod(dir, dirPerm); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	socket := filepath.Join(dir, "mpv.sock")
	if len(socket) > maxSocketPath {
		_ = os.RemoveAll(dir)
		// Checked rather than left to bind: the symptom is mpv exiting with
		// nothing to say, and the cause is nowhere near it.
		return nil, fmt.Errorf("the socket path %s is %d bytes and a unix socket may not exceed %d; set XDG_RUNTIME_DIR somewhere shorter",
			socket, len(socket), maxSocketPath)
	}

	cookies, err := visitor.Write(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}

	args := append(append([]string(nil), opt.Args...), flags(socket, cookies)...)
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = opt.Env
	// Streams left nil, which exec connects to the null device: mpv's own
	// output would otherwise land on the screen bivy is drawing.

	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("starting %s: %w", binary, err)
	}

	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()

	conn, err := dial(ctx, socket, exited)
	if err != nil {
		_ = cmd.Process.Kill()
		<-exited
		_ = os.RemoveAll(dir)
		return nil, err
	}

	p := &Player{
		cmd:     cmd,
		conn:    conn,
		dir:     dir,
		exited:  exited,
		events:  make(chan Event, 16),
		waiting: map[int]chan reply{},
		closed:  make(chan struct{}),
	}
	go p.read()

	// Ask for mpv's own errors down the socket. --terminal=no keeps them off
	// the screen bivy is drawing, which would otherwise mean nobody ever sees
	// them at all.
	if err := p.send("request_log_messages", "error"); err != nil {
		_ = p.Close()
		return nil, err
	}
	return p, nil
}

// flags is every option bivy gives mpv, and the reason for each. Version-gated
// ones name the version that makes the gate deletable, so the compatibility
// scar tissue has an expiry date (PLAN.md §4).
//
// --force-window is deliberately absent. It would put a window up the moment
// mpv starts, which sounds like the answer to "did anything happen" — but bivy
// already answers that on its own status line, and the cost is an empty window
// sitting there for the whole session, including after every video ends. On a
// compositor that offers no decorations it has no title bar to close it by
// either, so it is a pane the user cannot get rid of and did not ask for.
func flags(socket, cookies string) []string {
	return []string{
		// ADR-006: the user's own mpv setup is neither read nor written.
		"--no-config",
		// Wait for something to play instead of exiting, so one process
		// serves every video chosen in a session.
		"--idle=yes",
		// The control channel. Since mpv 0.17.0.
		"--input-ipc-server=" + socket,
		// The hardware decode path. Since mpv 0.29.0; before that it was
		// spelled --vo=opengl, which is the gate this constant names.
		"--vo=gpu",
		// bivy owns the terminal, and mpv writing status lines into the
		// screen bivy is drawing is the most visible way this goes wrong.
		"--terminal=no",
		// One invented visitor, replaced before every video (ADR-016).
		"--ytdl-raw-options=cookies=" + cookies,
	}
}

func runtimeDir(override string) string {
	if filepath.IsAbs(override) {
		return override
	}
	if d := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(d) {
		return d
	}
	return os.TempDir()
}

// dial waits for mpv to create the socket, which it does after the process is
// already running — so connecting fails for an ordinary reason at first.
// Polling ends when mpv exits, so a binary that refuses one of the flags above
// is reported now rather than after the full timeout.
func dial(ctx context.Context, socket string, exited <-chan struct{}) (net.Conn, error) {
	deadline := time.Now().Add(startWait)

	for {
		conn, err := net.Dial("unix", socket)
		if err == nil {
			return conn, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-exited:
			return nil, errors.New("mpv exited before it opened its control socket")
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("mpv did not open %s within %s", socket, startWait)
		}
	}
}

// Play asks mpv to play a video. It takes a video rather than a URL so that
// what reaches mpv is derived from an identifier bivy has checked, never a
// string that arrived from a feed.
func (p *Player) Play(v media.Video) error {
	if !media.IsVideoID(v.ID) {
		return fmt.Errorf("%q is not a video identifier", v.ID)
	}

	// Before the load, not after: what the service writes back has to outlast
	// the playback that needs it, and the next video discards it (ADR-016).
	if _, err := visitor.Write(p.dir); err != nil {
		return err
	}

	_, err := p.command("loadfile", v.URL(), "replace")
	return err
}

// Stop ends playback without closing the player.
func (p *Player) Stop() error {
	_, err := p.command("stop")
	return err
}

// Events yields what mpv reports. Buffered and dropped when full rather than
// blocking, so mpv is never held up because bivy is busy drawing.
func (p *Player) Events() <-chan Event { return p.events }

// Close quits mpv, waits for it, and removes the socket directory.
func (p *Player) Close() error {
	var err error
	p.closeOnce.Do(func() {
		// Asked to quit before the player is marked closed, because commands
		// are refused after that and this one would never be sent. Sent
		// without waiting for a reply: mpv is entitled to exit rather than
		// answer, and waiting for an answer that is not coming turns every
		// quit into a timeout.
		_ = p.send("quit")
		close(p.closed)

		select {
		case <-p.exited:
		case <-time.After(quitWait):
			// It was asked politely and did not go. §7 says quitting bivy
			// stops mpv, and an unattended window nobody asked for is worse
			// than an abrupt exit.
			_ = p.cmd.Process.Kill()
			<-p.exited
		}

		_ = p.conn.Close()
		err = os.RemoveAll(p.dir)
	})
	return err
}

func (p *Player) command(args ...any) (json.RawMessage, error) {
	select {
	case <-p.closed:
		return nil, errors.New("the player is closed")
	default:
	}

	p.mu.Lock()
	p.nextID++
	id := p.nextID
	answer := make(chan reply, 1)
	p.waiting[id] = answer
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		delete(p.waiting, id)
		p.mu.Unlock()
	}()

	if err := p.write(id, args); err != nil {
		return nil, err
	}

	select {
	case r := <-answer:
		if r.Error != "" && r.Error != "success" {
			return nil, fmt.Errorf("mpv refused %v: %s", args[0], r.Error)
		}
		return r.Data, nil
	case <-p.closed:
		return nil, errors.New("the player closed while waiting for a reply")
	case <-time.After(startWait):
		return nil, fmt.Errorf("mpv did not answer %v", args[0])
	}
}

// send writes a command and does not wait for the reply.
func (p *Player) send(args ...any) error {
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	p.mu.Unlock()
	return p.write(id, args)
}

func (p *Player) write(id int, args []any) error {
	line, err := json.Marshal(request{Command: args, RequestID: id})
	if err != nil {
		return err
	}
	if _, err := p.conn.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("talking to mpv: %w", err)
	}
	return nil
}

func (p *Player) read() {
	defer close(p.events)

	scanner := bufio.NewScanner(p.conn)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)

	for scanner.Scan() {
		var r reply
		// A separate program whose output format is not bivy's to
		// guarantee, so an unparseable line is skipped rather than ending
		// the conversation.
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			continue
		}

		if r.Event == "log-message" {
			p.remember(r)
			continue
		}
		if r.Event == "start-file" {
			p.forget()
		}
		if r.Event != "" {
			select {
			case p.events <- p.describe(r):
			default:
			}
			continue
		}

		p.mu.Lock()
		answer, waiting := p.waiting[r.RequestID]
		p.mu.Unlock()
		if waiting {
			answer <- r
		}
	}
}

// remember keeps the first complaint of an attempt, not the last: a failure
// arrives as a cascade, and every line after the first is a consequence of it.
func (p *Player) remember(r reply) {
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.trouble != "" {
		return
	}
	p.trouble, p.troubleAt = text, time.Now()
}

// forget clears the last attempt's complaint, so a new one starts silent.
func (p *Player) forget() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.trouble, p.troubleAt = "", time.Time{}
}

// describe turns a reply into an event, attaching what mpv complained about
// when playback ended for a reason that is not the end of the video.
//
// mpv reports that a file ended, not why it could not continue. A window the
// user closed and a player that died produce the same reason, and only the
// error alongside tells them apart.
func (p *Player) describe(r reply) Event {
	e := Event{Name: r.Event, Reason: r.Reason, Detail: r.FileError}
	if e.Name != "end-file" || e.Reason == "eof" {
		return e
	}

	// What mpv complained about outranks its file_error, which is a category
	// rather than a reason: "loading failed" is what it says whether the
	// service refused, the network went, or the file is not a video.
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.trouble != "" && time.Since(p.troubleAt) < troubleWindow {
		e.Detail = p.trouble
		p.trouble = ""
	}
	return e
}

// Version asks an mpv binary what it is.
func Version(ctx context.Context, binary string) (string, error) {
	if binary == "" {
		binary = "mpv"
	}
	out, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return "", err
	}
	return ParseVersion(string(out)), nil
}

// ParseVersion reads the version out of mpv's own banner, which looks like
// "mpv v0.38.0 Copyright ...".
func ParseVersion(banner string) string {
	fields := strings.Fields(banner)
	if len(fields) < 2 {
		return ""
	}
	v := strings.TrimPrefix(fields[1], "v")
	if _, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0]); err != nil {
		return ""
	}
	return v
}

// OlderThan reports whether version is below the minimum bivy supports. An
// unreadable version is not treated as too old: refusing to run because a
// banner changed shape would be worse than trying and reporting what happened.
func OlderThan(version, minimum string) bool {
	have, want := parts(version), parts(minimum)
	if have == nil {
		return false
	}
	for i := range want {
		if i >= len(have) {
			return true
		}
		if have[i] != want[i] {
			return have[i] < want[i]
		}
	}
	return false
}

func parts(v string) []int {
	var out []int
	for _, f := range strings.Split(v, ".") {
		n, err := strconv.Atoi(strings.TrimFunc(f, func(r rune) bool { return r < '0' || r > '9' }))
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}
