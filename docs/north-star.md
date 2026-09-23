# North star

*A way to watch what you follow that leaves nothing of you behind, and nothing
of itself behind either.*

## The invariant

Four verbs, and the whole program is in service of them:

```
follow  →  see what is new  →  pick one  →  watch it
```

Follow a channel. Launch and see what it has posted. Pick one. Watch it.
Everything else is a comfort, and comforts are argued for in
`docs/decisions.md` before they are built.

## Who it is for

Someone who lives in a terminal, runs a modern one, and does not want an
account with the service they are watching. They are not looking to be
recommended anything. They already know what they follow.

That is a narrow audience on purpose, and the narrowness is what makes the
refusals in §3 of the plan cheap to keep.

## What "good" means here

One command, honest about what it can do on the terminal it finds itself in,
and removable without residue.

Concretely, and each of these is a test rather than an aspiration:

- **Nothing identifies the user.** No account, no API key, no identifier that
  outlives the session — the visitor cookie the extractor carries is invented
  at startup and gone when bivy exits, so nothing ties one session to the next.
  One package touches the network, so a stranger can check that claim rather
  than believe it.
- **No residue.** Two directories, listed in the plan's §8, and nothing else
  ever. The isolation suite runs a first launch against a scratch home
  directory and asserts the write set matches that list exactly. No cache, no
  partial files, nothing to sweep up afterwards.
- **Nothing of anyone else's is touched.** mpv is launched with `--no-config`
  and explicit flags; the user's own mpv setup is left exactly as it was found.
- **Honest about the terminal.** The image capability is probed once and
  reported, not guessed at by trying commands until one appears to work. Where
  the protocol is absent, bivy says so and renders text.

## What would mean this failed

Not "it has few users" — the audience is small by construction. These:

- A request goes out that identifies the person running it.
- Something is written outside the two directories in §8, or something survives
  an uninstall.
- A stream URL or any other secret-shaped string reaches an `argv`, where every
  account on the machine can read it.
- Someone reads the code and cannot tell why a decision was made, because it
  was made silently.

A stranger with `grep` and an afternoon should be able to check every claim
this project makes. That is the standard being aimed at, and it is only
possible because the reasoning is written down where they can find it.

## The scope fence

`PLAN.md` §3 holds the not-list, and it is as binding as the goals. Things on
it enter through a decision record, never through a pull request that quietly
grows what the program is.
