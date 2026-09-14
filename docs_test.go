// Package bivy holds no code. It holds the compiler for the prose that
// describes the code, which has none of its own yet.
//
// The documents make claims about what commands exist, what the budgets are,
// and what the program will and will not do. Those claims rot silently, and
// they rot fastest in the window where there is more prose than code. Each
// test below pins one of them to the file that actually decides it.
package bivy

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every `make <target>` the documentation mentions.
//
// Inside code, and at the start of a line there: a command is typed at a
// prompt. "I make cartoons on my main account" is a line of example output
// that happens to sit in a fenced block, and a parser that cannot tell those
// apart reports the README as documenting a target nobody wrote.
var documentedTarget = regexp.MustCompile("(?m)^\\s*make ([a-z][a-z-]*)")

var code = regexp.MustCompile("(?s)```.*?```|`[^`\n]+`")

// inCode is every fenced block and backticked span in a document, which is
// where a command can be written down.
func inCode(text string) string {
	return strings.Join(code.FindAllString(text, -1), "\n")
}

// A Makefile rule: a target at the start of a line, followed by its
// prerequisites.
var makefileRule = regexp.MustCompile(`(?m)^([a-z][a-z-]*):(.*)$`)

func read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func makefileTargets(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range makefileRule.FindAllStringSubmatch(read(t, "Makefile"), -1) {
		out[m[1]] = strings.TrimSpace(m[2])
	}
	return out
}

func TestEveryDocumentedMakeTargetExists(t *testing.T) {
	targets := makefileTargets(t)

	var checked int
	for _, m := range documentedTarget.FindAllStringSubmatch(inCode(read(t, "README.md")), -1) {
		checked++
		if _, found := targets[m[1]]; !found {
			t.Errorf("README.md documents `make %s`, which the Makefile does not define", m[1])
		}
	}

	// The self-check the standard asks for: a parser that silently stops
	// matching reports a clean run over nothing at all, which is the failure
	// mode a prose compiler is most likely to have.
	if checked < 4 {
		t.Fatalf("the README parser found %d make targets; it has stopped matching", checked)
	}
}

// The specific rot this exists for. The README describes what `make check`
// runs; the Makefile decides. When a prerequisite is added to the gate and the
// description is not updated, the README quietly understates what a green
// check proves.
func TestTheDocumentedGateMatchesTheMakefile(t *testing.T) {
	prereqs := strings.Fields(makefileTargets(t)["check"])
	if len(prereqs) == 0 {
		t.Fatal("the Makefile's check target has no prerequisites; the gate is empty")
	}

	line := lineContaining(t, read(t, "README.md"), "make check ")
	for _, p := range prereqs {
		if !strings.Contains(line, p) {
			t.Errorf("the Makefile runs %q as part of `make check`, which the README does not mention:\n  %s", p, line)
		}
	}
}

// PLAN.md §0 is the authored copy of the budgets and scripts/budgets.sh is the
// enforced one. A number in the table that no command reads is the exact
// failure §0 exists to prevent, and a constant in the script that the table
// never mentions is a limit nobody agreed to.
func TestEveryBudgetIsBothDeclaredAndAsserted(t *testing.T) {
	script := read(t, filepath.Join("scripts", "budgets.sh"))
	plan := read(t, "PLAN.md")

	declared := regexp.MustCompile(`(?m)^(M(?:AX|IN)_[A-Z_]+)=`).FindAllStringSubmatch(script, -1)
	if len(declared) < 5 {
		t.Fatalf("found %d budget constants in the script; the parser has stopped matching", len(declared))
	}
	for _, m := range declared {
		if !strings.Contains(plan, "`"+m[1]+"`") {
			t.Errorf("scripts/budgets.sh enforces %s, which the PLAN.md §0 table does not name", m[1])
		}
	}

	for _, m := range regexp.MustCompile("`(M(?:AX|IN)_[A-Z_]+)`").FindAllStringSubmatch(plan, -1) {
		if !strings.Contains(script, "\n"+m[1]+"=") {
			t.Errorf("PLAN.md §0 names %s, which scripts/budgets.sh does not declare", m[1])
		}
	}
}

// The decision log is append-only and numbered, and it is cited by number from
// four other documents. A gap or a repeat means one of those citations points
// at the wrong record, or at nothing.
func TestTheDecisionLogIsContiguousAndStatesStatus(t *testing.T) {
	body := read(t, filepath.Join("docs", "decisions.md"))

	headings := regexp.MustCompile(`(?m)^## ADR-(\d+) — .+$`).FindAllStringSubmatch(body, -1)
	if len(headings) == 0 {
		t.Fatal("no ADR headings found; the parser has stopped matching")
	}
	for i, m := range headings {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatal(err)
		}
		if n != i+1 {
			t.Errorf("ADR numbering jumps: record %d is numbered %s", i+1, m[1])
		}
	}

	if got, want := strings.Count(body, "**Status:**"), len(headings); got != want {
		t.Errorf("%d records carry a status line, and there are %d records", got, want)
	}

	// A decision log with no refusals in it is a changelog. The refusals are
	// the half that stops scope being re-proposed every session, and the
	// conformance check looks for exactly this shape of title.
	refusals := regexp.MustCompile(`(?mi)^#+.*(not |no |never |instead of |declined|deferred)`).FindAllString(body, -1)
	if len(refusals) < 3 {
		t.Errorf("only %d refusal-shaped ADR titles; the log is becoming a changelog", len(refusals))
	}
}

// Every ADR cited anywhere in the tracked documents exists. Citing a record by
// number is how a settled decision is defended without re-arguing it, and a
// citation that points at nothing does not defend anything.
func TestEveryCitedDecisionRecordExists(t *testing.T) {
	log := read(t, filepath.Join("docs", "decisions.md"))

	var cited int
	for _, f := range proseFiles(t) {
		if f == filepath.Join("docs", "decisions.md") {
			continue
		}
		for _, m := range regexp.MustCompile(`ADR-(\d+)`).FindAllStringSubmatch(read(t, f), -1) {
			cited++
			if !strings.Contains(log, "## ADR-"+m[1]+" — ") {
				t.Errorf("%s cites ADR-%s, which docs/decisions.md does not contain", f, m[1])
			}
		}
	}
	if cited < 5 {
		t.Fatalf("found %d decision-record citations; the parser has stopped matching", cited)
	}
}

// PLAN.md §11 says `make check` is what CI runs. It said that for two
// milestones while there was no CI at all, which is the failure the §0
// discipline exists to prevent applied to a sentence instead of a number.
//
// Held in both directions: the claim needs a workflow, and the workflow has to
// run the target the claim names.
func TestTheCIClaimIsBackedByAWorkflow(t *testing.T) {
	plan := read(t, "PLAN.md")
	if !strings.Contains(plan, "what CI runs") {
		t.Skip("PLAN.md no longer claims CI runs the gate")
	}

	dir := filepath.Join(".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("PLAN.md §11 says `make check` is what CI runs, and %s does not exist", dir)
	}

	var runsTheGate bool
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yml") && !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		if strings.Contains(read(t, filepath.Join(dir, e.Name())), "make check") {
			runsTheGate = true
		}
	}
	if !runsTheGate {
		t.Errorf("no workflow in %s runs `make check`, which §11 says CI runs", dir)
	}
}

// The README names the platforms, something has to compile for them, and a
// release has to ship them. One list answers all three questions, and this is
// what holds it there: a platform compiled but never shipped, or shipped but
// never compiled, is drift that a second copy of the list would hide.
func TestEveryClaimedPlatformIsBuiltAndShipped(t *testing.T) {
	const list = "scripts/platforms"
	platforms := read(t, filepath.FromSlash(list))

	if _, found := makefileTargets(t)["crossbuild"]; !found {
		t.Fatal("no crossbuild target; nothing compiles for the platforms the README claims")
	}
	for _, reader := range []string{"Makefile", "scripts/dist.sh"} {
		if !strings.Contains(read(t, filepath.FromSlash(reader)), list) {
			t.Errorf("%s does not read %s, so it is carrying a second list of platforms", reader, list)
		}
	}

	documents := read(t, "README.md") + read(t, "PLAN.md")
	for _, platform := range []struct{ said, goos string }{
		{"Linux", "linux/"},
		{"macOS", "darwin/"},
	} {
		if !strings.Contains(documents, platform.said) {
			continue
		}
		if !strings.Contains(platforms, platform.goos) {
			t.Errorf("the documents claim %s and %s has no %s entry", platform.said, list, platform.goos)
		}
	}
}

// §11 describes a release as a tag that publishes archives. A description of a
// pipeline is not a pipeline, and this is the difference.
func TestTheReleaseClaimIsBackedByAWorkflow(t *testing.T) {
	plan := read(t, "PLAN.md")
	if !strings.Contains(plan, "A release is a tag") {
		t.Skip("PLAN.md no longer claims a tag publishes a release")
	}

	dir := filepath.Join(".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	var publishes string
	for _, e := range entries {
		body := read(t, filepath.Join(dir, e.Name()))
		if strings.Contains(body, "tags:") && strings.Contains(body, "gh release create") {
			publishes = body
		}
	}
	if publishes == "" {
		t.Fatalf("§11 says a tag publishes a release, and no workflow in %s creates one on a tag", dir)
	}

	// The checks §11 promises happen before anything is uploaded. Each is a
	// sentence in the plan that is worth exactly what runs it.
	for _, required := range []struct{ command, claim string }{
		{"make check", "the gate runs before a release"},
		{"make dist-reproducible", "the same commit is built twice and the checksums compared"},
		{"docs/review/", "a release ships with a review packet"},
	} {
		if !strings.Contains(publishes, required.command) {
			t.Errorf("the release workflow never runs %q, and §11 says %s", required.command, required.claim)
		}
	}
}

// Rebuilding a tag and comparing bytes is the offer that stands in for a
// signature, and it is worth nothing if the rebuild uses a different compiler:
// two Go releases turn the same source into different binaries. One line in
// go.mod answers it for the release build and for CI both.
func TestTheReproducibleBuildPinsItsCompiler(t *testing.T) {
	if !strings.Contains(read(t, "README.md"), "byte for byte") {
		t.Skip("the README no longer offers a byte-for-byte rebuild")
	}

	dist := read(t, filepath.FromSlash("scripts/dist.sh"))
	if !strings.Contains(dist, "GOTOOLCHAIN") {
		t.Error("scripts/dist.sh does not pin GOTOOLCHAIN, so a release is built by whichever Go is installed")
	}
	if !strings.Contains(dist, "go.mod") {
		t.Error("scripts/dist.sh pins a compiler version that go.mod does not name")
	}
	// Go stamps a binary with what git says, and git says different things
	// about checkouts holding identical source. The README tells a rebuilder
	// their checkout does not matter, and this is what makes that true.
	if !strings.Contains(dist, "-buildvcs=false") {
		t.Error("scripts/dist.sh lets Go stamp the VCS into the binary, so the same source in a clone and in a worktree build differently")
	}

	dir := filepath.Join(".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		body := read(t, filepath.Join(dir, e.Name()))
		if !strings.Contains(body, "setup-go") {
			continue
		}
		if !strings.Contains(body, "go-version-file: go.mod") {
			t.Errorf("%s sets up Go from something other than go.mod, which is the line the release build pins to", e.Name())
		}
	}
}

// The integration tests run somewhere, and that somewhere is not the gate.
// Both halves matter: tests nothing ever runs are decoration, and a gate that
// depends on a third party is one whose red means nothing about the diff.
func TestTheIntegrationTestsRunOnTheirOwnAndNotOnTheGate(t *testing.T) {
	plan := read(t, "PLAN.md")
	if !strings.Contains(plan, "integration tests on a schedule") {
		t.Skip("§10 no longer claims the integration tests run on a schedule")
	}

	dir := filepath.Join(".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	var scheduled bool
	for _, e := range entries {
		body := read(t, filepath.Join(dir, e.Name()))
		runsThem := strings.Contains(body, "make integration") ||
			strings.Contains(body, "-tags=integration")
		if !runsThem {
			continue
		}
		if strings.Contains(body, "pull_request") {
			t.Errorf("%s runs the integration tests on a pull request, which puts a third party in the gate", e.Name())
		}
		if strings.Contains(body, "schedule:") {
			scheduled = true
		}
	}
	if !scheduled {
		t.Errorf("§10 says the integration tests run on a schedule, and no workflow in %s schedules them", dir)
	}
}

// No workflow hands the extractor a credential (ADR-004, ADR-015).
//
// The scheduled run cannot resolve a stream, because the address it runs from
// is challenged, and the remedy the error message offers is a cookie. This is
// the one place where the tempting fix and the founding refusal are the same
// line of YAML.
func TestNothingInCIGivesTheExtractorACredential(t *testing.T) {
	dir := filepath.Join(".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		body := read(t, filepath.Join(dir, e.Name()))
		for _, credential := range []string{"--cookies", "cookies-from-browser", "netrc"} {
			if strings.Contains(body, credential) {
				t.Errorf("%s passes %s; ADR-004 says bivy never authenticates, and a credential in CI is a credential in a repository",
					e.Name(), credential)
			}
		}
	}
}

// §11 promises a weekly look at what has moved, and promises it will not act
// on the answer. Both halves are the claim: a job nothing schedules is a
// paragraph, and a job that opened a pull request would be moving the pin that
// decides what a published archive rebuilds into.
func TestTheOutdatedCheckOpensAnIssueAndNeverAPullRequest(t *testing.T) {
	plan := read(t, "PLAN.md")
	if !strings.Contains(plan, "asks what has moved upstream") {
		t.Skip("§11 no longer claims a weekly check")
	}

	dir := filepath.Join(".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	var checker string
	for _, e := range entries {
		body := read(t, filepath.Join(dir, e.Name()))
		if strings.Contains(body, "gh issue create") {
			checker = body
		}
	}
	if checker == "" {
		t.Fatalf("§11 says a weekly job opens a tracking issue, and nothing in %s does", dir)
	}

	if !strings.Contains(checker, "schedule:") || !strings.Contains(checker, "cron:") {
		t.Error("the check is not scheduled, so it runs when somebody remembers")
	}
	for _, acting := range []string{"gh pr create", "pull-requests: write", "peter-evans/create-pull-request"} {
		if strings.Contains(checker, acting) {
			t.Errorf("the check contains %q; §11 says it reports and does not act", acting)
		}
	}
}

// docs/review/ says in prose whether a release has happened yet. That sentence
// is read by someone deciding whether to trust the directory, and it is the
// kind of sentence that stays true for exactly one release.
func TestTheReviewDirectoryClaimMatchesItsContents(t *testing.T) {
	dir := filepath.Join("docs", "review")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	var packets []string
	for _, e := range entries {
		if e.Name() != "README.md" && strings.HasSuffix(e.Name(), ".md") {
			packets = append(packets, e.Name())
		}
	}

	const claim = "There is no release yet"
	saysNone := strings.Contains(read(t, filepath.Join(dir, "README.md")), claim)

	switch {
	case saysNone && len(packets) > 0:
		t.Errorf("%s/README.md still says %q, and %s is sitting next to it", dir, claim, packets[0])
	case !saysNone && len(packets) == 0:
		t.Errorf("%s/README.md no longer says %q, and there are no packets in it", dir, claim)
	}
}

// Nothing may claim the repository has no code once it has some. The status
// section is the first thing a reader believes and the last thing anyone
// remembers to update.
func TestTheStatusClaimMatchesTheTree(t *testing.T) {
	const claim = "**Nothing is built yet.**"
	hasClaim := strings.Contains(read(t, "README.md"), claim)

	var packages []string
	for _, root := range []string{"cmd", "internal"} {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			packages = append(packages, path)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	switch {
	case hasClaim && len(packages) > 0:
		t.Errorf("README.md still says %q, and there is code in %s", claim, packages[0])
	case !hasClaim && len(packages) == 0:
		t.Errorf("README.md no longer says %q, and there is still no code under cmd/ or internal/", claim)
	}
}

// No local filesystem detail in anything tracked. Paths rooted at a home or a
// mount point say where one machine keeps its files, which is nobody else's
// business and is not a fact about this project.
//
// This test is the gate for a rule that is otherwise only an instruction, and
// it is stricter than the conformance check's version: that one looks for this
// machine's own home path and account name, which finds a leak only on the
// machine that made it.
func TestNoLocalFilesystemPathsInTrackedFiles(t *testing.T) {
	roots := regexp.MustCompile(`(?:/home/|/Users/|/var/home/|/var/mnt/|/mnt/)[A-Za-z0-9._-]+`)
	homeVar := regexp.MustCompile(`\$HOME\b|\$\{HOME\}`)

	var scanned int
	for _, f := range trackedText(t) {
		// This file names the patterns it forbids; the rule is the fence,
		// not the breach.
		if f == "docs_test.go" {
			continue
		}
		scanned++
		body := read(t, f)
		if f == "Makefile" {
			// "$$" is make's escape for a literal dollar, so "$$HOME" is a
			// reference that reaches the shell as $HOME rather than a path
			// from anyone's machine. A bare $HOME still fails below.
			body = strings.ReplaceAll(body, "$$", "")
		}
		for _, m := range roots.FindAllString(body, -1) {
			t.Errorf("%s contains a local filesystem path: %s", f, m)
		}
		// A Makefile's $(HOME) is a variable reference and is fine. A literal
		// $HOME in prose is a path from somebody's machine.
		if loc := homeVar.FindString(body); loc != "" {
			t.Errorf("%s refers to %s directly; describe the location instead", f, loc)
		}
	}
	if scanned < 6 {
		t.Fatalf("scanned %d files; the walk has stopped matching", scanned)
	}
}

// The documents outside the append-only records. History and review packets
// describe the past and cannot be retired, so they are not held to the present.
func proseFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, f := range trackedText(t) {
		if strings.HasSuffix(f, ".md") {
			out = append(out, f)
		}
	}
	return out
}

func trackedText(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch path {
			case ".git", "tmp", "dist", filepath.Join("docs", "history"), filepath.Join("docs", "review"):
				return filepath.SkipDir
			}
			return nil
		}
		switch {
		case strings.HasSuffix(path, ".md"), strings.HasSuffix(path, ".go"),
			strings.HasSuffix(path, ".sh"), path == "Makefile":
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func lineContaining(t *testing.T, text, sub string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	t.Fatalf("no line contains %q", sub)
	return ""
}
