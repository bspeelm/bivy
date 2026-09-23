package visitor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// value is the identifier out of a jar, which is the last field of its one
// cookie line.
func value(t *testing.T, jar string) string {
	t.Helper()
	for _, line := range strings.Split(jar, "\n") {
		if strings.Contains(line, "VISITOR_INFO1_LIVE") {
			fields := strings.Split(line, "\t")
			return fields[len(fields)-1]
		}
	}
	t.Fatalf("no visitor identifier in %q", jar)
	return ""
}

// The whole point of ADR-018. One identifier for the session, so that bivy
// stops arriving as a crowd of brand-new visitors from one address.
func TestTheVisitorIsTheSameAllSession(t *testing.T) {
	dir := t.TempDir()

	path, err := Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := value(t, read(t, path))

	for i := range 20 {
		path, err := Write(dir)
		if err != nil {
			t.Fatal(err)
		}
		if id := value(t, read(t, path)); id != first {
			t.Fatalf("write %d sent %q, and the session began as %q", i+1, id, first)
		}
	}
}

// What the session identifier may not be is derived from the machine, or the
// same one twice. A process that ended takes its visitor with it (ADR-018).
func TestANewSessionIsANewVisitor(t *testing.T) {
	seen := map[string]bool{}
	for i := range 20 {
		id, err := identifier()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("the identifier %q came round again on session %d", id, i+1)
		}
		seen[id] = true
	}
}

// bivy holds no account (ADR-004). What it sends is a visitor identifier, and
// a test that only checked "there is a cookie" would not notice the difference.
func TestTheJarCarriesNoAccount(t *testing.T) {
	path, err := Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	jar := read(t, path)

	if !strings.Contains(jar, "VISITOR_INFO1_LIVE") {
		t.Errorf("no visitor identifier in the jar: %q", jar)
	}
	for _, account := range []string{"SID", "HSID", "SSID", "SAPISID", "APISID", "LOGIN_INFO", "__Secure-"} {
		if strings.Contains(jar, account) {
			t.Errorf("the jar carries %s, which is an account and not a visitor", account)
		}
	}
}

// Replacing rather than appending is what discards whatever the service wrote
// into the jar. This survives ADR-018 unchanged: it is the half of ADR-016
// that was doing the work, and the half a session-long identifier does not
// weaken. What bivy sends stays a random number; what the service adds to it
// never reaches a second request.
func TestWritingReplacesWhatTheServiceSent(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	first := value(t, read(t, path))

	// What yt-dlp does at the end of a request: writes the jar back with
	// everything the service set, including a token that outlives the session
	// by six months.
	returned := read(t, path) +
		".youtube.com\tTRUE\t/\tTRUE\t1804822338\t__Secure-YNID\t21.YT=somethingopaque\n" +
		".youtube.com\tTRUE\t/\tTRUE\t0\tYSC\thTob3RbdDd0\n"
	if err := os.WriteFile(path, []byte(returned), Perm); err != nil {
		t.Fatal(err)
	}

	if _, err := Write(dir); err != nil {
		t.Fatal(err)
	}
	jar := read(t, path)

	if strings.Contains(jar, "__Secure-YNID") || strings.Contains(jar, "YSC") {
		t.Errorf("what the service sent survived into the next request: %q", jar)
	}
	if got := value(t, jar); got != first {
		t.Errorf("the identifier changed to %q mid-session, and %q was the session's", got, first)
	}
}

func TestTheJarIsNotReadableByOtherUsers(t *testing.T) {
	path, err := Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != Perm {
		t.Errorf("the jar is %v, want %v", got, Perm)
	}
}

// A directory bivy cannot write is a failure worth reporting rather than a
// silent fall back to sending nothing, which is the case that does not play.
func TestAJarThatCannotBeWrittenIsReported(t *testing.T) {
	if _, err := Write(filepath.Join(t.TempDir(), "no-such-directory")); err == nil {
		t.Error("writing into a directory that does not exist returned no error")
	}
}
