# bivy

*a terminal browser and player for online video*

> **bivy** *(n.)* — a bivouac; the smallest shelter that still counts as one.

Follow channels. Launch and see what is new. Search when you want something
specific. Press enter and watch it in mpv.

bivy does not download, does not log in, and reads no cookies. One package in
the whole program touches the network, and it has one dependency in total,
which is what makes that a claim you can check rather than one you have to
take.

## Status

Following, the dashboard, playback, search and thumbnails all work.
Every release carries a review packet in `docs/review/` saying what was checked
before it went out and what was not.

## Using it

```
bivy                    what the channels you follow have posted
bivy follow <channel>   follow one: @handle, a channel URL, or an id
bivy unfollow <channel> stop following one, by id or by name
bivy list               the channels you follow
bivy list-only          the dashboard printed once, without the cursor
```

`bivy` on its own is the dashboard. The title says what you are looking at,
the row under the cursor is highlighted, and the keys are always on the last
line:

```
bivy · 2 new videos since your last visit
────────────────────────────────────────────────────────────────────
  • The newest thing that happened                       Aye · 2h ago
> • The one under the cursor                             Bee · 1d ago
  ✓ One that has been watched                            Aye · 5d ago

────────────────────────────────────────────────────────────────────
nothing playing
↑↓ move · enter play · f follow · / search · : commands · r refresh · :q quit
```

Enter on a channel row opens it and lists its videos; `esc` goes back one
screen. `M` loads thirty more of whatever you are looking at.

A dot marks anything posted since you last looked and a tick anything you have
watched. bivy sets that tick when a video plays to its end, and `m` sets or
clears it by hand — for something watched elsewhere, or put down on purpose. What is being chosen sits on the left; what is known about it is
right-aligned, so the titles line up whatever the channel names are.

A channel that cannot be reached is named under the list rather than quietly
left out, and its entries stay new until they have actually been shown. Run
bivy with its output piped somewhere and it prints the list once instead.

`/` searches. Results arrive in the same list and play the same way, and `esc`
puts the dashboard back without fetching every feed again. The second column
shows how long ago something was posted, or how long it runs — whichever the
source actually knows.

`:` opens a command line. Tab completes, and the list under it shows
everything there is:

```
:_

  search <words>   channels <words>   follow <channel>
  unfollow <channel>   refresh   help   quit
```

`/` is the same line with `search ` already in it.

`:channels <words>` finds channels rather than videos, and **`f` follows the
one under the cursor**:

```
bivy · channels · 4 channels for "papa meat"
────────────────────────────────────────────────────────────────────
>   Papa Meat                                       3.6M subscribers
  ✓ MeatCanyon                                      9.1M subscribers
    Meaty Magic                                     266K subscribers

────────────────────────────────────────────────────────────────────
I make cartoons on my main account
↑↓ move · f follow · / search · esc back · :q quit
```

A tick marks one you already follow, and the status line carries the
channel's own description of itself — which is what tells two similarly named
channels apart. `f` works on a video row too, following the channel that video
came from.

A command can be shortened to any prefix only one command answers to, so `:q`
quits and `:se cats` searches. Quitting is deliberately not a key: `q` sat one
keystroke from every other key on the list, and hitting it by accident costs
the screen you were reading.

A command exists because it takes an argument or because it is rare; anything
else is a key. That rule is ADR-011 and it is what keeps the list short.

## Thumbnails

Where your terminal speaks the kitty graphics protocol, the row under the
cursor gets its thumbnail in a pane to the left of the titles, halfway down it.
The pane is a third of the window, so it grows with the terminal and disappears
on one too small to spare the room. bivy asks the terminal whether it can
draw rather than guessing from `$TERM`, so moving between terminals is not
something you have to configure — and where the answer is no, nothing changes
and no stray bytes are printed.

One picture at a time, for the row you are looking at, kept in memory for the
session and written nowhere. There is no thumbnail cache, because a
thumbnail cache is a viewing history in image form in a directory you did not
create.

## What it will not do

Read `PLAN.md` §3 for the binding version. In short: it will never download a
file, never ask you to log in, never read a browser cookie, never draw video
into the terminal, and never write a cache. Each of those is a numbered
decision in `docs/decisions.md` with the reasoning attached, so you can
disagree with the argument rather than guess at the motive.

## Installing

Download the archive for your platform from the releases page, check it, and
put the binary somewhere on your `PATH`:

```
sha256sum -c SHA256SUMS --ignore-missing
tar xzf bivy_<version>_<os>_<arch>.tar.gz
install -Dm755 bivy_<version>_<os>_<arch>/bivy ~/.local/bin/bivy
```

Those checksums catch a damaged download. They are not signatures and they do
not tell you who built the archive: anyone who could replace an archive could
replace the checksum file sitting beside it. What stands in for a signature is
that the build is reproducible — `make dist` on a clean checkout of the tag
produces the published archives byte for byte, so you can rebuild and compare
instead of trusting the upload (ADR-014).

That command handles the one condition that would otherwise trip you up: it
pins the compiler to the version named in `go.mod`, because two Go releases
turn the same source into different binaries. Nothing else about your checkout
matters — a clone, a worktree, or a tree with a file touched all produce the
same archives, because a release build records nothing about the git it was
built from.

Or build it, which needs Go and nothing else:

```
go install github.com/bspeelm/bivy/cmd/bivy@latest
```

## Requirements

- **mpv**, 0.29 or newer, for playback. bivy decodes nothing itself, and it
  launches mpv with `--no-config` so your own mpv setup is left exactly as it
  was found.
- **yt-dlp**, for search, and used by mpv to resolve a video for playback.
  Never needed for the dashboard.
- A terminal that speaks the kitty graphics protocol, for thumbnails. Optional;
  without one, bivy renders text.

Neither mpv nor yt-dlp is needed to follow channels or read the dashboard, and
bivy says which one is missing when it needs one.

mpv also has to be able to reach your display server. Running bivy inside a
container usually means using the host's mpv rather than one installed next to
bivy: a container's own mpv cannot share buffers with the host compositor, and
fails on the display connection after loading the video. A wrapper early on
`PATH` that forwards to the host's mpv is enough — bivy only needs to find
something called mpv that can open a window.

## What it leaves behind

Two directories: one for configuration, one for data. That is the whole write
set, and a test asserts it by running a first launch against a scratch home
directory and comparing. No cache directory, no partial files, nothing to sweep
up. Remove those two directories and bivy was never here.

## Building and checking

```
make install-binary  build it and put it on ~/.local/bin
make build     build bivy for this machine
make check     lint, vet, race tests, budgets, and the standard conformance check
make crossbuild  compile for every platform the project claims
make dist      the release archives and their checksums
make test      go test
make budgets   the PLAN.md §0 budgets, asserted
make help      every target, with a line each
```

`make check` is the gate. It is what must pass before a commit, and the budgets
it asserts are armed from the first commit rather than introduced once the
project is already over them.

## Licence

MIT.
