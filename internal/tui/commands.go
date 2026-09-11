package tui

import (
	"fmt"
	"sort"
	"strings"
)

// Intent is what a command asks for. The command line parses; the caller acts.
// Nothing here reaches the network, the disk or a process, which is what keeps
// this package renderable in a test.
type Intent any

type (
	// Search asks for videos matching words.
	Search struct{ Query string }
	// Channels asks for channels matching words.
	Channels struct{ Query string }
	// Follow adds a channel: a handle, a channel URL, an identifier, or the
	// name of a channel already on screen.
	Follow struct{ Target string }
	// Unfollow removes one, by the name shown or by identifier.
	Unfollow struct{ Target string }
	// Refresh fetches the followed feeds again.
	Refresh struct{}
	// ShowHelp lists the commands.
	ShowHelp struct{}
	// Quit ends the session.
	Quit struct{}
)

// Command is one entry in the command line.
type Command struct {
	Name    string
	Summary string
	// Argument names what follows, and is empty for commands that take none.
	Argument string
	run      func(arg string) (Intent, error)
}

// Commands is every command, in the order the completion list shows them.
//
// ADR-011: a command exists because it takes an argument, or because it is
// rare. Anything that is neither is a key, and adding a command for something
// that is neither is the first sign that fence has stopped holding.
var Commands = []Command{
	{
		Name:     "search",
		Summary:  "find videos by words",
		Argument: "words",
		run: func(arg string) (Intent, error) {
			if arg == "" {
				return nil, badArgument{"search", "something to look for"}
			}
			return Search{Query: arg}, nil
		},
	},
	{
		Name:     "channels",
		Summary:  "find channels by words, then press f to follow one",
		Argument: "words",
		run: func(arg string) (Intent, error) {
			if arg == "" {
				return nil, badArgument{"channels", "something to look for"}
			}
			return Channels{Query: arg}, nil
		},
	},
	{
		Name:     "follow",
		Summary:  "add a channel: @handle, a URL, an id, or a name on screen",
		Argument: "channel",
		run: func(arg string) (Intent, error) {
			if arg == "" {
				return nil, badArgument{"follow", "a channel"}
			}
			return Follow{Target: arg}, nil
		},
	},
	{
		Name:     "unfollow",
		Summary:  "stop following one, by name or id",
		Argument: "channel",
		run: func(arg string) (Intent, error) {
			if arg == "" {
				return nil, badArgument{"unfollow", "a channel"}
			}
			return Unfollow{Target: arg}, nil
		},
	},
	{
		Name:    "refresh",
		Summary: "fetch the feeds again",
		run:     func(string) (Intent, error) { return Refresh{}, nil },
	},
	{
		Name:    "help",
		Summary: "what the keys and commands do",
		run:     func(string) (Intent, error) { return ShowHelp{}, nil },
	},
	{
		Name:    "quit",
		Summary: "leave",
		run:     func(string) (Intent, error) { return Quit{}, nil },
	},
}

// badArgument is a command that needs something it was not given.
type badArgument struct{ name, wanted string }

func (e badArgument) Error() string { return e.name + " what? give it " + e.wanted }

// ambiguous is a prefix that more than one command answers to.
type ambiguous struct {
	typed   string
	matches []string
}

func (e ambiguous) Error() string {
	return fmt.Sprintf("%q could be %s", e.typed, strings.Join(e.matches, " or "))
}

// unknownCommand names what was typed and, where one is close, what was
// probably meant.
type unknownCommand struct{ typed, nearest string }

func (e unknownCommand) Error() string {
	if e.nearest != "" {
		return fmt.Sprintf("no command %q — did you mean %s?", e.typed, e.nearest)
	}
	return fmt.Sprintf("no command %q — press tab to see what there is", e.typed)
}

// Parse turns a command line into an intent.
//
// A command may be shortened to any prefix only one command answers to, so
// ":q" is quit and ":se cats" is a search. That is the spelling anyone who has
// used a modal editor will try first, and it is what makes quitting cheap
// enough to be worth taking off the keyboard.
func Parse(line string) (Intent, error) {
	name, arg := split(line)
	if name == "" {
		return nil, nil
	}

	// An exact name wins over being a prefix of a longer one.
	if c, found := lookup(name); found {
		return c.run(arg)
	}

	switch m := Matching(name); len(m) {
	case 1:
		return m[0].run(arg)
	case 0:
		return nil, unknownCommand{typed: name, nearest: nearest(name)}
	default:
		names := make([]string, 0, len(m))
		for _, c := range m {
			names = append(names, c.Name)
		}
		return nil, ambiguous{typed: name, matches: names}
	}
}

// split separates the command name from its argument.
func split(line string) (name, arg string) {
	line = strings.TrimLeft(line, " ")
	cut := strings.IndexByte(line, ' ')
	if cut < 0 {
		return line, ""
	}
	return line[:cut], strings.TrimSpace(line[cut+1:])
}

// Matching is every command the line could still become.
func Matching(line string) []Command {
	name, arg := split(line)
	// Once an argument has been started the command is settled, so the list
	// stops offering alternatives to it.
	if arg != "" || strings.HasSuffix(line, " ") {
		if c, found := lookup(name); found {
			return []Command{c}
		}
		return nil
	}

	var out []Command
	for _, c := range Commands {
		if strings.HasPrefix(c.Name, name) {
			out = append(out, c)
		}
	}
	return out
}

func lookup(name string) (Command, bool) {
	for _, c := range Commands {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// Complete extends the line as far as every match agrees on.
//
// A single match gains a trailing space when it takes an argument, so the
// cursor lands where the argument goes rather than against the name.
func Complete(line string) string {
	name, arg := split(line)
	if arg != "" || strings.HasSuffix(line, " ") {
		return line
	}

	m := Matching(name)
	if len(m) == 0 {
		return line
	}

	prefix := m[0].Name
	for _, c := range m[1:] {
		for !strings.HasPrefix(c.Name, prefix) {
			prefix = prefix[:len(prefix)-1]
		}
	}
	if len(m) == 1 && m[0].Argument != "" {
		return prefix + " "
	}
	return prefix
}

// nearest is the command closest to what was typed, when one is close enough
// to be worth suggesting.
func nearest(typed string) string {
	best, bestDistance := "", len(typed)/2+1

	names := make([]string, 0, len(Commands))
	for _, c := range Commands {
		names = append(names, c.Name)
	}
	sort.Strings(names)

	for _, name := range names {
		if d := distance(typed, name); d <= bestDistance {
			best, bestDistance = name, d
		}
	}
	return best
}

// distance is the edit distance between two short words.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(prev[j]+1, min(curr[j-1]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
