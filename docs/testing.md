[← Development](development.md) · [Back to README](../README.md)

# Testing

What the machines check on every change, and what a human has to check before
a release. Tick the manual items with a date when you run them.

## Automated gates

| Command | What it covers |
|---------|----------------|
| `make lint` | gofmt, `go vet`, staticcheck, the import layering gate, and a build plus vet of all five release targets |
| `make test` | `go test -race ./...`: domain, use cases, both adapters, the CLI, against fakes and a fake Herdr socket server |
| CI (`.github/workflows/ci.yml`) | the two above on Ubuntu, `go test -race` on macOS, and `go test` on Windows, for every push to `main` and every pull request |
| Release (`.github/workflows/release.yml`) | on a `v*` tag: the manifest version must equal the tag, tests pass, then GoReleaser publishes five binaries and `checksums.txt` |

No test touches the network or real time: the Herdr adapter talks to a fake
NDJSON socket server, the Telegram adapter to an in-process HTTP fake, and
anything that waits takes an injected clock.

## Install from a release

`scripts/verify-install.sh <version> [linux|macos|all]` replays what
`herdr plugin install` does on a machine with no toolchain. Linux runs two
throwaway `debian:bookworm-slim` containers (amd64 and arm64) that clone the
tag and run `scripts/install.sh`; macOS clones the tag into a temporary
directory and runs the same script with a `PATH` that has no Go. Nothing
outside the containers and that temporary directory is touched.

- [x] `sh scripts/verify-install.sh <version> linux` prints `verify: linux/amd64 ok` and `verify: linux/arm64 ok` (2026-09-03, v0.1.0, v0.1.1, v0.2.0, v0.3.0, v0.4.0 and v0.5.0)
- [x] `herdr plugin install permgps/herdr-telegram-agents -y` in a `debian:bookworm-slim` container with Herdr installed from `herdr.dev/install.sh` and no Go: the preview lists 7 actions, 2 panes and 2 build commands, `herdr plugin list` shows the plugin enabled, and the managed binary reports its version (2026-09-03, v0.2.0 with Herdr 0.8.2 on linux/arm64)
- [x] `sh scripts/verify-install.sh <version> macos` prints the version line and `verify: macos ok` (2026-09-03, v0.1.0, v0.1.1, v0.3.0, v0.4.0 and v0.5.0)
- [x] A corrupted `checksums.txt` makes `scripts/install.sh` exit non-zero with `checksum mismatch` and leave no binary behind (2026-09-03, against a local snapshot)
- [x] The release page lists `herdr-tg_darwin_amd64`, `herdr-tg_darwin_arm64`, `herdr-tg_linux_amd64`, `herdr-tg_linux_arm64`, `herdr-tg_windows_amd64.exe` and `checksums.txt` (2026-09-03, v0.1.0, v0.1.1, v0.4.0 and v0.5.0)

Installing the published plugin over a development checkout on your own
machine is reversible; do it in this order so you end up where you started:

- [x] `herdr plugin unlink permgps.telegram-agents` (2026-09-03, v0.1.1)
- [x] `herdr plugin install permgps/herdr-telegram-agents` succeeds without a Go toolchain on `PATH` (2026-09-03, v0.1.1: the managed copy under `~/.config/herdr/plugins/github/` reports `herdr-tg 0.1.1 darwin/arm64`)
- [x] The daemon starts with the existing configuration (the config and state directories are untouched by the install) and the actions work (2026-09-03, v0.1.1: `status` reported `version=0.1.1 agents=3 dropped=0 herdr=ok`, no topic was created or renamed)
- [x] `herdr plugin uninstall permgps.telegram-agents`, then `herdr plugin link <checkout>` and `make build` to return to development (2026-09-03, v0.1.1: `config.json` byte-identical afterwards)

## Marketplace

The card on [herdr.dev/plugins](https://herdr.dev/plugins/) is built from the
GitHub topic `herdr-plugin` and the manifest on `main`; the index refreshes
every 30 minutes, so a release becomes visible without any further step.

- [x] The index entry shows the current manifest version and the GitHub repository description (2026-09-03, v0.1.1; `assets.herdr.dev/plugins/index.json` generated at 10:01Z listed `Telegram Agents 0.1.1` with `headCommit` 45911c9 about 15 minutes after the topic was added; the page itself renders that index in the browser)

## Herdr and Telegram end-to-end

Needs a real Herdr session with at least two agents and the configured
Telegram group on a phone.

- [ ] **Setup wizard**: the setup action opens the popup, the token is accepted, the `t.me/<bot>?start=setup` link adds the bot to the group with **Manage topics** and **Delete messages**, and the daemon starts
- [ ] **Topic per agent**: every live agent has a topic named `<workspace> · <agent>`; a new agent creates one within a few seconds
- [ ] **Status icons**: ⚡ working, ✅ idle, ❓ blocked, 🏆 done, 👀 unknown, 🏁 on exit; the icon follows the agent within one debounce window
- [ ] **Rename both ways**: renaming the tab in Herdr renames the topic; renaming the topic in Telegram renames the tab (or the custom agent name)
- [ ] **Close and reopen**: closing a topic by hand mutes edits and posts for that agent; reopening refreshes the name and icon
- [ ] **Blocked**: an agent waiting for an answer posts its screen with a notification, once, and answering with `y`, `1`, `esc` reaches the agent
- [ ] **Buttons**: a Claude Code question with three options arrives with buttons `1️⃣` to `3️⃣`; pressing one shows `sent: <n>`, the keyboard becomes `✅ <n> · …`, the transcript shows the answer; pressing the ✅ button says `already answered`
- [ ] **Approval dialog buttons**: a tool-approval prompt (session without auto mode) arrives with `Yes` / `Yes, and don't ask again …` / `No, and tell Claude …` buttons and `1️⃣` lets the tool run
- [ ] **Follow-up question**: after answering the first of two questions in one AskUserQuestion call the second arrives with its own buttons within a couple of seconds; note how a multi-select dialog renders and whether a press toggles
- [ ] **Stale buttons**: answer in Herdr and press the old button: `agent is not waiting anymore` and the keyboard disappears; an agent exiting with buttons pending loses them on exit
- [ ] **Button access control**: a press from a non-operator account answers `not allowed` and sends nothing
- [ ] **Done**: a finished agent posts its tail silently
  - [ ] `Done post` = `Reply` in `/options`, finish a turn: the agent's last message arrives as monospace text, the log has `reply posted` with the transcript path
  - [ ] `Done post` = `Formatted`, finish a turn whose reply has a list and inline code: bullets and `<code>` render, a fenced block stays one block
  - [ ] a non-Claude agent, or a Claude pane whose directory has no `~/.claude/projects/<slug>/`, still posts the screen and the log shows `reply source unavailable`
- [ ] **`/screen`, `/screen 40`, `/screen all`**: the visible screen, a tail, and everything since your last message (a `.txt` document when long)
- [ ] **Prompts**: plain text in a topic is typed into the agent and the icon turns ⚡
- [ ] **`/keys`, `/focus`, `/status`, `/help`** in a topic
- [ ] **Claude Code commands**: `/clear`, `/compact`, `/usage`, `/model` on an idle agent; `/usage` and a bare `/model` come back as a panel and leave no overlay open; a command sent while the agent works is refused with a hint
- [ ] **General topic**: `/status` lists every agent with a link, `/help` works, other messages are ignored
- [x] **Options panel**: `/options` in General shows the groups; Sync → untick the checkbox → toast `saved`, button `☐`; a status change in Herdr makes no topic edit, `/status` starts with `🔇`, the `status` action prints `sync=off`; tick it again → `resync requested` in the log and the icons repaint (2026-09-03, v0.2.0-4-g608197f, from the phone)
- [x] **Icon picker**: Appearance → `working` → a grid of eight emoji per row over two pages renders and taps well on the phone (fallback: six per row); pick 🔥 → the working topic's icon changes; pick ✅ → toast `used by idle`; `↺ Reset to defaults` → ⚡ is back; `✖ Close` → summary text without buttons; a second `/options` strips the old panel's keyboard (2026-09-03, v0.2.0-4-g608197f, from the phone)
- [x] **Options survive a restart**: restart the daemon with sync off → the started notice ends with `(sync off, see /options)` and the log has the warning; switch it on → resync (2026-09-03, v0.2.0-4-g608197f, from the phone)
- [ ] **Quiet at the desk**: `/options` → Quiet → `Away after` → `1m`; type in Herdr and make this session's agent change status → no topic edit, the log has `quiet on: operator at the desk` and `reconcile deferred: operator at the desk (quiet)`, `/status` starts with `🔕 quiet: you are at the desk`; a question arrives without a sound; leave the Mac alone for a minute → the icons repaint in one burst (one `editForumTopic` per changed agent), the question is posted again with a sound, the log has `quiet off: operator away, catching up` and `catch-up done`; touch the mouse and leave again → no second sound and no edit; set `Away after` back to `3m`. Partly verified 2026-09-03 (`v0.4.0-5-gc9a29d6-dirty`, restart while typing): `quiet on: operator at the desk` at start, the first reconcile deferred, `status` ends with `quiet=on`, done posts logged with `notify=false`, status changes logged as `topic edit skipped … reason=quiet` with no `editForumTopic`; the leaving half (catch-up burst, one sound, flapping) is still to run
- [ ] **`/away` and `/here`**: while at the desk `/away 5m` in General answers `🏃 away until HH:MM …` and the catch-up runs at once, `/status` shows `🏃 away (manual) until HH:MM`, the `status` action ends with `quiet=away-manual`; `/here` answers `🖥 presence is automatic again: at the desk, quiet on`; `/away` in a topic answers `presence commands live in General`
- [ ] **Held posts**: `Screen posts` → `Held`; a question while at the desk posts nothing; leaving posts it with a sound; set it back to `Silent`
- [ ] **Quiet off**: untick `Quiet while at the desk` while at the desk → the pending icon edits go out at once and `/away` answers `quiet mode is off (/options → Quiet)`; tick it again
- [ ] **Access control**: a message from a non-operator, or in another chat, is ignored and logged
- [ ] **Actions**: `start`, `stop`, `restart`, `status`, `resync`, `logs` each report their outcome as a Herdr notification; `status` shows the daemon's line with agents, dropped jobs, socket health, `sync=on|off`, `cleanup=<n>d|off` and `quiet=on|away|away-manual|off`
- [ ] **Secret redaction**: an agent prints a fake key (`echo sk-test1234567890abcdefghijklmnop`); `/screen` shows `sk-…mnop`, the log has `secrets redacted kinds="openai=1"` and no value; `/options` → Privacy → untick `Redact secrets` → `/screen` shows the raw text; tick it again
- [x] **Topic cleanup**: with the daemon stopped, set one exited closed entry's `updated_at` in `mapping.json` to 40 days ago, start the daemon → the topic is gone from Telegram and from the file (2026-09-03, `v0.3.0-1-gfb4d386-dirty`, topic `My · claude`, thread 5: `topic deleted … already_gone=false`, `stale topics sweep … deleted=1`; the bot held **Delete messages**). A first attempt hit an entry whose topic had already been removed by hand: Telegram answered `Bad Request: TOPIC_ID_INVALID`, the adapter mapped it to "gone" and the entry was forgotten the same way
- [ ] **Topic cleanup from the panel**: with `Delete closed topics after` at `Off`, an exited entry older than 7 days survives a `resync` (no `mapping pruned` line); pick `7d` → `sweep requested` and `stale topics sweep` in the log; set the option back to `30 days`
- [ ] **Doctor**: the doctor action opens the overlay with seven ✓ lines and `7 ok, 0 warnings, 0 failures` (2026-09-03, `v0.3.0-1-gfb4d386-dirty`: the pane run directly and the action from Herdr both green against the real group and Herdr 0.7.5); stop the daemon → `daemon` shows `!` not running; start it again; a throwaway copy of `config.json` with a wrong token shows `✗ telegram: token rejected` (restore the file)
- [x] **Send test message**: the action posts a 🔔 message into General and reports `send-test: delivered to General (message N)` (2026-09-03, `v0.3.0-1-gfb4d386-dirty`, message 980); with the daemon stopped it still works (by design: the action never talks to the daemon)

## Resilience

- [ ] **Herdr restart**: quit and reopen Herdr; the daemon logs `herdr stream reset` and reconciles within one reconcile interval, with no duplicate topics
- [ ] **Flapping socket**: repeatedly interrupting the socket makes the retry delay climb (`herdr stream reset` with a rising `attempt` and `retry_ms`), and a stable connection resets it
- [ ] **Socket gone**: stop Herdr for more than 60 s; the daemon posts the "socket unreachable" notice to General and exits cleanly
- [ ] **Network loss**: disable Wi-Fi for two minutes; the Telegram poller recovers and queued edits go out afterwards
- [ ] **Rights**: revoke **Manage topics**, see the General notice and paused edits, restore it and see edits resume
- [ ] **Bot removed**: removing the bot from the group ends the daemon with a Herdr notification
- [ ] **Many agents**: with a dozen or more agents changing status at once, the log shows no `bridge dropped jobs` warning and `status` reports `dropped=0`
- [x] **Control channel**: `status` and `resync` answer while the daemon runs, `stop` ends it, and `daemon.log` shows `control listening` and `control command` entries rather than signals (2026-09-03: status and resync verified on the live daemon)

## Windows (unverified)

Nothing here has been run against a real Herdr on Windows yet; the code is
built and unit-tested on a Windows CI runner only.

- [ ] `herdr plugin install` runs `scripts/install.ps1` and produces `bin\herdr-tg.exe` with a verified checksum
- [ ] The startup hook spawns the daemon detached (`DETACHED_PROCESS`), and closing the launching terminal leaves it running
- [ ] The Herdr named pipe from `HERDR_SOCKET_PATH` is reachable: agents appear and events flow
- [ ] `stop` and `resync` reach the daemon through the control pipe (`\\.\pipe\herdr-tg-<hash>`), with no POSIX signals involved
- [ ] `status` reports the daemon's line, `quiet=` included
- [ ] Presence works: typing keeps `quiet=on` (the `GetLastInputInfo` idle source), leaving the machine for `Away after` minutes turns it to `away` and the topics catch up
- [ ] A daemon that is not listening is reported as "not listening on its control channel" and `stop` escalates to a kill

## See Also

- [Development](development.md): building from source, make targets and the tree layout
- [README: Upgrade](../README.md#upgrade): reinstall-based updates and what survives them
