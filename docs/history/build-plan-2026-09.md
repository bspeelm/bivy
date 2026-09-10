# bivy — build plan

*a terminal browser and player for online video*

> **bivy** *(n.)* — a bivouac; the smallest shelter that still counts as one.

> **Retired.** This was the execution plan written before milestone 0. Its
> content is now `PLAN.md` (the standing contract), `docs/north-star.md` and
> `docs/decisions.md`. It is kept because it records the reasoning at the
> moment the project was founded, and because two live planning documents is
> the drift the standard exists to prevent.

This is the execution plan. It precedes `PLAN.md`, which is the standing
contract (§0 budgets through §13) and is written as part of milestone 0.

Code worth reusing is described here by shape. The specifics were kept in
untracked scratch notes rather than in this document.

---

## Context

bivy browses and plays. It does not download.

The starting point was a security review of an existing terminal video
downloader. It came back clean — no malware, no telemetry, no shell injection —
but roughly two-thirds of it is downloading, and its player shells out and walks
away with no playback control. Stripping it down meant deleting some 6,300 lines
and then gutting a 3,177-line state machine whose seven download-only states run
through it. Starting fresh and lifting the few isolated pieces that are good is
cheaper and ends somewhere better.

Two gates are already settled and are not reopened here:

- **§2.2 Smallest Honest Alternative — cleared.** The shell version fails on
  persistent watch state, dashboard-on-launch, and thumbnail rendering that
  survives a `$TERM` change.
- **Video plays in mpv's own window.** The terminal owns browsing; mpv owns
  video. In-terminal playback is refused, with the reasoning recorded so it is
  not re-argued.

## What it is

Follow channels. Launch and see what is new. Search when you want something
specific. Press enter and watch it.

The person this is for lives in a terminal, runs a modern one that speaks the
kitty graphics protocol, and does not want an account. bivy never
authenticates, reads no cookies, and holds no credential — so most of what a
client like this normally has to get right does not apply, and the privacy
claim can be made machine-checkable instead of promised.

## Milestone 0 — the skeleton that passes the check

Founding documents come before the first line of application code, and
the conformance check must print `conformant.` This is the only
milestone to execute before review.

```
.agent-standard          standard = 1, stakes = zero, todo = 0
CLAUDE.md                from the adoption template, brackets filled
PLAN.md                  §0 budgets … §13
README.md                what it does, what to type, what to expect
LICENSE                  MIT
Makefile                 check: lint vet race budgets standard
go.mod                   module github.com/bspeelm/bivy
.gitignore               includes /tmp/
scripts/budgets.sh       the MAX_* numbers, armed before there is code
docs/north-star.md
docs/decisions.md        ADR-001..007, refusals included
docs/history/            tracked, empty
docs/review/             tracked, empty
docs_test.go             the prose compiler (§5.3)
```

The repository is initialised first — the check skips four instinct-fence
rows without one.

### §0 budgets — starting numbers

Armed from the first commit, before there is anything to assert against.

| budget | limit | counted by |
|---|---|---|
| direct Go dependencies | 6 | `go.mod` require block, minus `// indirect` |
| total modules | 25 | `go list -m all` |
| packages importing `net/http` | **1** | `internal/feed`, nothing else |
| code lines | 5,000 | `cmd` + `internal`, non-test, non-comment |
| comment ratio | 25% | hard ceiling — over budget retires a comment |
| live prose | 3,000 lines | every `.md` outside `history/` and `review/` |
| binary (linux_amd64, stripped) | 12 MiB | built and measured by the script |
| `panic(` in non-test code | 0 | grep |
| test lines : code lines | ≥ 1 : 3 | a floor, not a target |

The `net/http = 1` row is the one worth defending. *What does this program say
on the wire* gets a one-package answer, which turns the central promise into
something a command can disagree with.

### ADRs written before code

Titles state decisions. Refusals are recorded as carefully as choices — they
are what stops scope being re-proposed every session.

- **ADR-001** — Threat model: what a hostile feed, the network, and another
  local user can each do.
- **ADR-002** — Video plays in mpv's own window, not in the terminal. Records
  the counter-argument that settled it and names the condition that reopens it.
- **ADR-003** — Channel updates come from per-channel RSS, not from the
  extractor.
- **ADR-004** — bivy never authenticates and reads no browser cookies.
- **ADR-005** — Downloading is not a feature.
- **ADR-006** — The user's own mpv configuration is not read.
- **ADR-007** — Thumbnails are never written to disk.

## Architecture

Three rules, chosen because they are what make the tests cheap. Breaking one
quietly is how this stops being testable.

1. **`internal/tui` does no I/O.** It renders a model and emits intents. That is
   what makes golden-file testing of every screen possible. Asserted by
   `budgets.sh` against the package's import graph.
2. **`internal/feed` is the only package importing `net/http`.** Asserted.
3. **`internal/follow` is pure functions over data.** Follow lists and watch
   state are where a program like this accumulates its strangest bugs, and pure
   code is where tests are cheapest.

| package | job |
|---|---|
| `cmd/bivy` | entry point, flag parsing |
| `internal/config` | TOML configuration |
| `internal/feed` | fetch and parse per-channel XML feeds. **Sole `net/http` importer.** |
| `internal/ytdlp` | subprocess only: resolve a search query, resolve a stream URL |
| `internal/mpv` | launch and drive mpv over an IPC socket |
| `internal/follow` | follows and watch state, pure |
| `internal/store` | atomic `0600` persistence |
| `internal/graphics` | terminal capability probe, kitty-protocol image emit |
| `internal/tui` | dashboard, search, thumbnail grid |

### The dashboard needs no extractor

Per-channel feeds are static XML over plain HTTP — no auth, no API key, no
cookies — returning video id, title, publish date, author and thumbnail URL.
That is a complete dashboard row, and it is far lower-profile than driving an
extractor at each channel page.

This scopes the extractor down to two jobs: resolve a search query, and resolve
a stream URL. It is what makes the one-package network rule achievable.

### Driving mpv

mpv is launched idle with an IPC socket and given work over it, rather than
being handed a URL and abandoned. That buys pause, seek, position and
end-of-playback, and it keeps the stream URL out of `argv` where every account
on the machine can read it. `--no-config` — the user's own mpv setup is left
exactly as it was found. Version-gated flags name the mpv version that makes
the gate deletable.

## The filesystem contract

Enforced by a test, because *removable without residue* is a promise worth
keeping and the user asked for it directly.

- Exactly **two** directories are ever written: the config directory and the
  data directory. macOS honours `XDG_*` when set, else the Apple paths.
- The mpv IPC socket lives in a `0700` directory under `$XDG_RUNTIME_DIR` and
  is removed on exit.
- **No cache directory.** Thumbnails live in memory for the session (ADR-007).
- An isolation test runs a first launch against a scratch home directory and asserts
  the write set matches that list *exactly*.
- No partial-file residue anywhere — bivy does not download.

## Load-bearing map (§2.3)

Surfaces that get different treatment for their entire lives:

- **`internal/feed`** — receives bytes chosen by a remote server. Capped
  reader, size-limited, XML parsing fuzzed.
- **`internal/ytdlp` and `internal/mpv`** — execute external processes. Every
  positional argument is preceded by `--`, and any input beginning with `-` is
  refused before it reaches an argv. This is a real defect found in the
  reviewed project and confirmed by running its own URL builder; it is designed
  out here rather than patched later.
- **`internal/graphics`** — writes raw escape sequences derived from remote
  image bytes to the terminal. Control characters in remote titles are stripped
  at the boundary rather than trusted to the renderer.
- **`internal/store`** — the only package that escapes the project's own tree.
- **`Makefile`, `scripts/budgets.sh`, CI** — the enforcement layer is itself
  load-bearing, human-read, and frozen against casual change.

## Milestones after the skeleton

Each ends at something runnable and is checked against the north star before
execution.

1. **Follow and dashboard.** `bivy follow <channel>`, feed fetch, dashboard on
   launch showing what is new since last visit. No player yet.
2. **Play.** mpv over IPC. Enter on a dashboard row plays it and marks it
   watched.
3. **Search.** Extractor-backed query, results into the same list model.
4. **Thumbnails.** Capability probe, kitty-protocol grid, graceful text-only
   fallback where the protocol is absent.

## Verification

- The conformance check — must print `conformant.` The gate for
  milestone 0, and the only thing that must pass before review.
- `make check` — lint, vet, race tests, budgets, then the standard check.
- `go test ./...` — including `docs_test.go`, which holds README claims against
  the Makefile that defines them.
- The isolation test against a scratch home directory, asserting the exact write set.
- Manual, once milestone 2 lands: launch on Linux and on macOS, confirm the
  dashboard renders, a row plays in an mpv window, and the home directory
  afterwards contains exactly the two directories.
