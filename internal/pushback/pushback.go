// Package pushback records that the service asked bivy to stop, and is the one
// place the rest of the program asks whether it did. ADR-017 has the reasoning.
//
// It holds session state and nothing else: nothing here opens a socket or a
// file, so the §0 budget keeping net/http to one package is untouched.
package pushback

import (
	"errors"
	"strings"
	"sync"
	"time"
)

// The two ways the service says no, phrased to sit inside Says below.
const (
	RateLimited = "is rate-limiting"
	BotCheck    = "is bot-checking"
)

// ErrStopped replaces a request once the gate is shut; nothing retries it.
var ErrStopped = errors.New("bivy has stopped making requests this session")

// Gate is the session's answer to whether the service has pushed back. The
// zero value is open, and a nil *Gate is open forever, so a caller that was
// never given one needs no branch.
type Gate struct {
	mu     sync.Mutex
	at     time.Time
	reason string
}

// Trip shuts the gate for the rest of the session. The first reason is kept:
// a refusal arrives as a cascade, and only the first line names the cause.
func (g *Gate) Trip(reason string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.at.IsZero() {
		g.at, g.reason = time.Now(), reason
	}
}

func (g *Gate) Shut() bool {
	_, _, ok := g.Tripped()
	return ok
}

func (g *Gate) Tripped() (reason string, at time.Time, ok bool) {
	if g == nil {
		return "", time.Time{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reason, g.at, !g.at.IsZero()
}

// Err is ErrStopped once the gate is shut: one line at the top of a request.
func (g *Gate) Err() error {
	if g.Shut() {
		return ErrStopped
	}
	return nil
}

// Says is the status line: what is happening, and the two things that change it.
func (g *Gate) Says() string {
	reason, _, ok := g.Tripped()
	if !ok {
		return ""
	}
	return "YouTube " + reason + " this connection — bivy has stopped making requests; try later or from another network"
}

// challenges are what the service says when it wants a human rather than a
// client, across an extractor's stderr, an mpv complaint, and a page served
// where a feed was asked for.
var challenges = []struct {
	marker string
	reason string
}{
	{"too many requests", RateLimited},
	{"http error 429", RateLimited},
	{"error 429", RateLimited},
	{"confirm you're not a bot", BotCheck},
	{"confirm you’re not a bot", BotCheck},
	{"sign in to confirm", BotCheck},
	{"unusual traffic", BotCheck},
	{"consent.youtube.com", BotCheck},
	{"before you continue to youtube", BotCheck},
}

// Detect reports whether text is the service pushing back rather than an
// ordinary failure, and which kind it is.
func Detect(text string) (string, bool) {
	text = strings.ToLower(text)
	for _, c := range challenges {
		if strings.Contains(text, c.marker) {
			return c.reason, true
		}
	}
	return "", false
}
