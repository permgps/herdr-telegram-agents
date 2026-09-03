#!/bin/sh
# Downloads the release binary for this host into bin/herdr-tg and checks
# its SHA-256 against the release checksums file. Run by herdr as the
# plugin's [[build]] step on linux and macos; it gets no HERDR_* variables,
# so everything it needs comes from the checkout and the network.
# HERDR_TG_BASE_URL overrides the download location (used by the install
# verification against a local snapshot).
set -eu

cd "$(dirname "$0")/.."

repo=permgps/herdr-telegram-agents

version=$(sed -n 's/^version *= *"\(.*\)"/\1/p' herdr-plugin.toml | head -1)
if [ -z "$version" ]; then
	echo "install: no version in herdr-plugin.toml" >&2
	exit 1
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
darwin | linux) ;;
*)
	echo "install: unsupported os: $os" >&2
	exit 1
	;;
esac

arch=$(uname -m)
case "$arch" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*)
	echo "install: unsupported arch: $arch" >&2
	exit 1
	;;
esac

if ! command -v curl >/dev/null 2>&1; then
	echo "install: curl is required" >&2
	exit 1
fi

asset="herdr-tg_${os}_${arch}"
base=${HERDR_TG_BASE_URL:-https://github.com/${repo}/releases/download/v${version}}
echo "install: herdr-tg ${version} for ${os}/${arch}"

mkdir -p bin
tmp=bin/herdr-tg.tmp
sums=bin/checksums.txt
trap 'rm -f "$tmp" "$sums"' EXIT

echo "install: downloading ${asset}"
curl -fsSL "${base}/${asset}" -o "$tmp"
curl -fsSL "${base}/checksums.txt" -o "$sums"

expected=$(grep " \{1,\}${asset}\$" "$sums" | cut -d' ' -f1 | head -1)
if [ -z "$expected" ]; then
	echo "install: ${asset} is missing from checksums.txt" >&2
	exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp" | cut -d' ' -f1)
else
	actual=$(shasum -a 256 "$tmp" | cut -d' ' -f1)
fi
if [ "$expected" != "$actual" ]; then
	echo "install: checksum mismatch for ${asset}" >&2
	echo "install: expected ${expected}, got ${actual}" >&2
	exit 1
fi
echo "install: checksum ok"

mv "$tmp" bin/herdr-tg
chmod 0755 bin/herdr-tg
rm -f "$sums"
trap - EXIT

echo "install: installed bin/herdr-tg"
./bin/herdr-tg version
