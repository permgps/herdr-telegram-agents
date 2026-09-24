# Telegram Agents for Herdr

> One Telegram forum topic per live Herdr agent: status in the icon and messages both ways.

A [Herdr](https://herdr.dev) plugin for watching and controlling coding agents
from a Telegram forum group. Each live agent gets a topic. Questions and
completion posts arrive there; replies and command buttons go back to the
agent. The bot can also ring an operator in a private chat when an agent needs
an answer.

<img src="docs/images/herdr-agents.png" alt="Herdr Agents panel" width="900">
<img src="docs/images/telegram-topics.png" alt="Matching Telegram topics" width="900">

## Install

You need Herdr 0.7.5 or newer, a bot token from [@BotFather](https://t.me/BotFather),
and a Telegram supergroup with Topics enabled. The install step needs `sh` and
`curl` on macOS or Linux, or PowerShell 5.1+ on Windows.

```bash
herdr plugin install permgps/herdr-telegram-agents
```

Herdr downloads a checksum-verified release binary. Published targets are
macOS and Linux on amd64 and arm64, and Windows on amd64. The plugin needs no
Go toolchain on the machine where it is installed.

## Setup

1. Create a bot with @BotFather and a supergroup with Topics enabled.
2. In a Herdr pane, run `herdr plugin action invoke permgps.telegram-agents.setup`.
3. Enter the token in the popup, open its bot link, press **Start**, and add
   the bot to the group with **Manage topics**, **Delete messages**, and
   **Pin messages**. The account that selects the group becomes an operator.
4. Confirm the group in the popup. The daemon starts and later starts with
   Herdr automatically.
5. Mute the group if frequent topic icon notices are distracting. Agent
   questions can still ring in the bot's private chat.

## Use

Write in an agent's topic to prompt it. A short reply such as `y`, `n`, `1`,
`enter`, or `esc` answers a blocked agent; numbered questions also have
buttons. `/screen` shows the screen, `/keys` sends raw keys, and `/git` shows
allow-listed repository information. Photos, documents, voice notes, audio,
and video sent to a topic are saved in the plugin inbox and passed to the
agent as file paths.

In General, `/status` shows all agents, `/new` starts one, and `/options`
opens the settings panel. The panel controls syncing, notification behavior,
icons, redaction, and cleanup. The pinned dashboard shows the same agent
statuses. Operators can add read-only observers with `/observers`.

See [Commands](docs/commands.md) for every command and attachment rule, and
[Behavior](docs/behaviour.md) for sync, settings, access, and daemon details.

## Check for updates

Open `/options` in General and press **Check for updates**. The first press
checks published GitHub releases, including prereleases, and shows the newest
higher version. If the installation is eligible, press **Update** separately
to authorize installation. The panel shows progress and the final result.
There are no scheduled checks or unattended installations.

A managed GitHub installation is reinstalled at the exact release tag. A
clean locally linked checkout on `main` fast-forwards to the release commit
and runs the checksum-verified installer. An intentionally pinned managed
installation, a dirty or detached local checkout, or a source from another
repository shows a reason without an Update button. The previous version is
restored when a replacement fails to start; inspect `update.json` and
`daemon.log` if recovery itself fails.

See [Update behavior and recovery](docs/behaviour.md#plugin-updates) for
checks, state, and manual commands.

## Documentation

| Page | Contents |
|------|----------|
| [Commands](docs/commands.md) | Topic and General commands, posts, buttons, attachments |
| [Behavior](docs/behaviour.md) | Sync rules, settings, updates, access, state and logs |
| [Development](docs/development.md) | Source builds, release process, updater internals |
| [Testing](docs/testing.md) | Automated gates and manual release checks |

## License

[MIT](LICENSE).
