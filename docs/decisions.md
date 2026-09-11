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

---

## ADR-008 — Following a handle fetches one page; nothing else bivy does reads HTML

**Status:** accepted, milestone 1.

A feed is keyed by channel identifier, and nobody has one. What people have is
`@handle`, or a URL they copied from a browser. Working out which channel a
handle names means fetching that channel's page and reading the identifier out
of it, and that is a second kind of request — for a page meant for a browser
rather than for a static feed.

The alternative was refusing: accept identifiers and `/channel/UC…` URLs only,
and tell the user to go and find one. It was rejected because the errand it
sets is "open a browser, load the channel, view source or install an
extension", and a program whose first instruction is to go and use the thing it
replaces has not been built yet. The privacy story is also not improved by it:
the user makes the same request, from the same machine, with a browser that
sends far more than bivy does.

What keeps it bounded, and what a reviewer should check has not slipped:

- It happens **when a channel is followed**, and never at launch. The dashboard
  path makes feed requests only (ADR-003).
- It is a plain GET with the same fixed user agent, no cookie and no header
  that identifies anyone — the same rules every other request obeys.
- The response is read through the same capped reader, and nothing from it is
  kept except a string that has been checked to be shaped like an identifier.
- Failing is normal. A consent wall or a redesign means the user is told to
  pass a `/channel/UC…` URL instead, which is the refused design still
  available as a fallback.

This is why §9 names three kinds of outbound request rather than two, and the
count is the thing to keep an eye on: a fourth is a decision, not a detail.

---

## ADR-009 — The interactive layer is written here, not taken from a terminal framework

**Status:** accepted, milestone 2. This supersedes the open question in §13.

The obvious choice is a terminal framework. They are good, they are what
everyone reaches for, and they solve the fiddly parts — raw mode, resize,
key decoding — that are exactly the parts easy to get subtly wrong.

The counter-argument that decided it is the dependency arithmetic. The
budget in §0 is six direct dependencies and twenty-five modules, and the
frameworks in this language cost fifteen to twenty modules on their own. Paying
most of a whole-project budget, in one commit, for a list with a cursor on it,
is the trade §0 exists to make visible. What bivy needs is raw mode, four keys,
a redraw and a window that follows the cursor.

The second reason is milestone 4. A thumbnail grid speaks a graphics protocol
directly, in bytes, and a framework's renderer is the thing standing between
bivy and the terminal when that happens. Owning the output path is worth more
here than it would be in a program that only ever draws text.

What is taken instead is one dependency, `golang.org/x/term`, for `MakeRaw`,
`Restore` and `GetSize`. Those are termios calls; hand-rolling them means
platform-specific ioctl code, which is real risk for no saving. One direct
dependency, two modules, and a reviewer can read all of it.

**What would reopen this:** the interactive layer growing past roughly four
hundred lines, or needing a second screen with its own layout and focus rules.
Either means bivy has become the kind of program a framework is for.

---

## ADR-010 — mpv resolves the stream itself; bivy hands it a page URL

**Status:** accepted, milestone 2.

bivy sends mpv the video's own page URL over the IPC socket and lets mpv's
extractor hook resolve it. bivy does not run the extractor itself for playback.

The alternative is bivy running `yt-dlp` to obtain a direct stream URL and
handing that to mpv. It buys control over format selection and puts extractor
failures in bivy's own error messages, and it is what §4 anticipated. It is not
done yet because it duplicates work mpv already does, doubles the number of
places an extractor is invoked, and adds a package this milestone does not
otherwise need. Milestone 3 introduces `internal/ytdlp` for search; if format
control turns out to matter, that is when it is cheap to revisit.

This does not weaken §7. The URL bivy sends is built from a video identifier it
has checked, and it travels over the socket rather than an argv. mpv then puts
that same public page URL on yt-dlp's command line — but the **resolved** URL,
the one carrying access tokens, is passed back to mpv internally and never
appears in any process table entry. The property §7 is protecting is preserved
by this arrangement rather than despite it.

What it costs: yt-dlp becomes a runtime requirement for playback rather than
only for search, and an extractor failure surfaces as mpv saying it could not
play something. Both are stated in the README rather than discovered.

**What would reopen this:** needing to choose a format, needing to know why a
resolve failed, or milestone 3 making the extractor a package that already
exists.

---

## ADR-011 — A command line for what takes an argument, and keys for everything else

**Status:** accepted, after milestone 3.

`:` opens a command line. `/` opens the same line with `search ` already in it,
because searching is the one command common enough to deserve a key of its own
and that spelling is the one every terminal user already knows.

### The argument against having one at all

A command line is a text-entry surface in a program driven by four keys. It
costs line editing, parsing, error reporting, and somewhere to list what
exists — and the real cost is not the code. Once a command line exists, a
feature request becomes "add a command for it" rather than being refused, and
§3 is the fence that keeps bivy from becoming the program it was built instead
of.

What answers it is that bivy already had the problem a command line solves.
Following a channel was a thing you could only do by quitting, typing
`bivy follow`, and starting again — while looking at a screen full of that
channel's videos. The alternative to a command line was a key per verb, and
`f` for follow, `u` for unfollow, `h` for help is how a program ends up with
twenty-six meanings for twenty-six letters and no room for the twenty-seventh.

### The rule that keeps it small

**A command exists because it takes an argument, or because it is rare.
Anything that is neither is a key.**

So `search`, `follow` and `unfollow` are commands: each needs words after it.
`help` and `quit` are commands because they are rare. Moving, playing,
refreshing and going back are keys, and adding a command for any of them would
be the first sign this fence has stopped holding.

The completion list is part of the bargain. A command line that does not show
what it accepts is a guessing game, so tab completes as far as the matches
agree and the list under the line names every command with a one-line summary.

### What this does not become

Not a scripting surface, not a configuration language, not a way to reach
anything §3 refuses. A command that would download something, log in, or open a
page is refused for the reasons already recorded, and the command line existing
does not reopen any of them.

### Quitting is not a key

`q` sat one keystroke from every other key on the list, and the cost of
hitting it by accident is losing the screen you were reading — a dashboard
that took a fetch per followed channel to build, or a search you have not
finished looking at. It costs `:q` instead.

That is only bearable because a command may be shortened to any prefix one
command answers to, which is the spelling anyone who has used a modal editor
tries first. Ctrl-C still leaves, because a program that cannot be left from
the keyboard is worse than one that can be left by accident.

**What would reopen this:** nothing. The question was whether to have one; it
is answered. Whether a particular command should exist is answered by the rule
above, in the pull request that proposes it.

---

## ADR-012 — One picture, for the row under the cursor, rather than a grid of them

**Status:** accepted, milestone 4. This narrows what §12 said that milestone
would be.

The plan said milestone 4 was a thumbnail grid, and the thing bivy was first
described against shows one. A grid is refused, and what is built instead is a
single picture above the list, for whatever the cursor is on.

### The argument for the grid

It is what was asked for, and it is the part of this program that is unlike
reading a list of titles: a wall of thumbnails is how anyone actually picks a
video, because a title is a worse description of a video than a frame of it.

### What answers it

The list came first and was chosen deliberately. The frame this program now
draws — a title, a rule, a list with the cursor in reverse, a status line, keys
on the bottom row — is a list at every point, and every key means what it means
because there is a row under a cursor. A grid is a different program: two
dimensions of movement, a different notion of what is selected, and a screen
that has no room for a list beside it.

Having both is worse than having either. Two screens with different navigation
in a program with four keys is the kind of thing a command line gets added to
paper over, and ADR-011 exists to stop that.

The cheaper argument is bandwidth, and it is not the reason but it is real: a
picture is tens of kilobytes of base64 down a pseudo-terminal, and a grid is
that times twenty, re-sent on every scroll. One picture is fetched for the row
somebody is looking at, and a list of thirty rows is not thirty requests
nobody asked for.

### What is kept

Everything the grid was for, except the wall: the picture is the size of a
real thumbnail, it changes as the cursor moves, and it is drawn by the
protocol rather than approximated in text.

**What would reopen this:** a second screen, entered deliberately and left
deliberately, that is a grid and nothing else — not a mode the list grows into.
That is a design worth having, and it is not this milestone.
