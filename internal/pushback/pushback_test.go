package pushback

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

// A gate nobody was given is open, because a caller without one should need no
// branch of its own.
func TestANilGateIsOpen(t *testing.T) {
	var g *Gate
	g.Trip(RateLimited)
	if g.Shut() {
		t.Error("a nil gate reports itself shut")
	}
	if err := g.Err(); err != nil {
		t.Errorf("a nil gate refused a request: %v", err)
	}
	if says := g.Says(); says != "" {
		t.Errorf("a nil gate has something to say: %q", says)
	}
}

func TestTheZeroValueIsOpen(t *testing.T) {
	var g Gate
	if g.Shut() || g.Err() != nil {
		t.Error("a fresh gate is already shut")
	}
}

// Once shut it stays shut: the remedy is to stop asking for the session, and a
// gate that reopened on its own would be the retry loop this replaces.
func TestTrippingIsForTheSession(t *testing.T) {
	var g Gate
	g.Trip(RateLimited)
	if !g.Shut() {
		t.Fatal("the gate did not shut")
	}
	if !errors.Is(g.Err(), ErrStopped) {
		t.Errorf("a shut gate returned %v, not ErrStopped", g.Err())
	}

	g.Trip(BotCheck)
	reason, at, ok := g.Tripped()
	if !ok || at.IsZero() {
		t.Fatal("the gate forgot that it had tripped")
	}
	// The first reason is the cause; what follows it is consequence.
	if reason != RateLimited {
		t.Errorf("the gate kept %q, not the reason it first shut for", reason)
	}
}

func TestTheStatusLineNamesTheReason(t *testing.T) {
	for _, reason := range []string{RateLimited, BotCheck} {
		var g Gate
		g.Trip(reason)
		says := g.Says()
		for _, want := range []string{"YouTube", reason, "stopped making requests", "another network"} {
			if !strings.Contains(says, want) {
				t.Errorf("the status line for %q does not mention %q: %q", reason, want, says)
			}
		}
	}
}

// The three places a refusal reaches bivy in words: a page where a feed was
// asked for, an extractor's stderr, and something mpv said.
func TestDetectReadsWhatTheServiceSaid(t *testing.T) {
	pushing := map[string]string{
		"ERROR: [youtube] abc: Sign in to confirm you're not a bot":            BotCheck,
		"ERROR: Unable to download webpage: HTTP Error 429: Too Many Requests": RateLimited,
		"<html><title>Before you continue to YouTube</title>":                  BotCheck,
		"https://consent.youtube.com/m?continue=...":                           BotCheck,
		"Our systems have detected unusual traffic from your network":          BotCheck,
		"[ffmpeg/demuxer] http: HTTP error 429 Too Many Requests":              RateLimited,
	}
	for text, want := range pushing {
		got, ok := Detect(text)
		if !ok {
			t.Errorf("Detect missed push-back in %q", text)
			continue
		}
		if got != want {
			t.Errorf("Detect called %q %q, want %q", text, got, want)
		}
	}

	// An ordinary failure is not push-back, and a gate that shut on one would
	// stop a session for a channel that simply moved.
	for _, ordinary := range []string{
		"ERROR: [youtube] abc: Video unavailable",
		"404 Not Found",
		"dial tcp: lookup www.youtube.com: no such host",
		"",
	} {
		if reason, ok := Detect(ordinary); ok {
			t.Errorf("Detect read %q as push-back (%q)", ordinary, reason)
		}
	}
}

func TestAGateIsSafeUnderConcurrency(t *testing.T) {
	var g Gate
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Trip(RateLimited)
			_ = g.Shut()
			_ = g.Says()
		}()
	}
	wg.Wait()
	if !g.Shut() {
		t.Error("the gate did not shut")
	}
}
