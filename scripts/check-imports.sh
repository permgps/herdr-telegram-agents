#!/bin/sh
# Forbidden-import gate for the Explicit Architecture layering.
# Rules (see .ai-factory/ARCHITECTURE.md "Dependency Rules"):
#   - internal/domain imports the standard library only
#   - internal/app imports only internal/domain from this module, no third-party
#   - internal/testkit imports only internal/domain from this module
#   - internal/cli imports only internal/compose and internal/domain from this module
#   - cmd/herdr-tg imports only internal/cli from this module
#   - internal/adapters/herdr and internal/adapters/telegram never import each other,
#     internal/app or internal/cli
#   - github.com/go-telegram/bot only from internal/adapters/telegram
#   - net, os/exec, github.com/Microsoft/go-winio only from internal/adapters/herdr,
#     internal/adapters/system (later) and internal/testkit (fake socket server; net only)
#   - os.Getenv / os.LookupEnv / os.Environ only in internal/adapters/system and internal/cli
# Prints one "<pkg> imports <forbidden>" line per violation and exits 1.
# VERBOSE=1 echoes every package/import pair that is checked.

set -u

MOD=github.com/permgps/herdr-telegram-agents
fail=0

violation() {
	echo "$1 imports $2"
	fail=1
}

log() {
	if [ "${VERBOSE:-0}" = "1" ]; then
		echo "check: $*"
	fi
}

# is_third_party <import>: 1 when the first path element contains a dot
# (github.com/..., golang.org/...), which is how Go distinguishes external
# modules from the standard library.
is_third_party() {
	first=${1%%/*}
	case "$first" in
	*.*) return 0 ;;
	*) return 1 ;;
	esac
}

tmp=$(mktemp) || exit 1
trap 'rm -f "$tmp"' EXIT

if ! go list -f '{{.ImportPath}} {{join .Imports " "}}' ./... >"$tmp"; then
	echo "go list failed"
	exit 1
fi

while read -r pkg imports; do
	rel=${pkg#"$MOD"/}
	for imp in $imports; do
		log "$rel -> $imp"
		third=0
		if is_third_party "$imp"; then third=1; fi
		inmod=0
		case "$imp" in "$MOD"/*) inmod=1 ;; esac

		# Layer rules by importing package.
		case "$rel" in
		internal/domain)
			[ "$third" = 1 ] && violation "$rel" "$imp"
			[ "$inmod" = 1 ] && violation "$rel" "$imp"
			;;
		internal/app | internal/app/*)
			[ "$third" = 1 ] && violation "$rel" "$imp"
			if [ "$inmod" = 1 ] && [ "$imp" != "$MOD/internal/domain" ]; then violation "$rel" "$imp"; fi
			;;
		internal/testkit)
			[ "$third" = 1 ] && violation "$rel" "$imp"
			if [ "$inmod" = 1 ] && [ "$imp" != "$MOD/internal/domain" ]; then violation "$rel" "$imp"; fi
			;;
		internal/cli | internal/cli/*)
			if [ "$inmod" = 1 ]; then
				case "$imp" in
				"$MOD/internal/compose" | "$MOD/internal/domain") ;;
				*) violation "$rel" "$imp" ;;
				esac
			fi
			;;
		cmd/*)
			if [ "$inmod" = 1 ] && [ "$imp" != "$MOD/internal/cli" ]; then violation "$rel" "$imp"; fi
			;;
		internal/adapters/herdr | internal/adapters/telegram)
			case "$imp" in
			"$MOD/internal/adapters/"* | "$MOD/internal/app"* | "$MOD/internal/cli"*) violation "$rel" "$imp" ;;
			esac
			;;
		esac

		# Global rules by imported package.
		case "$imp" in
		github.com/go-telegram/bot | github.com/go-telegram/bot/*)
			[ "$rel" != "internal/adapters/telegram" ] && violation "$rel" "$imp"
			;;
		net)
			case "$rel" in
			internal/adapters/herdr | internal/adapters/system | internal/testkit) ;;
			*) violation "$rel" "$imp" ;;
			esac
			;;
		os/exec | github.com/Microsoft/go-winio | github.com/Microsoft/go-winio/*)
			case "$rel" in
			internal/adapters/herdr | internal/adapters/system) ;;
			*) violation "$rel" "$imp" ;;
			esac
			;;
		esac
	done
done <"$tmp"

# Environment access is confined to two packages.
envhits=$(grep -rn --include='*.go' -e 'os\.Getenv' -e 'os\.LookupEnv' -e 'os\.Environ' cmd internal 2>/dev/null |
	grep -v '_test\.go:' |
	grep -v '^internal/adapters/system/' |
	grep -v '^internal/cli/' || true)
if [ -n "$envhits" ]; then
	echo "$envhits" | while read -r hit; do
		echo "${hit%%:*} imports os.Getenv (env access allowed only in internal/adapters/system and internal/cli)"
	done
	fail=1
fi

if [ "$fail" = 1 ]; then
	exit 1
fi
echo "imports ok"
