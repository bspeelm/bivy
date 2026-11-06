package main

import (
	"slices"
	"strings"
	"testing"
)

func TestTakeProxyFindsItWhereverItIsWritten(t *testing.T) {
	cases := map[string][]string{
		"before the command": {"--proxy", "http://127.0.0.1:8888", "list"},
		"after the command":  {"list", "--proxy", "http://127.0.0.1:8888"},
		"with an equals":     {"--proxy=http://127.0.0.1:8888", "list"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			proxy, rest, err := takeProxy(args)
			if err != nil {
				t.Fatal(err)
			}
			if proxy == nil || proxy.String() != "http://127.0.0.1:8888" {
				t.Errorf("proxy = %v", proxy)
			}
			if !slices.Equal(rest, []string{"list"}) {
				t.Errorf("rest = %q", rest)
			}
		})
	}
}

func TestWithNoProxyOptionBivyGoesDirect(t *testing.T) {
	proxy, rest, err := takeProxy([]string{"follow", "@someone"})
	if err != nil {
		t.Fatal(err)
	}
	if proxy != nil {
		t.Errorf("no proxy was asked for, got %v", proxy)
	}
	if !slices.Equal(rest, []string{"follow", "@someone"}) {
		t.Errorf("rest = %q", rest)
	}
}

// Each of these has to stop bivy: carrying on would go direct, which is what
// someone passing --proxy is trying not to do.
func TestABadProxyOptionStopsBivyRatherThanGoingDirect(t *testing.T) {
	cases := map[string][]string{
		"no address":              {"--proxy"},
		"empty":                   {"--proxy="},
		"option-shaped":           {"--proxy", "--upload"},
		"no port":                 {"--proxy", "http://127.0.0.1"},
		"no host":                 {"--proxy", "http://:8888"},
		"no scheme":               {"--proxy", "127.0.0.1:8888"},
		"https":                   {"--proxy", "https://127.0.0.1:8888"},
		"socks":                   {"--proxy", "socks5://127.0.0.1:8888"},
		"a path":                  {"--proxy", "http://127.0.0.1:8888/x"},
		"a query":                 {"--proxy", "http://127.0.0.1:8888?x=1"},
		"given twice":             {"--proxy", "http://127.0.0.1:1", "--proxy", "http://127.0.0.1:2"},
		"missing after a command": {"list", "--proxy"},
		"a password":              {"--proxy", "http://me:secret@127.0.0.1:8888"},
		"just a user":             {"--proxy", "http://me@127.0.0.1:8888"},
		"control chars":           {"--proxy", "http://127.0.0.1:8888\x1b[2J"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := takeProxy(args); err == nil {
				t.Errorf("%q was accepted", args)
			}
		})
	}
}

func TestARefusedPasswordIsNotRepeatedBack(t *testing.T) {
	_, _, err := takeProxy([]string{"--proxy", "http://me:secret@127.0.0.1:8888"})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("err = %v", err)
	}
}
