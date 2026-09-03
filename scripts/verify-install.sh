#!/bin/sh
# Verifies that `herdr plugin install` would work on a clean machine: it
# runs scripts/install.sh where no Go toolchain exists and checks that the
# downloaded binary runs.
#
#   sh scripts/verify-install.sh 0.1.0 [linux|macos|all]
#
# linux runs two throwaway debian containers (amd64 and arm64) that clone
# the tag from GitHub; macos clones the tag into a temporary directory and
# runs the script with a PATH that has no Go. Nothing outside the
# containers and that temporary directory is touched, and the directory is
# removed at the end.
#
# HERDR_TG_BASE_URL is passed through, so a local snapshot can be verified
# before a tag exists.
set -eu

version=${1:-}
targets=${2:-all}
if [ -z "$version" ]; then
	echo "usage: sh scripts/verify-install.sh <version> [linux|macos|all]" >&2
	exit 2
fi
repo_url=${HERDR_TG_REPO_URL:-https://github.com/permgps/herdr-telegram-agents}
tag="v${version}"

verify_linux() {
	if ! command -v docker >/dev/null 2>&1; then
		echo "verify: docker is not installed" >&2
		exit 1
	fi
	if ! docker info >/dev/null 2>&1; then
		echo "verify: docker daemon is not running; start Docker Desktop" >&2
		exit 1
	fi
	for arch in amd64 arm64; do
		echo "verify: linux/${arch} in debian:bookworm-slim"
		docker run --rm --platform "linux/${arch}" \
			-e "HERDR_TG_BASE_URL=${HERDR_TG_BASE_URL:-}" \
			debian:bookworm-slim sh -c "
				set -eu
				apt-get update -qq >/dev/null
				apt-get install -y -qq curl ca-certificates git >/dev/null
				git clone --depth 1 --branch ${tag} ${repo_url} /plugin >/dev/null 2>&1
				cd /plugin
				sh scripts/install.sh
			"
		echo "verify: linux/${arch} ok"
	done
}

verify_macos() {
	if [ "$(uname -s)" != "Darwin" ]; then
		echo "verify: not on macOS, skipping the macos target"
		return 0
	fi
	dir=$(mktemp -d)
	# Only this directory is created and removed; nothing else on the host
	# is touched.
	trap 'rm -rf "$dir"' EXIT INT TERM
	echo "verify: macos in $dir"
	git clone --depth 1 --branch "$tag" "$repo_url" "$dir/plugin" >/dev/null 2>&1
	env -i HOME="$HOME" PATH=/usr/bin:/bin:/usr/sbin:/sbin \
		HERDR_TG_BASE_URL="${HERDR_TG_BASE_URL:-}" \
		sh -c "cd '$dir/plugin' && sh scripts/install.sh"
	"$dir/plugin/bin/herdr-tg" version
	rm -rf "$dir"
	trap - EXIT INT TERM
	echo "verify: macos ok"
}

case "$targets" in
linux) verify_linux ;;
macos) verify_macos ;;
all)
	verify_linux
	verify_macos
	;;
*)
	echo "verify: unknown target $targets (want linux, macos or all)" >&2
	exit 2
	;;
esac

echo "verify: all requested targets ok"
