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

Following, the dashboard, playback and search work. Thumbnails are still to
come; `PLAN.md` §12 lists the rest.

## Using it

```
bivy                    what the channels you follow have posted
bivy follow <channel>   follow one: @handle, a channel URL, or an id
bivy unfollow <channel> stop following one, by id or by name
bivy list               the channels you follow
bivy list-only          the dashboard printed once, without the cursor
```

`bivy` on its own is the dashboard. Newest first, one line each, a dot against
anything posted since you last looked, and a tick against anything you have
watched:

```
bivy · 2 new videos since your last visit

  •  2h ago  Aye                  The newest thing that happened
> •  1d ago  Bee                  Something from yesterday
  ✓  5d ago  Aye                  One you have already watched

  ↑↓ move · enter play · r refresh · q quit
```

Enter plays the row under the cursor in mpv's own window and leaves you on the
dashboard, so you can keep browsing. One mpv serves the whole session, and it
puts no window up until there is something to show.

The window is mpv's, so mpv's keys work in it — `q` closes it, `space` pauses,
`f` is fullscreen. Worth knowing if your desktop gives it no title bar: on
Wayland, mpv draws its own decorations through libdecor, and a build without
that on a compositor offering none server-side has no close button to click. A video
is marked watched when it reaches its end, not when it starts — closing it
after ten seconds does not count as having watched it.

A channel that cannot be reached is named under the list rather than quietly
left out, and its entries stay new until they have actually been shown. Run
bivy with its output piped somewhere and it prints the list once instead.

`/` searches. Results arrive in the same list and play the same way, and `esc`
puts the dashboard back without fetching every feed again. The second column
shows how long ago something was posted, or how long it runs — whichever the
source actually knows.

## Still to come

| | |
|---|---|
| **Thumbnails** | rendered in the terminal where the graphics protocol is available, with bivy saying so plainly where it is not. |

## What it will not do

Read `PLAN.md` §3 for the binding version. In short: it will never download a
file, never ask you to log in, never read a browser cookie, never draw video
into the terminal, and never write a cache. Each of those is a numbered
decision in `docs/decisions.md` with the reasoning attached, so you can
disagree with the argument rather than guess at the motive.

## Requirements

- **mpv**, 0.29 or newer, for playback. bivy decodes nothing itself, and it
  launches mpv with `--no-config` so your own mpv setup is left exactly as it
  was found.
- **yt-dlp**, for search, and used by mpv to resolve a video for playback.
  Never needed for the dashboard.

Neither is needed to follow channels or read the dashboard. bivy says which one
is missing when it needs one.
- A terminal that speaks the kitty graphics protocol, for thumbnails. Optional;
  without one, bivy renders text.

## What it leaves behind

Two directories: one for configuration, one for data. That is the whole write
set, and a test asserts it by running a first launch against a scratch home
directory and comparing. No cache directory, no partial files, nothing to sweep
up. Remove those two directories and bivy was never here.

## Building and checking

```
make build     build bivy for this machine
make install   build it and put it on ~/.local/bin
make check     lint, vet, race tests, budgets, and the standard conformance check
make crossbuild  compile for every platform the project claims
make test      go test
make budgets   the PLAN.md §0 budgets, asserted
make help      every target, with a line each
```

`make check` is the gate. It is what must pass before a commit, and the budgets
it asserts are armed from the first commit rather than introduced once the
project is already over them.

## Licence

MIT.
