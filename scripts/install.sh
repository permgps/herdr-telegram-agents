#!/bin/sh
# Downloads the release binary for this host into bin/herdr-tg and checks
# its SHA-256 against the release checksums file. Run by herdr as the
# plugin's [[build]] step on linux and macos; it gets no HERDR_* variables,
# so everything it needs comes from the checkout and the network.
# HERDR_TG_BASE_URL overrides the download location (used by the install
# verification against a local snapshot); it must be https:// unless
# HERDR_TG_ALLOW_INSECURE_BASE=1. HERDR_TG_EXPECTED_SHA256, set by the
# plugin's own updater, is the checksum the owner approved: the binary must
# match it as well as checksums.txt.
#
# Signed releases also carry release.txt (tag, commit, the checksums lines)
# and release.txt.sig, an SSHSIG by a key in scripts/signing/allowed_signers.
# When ssh-keygen can verify SSHSIG (OpenSSH 8.1+, proven by the self-test
# fixture in scripts/signing/), a bad signature refuses the install; without
# such an ssh-keygen, or for an unsigned release, the script warns and trusts
# checksums.txt as before. The binary is run (`herdr-tg version`) only when it
# is bound to something trusted: a verified signature or the approved
# checksum. bin/install-receipt records sha256, approved and signature for
# the plugin's updater.
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
# Only https, also across redirects, unless a local snapshot opts out.
secure="--proto =https --proto-redir =https"
case "$base" in
https://*) ;;
*)
	if [ "${HERDR_TG_ALLOW_INSECURE_BASE:-}" != "1" ]; then
		echo "install: HERDR_TG_BASE_URL must be https:// (set HERDR_TG_ALLOW_INSECURE_BASE=1 for a local snapshot)" >&2
		exit 1
	fi
	secure=""
	;;
esac
echo "install: herdr-tg ${version} for ${os}/${arch}"

mkdir -p bin
tmp=$(mktemp bin/herdr-tg.XXXXXX)
sums=$(mktemp bin/checksums.XXXXXX)
stmt=$(mktemp bin/release.XXXXXX)
sig=$(mktemp bin/release-sig.XXXXXX)
receipt=$(mktemp bin/receipt.XXXXXX)
trap 'rm -f "$tmp" "$sums" "$stmt" "$sig" "$receipt"' EXIT

# fetch_optional URL OUT succeeds when the file was downloaded and fails when
# the release has no such asset (HTTP 404); any other failure ends the script.
fetch_optional() {
	# shellcheck disable=SC2086
	code=$(curl $secure -sSL --max-time 60 --retry 2 -o "$2" -w '%{http_code}' "$1") || code=failed
	case "$code" in
	200) return 0 ;;
	404) return 1 ;;
	esac
	echo "install: downloading $(basename "$1") failed (${code})" >&2
	exit 1
}

# can_verify succeeds when this ssh-keygen verifies the bundled self-test
# signature, so a later failure means a bad signature, not an old tool.
can_verify() {
	command -v ssh-keygen >/dev/null 2>&1 || return 1
	ssh-keygen -Y verify -f scripts/signing/selftest_signers -I herdr-tg-selftest \
		-n herdr-tg-selftest -s scripts/signing/selftest.txt.sig \
		<scripts/signing/selftest.txt >/dev/null 2>&1
}

echo "install: downloading ${asset}"
# shellcheck disable=SC2086 # $secure is a deliberate word list
curl $secure -fsSL --max-time 120 --retry 2 "${base}/${asset}" -o "$tmp"
# shellcheck disable=SC2086
curl $secure -fsSL --max-time 60 --retry 2 "${base}/checksums.txt" -o "$sums"

lines=$(grep -c " \{1,\}${asset}\$" "$sums" || true)
if [ "$lines" = "0" ]; then
	echo "install: ${asset} is missing from checksums.txt" >&2
	exit 1
fi
if [ "$lines" != "1" ]; then
	echo "install: ${asset} is listed ${lines} times in checksums.txt" >&2
	exit 1
fi
expected=$(grep " \{1,\}${asset}\$" "$sums" | cut -d' ' -f1)
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
approved=$(printf '%s' "${HERDR_TG_EXPECTED_SHA256:-}" | tr '[:upper:]' '[:lower:]')
if [ -n "$approved" ] && [ "$approved" != "$actual" ]; then
	echo "install: ${asset} differs from the approved checksum" >&2
	echo "install: approved ${approved}, got ${actual}" >&2
	exit 1
fi
echo "install: checksum ok"

signature=absent
if fetch_optional "${base}/release.txt" "$stmt" && fetch_optional "${base}/release.txt.sig" "$sig"; then
	if can_verify; then
		if ! ssh-keygen -Y verify -f scripts/signing/allowed_signers -I herdr-tg-release \
			-n herdr-tg-release -s "$sig" <"$stmt" >/dev/null 2>&1; then
			echo "install: release signature does not verify" >&2
			exit 1
		fi
		if [ "$(head -n 1 "$stmt")" != "tag v${version}" ]; then
			echo "install: the signed statement is not for v${version}" >&2
			exit 1
		fi
		if [ "$(grep -c " \{1,\}${asset}\$" "$stmt" || true)" != "1" ]; then
			echo "install: the signed statement does not list ${asset} exactly once" >&2
			exit 1
		fi
		signed=$(grep " \{1,\}${asset}\$" "$stmt" | cut -d' ' -f1)
		if [ "$signed" != "$expected" ]; then
			echo "install: the signed checksum for ${asset} differs from checksums.txt" >&2
			exit 1
		fi
		signature=verified
		echo "install: release signature ok"
	else
		signature=unverifiable
		echo "install: cannot check the release signature (needs OpenSSH 8.1 or newer); trusting checksums.txt" >&2
	fi
else
	echo "install: release v${version} is not signed; trusting checksums.txt" >&2
fi

printf 'sha256 %s\napproved %s\nsignature %s\n' "$actual" "${approved:-none}" "$signature" >"$receipt"
mv "$tmp" bin/herdr-tg
chmod 0755 bin/herdr-tg
mv "$receipt" bin/install-receipt
rm -f "$sums" "$stmt" "$sig"
trap - EXIT

echo "install: installed bin/herdr-tg"
if [ "$signature" = verified ] || [ -n "$approved" ]; then
	./bin/herdr-tg version
else
	echo "install: skipped running an unverified binary"
fi
