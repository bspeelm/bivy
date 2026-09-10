package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scratch points every base directory at a temporary one, so a test never
// reads or writes the machine it runs on.
func scratch(t *testing.T) (*Store, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))

	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	return s, home
}

type payload struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestWriteThenRead(t *testing.T) {
	s, _ := scratch(t)

	want := payload{Name: "a channel", Count: 3}
	if err := s.WriteJSON("state.json", want); err != nil {
		t.Fatal(err)
	}

	var got payload
	if err := s.ReadJSON("state.json", &got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("read back %+v, want %+v", got, want)
	}
}

// The first launch reads a follow list that does not exist yet. That is the
// normal case, not a failure, and it must not leave a directory behind either.
func TestReadingWhatIsNotThereIsNotAnError(t *testing.T) {
	s, home := scratch(t)

	var got payload
	if err := s.ReadJSON("state.json", &got); err != nil {
		t.Fatalf("reading a file that does not exist: %v", err)
	}
	if got != (payload{}) {
		t.Errorf("read %+v from nothing", got)
	}

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a read created %v; a command that only reads leaves nothing", entries)
	}
}

// The follow list is a profile of what someone watches. It is nobody else's
// business, on a machine with other accounts on it.
func TestWhatIsWrittenIsOwnerOnly(t *testing.T) {
	s, _ := scratch(t)

	if err := s.WriteJSON("state.json", payload{Name: "x"}); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(filepath.Join(s.DataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fi.Mode().Perm(), fs.FileMode(0o600); got != want {
		t.Errorf("file mode = %v, want %v", got, want)
	}

	di, err := os.Stat(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := di.Mode().Perm(), fs.FileMode(0o700); got != want {
		t.Errorf("directory mode = %v, want %v", got, want)
	}
}

// A half-written follow list is worse than an old one, and a program that
// promises no residue does not get to leave a temporary file behind.
func TestWritingLeavesNoTemporaryFiles(t *testing.T) {
	s, _ := scratch(t)

	for i := range 3 {
		if err := s.WriteJSON("state.json", payload{Count: i}); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the data directory holds %v, want only state.json", names)
	}
}

// A rewrite replaces the file rather than overlaying it, so a shorter document
// does not leave the tail of a longer one behind.
func TestARewriteReplacesTheWholeFile(t *testing.T) {
	s, _ := scratch(t)

	if err := s.WriteJSON("state.json", payload{Name: strings.Repeat("long", 100)}); err != nil {
		t.Fatal(err)
	}
	if err := s.WriteJSON("state.json", payload{Name: "short"}); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(filepath.Join(s.DataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "longlong") {
		t.Error("the previous contents are still in the file")
	}
}

func TestUnreadableStateIsReportedNotIgnored(t *testing.T) {
	s, _ := scratch(t)

	if err := os.MkdirAll(s.DataDir, dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.DataDir, "state.json"), []byte("{not json"), filePerm); err != nil {
		t.Fatal(err)
	}

	var got payload
	err := s.ReadJSON("state.json", &got)
	if err == nil {
		t.Fatal("unreadable state was treated as empty state")
	}
	if !strings.Contains(err.Error(), "state.json") {
		t.Errorf("error = %v, want it to name the file", err)
	}
}

// Someone who has set XDG_DATA_HOME has said where they want application data.
// A program that ignores them in favour of a convention they already opted out
// of is the program that leaves things in surprising places.
func TestTheXdgVariablesWinOnEveryPlatform(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "elsewhere", "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "elsewhere", "data"))

	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.ConfigDir, filepath.Join(home, "elsewhere", "config", "bivy"); got != want {
		t.Errorf("ConfigDir = %q, want %q", got, want)
	}
	if got, want := s.DataDir, filepath.Join(home, "elsewhere", "data", "bivy"); got != want {
		t.Errorf("DataDir = %q, want %q", got, want)
	}
}

// A relative value is not a location bivy will honour: it would put the
// directory wherever the shell happened to be.
func TestARelativeXdgValueIsIgnored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "relative/path")
	t.Setenv("XDG_CONFIG_HOME", "")

	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(s.DataDir) {
		t.Errorf("DataDir = %q, which is not an absolute path", s.DataDir)
	}
	if strings.Contains(s.DataDir, "relative") {
		t.Errorf("DataDir = %q, want the relative value ignored", s.DataDir)
	}
}

func TestThePlatformFallbacks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")

	s, err := Open()
	if err != nil {
		t.Fatal(err)
	}

	wantConfig := filepath.Join(home, ".config", "bivy")
	wantData := filepath.Join(home, ".local", "share", "bivy")
	if runtime.GOOS == "darwin" {
		wantConfig = filepath.Join(home, "Library", "Application Support", "bivy")
		wantData = wantConfig
	}
	if s.ConfigDir != wantConfig {
		t.Errorf("ConfigDir = %q, want %q", s.ConfigDir, wantConfig)
	}
	if s.DataDir != wantData {
		t.Errorf("DataDir = %q, want %q", s.DataDir, wantData)
	}
}

// Dirs is the list PLAN.md §8 names, and the list the isolation test holds
// bivy to. Where the two collapse to one directory, it is one entry and not
// the same path twice.
func TestDirsIsTheWriteSet(t *testing.T) {
	s, _ := scratch(t)
	if got, want := len(s.Dirs()), 2; got != want {
		t.Errorf("%d directories, want %d", got, want)
	}

	same := &Store{ConfigDir: "/x/bivy", DataDir: "/x/bivy"}
	if got, want := len(same.Dirs()), 1; got != want {
		t.Errorf("%d directories when both are the same path, want %d", got, want)
	}
}
