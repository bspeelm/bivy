# bivy

*a terminal browser and player for online video*

> **bivy** *(n.)* — a bivouac; the smallest shelter that still counts as one.

Follow channels. Launch and see what is new. Search when you want something
specific. Press enter and watch it in mpv.

bivy does not download, does not log in, and reads no cookies. One package in
the whole program touches the network, which is what makes that a claim you can
check rather than one you have to take.

## Status

Following and the dashboard work. There is no player yet — pressing enter on a
row is milestone 2. `PLAN.md` §12 lists the rest.

## Using it

```
bivy                    what the channels you follow have posted
bivy follow <channel>   follow one: @handle, a channel URL, or an id
bivy unfollow <channel> stop following one, by id or by name
bivy list               the channels you follow
```

`bivy` on its own is the dashboard. Newest first, one line each, and a dot
against anything posted since you last looked:

```
bivy · 2 new videos since your last visit

  •  2h ago  Aye                  The newest thing that happened
  •  1d ago  Bee                  Something from yesterday
     5d ago  Aye                  Older, and already seen
```

A channel that cannot be reached is named under the list rather than quietly
left out, and its entries stay new until they have actually been shown.

## Still to come

| | |
|---|---|
| **Watch** | enter plays the video in mpv's own window, and marks it watched. |
| **Search** | a query, resolved through `yt-dlp`, returning rows that behave like any other row. |
| **Thumbnails** | rendered in the terminal where the graphics protocol is available, with bivy saying so plainly where it is not. |

## What it will not do

Read `PLAN.md` §3 for the binding version. In short: it will never download a
file, never ask you to log in, never read a browser cookie, never draw video
into the terminal, and never write a cache. Each of those is a numbered
decision in `docs/decisions.md` with the reasoning attached, so you can
disagree with the argument rather than guess at the motive.

## Requirements

- **mpv**, for playback. bivy decodes nothing itself. Not needed yet.
- **yt-dlp**, to resolve a search query and a stream URL. Not needed yet, and
  the dashboard will never need it.
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
make test      go test
make budgets   the PLAN.md §0 budgets, asserted
make help      every target, with a line each
```

`make check` is the gate. It is what must pass before a commit, and the budgets
it asserts are armed from the first commit rather than introduced once the
project is already over them.

## Licence

MIT.
