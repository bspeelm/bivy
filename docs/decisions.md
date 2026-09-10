# Decisions

Numbered and append-only. A record is superseded or amended by a later record,
never edited away and never deleted. Titles state the decision rather than
naming a topic.

Refusals are recorded here as well as choices. A decision already declined is
settled, and a proposal to revisit it is answered with the record's number
rather than by arguing it again.

---

## ADR-001 — Threat model: what a hostile feed, the network, and another local user can each do

**Status:** accepted, before any code was written.

Four questions, in this order because each answer constrains the next. Every
"therefore" below corresponds to a test.

### What can a hostile feed do?

A feed is bytes chosen by a remote server. It is the only untrusted input bivy
parses, and it arrives on the path that runs unattended at launch.

It can be enormous, so every network read is through a capped reader with a
size limit and a timeout. It can be malformed in ways that crash a parser, so
XML parsing is fuzzed. And it carries text that is rendered to a terminal —
titles and author names — so control characters are stripped at the boundary
rather than trusted to the renderer.

That last one is the subtle one. A terminal renderer that drops escape
sequences it does not recognise is mitigation by accident, not by design: it
still honours the ones it does recognise, and a remote title can carry a
clickable hyperlink pointing anywhere. Stripping at the boundary is a promise
the renderer cannot quietly stop keeping.

### What can the network see?

That someone fetched some feeds and asked an extractor to resolve a stream.

Nothing in that traffic identifies the person, because there is nothing to
identify them with: no account, no cookie, no API key, no identifier bivy
generates and persists. That is ADR-004, and it is the reason this section is
short.

IP-level exposure is real and bivy cannot fix it. A VPN on the machine is the
answer, and it is the user's, not the program's. Pretending otherwise would be
the kind of claim this project exists not to make.

### What can another user of this machine see?

`argv` is world-readable. Anything on a command line is visible to every
account on the machine via the process table, for as long as the process runs.

Therefore no resolved stream URL is ever passed to mpv as an argument. mpv is
launched idle and given work over an IPC socket, and that socket lives in a
`0700` directory under the runtime directory and is removed on exit. This is
the security half of the reason for ADR-002's architecture; the control half is
that a player you cannot pause is not a player.

### What does a stolen state file give an attacker?

The list of channels the user follows and what they have watched. That is a
profile, and it is worth protecting even though it is not a credential:
persisted state is written `0600`, atomically, into one directory.

There is no credential to steal. That is not a mitigation, it is an absence,
and it is worth more than any mitigation would have been.

---

## ADR-002 — Video plays in mpv's own window, not in the terminal

**Status:** accepted, before any code was written. This is a refusal.

The obvious ambition for a program like this is to draw the video in the
terminal. It is refused, and the counter-argument is recorded here so it is not
re-argued every session.

The kitty graphics protocol transmits **still images**. Video through it means
one full-frame image per frame: decode, convert, compress, push through a
pseudo-terminal, have the terminal decompress it and upload it to the GPU —
per frame. At 1080p24 that is on the order of 150 MB/s of raw frames with no
hardware path anywhere in the chain. The terminal video outputs that exist are
understood by the people who wrote them as demonstrations: soft, low framerate,
audio and video drifting apart.

So the split is: **the terminal owns the browser** — thumbnail grid, dashboard,
search results, all still images, which is exactly where the protocol shines.
**mpv owns the video**, with `--vo=gpu` and hardware decode, in its own window.

This also buys the §7 property that no stream URL reaches an `argv`, because
driving mpv over an IPC socket is what makes playback controllable at all.

**What would reopen this:** a terminal graphics protocol with a
hardware-accelerated video path — one that hands a decoder or a GPU surface to
the terminal rather than a sequence of compressed stills. Not a faster still
encoder. Nothing short of that changes the arithmetic above.

---

## ADR-003 — Channel updates come from per-channel feeds, not from the extractor

**Status:** accepted, before any code was written.

Every channel publishes a static XML feed at a stable URL keyed by channel
identifier. It needs no authentication, no API key and no cookie, and it
returns per entry: video id, title, publish timestamp, author, and a thumbnail
URL. That is a complete dashboard row.

The alternative is driving the extractor at each followed channel's page on
every launch. That is heavier, slower, far higher-profile, and it makes the
program's network behaviour a function of a third-party tool's behaviour rather
than of code in this repository.

Two consequences make this more than a convenience:

- It scopes the extractor down to exactly two jobs — resolve a search query,
  resolve a stream URL — neither of which runs at launch.
- It is what makes the `net/http` = 1 budget in §0 achievable, and that budget
  is how the privacy claim in §9 becomes checkable instead of promised.

`encoding/xml` parses it. This costs no dependency.

---

## ADR-004 — bivy never authenticates and reads no browser cookies

**Status:** accepted, before any code was written. This is a refusal.

No login. No OAuth. No API key. No import of a browser cookie jar, in any form,
including the "just point it at your profile" convenience that every tool in
this space eventually grows.

The refusal is worth stating as an absence rather than a policy: the cheapest
way to never leak a credential is to never hold one. It deletes the entire §6
class of problems — storage, permissions, expiry, revocation, what a stolen
config file is worth — rather than solving them.

What it costs: nothing that needs an account works. Private playlists,
age-restricted content, personal recommendations, subscription lists that live
server-side. Following is local, and stays local. That is the trade, made
deliberately.

**What would reopen this:** nothing short of the whole program becoming a
different program, at which point it is a different program.

---

## ADR-005 — Downloading is not a feature

**Status:** accepted, before any code was written. This is a refusal.

bivy browses and plays. No file is fetched to disk for later viewing, and there
is no queue, no resume, and no library.

This is the decision the project was founded on. The starting point was a
review of an existing terminal program of this kind: roughly two-thirds of it
was downloading and the adjacent features that grow around downloading, and its
player shelled out and walked away with no playback control at all. Removing
the downloading meant deleting several thousand lines and then gutting a
three-thousand-line state machine whose download-only states ran through it.

Downloading is also the direct source of the residue this project's north star
refuses: partial fragments left behind by a cancelled fetch, a cache directory
that grows without limit, a library directory the user did not ask for.

The rule is load-bearing for §8, and §8 is enforced by a test.

**What would reopen this:** nothing. A program that downloads is the program
this one was built instead of.

---

## ADR-006 — The user's own mpv configuration is not read

**Status:** accepted, before any code was written. This is a refusal.

mpv is launched with `--no-config` and an explicit flag set.

The argument for reading it is genuine: someone who has tuned their mpv would
like bivy to respect it. It is refused because a player launched under the
user's own configuration is a player whose behaviour bivy cannot predict, test
or support. A profile that forces an output driver, binds a key that quits, or
enables a script turns "bivy plays a video" into "bivy plays a video, depending
on a file bivy has never read".

The stronger half: `--no-config` is also a promise in the other direction.
bivy writes nothing into the user's mpv setup and leaves it exactly as it was
found, which is the same promise §8 makes about the filesystem.

Version-gated flags carry a comment naming the mpv version that makes the gate
deletable, so the compatibility scar tissue has an expiry date.

**What would reopen this:** an explicit, opt-in flag naming a configuration
file to load, if a real user asks for it. Not silent inheritance.

---

## ADR-007 — Thumbnails are never written to disk

**Status:** accepted, before any code was written. This is a refusal.

Thumbnails are fetched, held in memory for the session, and gone when it ends.
There is no cache directory, and §8 asserts that there is not.

A thumbnail cache is the obvious optimisation and it is refused on two counts.
It is a viewing history in image form, sitting in a directory the user did not
create and will not think to clear — the exact residue the north star refuses.
And it is a third directory, which breaks the write-set test in §8 and turns a
checkable promise into a caveat.

The cost is re-fetching thumbnails on each launch. For a dashboard of followed
channels that is a bounded, small number of small images, and it is the correct
price.

**What would reopen this:** a measured launch that is slow enough to matter,
and even then the answer is a session-lifetime memory budget before it is a
directory.
