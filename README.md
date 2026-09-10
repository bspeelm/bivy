# bivy

*a terminal browser and player for online video*

> **bivy** *(n.)* — a bivouac; the smallest shelter that still counts as one.

Follow channels. Launch and see what is new. Search when you want something
specific. Press enter and watch it in mpv.

bivy does not download, does not log in, and reads no cookies. One package in
the whole program touches the network, which is what makes that a claim you can
check rather than one you have to take.

## Status

**Nothing is built yet.** This repository currently holds the founding
documents, the budgets, and the checks that hold them together. `PLAN.md` §12
lists the milestones; milestone 0 is the skeleton you are looking at.

## What it will do

| | |
|---|---|
| **Follow** | `bivy follow <channel>` adds a channel to a local list. Nothing leaves the machine but the request for that channel's public feed. |
| **Dashboard** | launching bivy shows what the channels you follow have posted since you last looked. |
| **Search** | a query, resolved through `yt-dlp`, returning rows that behave like any other row. |
| **Watch** | enter plays the video in mpv's own window, and marks it watched. |

Thumbnails render in the terminal where the graphics protocol is available,
and bivy says so plainly where it is not.

## What it will not do

Read `PLAN.md` §3 for the binding version. In short: it will never download a
file, never ask you to log in, never read a browser cookie, never draw video
into the terminal, and never write a cache. Each of those is a numbered
decision in `docs/decisions.md` with the reasoning attached, so you can
disagree with the argument rather than guess at the motive.

## Requirements

- **mpv**, for playback. bivy decodes nothing itself.
- **yt-dlp**, to resolve a search query and a stream URL. The dashboard does
  not need it.
- A terminal that speaks the kitty graphics protocol, for thumbnails. Optional;
  without one, bivy renders text.

## What it leaves behind

Two directories: one for configuration, one for data. That is the whole write
set, and a test asserts it by running a first launch against a scratch home
directory and comparing. No cache directory, no partial files, nothing to sweep
up. Remove those two directories and bivy was never here.

## Building and checking

```
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
