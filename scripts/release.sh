#!/bin/sh
# Builds a release into dist/: Linux binaries for amd64 and arm64,
# release.json (read by Sideyard's update check), SHA256SUMS, and an
# install.sh that knows where to find releases.
#
#   VERSION=v1.2.3 SOURCE=https://github.com/owner/repo scripts/release.sh
#
# SOURCE is where the release will be published, laid out like GitHub
# Releases. Without it the binaries don't check for updates and install.sh
# needs SIDEYARD_SOURCE. Build the dashboard first (make web).
set -eu
cd "$(dirname "$0")/.."

: "${VERSION:?set VERSION, like v1.2.3}"
SOURCE="${SOURCE:-}"
SOURCE="${SOURCE%/}"
# The same shape Sideyard's update check accepts: vMAJOR.MINOR.PATCH, with an
# optional -pre-release. Anything else would leave servers unable to compare.
if ! printf '%s' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
	echo "VERSION must look like v1.2.3 or v1.2.3-rc.1, not $VERSION" >&2
	exit 1
fi
case "$SOURCE" in
"" | https://* | http://127.* | http://localhost* | http://\[::1\]*) ;;
*) echo "SOURCE must be https (or http on localhost, for testing), not $SOURCE" >&2 && exit 1 ;;
esac

sha256() {
	if command -v sha256sum >/dev/null; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}

mkdir -p dist
rm -f dist/sideyard-linux-* dist/release.json dist/SHA256SUMS dist/install.sh
ldflags="-s -w -X sideyard/internal/version.Version=$VERSION -X sideyard/internal/version.Source=$SOURCE"
assets=""
for arch in amd64 arm64; do
	name=sideyard-linux-$arch
	CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$ldflags" -o "dist/$name" ./cmd/sideyard
	sum=$(sha256 "dist/$name")
	size=$(wc -c <"dist/$name" | tr -d ' ')
	printf '%s  %s\n' "$sum" "$name" >>dist/SHA256SUMS
	assets="$assets${assets:+,
}    \"linux-$arch\": {\"name\": \"$name\", \"size\": $size, \"sha256\": \"$sum\"}"
done

cat >dist/release.json <<JSON
{
  "version": "$VERSION",
  "published": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "assets": {
$assets
  }
}
JSON

if [ -n "$SOURCE" ]; then
	sed "s#__SOURCE__#$SOURCE#" deploy/install.sh >dist/install.sh
else
	cp deploy/install.sh dist/install.sh
fi
chmod 755 dist/install.sh

echo "Built Sideyard $VERSION in dist/:"
ls -l dist/sideyard-linux-* dist/release.json dist/SHA256SUMS dist/install.sh | awk '{ print "  " $NF " (" $5 " bytes)" }'
if [ -n "$SOURCE" ]; then
	echo "Install link once published: curl -fsSL $SOURCE/releases/latest/download/install.sh | sudo bash"
fi
