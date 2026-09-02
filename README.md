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
- **Setup wizard** — token check, discovery of the forum group where the bot was
  promoted, operator capture, `config.json` under Herdr's plugin config dir.
- **Daemon lifecycle** — detached daemon started from the `[[startup]]` hook, pid
  file, `start` / `stop` / `restart` / `status` / `resync` / `logs` actions, JSON
  logs with rotation, automatic exit when the Herdr socket disappears.
- **Agent to topic sync** — one topic per agent, renamed on label or status change
  (debounced), closed with a 🏁 marker when the agent exits, drift healed on
  start and on `resync`.

Not there yet: messages from agents into topics, prompts from topics back to
agents, release binaries, Windows daemon control.

## Requirements

- Herdr `0.7.5` or newer
- A Telegram bot token from [@BotFather](https://t.me/BotFather)
- A Telegram supergroup with **Topics** enabled, where you can promote the bot
- Go `1.25` or newer (development builds only; releases will ship prebuilt binaries)
- `staticcheck` in `$HOME/go/bin` for `make lint`

## Build and link

```bash
make build
herdr plugin link /path/to/herdr-telegram-agents
```

Herdr then shows the **Telegram Agents** actions listed below and runs
`bin/herdr-tg startup` after every session restore.

## Setup

1. Create a bot with @BotFather and copy its token.
2. Create a supergroup and enable **Topics** in its settings (this makes it a
   forum). You must be its owner or an administrator who can add admins.
3. In Herdr run the action **Telegram Agents: setup** (from a Herdr pane:
   `herdr plugin action invoke permgps.telegram-agents.setup`). A popup asks
   for the token (typed visibly; the popup closes when setup ends) and prints
   a `https://t.me/<bot>?start=setup` link.
4. Open the link, press **Start** and tap **Choose group**. Telegram lists your
   forum groups and adds the bot to the one you pick as an administrator with
   **Manage topics**. The person who picks the group becomes the operator.
   Adding the bot to a forum group by hand with that right works as well.
5. Back in the popup confirm the group. The wizard saves `config.json` and
   starts the daemon. From now on the daemon starts automatically with Herdr
   while a configuration exists.

What gets stored:

| File | Location | Content |
|------|----------|---------|
| `config.json` | Herdr plugin config dir (`HERDR_PLUGIN_CONFIG_DIR`), mode 0600 | bot token, chat id and title, operator ids, log level |
| `mapping.json` | Herdr plugin state dir (`HERDR_PLUGIN_STATE_DIR`) | agent to topic mapping, pruned after 7 days |
| `daemon.pid` | state dir | pid of the running daemon |
| `daemon.log`, `daemon.log.1`, `daemon.log.2` | state dir | JSON log, rotated at 5 MiB |
| `daemon.err.log` | state dir | stderr of the last daemon start |

Run the setup action again to reconfigure; it asks before overwriting.

## Actions

| Action | What it does |
|--------|--------------|
| `Telegram Agents: setup` | Opens the setup popup |
| `Telegram Agents: start` | Starts the daemon if it is not running |
| `Telegram Agents: stop` | Asks the daemon to exit (SIGTERM, then SIGKILL after 10 s) |
| `Telegram Agents: restart` | Stop followed by start |
| `Telegram Agents: status` | Reports whether the daemon runs, its pid and uptime |
| `Telegram Agents: resync` | Asks the running daemon to re-check every topic against the live agents (SIGHUP) |
| `Telegram Agents: logs` | Opens an overlay with the last 100 log lines and follows the file |

Every action reports its outcome as a Herdr notification. `stop` and `resync`
use POSIX signals and report "not available on this platform" on Windows until
the Windows milestone.

## How the sync behaves

- A topic is named `<status emoji> <agent label>`: ⚙️ working, 💤 idle,
  ❓ blocked, ✅ done, ❔ unknown, 🏁 exited. The topic icon follows the status
  when the group's icon pack has a matching emoji.
- Agents are identified by pane and terminal id; a topic is created the first
  time an agent appears and reused after a restart.
- When an agent's pane closes the topic is renamed with 🏁 and closed. Topics
  of agents that vanished while the daemon was down are closed on the next
  start.
- The daemon exits by itself when the Herdr socket is gone for 60 s, when the
  bot token is rejected, when another process polls the same bot, or when the
  bot is removed from the group. Losing **Manage topics** only pauses edits
  until the right is granted again.

## Logs and state

`LOG_LEVEL=debug|info|warn|error` in Herdr's environment overrides the level
saved in `config.json` (default `info`). The daemon writes JSON lines to
`daemon.log` in the state dir; the logs action renders them as
`15:04:05 INFO message key=value`. Delete `mapping.json` while the daemon is
stopped to forget every topic; the next start creates fresh ones and leaves the
old topics untouched.

## Development

```bash
make build   # bin/herdr-tg for the host platform
make test    # go test -race ./...
make lint    # gofmt, go vet, staticcheck, import layering gate, cross-compile check
```

`make crosscheck` alone builds and vets darwin/amd64, darwin/arm64, linux/amd64,
linux/arm64 and windows/amd64.

The undocumented `dev` subcommand talks to the live Herdr socket and works only
inside a Herdr pane (`HERDR_ENV=1`):

```bash
bin/herdr-tg dev agents   # list agents Herdr currently tracks
bin/herdr-tg dev watch    # stream agent events until Ctrl-C
```

The socket path comes from `HERDR_SOCKET_PATH` and falls back to
`~/.config/herdr/herdr.sock`.

### Layout

| Path | Purpose |
|------|---------|
| `cmd/herdr-tg/` | Binary entry point |
| `internal/domain/` | Agents, statuses, topics, mapping, config, events and the ports (standard library only) |
| `internal/app/` | Use cases: agent registry, reconciler, debounce, setup wizard, supervisor, daemon loop |
| `internal/adapters/herdr/` | Herdr socket adapter: dialers, one-shot calls, event stream, `herdr` CLI runner |
| `internal/adapters/telegram/` | Telegram Bot API adapter: bot, queue, formatting, icons, inbound updates, setup probe |
| `internal/adapters/state/` | `config.json`, `mapping.json` and pid file stores |
| `internal/adapters/logging/` | JSON file logger with size-based rotation |
| `internal/adapters/system/` | `HERDR_*` environment, detached process spawn, signals |
| `internal/cli/` | Subcommands behind the single binary |
| `internal/compose/` | Composition root wiring adapters into the use cases |
| `internal/testkit/` | Fakes for every port and a fake Herdr socket server |
| `scripts/` | Import layering gate and cross-compile check |
| `herdr-plugin.toml` | Plugin manifest |

Dependencies point inward (`cli` → `compose` → `app` → `domain`, adapters →
`domain`); the layering is enforced by `scripts/check-imports.sh` in
`make lint`. No test touches the network: the Herdr adapter is tested against
a fake socket server and the Telegram adapter against an in-process HTTP fake.

## License

Not chosen yet; a `LICENSE` file will be added before the first release.
