package tui

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		line string
		want Intent
	}{
		{"search cats", Search{Query: "cats"}},
		{"search  two words ", Search{Query: "two words"}},
		{"follow @someone", Follow{Target: "@someone"}},
		{"unfollow Aye", Unfollow{Target: "Aye"}},
		{"refresh", Refresh{}},
		{"help", ShowHelp{}},
		{"quit", Quit{}},
		// A command that takes none is not bothered by one.
		{"quit now", Quit{}},
		{"", nil},
		{"   ", nil},
	} {
		got, err := Parse(tc.line)
		if err != nil {
			t.Errorf("Parse(%q): %v", tc.line, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Parse(%q) = %#v, want %#v", tc.line, got, tc.want)
		}
	}
}

// A command that needs an argument and was given none says what it wanted,
// rather than doing nothing or doing something arbitrary.
func TestACommandMissingItsArgumentSaysWhatItWanted(t *testing.T) {
	for _, line := range []string{"search", "search   ", "follow", "unfollow"} {
		_, err := Parse(line)
		if err == nil {
			t.Errorf("Parse(%q) returned no error", line)
			continue
		}
		if !strings.Contains(err.Error(), "what?") {
			t.Errorf("Parse(%q) said %q, want it to ask for what it needs", line, err)
		}
	}
}

// A near miss is worth a suggestion; a wild one is worth pointing at the list.
func TestAnUnknownCommandSuggestsTheNearest(t *testing.T) {
	_, err := Parse("serach cats")
	if err == nil {
		t.Fatal("a misspelled command was accepted")
	}
	if !strings.Contains(err.Error(), "search") {
		t.Errorf("error = %q, want it to suggest search", err)
	}

	_, err = Parse("xyzzy")
	if err == nil {
		t.Fatal("nonsense was accepted")
	}
	if !strings.Contains(err.Error(), "tab") {
		t.Errorf("error = %q, want it to point at the completion list", err)
	}
}

func TestComplete(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		// One match that takes an argument lands the cursor where the
		// argument goes.
		{"se", "search "},
		{"fo", "follow "},
		{"un", "unfollow "},
		{"q", "quit"},
		{"h", "help"},
		// Two matches agree only on the letter they share.
		{"", ""},
		// Already complete, or past the name: left alone.
		{"search cats", "search cats"},
		{"search ", "search "},
		{"nonsense", "nonsense"},
	} {
		if got := Complete(tc.line); got != tc.want {
			t.Errorf("Complete(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// Completing twice must not keep appending. A line that is already as far as
// the matches agree is a fixed point.
func TestCompleteIsStable(t *testing.T) {
	for _, line := range []string{"s", "se", "f", "q", "", "search cats"} {
		once := Complete(line)
		if twice := Complete(once); twice != once {
			t.Errorf("Complete(%q) = %q, and again = %q", line, once, twice)
		}
	}
}

func TestMatching(t *testing.T) {
	if got := len(Matching("")); got != len(Commands) {
		t.Errorf("an empty line matched %d commands, want all %d", got, len(Commands))
	}
	if got := Matching("se"); len(got) != 1 || got[0].Name != "search" {
		t.Errorf("Matching(%q) = %v, want just search", "se", got)
	}
	if got := Matching("zzz"); len(got) != 0 {
		t.Errorf("Matching(%q) = %v, want nothing", "zzz", got)
	}
	// Once an argument has been started the command is settled, so the list
	// stops offering alternatives to it.
	if got := Matching("search ca"); len(got) != 1 || got[0].Name != "search" {
		t.Errorf("Matching with an argument = %v, want just the command", got)
	}
}

// ADR-011: a command exists because it takes an argument, or because it is
// rare. Anything that is neither is a key, and this is the test that notices
// when that stops being true.
func TestEveryCommandEarnsItsPlace(t *testing.T) {
	// The rare ones: no argument, and not something anyone does repeatedly.
	rare := map[string]bool{"help": true, "quit": true, "refresh": true}

	for _, c := range Commands {
		if c.Argument == "" && !rare[c.Name] {
			t.Errorf("%q takes no argument and is not rare; ADR-011 says it should be a key", c.Name)
		}
		if c.Summary == "" {
			t.Errorf("%q has no summary, and the completion list is half the bargain", c.Name)
		}
		if c.run == nil {
			t.Errorf("%q does nothing", c.Name)
		}
	}
}

// The command line must not reach anything §3 refuses.
func TestNoCommandReachesARefusedFeature(t *testing.T) {
	for _, c := range Commands {
		for _, refused := range []string{"download", "login", "log-in", "cookie", "account", "save"} {
			if strings.Contains(c.Name, refused) {
				t.Errorf("there is a %q command, and PLAN.md §3 refuses that", c.Name)
			}
		}
	}
}

func TestCommandNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Commands {
		if seen[c.Name] {
			t.Errorf("%q is defined twice", c.Name)
		}
		seen[c.Name] = true
	}
}
