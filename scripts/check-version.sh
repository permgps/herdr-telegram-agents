#!/bin/sh
# Fails unless herdr-plugin.toml's version matches the release tag, so a
# checkout at a tag always installs the binary that tag built.
# Usage: sh scripts/check-version.sh v0.1.0
set -eu

tag=${1:-}
if [ -z "$tag" ]; then
	echo "usage: sh scripts/check-version.sh <tag>" >&2
	exit 2
fi

cd "$(dirname "$0")/.."
version=$(sed -n 's/^version *= *"\(.*\)"/\1/p' herdr-plugin.toml | head -1)
if [ -z "$version" ]; then
	echo "no version in herdr-plugin.toml" >&2
	exit 1
fi
if [ "v$version" != "$tag" ]; then
	echo "manifest version $version does not match tag $tag" >&2
	echo "bump version in herdr-plugin.toml in the commit you tag" >&2
	exit 1
fi
echo "version ok: manifest $version matches $tag"
