#!/bin/sh
# The release artefacts: one archive per platform in scripts/platforms, and a
# checksum file over them. Run by `make dist`, which the release workflow runs
# on a tag.
#
# The same build flags as `make build` and as the binary budget, so what is
# measured, what is tested and what is downloaded are one artefact rather than
# three that resemble each other — with one addition, -buildvcs=false, whose
# reason is at the build itself.

set -eu
cd "$(dirname "$0")/.."

VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
OUT=dist

# The compiler is part of the artefact. Two Go versions build the same source
# into different bytes, so "rebuild the tag and compare" is only an offer worth
# making if the rebuild uses the compiler the release used. go.mod names it and
# CI reads the same line, so there is one answer rather than two.
#
# Pinned rather than floored: GOTOOLCHAIN=auto takes the newer of the local
# toolchain and this one, which is the right default for building and the wrong
# one for reproducing.
goversion=$(awk '/^go /{print $2; exit}' go.mod)
case $goversion in
	*.*.*) ;;
	*.*) goversion="$goversion.0" ;;
esac
GOTOOLCHAIN="go$goversion"
export GOTOOLCHAIN

# Two builds of one commit must produce the same bytes, or a checksum says only
# "this is the copy I happened to upload". tar records mtimes, owners and
# directory order, and gzip records a timestamp of its own; none of those are
# properties of the release.
#
# -buildvcs=false is part of the same answer. Go otherwise stamps the binary
# with what git says, and git says different things about checkouts that hold
# identical source: a clone at the tag reports the tag, a linked worktree
# reports (devel), and a tree with one file touched reports itself modified.
# A rebuilder comparing checksums would read that as a tampered archive, which
# is a false alarm on exactly the check this is here to support. The version is
# stamped explicitly through -X, so nothing is lost that anyone reads.
#
# GNU tar only. Elsewhere the archive is still correct, just not byte-stable,
# and this says so rather than failing: the reproducibility claim is gated in
# CI, which is where the published artefacts are built.
epoch=$(git log -1 --format=%ct 2>/dev/null || echo 0)
SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$epoch}
if tar --version 2>/dev/null | grep -q GNU; then
	deterministic="--sort=name --owner=0 --group=0 --numeric-owner --mtime=@$SOURCE_DATE_EPOCH"
else
	deterministic=""
	echo "note: not GNU tar, so these archives are not byte-reproducible"
fi

sum() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

rm -rf "$OUT"
mkdir -p "$OUT"

while read -r platform; do
	case $platform in ''|\#*) continue ;; esac
	goos=${platform%/*}
	goarch=${platform#*/}

	name="bivy_${VERSION}_${goos}_${goarch}"
	stage="$OUT/$name"
	mkdir -p "$stage"

	GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
		go build -mod=readonly -trimpath -buildvcs=false \
		-ldflags "-s -w -X main.Version=$VERSION" \
		-o "$stage/bivy" ./cmd/bivy

	# The licence and the README travel with the binary: an archive that is
	# only an executable leaves whoever downloaded it with no statement of
	# terms and nothing saying what it needs installed.
	cp README.md LICENSE "$stage"

	# shellcheck disable=SC2086
	tar $deterministic -cf - -C "$OUT" "$name" | gzip -n -9 > "$stage.tar.gz"
	rm -rf "$stage"
	printf '  %s\n' "$stage.tar.gz"
done < scripts/platforms

cd "$OUT"
sum ./*.tar.gz > SHA256SUMS
printf '  %s/SHA256SUMS\n' "$OUT"
