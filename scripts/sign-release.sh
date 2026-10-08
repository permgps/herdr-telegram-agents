#!/bin/sh
# Signs a published release with the owner's offline key and uploads the
# signed statement:
#
#   sh scripts/sign-release.sh 0.17.0 [--force]      (or make sign-release VERSION=0.17.0)
#
# release.txt holds "tag vX.Y.Z", "commit <the tag's commit>" and the
# checksums.txt lines verbatim; release.txt.sig is its SSHSIG
# (`ssh-keygen -Y sign -n herdr-tg-release`). The key is
# HERDR_TG_SIGNING_KEY (default ~/.ssh/herdr-tg-release); ssh-keygen asks for
# its passphrase. The signature is checked against
# scripts/signing/allowed_signers before anything is uploaded. Needs gh, git
# and ssh-keygen; run it after the release workflow published the tag and
# before pushing main.
set -eu

version=${1:-}
force=${2:-}
if [ -z "$version" ] || { [ -n "$force" ] && [ "$force" != "--force" ]; }; then
	echo "usage: sh scripts/sign-release.sh <version> [--force]" >&2
	exit 2
fi
tag="v${version}"
key=${HERDR_TG_SIGNING_KEY:-$HOME/.ssh/herdr-tg-release}

cd "$(dirname "$0")/.."

for tool in gh git ssh-keygen; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "sign-release: $tool is required" >&2
		exit 1
	fi
done
if [ ! -f "$key" ]; then
	echo "sign-release: no signing key at $key (set HERDR_TG_SIGNING_KEY)" >&2
	exit 1
fi

echo "sign-release: fetching ${tag}"
git fetch --quiet origin "refs/tags/${tag}:refs/tags/${tag}" --no-tags
commit=$(git rev-parse "${tag}^{commit}")

draft=$(gh release view "$tag" --json isDraft --jq .isDraft)
if [ "$draft" != "false" ]; then
	echo "sign-release: ${tag} is a draft; publish it first" >&2
	exit 1
fi
# A release.txt without its .sig is left by an interrupted upload; either
# file needs --clobber to be replaced.
signed=$(gh release view "$tag" --json assets --jq '[.assets[].name] | map(select(. == "release.txt.sig")) | length')
partial=$(gh release view "$tag" --json assets --jq '[.assets[].name] | map(select(. == "release.txt")) | length')
clobber=""
if [ "$signed" != "0" ] && [ "$force" != "--force" ]; then
	echo "sign-release: ${tag} is already signed (pass --force to replace the signature)" >&2
	exit 1
fi
if [ "$signed" != "0" ] || [ "$partial" != "0" ]; then
	clobber="--clobber"
fi

dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT INT TERM
gh release download "$tag" --pattern checksums.txt --dir "$dir"

# The five assets .goreleaser.yaml builds, each listed exactly once.
for asset in herdr-tg_darwin_amd64 herdr-tg_darwin_arm64 herdr-tg_linux_amd64 herdr-tg_linux_arm64 herdr-tg_windows_amd64.exe; do
	if [ "$(grep -c "^[0-9a-f]\{64\}  ${asset}\$" "$dir/checksums.txt" || true)" != "1" ]; then
		echo "sign-release: checksums.txt does not list ${asset} exactly once" >&2
		exit 1
	fi
done
if [ "$(grep -c . "$dir/checksums.txt")" != "5" ]; then
	echo "sign-release: checksums.txt lists unexpected assets" >&2
	exit 1
fi

{
	printf 'tag %s\ncommit %s\n' "$tag" "$commit"
	cat "$dir/checksums.txt"
} >"$dir/release.txt"

echo "sign-release: signing ${tag} at ${commit} with $(ssh-keygen -lf "${key}.pub" 2>/dev/null || echo "$key")"
ssh-keygen -q -Y sign -f "$key" -n herdr-tg-release "$dir/release.txt"
if ! ssh-keygen -Y verify -f scripts/signing/allowed_signers -I herdr-tg-release -n herdr-tg-release \
	-s "$dir/release.txt.sig" <"$dir/release.txt" >/dev/null 2>&1; then
	echo "sign-release: the signature does not verify against scripts/signing/allowed_signers; is $key the release key?" >&2
	exit 1
fi

# shellcheck disable=SC2086 # $clobber is empty or one flag
gh release upload "$tag" "$dir/release.txt" "$dir/release.txt.sig" $clobber
short=$(printf '%s' "$commit" | cut -c1-12)
echo "sign-release: ${tag} signed (commit ${short}); now run sh scripts/verify-install.sh ${version} all"
