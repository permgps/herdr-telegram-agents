# Telegram Agents for Herdr

> One Telegram forum topic per live Herdr agent: status in the topic name and icon, messages both ways.

A [Herdr](https://herdr.dev) plugin that mirrors the Herdr **Agents** panel into a
Telegram forum supergroup. Every coding agent Herdr detects (Claude Code, Codex,
Gemini, ...) gets its own topic; the topic name carries a status emoji, the topic
icon follows the status, and messages written in a topic are delivered to the
agent as prompts. The goal is a remote control surface for your agents from a
phone, installed with one command and no extra toolchain.

## Status

Early development. Done so far:

- **Project skeleton** — single Go binary `herdr-tg`, plugin manifest, `make` targets.
- **Herdr adapter** — socket client for protocol 17 (`agent.list`, `agent.read`,
  `agent.prompt`, `agent.send_keys`, `agent.rename`, `notification.show`) and a
  reconnecting `events.subscribe` stream.
- **Telegram adapter** — forum topic lifecycle, rate-limited call queue with 429
  back-off, message chunking, status icons, inbound update filtering.

Not there yet: setup wizard, background daemon, agent-to-topic sync, release
binaries. Until those land the plugin only exposes a `version` action and the
developer diagnostics below.

## Requirements

- Herdr `0.7.5` or newer
- Go `1.25` or newer (development builds only; releases will ship prebuilt binaries)
- `staticcheck` in `$HOME/go/bin` for `make lint`

## Build and test

```bash
make build   # bin/herdr-tg for the host platform
make test    # go test -race ./...
make lint    # gofmt, go vet, staticcheck, import layering gate, cross-compile check
```

`make crosscheck` alone builds and vets darwin/amd64, darwin/arm64, linux/amd64,
linux/arm64 and windows/amd64.

## Link the plugin into Herdr

```bash
make build
herdr plugin link /path/to/herdr-telegram-agents
```

Herdr then shows the action **Telegram Agents: version**, which runs
`bin/herdr-tg version`.

## Developer diagnostics

The `dev` subcommand talks to the live Herdr socket and works only inside a
Herdr pane (`HERDR_ENV=1`):

```bash
bin/herdr-tg dev agents   # list agents Herdr currently tracks
bin/herdr-tg dev watch    # stream agent events until Ctrl-C
```

Set `LOG_LEVEL=debug` to see every socket call. The socket path comes from
`HERDR_SOCKET_PATH` and falls back to `~/.config/herdr/herdr.sock`.

## Layout

| Path | Purpose |
|------|---------|
| `cmd/herdr-tg/` | Binary entry point |
| `internal/domain/` | Agents, statuses, topics, events and the gateway ports (standard library only) |
| `internal/adapters/herdr/` | Herdr socket adapter: dialers, one-shot calls, event stream |
| `internal/adapters/telegram/` | Telegram Bot API adapter: bot, queue, formatting, icons, inbound updates |
| `internal/cli/` | Subcommands behind the single binary |
| `internal/compose/` | Wiring of adapters |
| `internal/testkit/` | Fake Herdr socket server for tests |
| `scripts/` | Import layering gate and cross-compile check |
| `herdr-plugin.toml` | Plugin manifest |

Dependencies point inward (`cli` → `compose` → adapters → `domain`); the
layering is enforced by `scripts/check-imports.sh` in `make lint`. No test
touches the network: the Herdr adapter is tested against a fake socket server
and the Telegram adapter against an in-process HTTP fake.

## License

Not chosen yet; a `LICENSE` file will be added before the first release.
