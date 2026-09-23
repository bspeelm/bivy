# PLAN.md — bivy

*a terminal browser and player for online video*

> **bivy** *(n.)* — a bivouac; the smallest shelter that still counts as one.

bivy follows channels, shows what is new when it starts, searches when asked,
and plays what you pick. One binary, mpv for playback, `yt-dlp` for the two
things that genuinely need an extractor. It does not download, and it never
holds an account.

This document is the plan and the standards in one place. §0 is asserted by
`make check`, the non-goals in §3 are as binding as the goals, and every
standard below names the thing that enforces it. A standard with nothing
enforcing it is a wish.

The rules here were written before the code. Each one is a decision that is
easy to make silently and expensive to reverse: what reaches an argv, what is
written to disk, what the program says on the wire. Written down first, they
are arguments to be won rather than defaults to be discovered.

---

## §0 Budgets

Asserted by `make budgets`, which `make check` runs. A budget change is a
commit to this section with a reason, reviewed like code. Numbers may start
generous and tighten; they may not silently grow.

Every constant named here exists in `scripts/budgets.sh`, and a test holds the
two lists together — a budget in the prose that no command reads is the exact
failure this section exists to prevent.

| budget | limit | constant | how it is counted |
|---|---|---|---|
| direct Go dependencies | 6 | `MAX_DIRECT_DEPS` | the `go.mod` require block, excluding `// indirect` |
| total modules | 25 | `MAX_MODULES` | `go list -m all`, minus self |
| packages importing `net/http` | **1** | `MAX_HTTP_PACKAGES` | `internal/feed`, and nothing else, tests included |
| code lines | 5,000 | `MAX_CODE_LINES` | `cmd` and `internal`, non-test, comments and blanks excluded |
| comment lines : code lines | 25% | `MAX_COMMENT_RATIO` | the one **hard** ceiling: over budget removes a comment |
| live prose | 3,000 lines | `MAX_DOC_LINES` | every `.md` outside `docs/history/` and `docs/review/` |
| binary size (linux_amd64, stripped) | 12 MiB | `MAX_BINARY_BYTES` | `make budgets` builds exactly that and measures it |
| `panic(` in non-test code | 0 | `MAX_PANICS` | grep, excluding `_test.go` |
| test lines : code lines | ≥ 1 : 3 | `MIN_TEST_RATIO` | `wc -l` over `_test.go` against the rest; a floor, not a target |

Three of these behave differently from the rest.

**`net/http` = 1** is the row worth defending, and it is the reason the
architecture in §4 is shaped the way it is. *What does this program say on the
wire* gets a one-package answer, which turns the central promise of §9 into
something a command can disagree with. It counts test imports too: a test that
reaches for `httptest` in a second package breaks the rule as surely as the
code would.

The **comment ratio** is a hard ceiling. Going over means removing a comment,
not raising the number. It is the only limit here that pushes back on writing
more prose about the code instead of writing less code.

**Live prose** is an absolute line count rather than a share of code, because
this project writes its plan before its code. A ratio would report a project
that planned first as worse than one that did not. Over budget retires a
document to `docs/history/`; it does not raise the cap.

The dependency budget is 6 because a terminal interface, a TOML parser and an
XML feed honestly cost about that, and the difference between 6 and 60 is
entirely a matter of whether anyone was counting. If a feature does not fit the
budget, the feature waits or the budget change is argued in a decision record —
in that order.

---

## §1 What this is

Follow channels. Launch and see what is new. Search when you want something
specific. Press enter and watch it.

The person this is for lives in a terminal, runs a modern one, and does not
want an account. bivy never authenticates, reads no browser cookie jar and
holds no credential — the one cookie it sends is a visitor identifier it
invents for the session and never persists (ADR-018) — so most of what a
client like this normally has to get right does not apply here, and the
privacy claim can be made machine-checkable instead of promised.

### The smallest honest alternative

§2.2 of the standard asks what the smallest version that solves the real
problem looks like, and whether it is enough. Here it is real and close: an
extractor listing a playlist, a fuzzy finder to pick from it, a terminal image
command for thumbnails, mpv to play, plus a text file of channel identifiers
and an HTTP client over the feeds. Roughly a well-known shell script plus
twenty lines more.

That was put as a gate and answered. It is insufficient on four counts:

- **No persistent watch state.** Knowing what is new requires remembering what
  was already seen, across sessions, per channel.
- **No dashboard on launch.** The shell version starts at a prompt. The whole
  point of this one is that starting it *is* the question being answered.
- **Thumbnails are a fallback stack, not a capability.** Which image command
  works is discovered by trying them in order and hoping.
- **Nothing survives a `$TERM` change.** Move between terminals and the
  fallback order silently becomes wrong.

A program can hold state, probe once and be honest about what it found. A
pipeline cannot. That is the whole of the justification, and if those four ever
stop being true, so does bivy.

---

## §2 Threat model

Recorded in full as ADR-001. In summary, four questions:

**What can a hostile feed do?** It chooses bytes bivy parses and strings bivy
renders. Therefore: every network read is through a capped reader with a size
limit, XML parsing is fuzzed, and control characters in remote text are
stripped at the boundary rather than trusted to the renderer.

**What can the network see?** That bivy fetched some feeds and asked an
extractor for a stream. Nothing that identifies the person: no account, no API
key, no identifier bivy invents that outlives the session — the visitor cookie
the extractor carries is invented at startup and discarded with the process
(ADR-018), so requests within a sitting can be linked by it and nothing links
one sitting to the next. IP-level
exposure is
the user's VPN's job and is explicitly out of scope — a program cannot fix it
and should not pretend to.

**What can another user of this machine see?** `argv` is world-readable, so no
stream URL is ever passed as a command-line argument to mpv; it goes over the
IPC socket, and that socket lives in a `0700` directory. Persisted state is
written `0600`.

**What does a stolen state file give an attacker?** A list of channels the user
follows and what they have watched. That is not nothing, so it is `0600` and
lives in one place — but there is no credential to steal, by construction, and
that is the point of ADR-004.

---

## §3 What it is not

As binding as the goals. Things enter this program through a decision record,
never through a pull request that quietly grows what it is.

- **Not a downloader.** No file is fetched to disk for later. ADR-005.
- **Not a video surface.** Video plays in mpv's own window. The terminal
  renders the browser; it does not render frames. ADR-002.
- **Not a client with an account.** No login, no OAuth, no cookie import, no
  API key. ADR-004.
- **Not a comment reader, a subscription manager, or a second front end for
  anything.** Following is local, and stays local.
- **Not a cache.** Thumbnails live in memory for the session and are gone when
  it ends. ADR-007.
- **Not a replacement for the user's mpv setup.** mpv is launched with
  `--no-config` and explicit flags, and the user's own configuration is left
  exactly as it was found. ADR-006.

---

## §4 Architecture

Three rules, chosen because they are what make the tests cheap. Breaking one
quietly is how this stops being testable, so each names what disagrees with it.

1. **`internal/tui` does no I/O.** It renders a model and emits intents. That
   is what makes golden-file testing of every screen possible. *Asserted by
   `make budgets` against the package's import graph.*
2. **`internal/feed` is the only package importing `net/http`.** *Asserted by
   `make budgets`, tests included.*
3. **`internal/follow` is pure functions over data.** Follow lists and watch
   state are where a program like this accumulates its strangest bugs, and pure
   code is where tests are cheapest. *Asserted by review, and by the absence of
   any import it would need.*

| package | job |
|---|---|
| `cmd/bivy` | entry point, flag parsing |
| `internal/media` | the domain types, so that nothing which renders a video imports what fetches one |
| `internal/term` | raw mode, the alternate screen, key decoding, redraw. The interactive half of the tui split (ADR-009) |
| `internal/config` | TOML configuration |
| `internal/feed` | fetch and parse per-channel XML feeds. **Sole `net/http` importer.** |
| `internal/ytdlp` | subprocess only: resolve a search query, and list a channel whose feed is refusing (ADR-013) |
| `internal/mpv` | launch and drive mpv over an IPC socket |
| `internal/follow` | follows and watch state, pure |
| `internal/store` | atomic `0600` persistence |
| `internal/graphics` | terminal capability probe, kitty-protocol image emit. **Writes escape sequences derived from remote image bytes** |
| `internal/tui` | dashboard, search, the command line, thumbnail grid |

### The dashboard needs no extractor

Each channel publishes a static XML feed: no auth, no API key, no cookie,
returning video id, title, publish date, author and a thumbnail URL. That is a
complete dashboard row, and it is far lower-profile than driving an extractor
at each channel page.

This is what scopes the extractor down to two jobs — resolve a search query,
resolve a stream URL — and it is what makes rule 2 achievable at all. ADR-003.

### Driving mpv

mpv is launched idle with an IPC socket and given work over it, rather than
handed a URL and abandoned. That buys pause, seek, position and
end-of-playback, and it keeps the stream URL out of `argv`. `--no-config`, and
`--vo=gpu` so the decode path is the hardware one. Version-gated flags name the
mpv version that makes the gate deletable.

---

## §5 The standards, each with what enforces it

| standard | enforced by |
|---|---|
| The budgets in §0 | `make budgets` |
| No `panic(` in shipping code | `make budgets` |
| `internal/tui` does no I/O | `make budgets`, import graph |
| One package on the network | `make budgets`, import graph |
| Formatting | `make lint` |
| Correctness under concurrency | `make race` |
| README claims match the Makefile | `docs_test.go` |
| §0 budgets match the script | `docs_test.go` |
| The decision log is contiguous and states status | `docs_test.go` |
| The dependency stays at one | `make budgets`, and §0 |
| No local filesystem detail in tracked files | `docs_test.go`, and the conformance check |
| The founding documents exist | `make standard` |

---

## §6 Credentials

There are none. bivy authenticates against nothing, reads no browser cookie
jar, accepts no API key and stores no token. The visitor identifier it sends is
not one of these: it is invented, random, held in memory, and gone when bivy
exits (ADR-018). This section exists to be short
and to stay short: the cheapest way to never leak a credential is to never hold
one. ADR-004.

---

## §7 Playback and process hygiene

- mpv is a child process with an IPC socket, not a fire-and-forget `exec`. One
  process serves a whole session; a second video reuses it.
- No window is forced while mpv is idle. bivy says on its own status line that
  something is loading, and a forced window is an empty pane for the rest of
  the session — with no title bar to close it by, where the compositor offers
  no decorations.
- mpv is asked to quit, never killed while it will still answer. A killed mpv
  can outlive the process that killed it.
- The socket lives in a `0700` directory under the runtime directory — or the
  system temporary directory where there is none, which is every Mac — and is
  removed on exit, including when startup failed.
- No stream URL appears in any `argv`, because `argv` is world-readable. What
  bivy sends mpv is a page URL built from a checked identifier, and the
  resolved URL never leaves mpv (ADR-010).
- A video is marked watched when it reaches its own end, not when it starts. A
  video opened and abandoned has not been watched.
- **Every positional argument to an external process is preceded by `--`, and
  any input beginning with `-` is refused before it reaches an argv.** This is
  a real defect found in a reviewed program of this kind, where an option-shaped
  string reached an extractor that has a flag for running shell commands. It is
  designed out here rather than patched later, and it is on the load-bearing
  map so it is tested rather than remembered.
- Quitting bivy stops mpv. A hard kill of bivy is the one case where it cannot,
  and that is stated rather than papered over.

---

## §8 The filesystem contract

Enforced by an isolation test, because *removable without residue* is a promise
worth keeping.

Exactly **two** directories are ever written:

- the configuration directory
- the data directory

macOS honours the XDG variables when they are set, and uses the Apple paths
otherwise. The mpv IPC socket lives under the runtime directory and is removed
on exit; it is a socket, not a directory bivy owns.

There is **no cache directory**. Thumbnails live in memory for the session
(ADR-007). There is no partial-file residue anywhere, because bivy does not
download (ADR-005).

The isolation test runs a first launch against a scratch home directory and
asserts the write set matches that list exactly. A new directory means a test
failure, not a surprise.

---

## §9 The network contract

bivy makes exactly three kinds of outbound request:

1. **Feed fetches**, from `internal/feed`, over HTTPS to a static per-channel
   XML endpoint. No auth header, no cookie, no query parameter that identifies
   anyone. This is the only kind that runs at launch.
2. **One channel-page fetch**, from `internal/feed`, when a handle is followed
   — the only time bivy reads a page meant for a browser, and never at launch.
   ADR-008.
3. **Extractor calls**, from `internal/ytdlp` for search and from mpv itself
   for playback (ADR-010). Subprocess executions rather than requests bivy
   makes itself, each carrying the visitor identifier bivy invented for this
   session and discards when it exits (ADR-018). One of them runs at launch,
   and only then: a channel whose feed would not answer is listed by the
   extractor instead (ADR-013).

Nothing else. No analytics, no update check, no crash reporting, no ping. The
budget in §0 is what makes this checkable rather than merely stated: one
package imports `net/http`, and a reviewer can read it in an afternoon. The
count above is the thing to watch — a fourth kind is a decision, not a detail.

---

## §10 Testing

- **Golden files for every screen.** Possible because of architectural rule 1.
- **A test that every command earns its place** under ADR-011's rule, so the
  command line does not become the place features go to avoid §3.
- **Fuzzing on the feed parser**, because it is the one place remote bytes are
  parsed.
- **A test that a terminal which cannot draw is sent no graphics**, because
  the bytes a terminal does not understand are the ones it prints.
- **A table test on argument construction** for both external processes,
  covering option-shaped input. The extractor has an option that runs a shell
  command, so this is the one that matters.
- **A stand-in for mpv** that speaks its IPC protocol, so the player is tested
  without mpv being installed. A test that asks whether something is installed
  passes where it was written and fails where the artifact is built.
- **The isolation test** in §8, against a scratch home directory.
- **The integration tests on a schedule, and never on a pull request.** They
  need a real mpv, a real extractor and the live network; the gate needs none
  of the three. A failure there is news about a third party rather than a
  broken diff, and a gate that reddens for reasons no diff can cause is one
  people learn to ignore. What that run cannot cover is stream resolution: the
  address it runs from is challenged, and the remedy on offer is a cookie
  (ADR-015). Playback is verified by hand, and every review packet says so.
- **The prose compiler**, `docs_test.go`, which holds the README and this
  document against the files that define what they claim.
- The floor in §0 is one test line per three code lines. It is a floor and not
  a target; the tests above matter more than the ratio does.

---

## §11 CI and release

`make check` is the gate: lint, vet, race tests, budgets, then the conformance
check. It is what CI runs and what must pass before a commit — on Linux and on
macOS both, because `internal/store` chooses its directories by platform and a
branch nothing runs is a branch nobody has tried. `make crossbuild` compiles
every platform the README claims, on every check.

The conformance check skips in CI rather than failing: the development
standard lives outside this repository and is not checked out beside it. That
is the one part of the gate a green CI run does not prove.

Once a week, a job asks what has moved upstream: the modules, the Go version
`go.mod` names, and the actions these workflows run. It opens one tracking
issue and never a pull request. The toolchain line decides what a published
archive rebuilds into, so a bot that bumped it would be quietly changing the
answer to "does this tag still build the bytes you downloaded" — which is the
one question the reproducibility offer exists to answer. mpv and the extractor
are not in that job: they are floors rather than pins, and what breaks with
them is behaviour, which the scheduled integration run exercises instead.

A release is a tag (ADR-014). Pushing `v<major>.<minor>.<patch>` runs that same
gate on both platforms, then builds one archive per platform in
`scripts/platforms` and publishes them with a `SHA256SUMS` file. `make dist` is
that build, and `make dist-reproducible` is what runs before publishing: the
same commit is built twice and the checksums compared, because a checksum over
a single build says only which copy was uploaded.

The compiler is part of the artefact, so `make dist` pins it to the version
`go.mod` names rather than taking whichever Go is installed — the offer to
rebuild a tag and compare bytes is worth nothing if the rebuild uses a
different compiler. CI reads its Go version from the same line.

Releases ship with a review packet in `docs/review/`, named for the version,
stating what was checked and what was not. The workflow refuses to publish
without one, and the packet is the release notes — one document rather than two
that describe the same release and disagree by the second one.

The archives are not signed, and the documentation says so in those words: a
checksum published beside its own artefact detects a corrupted download and
nothing else. Reproducibility is what carries that weight instead (ADR-014).

---

## §12 Milestones

Milestone 0 is done when the conformance check passes. Each milestone after it
ends at something runnable, and is checked against the north star before it is
executed.

| # | milestone | done when |
|---|---|---|
| 0 | **The skeleton.** Founding documents, budgets armed, prose compiler. | ✅ the conformance check prints `conformant.` |
| 1 | **Follow and dashboard.** Add a channel, fetch feeds, show what is new since the last visit. No player yet. | ✅ a followed channel's new videos are listed on launch |
| 2 | **Play.** mpv over IPC. | ✅ enter on a dashboard row plays it in an mpv window and marks it watched |
| 3 | **Search.** Extractor-backed query into the same list model. | ✅ a query returns rows that play the same way |
| 4 | **Thumbnails.** Capability probe, kitty-protocol picture for the row under the cursor (ADR-012), text-only where the protocol is absent. | ✅ the picture renders where supported and nothing changes where not |

Manual verification, once milestone 2 lands: launch on Linux and on macOS,
confirm the dashboard renders, a row plays in an mpv window, and the home
directory afterwards contains exactly the two directories from §8.

---

## §13 Open decisions

Named here so they are decided deliberately rather than discovered.

- ~~**Which terminal-interface library.**~~ Settled by ADR-009: none. The
  interactive layer is written here over `golang.org/x/term`, which is the
  project's one direct dependency.
- **The §0 numbers themselves.** They were drafted before there was code to
  measure. They are armed now, which is the point; tightening them once the
  shape of the code is known is expected and is a commit to §0.
- **What `bivy` does when the extractor is absent.** Degrading to a
  feed-only browser with no search is plausible and is not yet decided.
