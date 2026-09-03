[← Behaviour](behaviour.md) · [Back to README](../README.md) · [Next: Testing →](testing.md)

# Development

How to build `herdr-tg` from source, run the checks, and find your way around
the tree. Users of the published plugin need none of this: `herdr plugin
install` downloads a prebuilt binary (see the README).

## Requirements

- Go `1.25` or newer
- `staticcheck` in `$HOME/go/bin` (`make lint` runs it)
- [GoReleaser](https://goreleaser.com) only for `make release-snapshot`

## Build from source

```bash
make build
herdr plugin link /path/to/herdr-telegram-agents
```

`herdr plugin link` skips the manifest's build step, so `make build` produces
`bin/herdr-tg` for your platform first. Unlink before installing the published
plugin over it.

## Make targets

```bash
make build   # bin/herdr-tg for the host platform
make test    # go test -race ./...
make lint    # gofmt, go vet, staticcheck, import layering gate, cross-compile check
```

`make crosscheck` alone builds and vets darwin/amd64, darwin/arm64, linux/amd64,
linux/arm64 and windows/amd64. `make release-snapshot` builds every release
target plus `checksums.txt` into `dist/` without publishing. GitHub Actions
runs the same lint and `go test -race` on every push and pull request, plus
the unit tests on Windows. The manual checklist, including the install
verification against the release assets, lives in [testing.md](testing.md).

## Publishing a release

`herdr plugin install` clones the head of `main` and runs `scripts/install.sh`,
which downloads the release asset named by `version` in `herdr-plugin.toml`.
So at every moment `main` must pair a manifest with a release that already
exists: push the tag first, let the release publish, and only then push
`main`. Pushing `main` with a bumped version before the release exists
breaks installs with a 404 until the workflow finishes.

1. Set `version` in `herdr-plugin.toml` to the new number in the commit you
   are going to tag; the install scripts download the asset named by that
   version, and the release workflow refuses a tag that does not match it.
   Changes to the manifest (actions, panes, startup) and to the install
   scripts ship in that same commit: an installer always runs the manifest
   from `main` against the binary of the released version.
2. `make lint && make test`, and make sure CI is green on the parent commit.
3. `git tag -a vX.Y.Z -m vX.Y.Z && git push origin vX.Y.Z`, without pushing
   `main` yet. The release workflow builds the five binaries and
   `checksums.txt` from the tagged commit; GoReleaser writes the release
   notes from the commit list.
4. Check that the release page lists all six assets, then
   `sh scripts/verify-install.sh X.Y.Z all` (it clones the tag, so it works
   before `main` moves).
5. `git push origin main`.
6. Nothing else: the repository carries the GitHub topic `herdr-plugin`, so
   the [marketplace](https://herdr.dev/plugins/) card picks up the new
   version within 30 minutes.

Commits that touch only Go code are safe to push to `main` at any time:
installers keep getting the binary of the last release until `version`
moves.

## The `dev` subcommand

The undocumented `dev` subcommand talks to the live Herdr socket and works only
inside a Herdr pane (`HERDR_ENV=1`):

```bash
bin/herdr-tg dev agents   # list agents Herdr currently tracks
bin/herdr-tg dev watch    # stream agent events until Ctrl-C
```

The socket path comes from `HERDR_SOCKET_PATH` and falls back to
`~/.config/herdr/herdr.sock`.

## Layout

| Path | Purpose |
|------|---------|
| `cmd/herdr-tg/` | Binary entry point |
| `internal/domain/` | Agents, statuses, topics, mapping, commands, config, events and the ports (standard library only) |
| `internal/app/` | Use cases: agent registry, reconciler, debounce, bridge (screens out, commands in), screen capture for `/screen all`, setup wizard, supervisor, daemon loop |
| `internal/adapters/herdr/` | Herdr socket adapter: dialers, one-shot calls, event stream, `herdr` CLI runner |
| `internal/adapters/telegram/` | Telegram Bot API adapter: bot, queue, formatting, icons, inbound updates, setup probe |
| `internal/adapters/state/` | `config.json`, `mapping.json` and pid file stores |
| `internal/adapters/logging/` | JSON file logger with size-based rotation |
| `internal/adapters/system/` | `HERDR_*` environment, detached process spawn, signals |
| `internal/cli/` | Subcommands behind the single binary |
| `internal/compose/` | Composition root wiring adapters into the use cases |
| `internal/testkit/` | Fakes for every port and a fake Herdr socket server |
| `scripts/` | Import layering gate, cross-compile check, install scripts, version gate, install verification |
| `.github/workflows/` | CI (lint, race tests, Windows tests) and the release workflow |
| `herdr-plugin.toml` | Plugin manifest |

Dependencies point inward (`cli` → `compose` → `app` → `domain`, adapters →
`domain`); the layering is enforced by `scripts/check-imports.sh` in
`make lint`. No test touches the network: the Herdr adapter is tested against
a fake socket server and the Telegram adapter against an in-process HTTP fake.

## See Also

- [Testing](testing.md): automated gates and the manual checklist before a release
- [README: Install](../README.md#install): how users get the prebuilt binary
- [README: Upgrade](../README.md#upgrade): reinstall-based updates and what survives them
