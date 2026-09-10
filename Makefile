# bivy.
#
# `make check` is the gate: it is what CI runs and what must pass before a
# commit. Everything else here is a component of it or a convenience.

# Exported rather than spliced into each recipe: go reads GOFLAGS from the
# environment itself, and `go vet` puts the flag before the subcommand, where
# go does not look for it.
export GOFLAGS := -mod=readonly

# The development standard this project is held to lives outside it, so that
# two projects cannot drift into two standards. Overridable, because where a
# checkout sits is a property of a machine and not of this project.
STANDARD_CHECK ?= ../agent-context/check.sh

.PHONY: help lint vet test race budgets standard check

help:
	@echo "make check       lint, vet, race tests, budgets, standard - the gate"
	@echo "make test        go test"
	@echo "make race        go test -race"
	@echo "make budgets     the PLAN.md §0 budgets"
	@echo "make standard    conformance against the development standard, if present"

lint:
	gofmt -l . | (! grep .) || { echo "gofmt -w the files above"; exit 1; }

vet:
	go vet ./...

test:
	go test ./...

# The race detector is not optional here: a terminal interface, a persistent
# child process and an HTTP client is three sources of concurrency, and a data
# race in that mix shows up as a rendering artifact nobody can reproduce.
race:
	go test -race ./...

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
