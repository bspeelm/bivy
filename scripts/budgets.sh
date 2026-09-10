#!/bin/sh
# The §0 budgets, asserted. Run by `make budgets`, which `make check` runs,
# which gates every commit.
#
# These assert from the first commit, before there is anything to assert
# against, because a budget introduced once a project is over it is not a
# budget -- it is a negotiation with a number already lost. Every count below
# is allowed to be zero and the script passes; what it will not do is stay
# quiet once one is exceeded.
#
# Changing a number here means changing PLAN.md §0 in the same commit, with a
# reason, in a decision record. Numbers may tighten. They may not silently
# grow. A test holds this list and that table together.

set -eu
cd "$(dirname "$0")/.."

MAX_DIRECT_DEPS=6
MAX_MODULES=25
MAX_HTTP_PACKAGES=1         # ADR-003: the feed package, and nothing else
MAX_PANICS=0
MAX_CODE_LINES=5000
MAX_COMMENT_RATIO=25        # hard ceiling: over budget retires a comment, never raises this
MAX_DOC_LINES=3000
MAX_BINARY_BYTES=12582912   # 12 MiB, linux_amd64, stripped
MIN_TEST_RATIO=3            # at least one test line per three code lines

fail=0
over() { fail=1; printf '\n  %s\n' "$1"; }

# --- dependencies ------------------------------------------------------------
# The budget does not say a dependency is bad; it says one more of them passes
# a human first.

direct=$(awk '/^require \(/{r=1;next} /^\)/{r=0} r&&!/\/\/ indirect/&&NF{n++}
              /^require [^(]/&&!/\/\/ indirect/{n++} END{print n+0}' go.mod)
printf 'direct deps:   %s (budget %s)\n' "$direct" "$MAX_DIRECT_DEPS"
[ "$direct" -le "$MAX_DIRECT_DEPS" ] || over "over the direct dependency budget -- argue it in an ADR first."

if command -v go >/dev/null 2>&1; then
	modules=$(go list -mod=readonly -m all 2>/dev/null | grep -cv '^github.com/bspeelm/bivy' || true)
	printf 'modules:       %s (budget %s)\n' "$modules" "$MAX_MODULES"
	[ "$modules" -le "$MAX_MODULES" ] || over "over the total module budget."
fi

# --- go.sum is the lockfile --------------------------------------------------
# A second pinning mechanism is a second answer to "what version is this", and
# the two drift silently.
for f in Gopkg.lock glide.lock vendor/modules.txt; do
	[ -e "$f" ] && over "$f exists; go.sum is the only lockfile (§0)."
done
printf 'lockfile:      go.sum only\n'

# --- source counts -----------------------------------------------------------
gofiles=$(find . -name '*.go' -not -path './dist/*' 2>/dev/null | wc -l | tr -d ' ')
if [ "$gofiles" -gt 0 ]; then
	testlines=$(find . -name '*_test.go' -exec cat {} + 2>/dev/null | wc -l | tr -d ' ')
	codelines=$(find ./cmd ./internal -name '*.go' -not -name '*_test.go' -exec cat {} + 2>/dev/null | wc -l | tr -d ' ')
	codelines=${codelines:-0}

	printf 'tests:         %s lines (floor: 1 per %s of code)\n' "$testlines" "$MIN_TEST_RATIO"
	if [ "$codelines" -gt 0 ] && [ $((testlines * MIN_TEST_RATIO)) -lt "$codelines" ]; then
		over "under the test floor -- this is a floor, not a target."
	fi
fi

# --- code, comments, prose ---------------------------------------------------
# Code is non-test, non-comment, non-blank. Comments are measured against it.
# Prose is an absolute line count rather than a share of code, because the plan
# is written before the code and a ratio would penalise that.
SOURCES=$(find ./cmd ./internal -name '*.go' -not -name '*_test.go' 2>/dev/null || true)
if [ -n "$SOURCES" ]; then
	# shellcheck disable=SC2086
	code=$(cat $SOURCES | awk '/^[[:space:]]*\/\*/{b=1} b{if(/\*\//)b=0; next} !/^[[:space:]]*\/\// && NF' | wc -l | tr -d ' ')
	# shellcheck disable=SC2086
	comments=$(cat $SOURCES | awk '/^[[:space:]]*\/\*/{b=1} b{c++; if(/\*\//)b=0; next} /^[[:space:]]*\/\//{c++} END{print c+0}')
	printf 'code:          %s lines (budget %s)\n' "$code" "$MAX_CODE_LINES"
	[ "$code" -le "$MAX_CODE_LINES" ] || over "over the code budget."
	if [ "$code" -gt 0 ]; then
		ratio=$((comments * 100 / code))
		printf 'comments:      %s lines, %s%% of code (budget %s%%)\n' "$comments" "$ratio" "$MAX_COMMENT_RATIO"
		[ "$ratio" -le "$MAX_COMMENT_RATIO" ] || over "over the comment budget -- retire a comment; this ceiling does not move."
	fi
else
	printf 'code:          none yet -- the budgets are armed and waiting\n'
fi

docs=$(find . -name '*.md' -not -path './.git/*' -not -path './tmp/*' \
	-not -path './docs/history/*' -not -path './docs/review/*' -exec cat {} + | wc -l | tr -d ' ')
printf 'prose:         %s lines (budget %s)\n' "$docs" "$MAX_DOC_LINES"
[ "$docs" -le "$MAX_DOC_LINES" ] || over "over the prose budget -- retire a doc to docs/history/, do not raise the cap."

# --- panic -------------------------------------------------------------------
# A panic in a terminal interface takes the terminal with it. Errors are values
# here.
panics=$(grep -rn 'panic(' --include='*.go' . 2>/dev/null | grep -vc '_test.go' || true)
printf 'panics:        %s (budget %s)\n' "$panics" "$MAX_PANICS"
[ "$panics" -le "$MAX_PANICS" ] || { over "panic( in non-test code:"; \
	grep -rn 'panic(' --include='*.go' . 2>/dev/null | grep -v '_test.go' | sed 's/^/    /'; }

# --- who talks to the network ------------------------------------------------
# "What does this program say on the wire" must have a one-package answer, and
# that is the claim the whole privacy story in §9 rests on. Test imports are
# counted: a second package reaching for httptest breaks the rule as surely as
# the code would.
if command -v go >/dev/null 2>&1; then
	httppkgs=$(go list -mod=readonly \
		-f '{{.ImportPath}} {{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}' \
		./... 2>/dev/null | grep -c ' net/http' || true)
	printf 'net/http:      %s packages (budget %s)\n' "$httppkgs" "$MAX_HTTP_PACKAGES"
	if [ "$httppkgs" -gt "$MAX_HTTP_PACKAGES" ]; then
		over "more packages import net/http than the architecture allows (§4, ADR-003):"
		go list -mod=readonly \
			-f '{{.ImportPath}} {{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}' \
			./... 2>/dev/null | grep ' net/http' | awk '{print "    " $1}'
	fi
fi

# --- verification has no off switch ------------------------------------------
# A knob that exists gets turned, and gets pasted into setup guides.
tls=$(grep -rln 'InsecureSkipVerify' --include='*.go' . 2>/dev/null || true)
if [ -n "$tls" ]; then
	over "TLS verification bypass present -- there is no off switch (ADR-001):"
	echo "$tls" | sed 's/^/    /'
else
	printf 'tls bypass:    none\n'
fi

# --- the shape of the terminal interface -------------------------------------
# internal/tui renders a model and emits intents; it does no I/O. That rule is
# what makes golden-file testing of every screen possible. Types may cross this
# boundary. Syscalls may not.
if command -v go >/dev/null 2>&1 && [ -d internal/tui ]; then
	tuiio=$(go list -mod=readonly -f '{{.ImportPath}} {{join .Imports " "}}' ./internal/tui/... 2>/dev/null \
		| grep -E ' (net/http|net|os|os/exec)($| )' || true)
	if [ -n "$tuiio" ]; then
		over "internal/tui does I/O, which is architectural rule 1 (§4):"
		echo "$tuiio" | awk '{print "    " $1}' | sort -u
	else
		printf 'tui i/o:       none\n'
	fi
fi

# --- the artifact ------------------------------------------------------------
# Built here rather than measured wherever one happens to be lying around: the
# budget names linux_amd64 stripped, so this builds exactly that and throws it
# away. A number nothing computes is not a budget, and the release is too late
# to find out.
if command -v go >/dev/null 2>&1 && [ -d cmd/bivy ]; then
	tmpbin=$(mktemp) || exit 1
	trap 'rm -f "$tmpbin"' EXIT INT TERM
	if GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
	   go build -mod=readonly -trimpath -ldflags '-s -w' -o "$tmpbin" ./cmd/bivy 2>/dev/null; then
		size=$(wc -c < "$tmpbin" | tr -d ' ')
		printf 'binary:        %s bytes (budget %s)\n' "$size" "$MAX_BINARY_BYTES"
		[ "$size" -le "$MAX_BINARY_BYTES" ] || over "over the binary budget."
	else
		over "the linux_amd64 build failed, so the binary budget could not be counted."
	fi
fi

if [ "$fail" -ne 0 ]; then
	printf '\nDo not argue with a budget. Change PLAN.md §0 and say why.\n'
	exit 1
fi
