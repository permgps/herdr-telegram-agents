[← Behaviour](behaviour.md) · [Back to README](../README.md) · [Next: Testing →](testing.md)

# Development

How to build `herdr-tg` from source, run the checks, and find your way around
the tree. Users of the published plugin need none of this: `herdr plugin
install` downloads a prebuilt binary (see the README).

## Requirements

- Go `1.25.14` or newer (use a maintained security patch release)
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
2. Run `make lint`, then `make test`, and make sure CI is green on the parent commit.
3. Run `git tag -a vX.Y.Z -m vX.Y.Z`, then `git push origin vX.Y.Z`, without pushing
   `main` yet. The release workflow builds the five binaries and
   `checksums.txt` from the tagged commit; GoReleaser writes the release
   notes from the commit list.
4. Check that the release page lists all six assets, then run
   `make sign-release VERSION=X.Y.Z`:
   - It fetches the tag and downloads `checksums.txt`.
   - It writes `release.txt` (tag, the tag's commit, the checksums lines).
   - It signs it with the offline release key (`ssh-keygen -Y sign`, which
     asks for the passphrase).
   - It checks the signature against `scripts/signing/allowed_signers` and
     uploads `release.txt` and `release.txt.sig`.
   Until this step the Telegram updater shows the release as `unsigned`.
5. `sh scripts/verify-install.sh X.Y.Z all`. It clones the tag, so it works
   before `main` moves, and fails unless the install receipt says
   `signature verified`. Pass `--allow-unsigned` as the third argument only
   for releases made before signing started.
6. `git push origin main`.
7. Nothing else: the repository carries the GitHub topic `herdr-plugin`, so
   the [marketplace](https://herdr.dev/plugins/) card picks up the new
   version within 30 minutes.

Commits that touch only Go code are safe to push to `main` at any time:
installers keep getting the binary of the last release until `version`
moves.

### The release key

The release key is a dedicated `ssh-ed25519` key with a passphrase, kept
offline by the maintainer (`~/.ssh/herdr-tg-release` by default,
`HERDR_TG_SIGNING_KEY` overrides it). It is never stored in GitHub, so
neither a stolen GitHub session nor a changed workflow can produce a release
the updater accepts. It was created with:

```sh
ssh-keygen -t ed25519 -f ~/.ssh/herdr-tg-release -C herdr-tg-release
```

Its public half lives in two places that `TestReleaseSignersMatchScripts`
keeps identical:

- `scripts/signing/allowed_signers`, read by the install scripts;
- `releaseSigners` in `internal/adapters/github/releasekeys.go`, compiled
  into the binary and the only keys the updater trusts.

Rotation takes three releases, because a running binary trusts only the
keys it was built with:

1. Release N lists the new key next to the old one and is signed with the
   old key.
2. Release N+1 is signed with the new key.
3. A later release drops the old line.

If the key is lost but not leaked, follow the same steps; the old key must
still sign release N. If the old key is gone, installed copies can no longer
update through Telegram: they show `bad_signature` for every new release.
Users then reinstall with `herdr plugin install
permgps/herdr-telegram-agents`. If the key leaked, rotate at once and say so
in the release notes.

## The `dev` subcommand

The undocumented `dev` subcommand talks to the live Herdr socket and works only
inside a Herdr pane (`HERDR_ENV=1`):

```bash
bin/herdr-tg dev agents   # list agents Herdr currently tracks
bin/herdr-tg dev watch    # stream agent events until Ctrl-C
```

The socket path comes from `HERDR_SOCKET_PATH` and falls back to
`~/.config/herdr/herdr.sock`.

## Telegram update worker

The General options panel calls the release checker in a background bridge
job. `internal/adapters/github/` lists published releases, checks the exact
host asset and checksum entry, and reads the manifest at the selected tag.
The installation reader uses `herdr plugin list --plugin
permgps.telegram-agents --json`; a linked checkout is inspected with Git.
The check result creates an in-memory, five-minute Update intent bound to
the operator and panel. Pressing Update consumes and revalidates it.

`update.json` is an atomic state-dir journal. The daemon writes a queued job
and launches `update-worker <job-id>` from a copy of its executable in the
state dir. The worker takes `update.lock/`, backs up the old version, stops
the daemon if it was running, installs the selected tag, and starts the
binary at the new plugin root. The status control channel must report a
new pid, target version, `herdr=ok` and `telegram=ready` before success.
Only then does the worker edit the original General panel with the final
result. A failure attempts one guarded rollback. A stopped daemon stays
stopped. The worker never starts a second Telegram poller.

The updater is tested with `httptest`, fake runners and disposable directories.
The release verification script still checks published install assets; see
[Testing](testing.md#telegram-update-checks) for the update matrix.

## Layout

| Path | Purpose |
|------|---------|
| `cmd/herdr-tg/` | Binary entry point |
| `internal/domain/` | Agents, statuses, topics, mapping, commands, config, options, presence, secret redaction, doctor checks, events and the ports (standard library only) |
| `internal/app/` | Use cases: agent registry, reconciler, debounce, bridge (screens out, commands in), screen capture for `/screen all`, the options panel, presence and quiet mode, the topic sweep, the quota lines, doctor, setup wizard, supervisor, daemon loop |
| `internal/adapters/herdr/` | Herdr socket adapter: dialers (Unix socket or Win32 named pipe normalized from `HERDR_SOCKET_PATH`), one-shot calls, event stream, `herdr` CLI runner |
| `internal/adapters/github/` | Published release discovery, signed release statements (SSHSIG verification against the compiled-in keys in `releasekeys.go`), asset checksums and tagged manifest reads |
| `internal/adapters/telegram/` | Telegram Bot API adapter: bot, queue, formatting, icons, inbound updates, setup probe |
| `internal/adapters/state/` | `config.json`, `mapping.json`, `options.json`, `claude-usage.json` (the status line tap), pid and atomic update job stores |
| `internal/adapters/logging/` | JSON file logger with size-based rotation |
| `internal/adapters/system/` | `HERDR_*` environment, detached process spawn, update lock and installer, signals, the control channel (Unix socket or named pipe), the input idle source for presence (macOS, Windows) |
| `internal/cli/` | Subcommands behind the single binary, `usage-tap` (Claude Code status line tap) among them |
| `internal/compose/` | Composition root wiring adapters into the use cases |
| `internal/testkit/` | Fakes for every port and a fake Herdr socket server |
| `scripts/` | Import layering gate, cross-compile check, install scripts, version gate, install verification, `sign-release.sh` |
| `scripts/signing/` | `allowed_signers` (release keys for the install scripts) and the `ssh-keygen` self-test fixture |
| `.github/workflows/` | CI (lint, race tests, Windows tests) and the release workflow |
| `herdr-plugin.toml` | Plugin manifest |

Dependencies point inward (`cli` → `compose` → `app` → `domain`, adapters →
`domain`); the layering is enforced by `scripts/check-imports.sh` in
`make lint`. No test touches the network: the Herdr adapter is tested against
a fake socket server and the Telegram adapter against an in-process HTTP fake.

## Private sharing internals

`domain/sharing.go` defines recipient and grant records; `domain/access.go`
contains the capability policy. `app.Sharing` owns durable policy and cancellation
contexts. Queue admission checks the original actor, destination, session and
revision immediately before dispatch. Network calls do not hold the policy lock.
Owner revoke/suspend callbacks apply denial on the daemon loop before rendering
through the bridge. Async attachment and Git completions retain their original
revision and return to the shared bridge for the final check.

`SharePanel`, `PrivateReconciler`, `PrivateControl`, `PrivateOutput` and
`PrivateDashboard` separate owner administration from guest behavior. The
composition root wires a mandatory private redactor and exact-session reply
source. `DestinationTelegram` methods require explicit chat/topic or chat/message
addresses. Legacy owner methods remain wrappers for the configured group.

There is one Telegram client and long poller. The HTTP admission wrapper saves
first contacts before the library advances its polling offset. Retained messages
can register contacts, while pre-start mutating messages and old callbacks are
refused. This does not provide exactly-once agent effects across crashes.

The client gives every request 60 s (`httpTimeout` in
`internal/adapters/telegram/connect.go`) and asks Telegram to hold
`getUpdates` for 49 s (`pollTimeout` 50 s; the library subtracts one
second). The hold must stay well under the deadline: with 59 s against
60 s a held poll answered a little late died with `Client.Timeout exceeded
while awaiting headers`. At startup `Connect` retries the token check and
the icon pack (`StartRetry`) until they succeed, fail for good
(`isRetryable` false: 401, 400, 403, 404) or the daemon is stopped; the
control channel is up before the build, and a waiting daemon reports
`telegram=waiting attempt=N`, never `telegram=ready`, so the update
worker's health check keeps waiting. `BuildDaemon` pings Herdr first, so a
missing socket still fails the start at once, and opens the Herdr event
stream only after Telegram answered: nothing would read it during the wait.

Sharing audit logs contain IDs, revisions, actions and outcome flags. Keep new
logs free of tokens, message bodies, attachment names/content, transcript paths
and raw session IDs. Use the existing `LOG_LEVEL` setting for DEBUG diagnostics.

## See Also

- [Testing](testing.md): automated gates and the manual checklist before a release
- [README: Install](../README.md#install): how users get the prebuilt binary
- [Plugin updates](behaviour.md#plugin-updates): two-press flow and recovery
