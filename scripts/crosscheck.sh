#!/bin/sh
# Cross-compile gate: builds and vets every release target so a build-tag
# typo (for example in internal/adapters/herdr/dial_windows.go) fails
# locally instead of in the release workflow. Prints one line per target.
# TARGETS overrides the list, e.g. TARGETS="linux/amd64" sh scripts/crosscheck.sh

set -eu

TARGETS=${TARGETS:-"darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64"}

for target in $TARGETS; do
	os=${target%/*}
	arch=${target#*/}
	echo "crosscheck: $os/$arch"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -o /dev/null ./...
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go vet ./...
done
echo "crosscheck ok"
