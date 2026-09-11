#!/bin/sh
# The release artefacts: one archive per platform in scripts/platforms, and a
# checksum file over them. Run by `make dist`, which the release workflow runs
# on a tag.
#
# The same build flags as `make build` and as the binary budget, so what is
# measured, what is tested and what is downloaded are one artefact rather than
# three that resemble each other.

set -eu
cd "$(dirname "$0")/.."

VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
OUT=dist

# Two builds of one commit must produce the same bytes, or a checksum says only
# "this is the copy I happened to upload". tar records mtimes, owners and
# directory order, and gzip records a timestamp of its own; none of those are
# properties of the release.
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
		go build -mod=readonly -trimpath \
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
