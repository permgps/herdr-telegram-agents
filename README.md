# Telegram Agents for Herdr

> One Telegram forum topic per live Herdr agent: status in the topic icon, messages both ways.

A [Herdr](https://herdr.dev) plugin that mirrors the Herdr **Agents** panel into a
Telegram forum supergroup, so you can watch and drive your coding agents
(Claude Code, Codex, Gemini, ...) from a phone. Every agent gets its own topic,
the topic icon follows the agent's status, its questions land in the topic with
a notification, and what you write there goes back to the agent. One command
installs it; no Go, no Node, nothing else on your machine.

<img src="docs/images/herdr-agents.png" alt="Herdr window with the agents panel: three agents and their statuses" width="900">

*The Herdr agents panel: three agents, one working, one idle, one waiting.*

<img src="docs/images/telegram-topics.png" alt="Telegram forum with one topic per Herdr agent; the open topic shows a Claude Code question" width="900">

*The same agents in Telegram: one topic each, the icon is the status, and the
open topic shows a Claude Code question you can answer from the phone.*

## What you get

- **A topic per agent**, named like the Agents panel row (`V3Jobs · claude`),
  created when the agent appears and reused after a restart.
- **Status at a glance**: the topic icon is ⚡ working, ✅ idle, ❓ blocked,
  🏆 done, 👀 unknown, 🏁 exited.
- **Questions come to you**: when an agent gets blocked on a question or an
  approval, the screen is posted with a notification; when it finishes, the
  tail is posted silently.
- **Answers go back**: plain text becomes a prompt, `y` / `n` / `1`..`9` /
  `enter` / `esc` answer dialogs, `/keys` sends raw keys.
- **Look at the screen** with `/screen`, or `/screen all` for everything the
  agent printed since your last message.
- **Claude Code commands** `/clear`, `/compact`, `/usage`, `/model` are typed
  into the agent and the result is posted back.
- **Rename or close** a topic in Telegram to rename or mute the agent in Herdr.
- **A control panel** in the General topic: `/status` with links to every
  agent, `/help`, daemon notices.
- **A daemon that looks after itself**: starts with Herdr, exits when Herdr
  is gone, heals topic drift on start and on `resync`.

Version `0.2.0`. macOS and Linux are verified end to end; Windows is built and
unit-tested on every change but has not been run against a real Herdr yet.

## Requirements

- Herdr `0.7.5` or newer
- A Telegram bot token from [@BotFather](https://t.me/BotFather)
- A Telegram supergroup with **Topics** enabled, where you can promote the bot
- `sh` and `curl` (macOS, Linux) or PowerShell 5.1+ (Windows) for the install step

## Install

```bash
herdr plugin install permgps/herdr-telegram-agents
```

Herdr clones the repository and runs the plugin's build step, which downloads
the release binary for your OS and architecture into `bin/`, verifies its
SHA-256 against the release checksums and makes it executable. Supported
targets: `darwin/amd64`, `darwin/arm64`, `linux/amd64`, `linux/arm64`,
`windows/amd64`. The plugin is listed on the
[Herdr marketplace](https://herdr.dev/plugins/); the command above is the one
the card shows.

Then run the setup below. Herdr shows the **Telegram Agents** actions listed
further down and runs `bin/herdr-tg startup` after every session restore.

If you download a binary with a browser instead, macOS marks it quarantined;
`xattr -d com.apple.quarantine bin/herdr-tg` clears that. Binaries fetched by
the install script carry no quarantine attribute.

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
   **Manage topics** and **Delete messages**. The person who picks the group
   becomes the operator. Adding the bot to a forum group by hand with those
   rights works as well.
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
| `control.sock` | state dir | the daemon's control channel for the stop, resync and status actions (a named pipe on Windows, so no file) |

Run the setup action again to reconfigure; it asks before overwriting.

## Talking to agents

When an agent turns **blocked** (a question or an approval dialog) the daemon
waits 1.5 s and posts the last 25 lines of the screen into its topic with a
notification. When it turns **done** the last 12 lines are posted silently. A
screen identical to the previous post for that agent is skipped. Agents that
are already blocked when the daemon starts are posted too.

When the blocked screen ends in a numbered dialog with two to five real
options (a Claude Code question, an approval prompt, a picker), the post
carries one button per option, `1️⃣ Yes`, `2️⃣ No, and tell Claude …`.
Pressing a button sends that number to the agent, exactly like replying `2`;
the button turns into `✅ 2 · …` and, if the agent asks a follow-up question,
it arrives with its own buttons. Buttons that no longer apply (you answered in
Herdr, the agent moved on, an older question) show a short notice instead of
acting. Claude Code's `Type something.` and `Chat about this` entries get no
button: reply with text, or with their digit, instead. In a multi-select
dialog a press toggles the option; send `enter` to submit.

Anything you write in a topic reaches the agent:

| You write | The agent gets |
|-----------|----------------|
| plain text | typed as a prompt and submitted (`agent.prompt`) |
| `y`, `n`, `yes`, `no`, `1`..`9`, `enter`, `ok`, `esc` while the agent is blocked | the matching key (`agent.send_keys`); in any other status these are prompts. Pressing a button under the question sends its number the same way |
| `/keys esc enter` | raw key names |
| `/screen` or `/screen 40` | the visible screen, or its last 40 lines (max 200) |
| `/screen all` | everything the agent printed since your last message (typed in Herdr or sent here); long output arrives as a `.txt` file |
| `/focus` | the pane is brought to the front in Herdr |
| `/clear`, `/compact [instructions]`, `/usage`, `/model [name]` | typed into the agent as its own Claude Code command; two seconds later the screen is posted as a quoted reply (`/usage` and a bare `/model` are closed with `esc` for you); only while the agent is idle |
| `/status` | `<emoji> <status> · <label> · pane <id>` |
| `/help` | the command list |

The four Claude Code commands are typed verbatim through `agent.prompt`, so
Claude Code runs them like keyboard input. `/clear` and `/model <name>` post
the last 12 lines afterwards, `/usage` and a bare `/model` post the panel
from its top rule down and then send `esc` so nothing stays open, `/compact`
posts nothing itself: the topic icon turns ⚡ while it runs and the usual
**done** post shows the summary. A command sent while the agent is working or
blocked is refused with a hint, because the text would land in the running
turn or in a dialog and the `esc` could interrupt it; Herdr's detection dips
out of **working** for a second or two while a tool runs, so a refusal can be
spurious, just send the command again. Agents of other kinds (Codex, Gemini)
get the same text as-is and the screen post shows how they reacted.

`/screen all` works from a history the daemon keeps in memory: while an agent
is **working** its screen is read about once a second and the lines that
scrolled up are appended; every time the agent starts working after a human
message (yours from Telegram, or one typed in Herdr) a mark is placed, and
`/screen all` returns what came after the last mark plus the current screen.
Herdr keeps no scrollback for Claude Code panes, so this is the only source.
Limits: the history starts empty when the daemon starts, is capped at 2000
lines per agent and is dropped when the agent exits; a burst larger than one
screen between two reads leaves a `…` gap; a prompt sent while the agent is
already working does not move the mark, and neither does a pause shorter than
5 s (Herdr's status detection dips out of **working** for a second or two
while an agent runs a tool). Output that fits in three messages is
posted as code blocks, anything longer as one `.txt` document.

Delivery is silent: the topic icon turning ⚡ within a few seconds shows the
agent took the prompt. A quoted `⚠️ ...` reply explains why a message did not
get through (agent gone, socket down). Messages in the topic of an
exited agent get `agent has exited`. Only the configured group and the operator
ids from setup are accepted; everything else is dropped and logged.

The **General** topic is the control panel: `/status` lists every live agent
with its status emoji and a link to its topic, `/help` shows the commands, and
the daemon posts silent notices there when it starts, stops, loses or regains
the **Manage topics** right, or gives up on the Herdr socket. Other messages in
General are ignored. The commands appear in Telegram's `/` menu for the group.

## Actions

| Action | What it does |
|--------|--------------|
| `Telegram Agents: setup` | Opens the setup popup |
| `Telegram Agents: start` | Starts the daemon if it is not running |
| `Telegram Agents: stop` | Asks the daemon to exit through its control socket (SIGTERM as the Unix fallback, then SIGKILL after 10 s) |
| `Telegram Agents: restart` | Stop followed by start |
| `Telegram Agents: status` | Reports whether the daemon runs, its pid and uptime, and the daemon's own line: version, live agents, dropped jobs and Herdr socket health |
| `Telegram Agents: resync` | Asks the running daemon to re-check every topic against the live agents (control socket, SIGHUP as the Unix fallback) |
| `Telegram Agents: logs` | Opens an overlay with the last 100 log lines and follows the file |

Every action reports its outcome as a Herdr notification. `stop`, `resync` and
`status` reach the daemon through a local control channel: a unix socket
(`control.sock` in the state dir) or a named pipe on Windows. A daemon from an
older build that does not answer still receives SIGTERM or SIGHUP on Unix and
is killed if it answers neither.

## How the sync behaves

Topic naming, the status icons, what happens when an agent exits or comes
back, renaming and closing topics by hand, the daemon's own exit rules and
where its logs live are described in [docs/behaviour.md](docs/behaviour.md).

## Upgrade

Herdr has no `plugin update`: reinstall to move to a newer version.

```bash
herdr plugin uninstall permgps.telegram-agents
herdr plugin install permgps/herdr-telegram-agents
```

The two commands name the same plugin in the two forms Herdr uses: `uninstall`
takes the plugin id, as printed by `herdr plugin list`, and `install` takes the
GitHub repository.

Your `config.json`, `mapping.json` and the Telegram topics survive: Herdr keeps
the plugin's config and state directories and never deletes their contents, so
the daemon picks up the same group and the same topics after the upgrade. The
manifest version always equals the release tag, so a checkout runs the binary
that tag was built from.

## Documentation

| Page | What it covers |
|------|----------------|
| [docs/behaviour.md](docs/behaviour.md) | Topic naming and icons, exit and resume rules, manual rename and close, logs and state |
| [docs/development.md](docs/development.md) | Building from source, `make` targets, publishing a release, the `dev` subcommand, the tree layout |
| [docs/testing.md](docs/testing.md) | Automated gates and the manual checklist run before a release |

## License

[MIT](LICENSE).
