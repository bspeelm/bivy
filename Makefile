# bivy.
#
# `make check` is the gate: it is what CI runs and what must pass before a
# commit. Everything else here is a component of it or a convenience.

# Exported rather than spliced into each recipe: go reads GOFLAGS from the
# environment itself, and `go vet` puts the flag before the subcommand, where
# go does not look for it.
export GOFLAGS := -mod=readonly

BINARY  := bivy

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.Version=$(VERSION)

# The development standard this project is held to lives outside it, so that
# two projects cannot drift into two standards. Overridable, because where a
# checkout sits is a property of a machine and not of this project.
STANDARD_CHECK ?= ../agent-context/check.sh

.PHONY: help lint vet test race budgets standard check build install-binary integration crossbuild clean

help:
	@echo "make check       lint, vet, race tests, budgets, standard - the gate"
	@echo "make test        go test"
	@echo "make race        go test -race"
	@echo "make integration go test -tags=integration - needs mpv installed"
	@echo "make budgets     the PLAN.md §0 budgets"
	@echo "make standard    conformance against the development standard, if present"
	@echo "make install-binary  build it, put it on PATH, and say what is missing"
	@echo "make build       build bivy for this machine"
	@echo "make crossbuild  compile for every platform the project claims"

lint:
	gofmt -l . | (! grep .) || { echo "gofmt -w the files above"; exit 1; }

vet:
	go vet ./...
	# The integration tests are behind a build tag, so the line above never
	# compiles them. Vetting them needs no mpv and catches a change to a
	# signature they use.
	go vet -tags=integration ./...

test:
	go test ./...

# The race detector is not optional here: a terminal interface, a persistent
# child process and an HTTP client is three sources of concurrency, and a data
# race in that mix shows up as a rendering artifact nobody can reproduce.
race:
	go test -race ./...

# Drives a real mpv. Not in `check`: the suite must not require mpv to be
# installed, because a test that asks whether something is present passes
# where it was written and fails where the artifact is built.
integration:
	go test -tags=integration ./...

budgets:
	@sh scripts/budgets.sh

# Absent, this is skipped rather than failed: the checkout is allowed to be
# standalone.
standard:
	@if [ -f "$(STANDARD_CHECK)" ]; then sh "$(STANDARD_CHECK)" .; \
	 else echo "the development standard is not beside this checkout; skipped"; fi

# `standard` is in here rather than beside it: a conformance check nothing
# blocks on is the failure it exists to catch.
check: lint vet race budgets standard

# The same flags the budget uses to measure the binary, so what is measured is
# what runs.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) ./cmd/bivy

# A build that only ever happens on the maintainer's machine is a claim about
# one machine. The README says Linux and macOS; this is what holds it to that.
crossbuild:
	@for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
	    echo "  $$t"; \
	    GOOS=$${t%/*} GOARCH=$${t#*/} CGO_ENABLED=0 \
	        go build -trimpath -o /dev/null ./... || exit 1; \
	done

# One command to be running the version in this working tree. It reports what
# it installed and what bivy will not be able to do without, because the two
# halves have different dependencies and "nothing happened" is what a missing
# one looks like.
#
# "on this PATH" rather than "installed": where bivy is built and where it is
# run are not always the same machine, and a checkout that builds somewhere
# without a player is a normal arrangement rather than a broken one.
install-binary: build
	install -Dm755 $(BINARY) $(HOME)/.local/bin/$(BINARY)
	@echo
	@echo "installed $$($(HOME)/.local/bin/$(BINARY) version) to ~/.local/bin/$(BINARY)"
	@case ":$$PATH:" in \
	    *":$(HOME)/.local/bin:"*) ;; \
	    *) echo; echo "~/.local/bin is not on your PATH. Add it:"; \
	       echo '       export PATH="$$HOME/.local/bin:$$PATH"' ;; \
	esac
	@command -v mpv >/dev/null 2>&1 || { echo; \
	    echo "mpv is not on this PATH, and bivy plays through it."; \
	    echo "       dnf install mpv · apt install mpv · brew install mpv"; }
	@command -v yt-dlp >/dev/null 2>&1 || { echo; \
	    echo "yt-dlp is not on this PATH, and search needs it. The dashboard does not."; \
	    echo "       dnf install yt-dlp · apt install yt-dlp · brew install yt-dlp"; }
	@echo; echo "next: $(BINARY)"

clean:
	rm -f $(BINARY)
