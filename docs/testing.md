[← Development](development.md) · [Back to README](../README.md)

# Testing

What the machines check on every change, and what a human has to check before
a release. Tick the manual items with a date when you run them. The operator reported on
2026-09-25 that the remaining live checks passed; older partial and pending
notes below record the state at the time and are superseded by this report.

## Automated gates

| Command | What it covers |
|---------|----------------|
| `make lint` | gofmt, `go vet`, staticcheck, the import layering gate, and a build plus vet of all five release targets |
| `make test` | `go test -race ./...`: domain, use cases, both adapters, the CLI, against fakes and a fake Herdr socket server |
| CI (`.github/workflows/ci.yml`) | the two above on Ubuntu, `go test -race` on macOS, and `go test` on Windows, for every push to `main` and every pull request |
| Release (`.github/workflows/release.yml`) | on a `v*` tag: the module files must be tidy (`go mod tidy -diff`, nothing is rewritten at release time), the manifest version must equal the tag, tests pass, then GoReleaser (an exact version) publishes five binaries and `checksums.txt`. Every action in both workflows is pinned to a commit SHA, updated by hand |

No test touches the network or real time: the Herdr adapter talks to a fake
NDJSON socket server, the Telegram adapter to an in-process HTTP fake, and
anything that waits takes an injected clock.

The daemon regression suite also checks a failed first Herdr snapshot, resync
event delivery to every consumer, bridge saturation and cancellation, failed
mapping saves, retained topic references above 500 entries, and continuation
after the first 50 stale-topic deletions. The state adapter tests verify that
pending creation markers survive a mapping file round trip.

## Telegram update checks

The updater tests use `httptest` for the GitHub releases API, fake Herdr and
Telegram ports, fake command runners, and disposable state and checkout
directories. They cover semantic versions and prereleases, unpublished or
incomplete releases, exact checksums, local and managed eligibility, stale
and replayed buttons, one worker launch, journal recovery, install and
rollback phases, health timeouts, and one final edit of the General panel.
No unit test installs a plugin into the current Herdr session or uses the
public network. Signed releases are covered in two places:

- The SSHSIG verifier is tested against a fixture made by real `ssh-keygen`
  and against tampered, foreign-key, wrong-namespace and malformed
  signatures.
- `TestInstallScriptSignature` runs `scripts/install.sh` against an
  `httptest` release on Linux and macOS. It covers a signed release, a bad
  signature, an unsigned release, an approved checksum without a signature,
  an approved mismatch, a signed digest that disagrees with `checksums.txt`,
  a statement for another tag and a host without `ssh-keygen`.

Before releasing this feature, run the automated gates and these isolated
integration cases against a mock GitHub release and disposable Herdr data:

- [ ] No newer release: show the installed version, with no Update button or daemon mutation.
- [ ] Stable and prerelease candidates: choose the highest valid published version, never a downgrade.
- [ ] Managed source: install the exact tag and confirm the new manifest, binary, daemon pid, `version=…`, `herdr=ok`, and `telegram=ready`.
- [ ] Clean linked `main`: fast-forward to the release commit, verify the binary checksum, and retain the local link.
- [ ] Dirty, detached, pinned, or wrong-repository source: show a reason without an Update button.
- [ ] Rate limit, broken pagination, missing asset, bad checksum, or incompatible Herdr: show failure or blocker without installation.
- [ ] Duplicate, expired, wrong-chat, wrong-operator, and replaced-panel callbacks: launch at most one worker.
- [ ] Interrupted worker and failed replacement start: inspect `update.json`, verify one rollback attempt or a recoverable `stuck` state, and one final General panel result.
- [ ] Previously stopped daemon: keep it stopped after a successful file update.
- [ ] Windows fixture: hold the old `.exe` open, verify stop-before-replace and guarded rollback, then confirm the new daemon version and poller readiness.
- [ ] Signed release, managed source: after the update, `daemon.log` has an `install receipt` line with `signature=verified`. Record whether `approved_present` was true, which shows whether Herdr passes `HERDR_TG_EXPECTED_SHA256` to `[[build]]`, and note the Herdr version.
- [ ] Unsigned target (tag pushed, `make sign-release` not run yet): the panel shows `This release is not signed yet` without an Update button; after signing, **Check again** offers `· signed` and Update.
- [ ] Windows install of a signed release: `bin\install-receipt` says `signature verified`; record `ssh-keygen` version (`ssh -V`). On a host without OpenSSH it says `signature unverifiable` and the install still succeeds.

The existing Windows checklist below describes earlier plugin behavior. It
does not verify the new update worker until the Windows fixture above passes.

## Herdr capture compatibility (2026-09-23)

An isolated macOS/arm64 sandbox used the official [Herdr 0.9.1 binary](https://github.com/herdrdev/herdr/releases/download/v0.9.1/herdr-macos-aarch64), SHA-256 `5fc7a7e7adfaca56fa80aa89dcb025693357268dab8285b9ce2d08a2313c89de`. Its socket protocol was 22; the installed baseline was Herdr 0.7.5, protocol 17. The sandbox used a temporary session and working directory, and was stopped and removed after the checks.

The sandbox agent reported `working` immediately before the first two reads;
it was checked as idle before the final read:

| Read | Result |
|------|--------|
| `agent.read --source recent --lines 400` | Exit 1, API code `agent_not_idle`, zero text lines |
| `agent.read --source visible --lines 400` | Exit 0, 40 text lines |
| `agent.read --source recent --lines 400` after the agent became idle | Exit 0, 40 text lines |

The refusal message said that alternate-screen history could be captured only
while idle and suggested `--source visible`. The 0.7.5 and 0.9.1 schemas were
compared for all 15 socket methods and the global/per-pane events this plugin
uses. Their used request, response, and event fields matched; protocol 22 adds
capability fields ignored by the plugin and an unused `workspace.reordered`
event. The compatibility claim is limited to those used contracts.

The regression test exercised `recent L1–L20 → visible L4–L24 → recent
L1–L28`; the resulting history had no gap marker or duplicate lines. An older
prefix that is not confirmed in committed history still produces a gap marker.
The final gates passed on 2026-09-23: `gofmt`, `make lint` (including `go vet`,
staticcheck, import checks and all five cross-build targets), and `make test`
(`go test -race ./...`).

- [x] **Default-session live check (partial, 2026-09-23):** `herdr update
  --handoff` installed Herdr 0.9.1/protocol 22 (installed binary SHA-256
  matches the sandbox asset) and reported `live handoff complete`. All 32 pane
  IDs and seven agents remained; the active Codex pane kept shell PID 5283 and
  process PID 77380. The plugin daemon stayed at PID 95311 as the only daemon,
  and status reported `herdr=ok`, `agents=7`, `pager=on`. A read-only recent
  read of the already-working Codex pane exited 0. From handoff through
  2026-09-23 01:36:14 +05, logs contained no `agent_not_idle` or protocol
  mismatch warnings. One capture read failed with `server_unavailable` while
  the old server shut down, followed by one stream reset and one `screen
  history gap` warning. The agent remained working through a 30-second
  read-only wait, so a natural work/idle history transition is still pending;
  no test prompt or keys were sent.

## Install from a release

`scripts/verify-install.sh <version> [linux|macos|all] [--allow-unsigned]` replays what
`herdr plugin install` does on a machine with no toolchain. Linux runs two
throwaway `debian:bookworm-slim` containers (amd64 and arm64) that clone the
tag and run `scripts/install.sh`; macOS clones the tag into a temporary
directory and runs the same script with a `PATH` that has no Go. Nothing
outside the containers and that temporary directory is touched.
`HERDR_TG_BASE_URL` must be `https://`; a local snapshot served over http
also needs `HERDR_TG_ALLOW_INSECURE_BASE=1`, which the script passes through.
Each run must end with `signature verified` in `bin/install-receipt` unless
`--allow-unsigned` is given (releases before signing started).

- [ ] First signed release: `sh scripts/verify-install.sh <version> all` passes with `signature verified` on linux/amd64, linux/arm64 and macOS

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

- [x] **Setup wizard**: the setup action opens the popup, the token is accepted, the `t.me/<bot>?start=setup_<code>` link adds the bot to the group with **Manage topics**, **Delete messages** and **Pin messages**, and the daemon starts
- [x] **Topic per agent**: every live agent has a topic named `<workspace> · <agent>`; a new agent creates one within a few seconds
- [x] **Status icons**: ⚡ working, ✅ idle, ❓ blocked, 🏆 done, 👀 unknown, 🏁 on exit; the icon follows the agent within one debounce window
- [x] **Rename both ways**: renaming the tab in Herdr renames the topic; renaming the topic in Telegram renames the tab (or the custom agent name)
- [x] **Close and reopen**: closing a topic by hand mutes edits and posts for that agent; reopening refreshes the name and icon
- [x] **Blocked**: an agent waiting for an answer posts its screen, once, and answering with `y`, `1`, `esc` reaches the agent (the sound comes from the bot's chat while `Questions in the bot's chat` is on, see **Pager** below)
- [x] **Pager**: mute the group on the phone; the log shows `pager reachable` with one operator at daemon start; make this session's agent ask a question → no sound from the topic (the post is there, silent, with its buttons), one sound from the bot's private chat with `❓ <agent> is waiting for you`, the options and a link that opens the topic at the post; answer in the topic; the log has `screen posted … notify=false paged=true` and `pager sent`. Verified 2026-09-06, `v0.7.0-3-gf65bdde-dirty`, from the phone: the question this session asked at the end of the plan arrived in the bot's private chat with a sound and its link opened the topic at the post (thread 1152, message 1506); the log had `screen posted … notify=false paged=true` and `pager sent operators=[<operator id>] failed=0`
- [x] **Pager off**: `/options` → Posts → untick `Questions in the bot's chat`; the next question rings from the topic post and nothing arrives in the bot's chat; tick it again → `pager set` and a second `pager reachable` in the log
- [x] **Pager unreachable**: add a second operator id that never pressed Start to `config.json` (daemon stopped), start → `doctor` shows `! operator chat: the bot cannot write to operator <id> …` while the first operator still gets paged; with only such an id the daemon logs `pager unreachable: open the bot and press Start`, posts `⚠️ questions will ring in the topics …` into General, the `status` action ends with `pager=unreachable` and the next question rings from the topic; restore `config.json`
- [x] **Dashboard**: at daemon start a pinned message appears in General without a sound (`dashboard created … pinned=true` in the log, the "pinned a message" notice is deleted); a prompt to this session's agent moves its line to ⚡ within 2 s (`dashboard edited`); a minute later the line shows `· 1 min`; `/status` prints the same lines without the footer. Machine side verified 2026-09-06, `v0.7.0-3-gf65bdde-dirty`: `telegram rights … pin_messages=true`, `pager reachable operators=1 reachable=[<operator id>] reason=start`, `dashboard created message_id=1493 pinned=true agents=5`, `mapping.json` holds `"dashboard_message_id": 1493`; the phone side (no sound, the pin bar in General, the moving durations) is still to look at
- [x] **Dashboard off and on**: `/options` → Sync → untick `Dashboard in General` → the message is unpinned and deleted (`dashboard removed`); tick it → a new one is created and pinned
- [x] **Dashboard restart**: restart the daemon → the footer read `⏹ stopped HH:MM` for a moment, the same message is re-pinned (`dashboard re-pinned`) and refreshed, no second message; delete the message by hand → the next status change recreates it (`dashboard recreated: message gone`). Restart half verified 2026-09-06, `v0.7.0-3-gf65bdde-dirty`: `dashboard stopped message_id=1493` at the stop, `dashboard re-pinned message_id=1493` at the next start and no second `dashboard created`; the hand-deleted case is still to run
- [x] **Pin right missing**: revoke **Pin messages** from the bot, delete the dashboard message and restart → `doctor` warns `pin messages no; the dashboard in General stays unpinned without Pin messages`, the log has `dashboard not pinned` once and the message keeps updating unpinned; grant the right again
- [x] **Buttons**: a Claude Code question with three options arrives with buttons `1️⃣` to `3️⃣`; pressing one shows `sent: <n>`, the keyboard becomes `✅ <n> · …`, the transcript shows the answer; pressing the ✅ button says `already answered`
- [x] **Approval dialog buttons**: a tool-approval prompt (session without auto mode) arrives with `Yes` / `Yes, and don't ask again …` / `No, and tell Claude …` buttons and `1️⃣` lets the tool run
- [x] **Follow-up question**: after answering the first of two questions in one AskUserQuestion call the second arrives with its own buttons within a couple of seconds; note how a multi-select dialog renders and whether a press toggles
- [x] **Stale buttons**: answer in Herdr and press the old button: `agent is not waiting anymore` and the keyboard disappears; an agent exiting with buttons pending loses them on exit
- [x] **Button access control**: a press from a non-operator account answers `not allowed` and sends nothing
- [x] **Type something button**: a Claude Code question with `Type something.` arrives with a `✏️ Type something` row; the press answers `now send the text`, the keyboard says `✏️ waiting for your text` and a quoted `✏️ …: send the text as your next message` opens the reply box on the phone; a reply `y` is typed as text (the agent shows it in its input, no key press), the keyboard becomes `✅ ✏️ · y` and the message gets 👀. Record in this line what Herdr reports as the status after `Type something.` is selected (blocked, idle or unknown). 2026-09-06, `v0.6.1-7` working tree: passed from the phone with the text `Тест` (`typing wait opened`, `typed text delivered` 16 s later); Herdr kept reporting `blocked` after the entry was selected
- [x] **Multi-select toggles**: an AskUserQuestion with `multiSelect` arrives with its option buttons plus `✔ Submit`; a press answers `toggled: <n>` and within two seconds the same keyboard shows the tick moved (no new post); `Submit` collapses it to `✅ submitted` and the follow-up question arrives with its own buttons. Record here the glyph pair Claude Code draws before the options. 2026-09-06, `v0.6.1-6-g60989ee`: Claude Code draws ASCII `[ ]` checkboxes, indents the option descriptions by two spaces and lists `[ ] Type something` without a period; the post arrived with the three toggles, `✔ Submit` and `✏️ Type something` (log `screen posted … buttons=5`). The phone pass the same evening found three more things, all fixed: a toggled option is drawn `[✔]` (U+2714, no variation selector) and the parser now takes any `[<one or two runes>]`; Herdr called the pane `working` for seconds after each keystroke, so the redraw and later presses verify the dialog on the screen; and `enter` toggles the row under the cursor instead of submitting, so `✔ Submit` walks the `❯` cursor to the bare `Submit` row with arrows (`down` ×4 from option 1 in a three-option dialog) before `enter`. With those in place: toggles redrawn in text and keyboard, `Submit` answered `✅ submitted`, and Claude Code's `Submit answers` / `Cancel` review arrived with two buttons
- [x] **Photo to inbox**: a photo with the caption `look` sent to a topic → a file under `<state dir>/inbox/` named `<date>-<message id>-photo.jpg`, the agent's input shows `look`, a blank line and the absolute path, the message gets 👀 then 👌 (2026-09-06: the daemon router dropped `TopicAttachment` until the live check, now covered by a daemon test; Claude Code shows the prompt as `[Image #N]<caption>` and needs the extra `enter` the daemon sends 1.5 s later, log `inbox prompt submitted`; ✅ is not an allowed Telegram reaction, `REACTION_INVALID`, so the done reaction became 👌)
- [x] **Album**: three photos sent together → one prompt with three paths, one per line (2026-09-06: two photos, `album settled parts=2`, one prompt)
- [x] **File too big**: a 25 MB document → `⚠️ file too big: 25 MB > 20 MB`, nothing saved (2026-09-06: a 25.5 MB CSV, `attachment refused reason=too_big`)
- [x] **Inbox off**: `/options` → Inbox → untick `Accept files` → a photo answers `⚠️ inbox is off (/options → Inbox)`; tick it again
- [x] **git status and diff**: in this repository's topic `/git status` → a quoted code block starting `## <branch>`; `/git diff` with a change larger than a message → a `herdr_tg-diff-<hhmmss>.patch` file with the caption `git diff HEAD · N lines`; `/git log 3` → three lines; `/git push` → `usage: /git status | diff [staged] | log [N]` (2026-09-06: status 5 lines inline, diff of 501 lines as a document, log 3 lines, push refused)
- [x] **git outside a repository**: `/git status` in the topic of an agent whose pane runs outside a repository (`/new` in a scratch workspace) → `⚠️ not a git repository`
- [x] **Done**: a finished agent posts its tail silently
- [x] **Reactions**: with `React to prompts` at its default (off) a plain prompt gets no reaction and the log has `reaction skipped` at debug level; tick it in `/options` → Posts → a plain prompt gets 👀 within a second and 👌 when the turn ends (done, or 5 s of idle); a `y` reply to a dialog gets none
- [x] **Question delay**: `/options` → Posts → `Question delay` → `10s`; answer a Claude Code question in Herdr within 10 s → no post, log `screen skipped … reason=not_blocked`; leave the next one unanswered → posted after ~11.5 s with buttons, log `capture compared`; set it back to `Off`
- [x] **Short done posts**: `Skip short done posts` → `30s`; a one-word prompt answered in 5 s → no done post, log `reason=short_turn`; a longer task posts as usual; set it back to `Off`
  - [x] `Done post` = `Reply` in `/options`, finish a turn: the agent's last message arrives as monospace text, the log has `reply posted` with the transcript path
  - [x] `Done post` = `Formatted`, finish a turn whose reply has a list and inline code: bullets and `<code>` render, a fenced block stays one block
  - [ ] **Default Formatted**: stop the daemon, move `options.json` aside, restart: a Claude Code done post arrives rendered (log `reply posted`), a shell or Gemini pane's done post arrives as the screen (log `reply source unavailable … mode=formatted`); restore `options.json` and restart
  - [x] an agent without a supported reply source, or a Claude pane whose directory has no `~/.claude/projects/<slug>/`, still posts the screen and the log shows `reply source unavailable`
  - [x] **Summary line**: a done post from this session's agent ends with `⏱ … · fable-5-1 · ✏️ N files · ↑ N tokens` outside the code block in all three `Done post` modes (`Screen`, `Reply`, `Formatted`); the log has `turn meta` with the same model, `turn_ms`, `files` and `output_tokens`, and `screen posted` / `reply posted … footer=true`; `/options` → Posts → untick `Turn summary line` → the next done post has no line; tick it. Machine side 2026-09-07, `v0.9.1-3-g6e1e07f-dirty`: built, restarted (pid 74558, daemon start 13:32:31 after the binary at 13:32:23, `daemon started` in the log at 13:32:32); the first done post of this session's own pane after the restart (13:34:14, message 1721) logged `screen posted … status=done … footer=true`, so the line went out in `Screen` mode; `turn meta` and `sendMessage … folded/footer` are debug lines and the daemon runs at info, so they and the phone side are still to look at
  - [x] **Folded reply**: `Done post` = `Reply`, finish a turn whose reply has more than 20 lines: the post arrives collapsed with an arrow and the summary line under it, opening it shows the whole text (log `reply posted … fold=20`, `sendMessage … folded=true footer=true`); `Fold long replies after` = `Off` → the same reply arrives open (`fold=0`); back to `20 lines`; a `Screen` done post is never folded
  - [x] **Codex summary line**: a Codex pane's done post in `Screen` mode ends with the summary line (duration, model, output tokens; no file count) read from its rollout. While its turn is still running, or if the rollout cannot be read, the post ends as before and the log has `turn meta unavailable` at debug, nothing at info
- [x] **Claude `/screen`, `/screen 40`, `/screen all`**: the visible screen, a tail, and everything since your last message (a `.txt` document when long)
- [x] **OpenCode reply flow**: OpenCode 2.0.18 with the Herdr integration supplied distinct exact sessions for two panes in one directory. The operator confirmed a clean bare `/screen` reply in each topic and a literal `/screen 40` tail. A matching `--continue` restart produced a new clean reply; the live reader rejected an old session key after a change and read the current key. Real Reply and Formatted completion posts succeeded, and the Screen/quiet defaults were restored. During a `sleep 90` tool call, `/screen` showed current terminal progress. With a temporary shell permission rule, Herdr reported blocked and `/screen` showed the OpenCode approval dialog for `pwd`; the dialog was canceled and the rule removed. A synthetic long Markdown message sent through the real Telegram Gateway delivered exactly five silent parts to the test topic with `… (+35190 chars)` on the last part. Automated tests cover the OpenCode-to-outbound cap; the long text was not generated by OpenCode. With the local OpenCode CLI symlink temporarily removed, bare `/screen` fell back to one terminal post and the symlink was restored. The daemon's INFO event recorded the post without an export error or session identifier. Empty/stale export and DEBUG privacy behavior are covered by automated tests.
- [ ] **Complete OpenCode replies**: `Done post` = `Formatted`. A long OpenCode session whose export is over 64 KiB gets its whole last answer, not the screen (log `reply posted`, no `reply source unavailable`). A turn with several tool steps posts only its final answer, once; the debug log may show `reply pending, retry scheduled` before it. The state directory's `tmp/` holds no `opencode-export-*.json` afterwards
- [ ] **Antigravity replies**: `Done post` = `Formatted`, an `agy` pane with Herdr's Antigravity integration (`agent_session` reported). A finished turn with tool steps posts only its final answer, whole (log `reply posted` with `source="agy transcript"`, no `reply source unavailable`); a done event in the middle of the turn posts nothing until the turn ends (debug `agy reply pending`). A bare `/screen` on the idle pane posts the answer rendered, `/screen 20` the screen. The turn summary line shows the duration only. Record the `agy` version
- [ ] **Pi replies**: `Done post` = `Formatted`, a `pi` pane in the TUI with Herdr's Pi integration installed (`herdr integration install pi`). `herdr agent list` shows the pane's `agent_session` with `agent` `pi` and `kind` `path` (record the shape only, not the value). A finished turn with tool steps posts only its final answer, whole, with the summary line (duration, model, files, tokens; log `reply posted` with `source="pi session"`, no `reply source unavailable`); a done event in the middle of the turn posts nothing until the turn ends (debug `pi reply pending`). After `/tree` back to an earlier point and a new prompt there, the post is the new branch's answer. `/new` in Pi: the next done post reads the new session. A bare `/screen` on the idle pane posts the answer rendered, `/screen 20` the screen. Record the Pi and Herdr versions
- [ ] **Muse replies**: `Done post` = `Formatted`, a `muse` pane on Herdr 0.9.x (protocol 22, `pane.process_info`). Record the Muse Code version and the OS, and which of `~/.local/share/muse`, `~/.muse` and `~/Library/Application Support/muse` holds `runtime/muse/sessions/` (names only, no file content). A finished run with commentary and tool work posts only its final answer, whole, with the summary line showing the duration (log `reply posted` with `source="muse session log"`, no `reply source unavailable`); record the `phase` value of the debug `muse reply found` line. A done event in the middle of a run posts nothing until it ends (debug `muse reply pending`). Esc in the middle of a run → the screen. Two Muse panes in directories with the same basename (`~/a/api`, `~/b/api`) each get their own answer. `/new` in a pane, then a run → the new session's answer (debug `muse session matched … chosen_by=newest_log`). A bare `/screen` on the idle pane posts the answer rendered, `/screen 20` the screen. If no answer is ever found, record the debug `muse session matched` counts (`runtime_files`, `candidates`, `pids`) for the issue.
- [ ] **Dashboard quota footer**: with Codex used in the last week, the dashboard and `/status` end with `🔑 Codex …` whose share and reset match `/status` inside Codex (log `quota source ready provider=Codex`). The doctor action shows `claude quota: not set up (optional)` with the tap command; put it in front of the status line command in `~/.claude/settings.json`, let Claude answer once → `claude-usage.json` appears in the state dir (mode 0600, two windows and `observed_at` only), the status line looks as before, and within a minute the dashboard gains `🔑 Claude 5h … · 7d …` matching `/usage` in Claude Code; doctor now says `from the status line, seen HH:MM`. `/options` → Sync → untick `Quota in the dashboard` → the lines leave the dashboard within 2 s; tick it again. Record the Claude Code and Codex versions
- [x] **Prompts**: plain text in a topic is typed into the agent and the icon turns ⚡
- [x] **`/keys`, `/focus`, `/status`, `/help`** in a topic
- [x] **Claude Code commands**: `/clear`, `/compact`, `/usage`, `/model` on an idle agent; `/usage` and a bare `/model` come back as a panel and leave no overlay open; a command sent while the agent works is refused with a hint
- [ ] **Pickers on non-Claude agents**: on an idle Codex agent `/model` posts the picker with the line `picker left open: …` under it and the picker stays open in Herdr (log `command picker kept`); `/keys down`, `/keys enter` choose a model; `/model` again, then a plain `hello` → `⚠️ the /model picker may still be open …` and nothing is typed (log `prompt held back for open picker`); `hello` again → typed into the picker as before; `/stop` closes it. Codex `/usage` opens its menu and behaves the same. On OpenCode `/model` and `/models` open the model picker the same way; `/models` on a Claude Code or Codex agent is refused with a hint and nothing is typed. Note the Herdr status a pane shows while its picker is open (`idle` or `blocked`). Claude Code `/usage` and a bare `/model` still close themselves and set no hold. `/help` and the `/` menu describe the new behaviour
- [x] **Stop and interrupt**: `/stop` on a working agent → the turn stops in Herdr and the reply is `⏹ sent esc`; `/interrupt` → `⛔ sent ctrl+c` and Claude Code shows the interrupt; `/stop` in General → `agent commands live in the agent's topic`
- [x] **Close**: `/close` → `Close <label>? …` with `Yes, close` / `No`; `No` → `not closed`; `/close` again, `Yes` → `closing <label> …`, the topic gets 🏁 and closes, the tab is gone in Herdr
- [x] **New agent**: `/new <workspace>` in General → `starting claude in <workspace> …`, then `started claude in <workspace> (pane …)` and a topic `<workspace> · claude` appears; `/new` → the workspace list; `/new zzz` → `no workspace named "zzz"` with the list; `/new <workspace> codex` starts Codex
- [x] **General topic**: `/status` lists every agent with a link and its status duration, `/help` works, other messages are ignored
- [x] **Options panel**: `/options` in General shows the groups; Sync → untick the checkbox → toast `saved`, button `☐`; a status change in Herdr makes no topic edit, `/status` starts with `🔇`, the `status` action prints `sync=off`; tick it again → `resync requested` in the log and the icons repaint (2026-09-03, v0.2.0-4-g608197f, from the phone)
- [x] **Icon picker**: Appearance → `working` → a grid of eight emoji per row over two pages renders and taps well on the phone (fallback: six per row); pick 🔥 → the working topic's icon changes; pick ✅ → toast `used by idle`; `↺ Reset to defaults` → ⚡ is back; `✖ Close` → summary text without buttons; a second `/options` strips the old panel's keyboard (2026-09-03, v0.2.0-4-g608197f, from the phone)
- [x] **Options survive a restart**: restart the daemon with sync off → the started notice ends with `(sync off, see /options)` and the log has the warning; switch it on → resync (2026-09-03, v0.2.0-4-g608197f, from the phone)
- [x] **Quiet at the desk**: `/options` → Quiet → tick `Quiet while at the desk` (off by default) and `Away after` → `1m`; type in Herdr and make this session's agent change status → no topic edit, the log has `quiet on: operator at the desk` and `reconcile deferred: operator at the desk (quiet)`, `/status` starts with `🔕 quiet: you are at the desk`; a question arrives without a sound; leave the Mac alone for a minute → the icons repaint in one burst (one `editForumTopic` per changed agent), the question is posted again with a sound, the log has `quiet off: operator away, catching up` and `catch-up done`; touch the mouse and leave again → no second sound and no edit; set `Away after` back to `3m`. Partly verified 2026-09-03 (`v0.4.0-5-gc9a29d6-dirty`, restart while typing): `quiet on: operator at the desk` at start, the first reconcile deferred, `status` ends with `quiet=on`, done posts logged with `notify=false`, status changes logged as `topic edit skipped … reason=quiet` with no `editForumTopic`; the leaving half (catch-up burst, one sound, flapping) is still to run
- [x] **`/away` and `/here`**: while at the desk `/away 5m` in General answers `🏃 away until HH:MM …` and the catch-up runs at once, `/status` shows `🏃 away (manual) until HH:MM`, the `status` action ends with `quiet=away-manual`; `/here` answers `🖥 presence is automatic again: at the desk, quiet on`; `/away` in a topic answers `presence commands live in General`
- [x] **Held posts**: `Screen posts` → `Held`; a question while at the desk posts nothing; leaving posts it with a sound; set it back to `Silent`
- [x] **Quiet off**: untick `Quiet while at the desk` while at the desk → the pending icon edits go out at once and `/away` answers `quiet mode is off (/options → Quiet)`; leave it unticked, that is the default
- [x] **Access control**: a message from another chat is dropped with `reason=foreign_chat` at debug; a message from an account that is neither operator nor observer is dropped and logged once at info as `unknown sender from_id=<id> name=… username=… thread_id=…`, later ones within ten minutes at debug only
- [x] **Icon notice on both clients**: change this session's agent's status and watch Telegram Desktop and the phone: both show the new icon while the "changed the topic icon" notice is visible and still after the daemon deletes it 20 s later (log `service message delete scheduled … delay_ms=20000`, then `service message deleted`); `/options` → Topics → `Keep icon notices for` → `Keep` → the next notice stays (log `service message kept`); set it back to `20s` (log `notice delay set seconds=20`). Machine side 2026-09-07, `v0.8.1-3-gcdfc05b-dirty` (pid 17121): the start logged `telegram notice delay set delay_ms=20000` and `notice delay set seconds=20 keep=false`; the deletion lines are debug, so the Desktop and phone halves and the `Keep` round trip are still to run with `LOG_LEVEL=debug`
- [x] **Trimmed frame**: a done post from this session's agent ends on the reply text, with no `❯` row, no `─` rules, no status line and no `⏵⏵` / `? for shortcuts` hint (log `chrome cut … lines=5`); `/screen` likewise; a question from this agent still arrives with its buttons and the post ends on the dialog's footer; `/options` → Posts → untick `Trim the input frame` → `/screen` shows the frame again; tick it. Machine side 2026-09-07, `v0.8.1-3-gcdfc05b-dirty`: the first two done posts after the restart logged `screen posted … status=done lines=6` where the same panes posted `lines=12` before (12 lines read, the five-line frame and a blank cut); `chrome cut` is a debug line, so the phone side, `/screen` and the question with buttons are still to look at
- [x] **Observers**: from a second Telegram account write into a topic → nothing reaches the agent and the log has `unknown sender` with that id; `/observers` in General lists it under `Seen recently, not allowed`; `/observers add <id>` → `👁 <id> is an observer now …`, `config.json` holds `"observer_ids": [<id>]`, `doctor` shows `1 observer` in the config line; the second account's `/status` in General is answered, its topic message is dropped with `reason=observer`, its button press answers `not allowed`; `/observers remove <id>` → `<id> is no longer an observer` and the key leaves `config.json`. Machine side 2026-09-07, `v0.8.1-3-gcdfc05b-dirty`: the start logged `telegram gateway started … operators=1 observers=0` and `commands registered … count=18` (`/observers` in the menu); the two-account half needs a second account and is still to run
- [x] **Actions**: `start`, `stop`, `restart`, `status`, `resync`, `logs` each report their outcome as a Herdr notification; `status` shows the daemon's line with agents, dropped jobs, socket health, `sync=on|off`, `cleanup=<n>d|off`, `quiet=on|away|away-manual|off` and `pager=on|off|unreachable`
- [x] **Secret redaction**: an agent prints a fake key (`echo sk-test1234567890abcdefghijklmnop`); `/screen` shows `sk-…mnop`, the log has `secrets redacted kinds="openai=1"` and no value; `/options` → Privacy → untick `Redact secrets` → `/screen` shows the raw text; tick it again
- [x] **Topic cleanup**: with the daemon stopped, set one exited closed entry's `updated_at` in `mapping.json` to 40 days ago, start the daemon → the topic is gone from Telegram and from the file (2026-09-03, `v0.3.0-1-gfb4d386-dirty`, topic `My · claude`, thread 5: `topic deleted … already_gone=false`, `stale topics sweep … deleted=1`; the bot held **Delete messages**). A first attempt hit an entry whose topic had already been removed by hand: Telegram answered `Bad Request: TOPIC_ID_INVALID`, the adapter mapped it to "gone" and the entry was forgotten the same way
- [x] **Topic cleanup from the panel**: with `Delete closed topics after` at `Off`, an exited entry older than 7 days survives a `resync` (no `mapping pruned` line); pick `7d` → `sweep requested` and `stale topics sweep` in the log; set the option back to `30 days`
- [x] **Doctor**: the doctor action opens the overlay with eight ✓ lines (`operator chat` among them, and `pin messages yes` in the group line) and `8 ok, 0 warnings, 0 failures` (2026-09-03, `v0.3.0-1-gfb4d386-dirty`, seven lines then: the pane run directly and the action from Herdr both green against the real group and Herdr 0.7.5); stop the daemon → `daemon` shows `!` not running; start it again; a throwaway copy of `config.json` with a wrong token shows `✗ telegram: token rejected` (restore the file)
- [x] **Send test message**: the action posts a 🔔 message into General and reports `send-test: delivered to General (message N)` (2026-09-03, `v0.3.0-1-gfb4d386-dirty`, message 980); with the daemon stopped it still works (by design: the action never talks to the daemon)

- [ ] Break one agent's reply reader (e.g. point its store at an unreadable
  file): after several done posts the topic gets one ⚠️ notice; repair it
  and the next readable reply clears the streak.
- [ ] A long OpenCode session (export over 16 MiB) posts the whole answer
  through the store read, not the 12-line screen.
- [ ] Without `sqlite3`, a normal session (export below the cap) still posts
  the whole answer through the export fallback.
- [ ] Plain text to a blocked Claude Code agent: at a question with `Type
  something` the text arrives as the answer (log `blocked text waits for
  the dialog entry`, then `blocked text typed into the dialog entry`; the
  buttons become `✅ ✏️ · …`); at a permission prompt nothing reaches the
  pane and the reply says the message was not sent. Repeat once from a
  private recipient. The screens were read in tmux on 2026-10-09 (Claude
  Code 2.1.295), not yet through Herdr and Telegram.

## Stable topics across Herdr restarts (v0.10.3)

The automated tests cover the session digest, version-1 mapping migration,
one-to-one candidate selection, restart event ordering, inbound and callback
routing, and the no-extra-topic-call path. Run these phone and existing-state
checks before publishing; record the date and only redacted thread/count
evidence here.

Automated gates for this change passed on 2026-09-23: `gofmt`, `go vet ./...`,
`staticcheck ./...`, `go test -race ./...`, `make lint` (including the import
gate and all five release cross-builds), and
`sh scripts/check-version.sh v0.10.3`. The controlled live restart check was pending on 2026-09-23: `herdr --help` lists no restart-only command, and the available server
stop or Herdr update actions risk interrupting the active agent sessions (the
update also installs a new Herdr version). No release-specific thread/count,
reply-routing, or phone icon/notice evidence had been collected at that point.
The operator reported the live checks passed on 2026-09-25; no thread IDs or
other detailed evidence were supplied for this record.

- [x] **Same-session restart:** record the live thread ids and mapping entry
  count, restart Herdr without starting a new agent session, and confirm the
  same threads and count remain. Check that no topic create/close notice
  appears, then reply in a retained topic and press one of its existing
  buttons.
- [x] **Version-1 mapping with duplicate generations:** restore a copy of an
  old mapping that has no `cwd` or `agent_kind` and has both a live and older
  closed entry for one pane/name. Start the daemon and confirm it adopts the
  sole eligible live thread, preserves the older topic, and writes mapping
  version 2. Repeat with two eligible candidates and no sole live candidate
  (both live or both closed); confirm it logs an ambiguity and does not attach
  either old thread arbitrarily.
- [x] **Muted topic:** close a live topic by hand, restart Herdr with the same
  session, and confirm the topic remains muted without an icon edit or screen
  post. Reopen it by hand and confirm the current name/icon returns.
- [x] **New session:** start or resume an agent so Herdr reports a different
  non-empty `agent_session`; confirm it receives a new topic and the previous
  topic is marked exited and closed. If the agent has no session identity,
  record whether the documented unique directory/name fallback applied.
- [x] **Phone icon and notice:** on Telegram Desktop and a phone, change the
  retained agent's status. Confirm the icon updates while the service notice
  is visible and stays current after the configured 20-second deletion. Also
  verify `/options` → Topics → `Keep icon notices for` → `Keep`, then restore
  `20s`.
- [ ] **Release assets:** after the tag workflow finishes, confirm these six
  assets exist: `herdr-tg_darwin_amd64`, `herdr-tg_darwin_arm64`,
  `herdr-tg_linux_amd64`, `herdr-tg_linux_arm64`,
  `herdr-tg_windows_amd64.exe`, and `checksums.txt`. Then run
  `sh scripts/verify-install.sh 0.10.3 all`.

## Resilience

- [x] **Herdr restart**: quit and reopen Herdr; the daemon logs `herdr stream reset` and reconciles within one reconcile interval, with no duplicate topics
- [x] **Flapping socket**: repeatedly interrupting the socket makes the retry delay climb (`herdr stream reset` with a rising `attempt` and `retry_ms`), and a stable connection resets it
- [x] **Socket gone**: stop Herdr for more than 60 s; the daemon posts the "socket unreachable" notice to General and exits cleanly
- [x] **Network loss**: disable Wi-Fi for two minutes; the Telegram poller recovers and queued edits go out afterwards
- [ ] **Start without a network**: with Wi-Fi off, start the daemon; Herdr shows one `waiting for Telegram` notice, `status` answers `telegram=waiting attempt=N` with a rising N, `daemon.log` has `telegram unreachable, retrying`; Wi-Fi on → `reached Telegram after …` notice and the topics sync
- [ ] **Stop while waiting**: with Wi-Fi off, start the daemon and run `stop` before Wi-Fi returns; the daemon exits 0 with `daemon stopped before Telegram answered` in the log and no `could not start` notice (macOS and Windows: on Windows the stop goes through the control channel, not a kill)
- [ ] **Long poll**: after a day of polling, `daemon.log` has no `Client.Timeout exceeded while awaiting headers` for `getUpdates` (the hold is 49 s against a 60 s deadline)
- [x] **Rights**: revoke **Manage topics**, see the General notice and paused edits, restore it and see edits resume
- [x] **Bot removed**: removing the bot from the group ends the daemon with a Herdr notification
- [x] **Many agents**: with a dozen or more agents changing status at once, the log shows no `bridge dropped jobs` warning and `status` reports `dropped=0`
- [x] **Control channel**: `status` and `resync` answer while the daemon runs, `stop` ends it, and `daemon.log` shows `control listening` and `control command` entries rather than signals (2026-09-03: status and resync verified on the live daemon)

## Windows

The operator reported that these checks passed against a real Herdr on Windows
on 2026-09-25. The code is also built and unit-tested on a Windows CI runner.

- [x] `herdr plugin install` runs `scripts/install.ps1` and produces `bin\herdr-tg.exe` with a verified checksum
- [x] The startup hook spawns the daemon detached (`DETACHED_PROCESS`), and closing the launching terminal leaves it running
- [x] The Herdr named pipe from `HERDR_SOCKET_PATH` is reachable: agents appear and events flow (`HERDR_SOCKET_PATH` filesystem marker is normalized into the `\\.\pipe\` namespace)
- [x] `stop` and `resync` reach the daemon through the control pipe (`\\.\pipe\herdr-tg-<hash>`), with no POSIX signals involved
- [x] `status` reports the daemon's line, `quiet=` and `pager=` included
- [x] Presence works with `Quiet while at the desk` ticked: typing keeps `quiet=on` (the `GetLastInputInfo` idle source), leaving the machine for `Away after` minutes turns it to `away` and the topics catch up
- [x] A daemon that is not listening is reported as "not listening on its control channel" and `stop` escalates to a kill
- [ ] `scripts/install.ps1` run from a checkout under a folder with brackets (for example `plugin[1]\`) installs `bin\herdr-tg.exe` with a verified checksum, and a Telegram update of that checkout succeeds (issue #19)

## Linux desktop

Quiet mode's Linux idle source (`internal/adapters/system/idle_chain.go`) is
unit-tested on every OS against a fake environment; these checks need a real
desktop. Record the distribution, desktop, session type
(`echo $XDG_SESSION_TYPE`), and the `gdbus` and `xprintidle` versions.

- [ ] **GNOME on Wayland**: tick `Quiet while at the desk`, `Away after` → `1m`; the log has `input idle source source=mutter`; typing keeps `status` at `quiet=on`; a minute without input turns it to `away` and the topics catch up
- [ ] **X11 with `xprintidle`** (GNOME on Xorg, or another X11 desktop without gdbus on PATH): the same, with `source=mutter` on GNOME and `source=xprintidle` elsewhere
- [ ] **Failure keeps the verdict**: while at the desk, make the source fail (rename `xprintidle` away on an X11 desktop without Mutter): one `presence sample failed, keeping the previous verdict` warning, `quiet=on` stays; restore it → `presence source recovered`
- [ ] **No session**: start the daemon from an SSH login without `DISPLAY` and `WAYLAND_DISPLAY`: one `presence: no input idle source on this machine` warning, `/away` answers `quiet mode has no input idle source on this machine`

## Private sharing acceptance

Status on 2026-09-30: race tests, vet, staticcheck, formatting/import checks and
all five platform build/vet targets passed. An approved recipient completed
registration and an owner confirmed Read access. The recipient confirmed on a
phone that the agent/service topics are visible and `/screen` works. Exact client
version was not supplied.

Status on 2026-10-07: the owner ran the remaining checks below on real phone and
Desktop clients and reported them passed (#21). Client versions were not
recorded. Record client versions and outcomes only, without tokens or private
conversation content.

- [x] Enable private Topics in BotFather; check that disabled capability refuses
  a grant while owner-group operation continues.
- [x] Send a fresh private message, find the numeric recipient in `/share`, and
  grant Read. Check that its contents were not forwarded and no old output was
  replayed. Repeat with duplicate display names and more than ten contacts.
- [x] On phone and Desktop, verify the first topic, icon and access card. Change
  name/icon repeatedly; confirm propagation before notices disappear after
  20 seconds. Repeat with Keep, and verify pin and creation notices.
- [x] Check `/agents`, its buttons, scoped command menu and navigation fallback.
  A second recipient must see only their own grants. Remove the last grant and
  verify that agent commands disappear from the menu.
- [x] Confirm Read cannot prompt, press control buttons, upload to the agent or
  invoke `/usage`. Test Control, repository-read, close confirmation and focus
  separately. Unknown commands and global administration remain refused.
- [x] Exercise prompts, attachments, albums, blocked questions, multi-select and
  text entry while the owner and another controller also act.
- [x] Check one blocked alert, silent done output, local silent/pause/resume,
  owner desk presence, global sync-off and actual client sound/mute behavior.
- [x] Revoke during an attachment download and a queued response. Try old
  buttons again. Check the neutral access notice and retained history.
- [x] Block/unblock the bot, exit/resume the same session, replace the session
  in its pane, and restart with incomplete identity. No unapproved reassociation
  or duplicate topic should occur.
- [x] Test a missing topic and an ambiguous creation result. Verify explicit
  repair; check the chat for orphan topics before retrying.
- [x] Verify owner setup, observers, pager, options/update panel and shutdown
  both with sharing unavailable and with active private mirrors.

## Audit follow-up (2026-10-09)

Use one Read and one Control recipient. Record client versions and outcomes
only.

- [ ] As the Read recipient, send `/agents` 20 times. The Control recipient's
  overview and dialog buttons still work, and the Read recipient's newest
  overview buttons work.
- [ ] Grant Read on an OpenCode agent that has already answered. A bare
  `/screen` shows the screen, not that earlier answer. After the next turn,
  `/screen` shows the new answer.
- [ ] As the owner, send `/model` to an OpenCode pane. The Control recipient's
  next plain message is held once with the picker warning and the resend goes
  through. Repeat with the recipient sending `/keys esc` first: nothing is held
  afterwards, not even for the owner.
- [ ] Turn `/options → Inbox` off and send a file as the Control recipient: it
  is refused without a download. Turn it back on with `Largest file` at 5 MB
  and send a larger file: refused with `file too big`.
- [ ] During a multi-step OpenCode tool turn, the recipient's done post shows
  only the final answer.
- [ ] Turn Wi-Fi off for 15 minutes with the daemon running: `daemon.log` has
  one `telegram polling error` warning, then at most one more with
  `suppressed=…`.

## Blocked agents on Herdr 0.9.3 (issue #37)

Herdr 0.9.3 or later. Record the Herdr version and outcomes only.

- [ ] A Claude Code agent asks a question with a `Type something.` entry:
  press ✏️ and send a two-line answer. It arrives in the dialog on one line,
  is submitted once, and the message gets 👀. `daemon.log` has
  `prompt refused at dialog, typing into pane` with `lines=2`.
- [ ] A Muse agent waits at a question: send a plain answer. It arrives and
  is submitted.
- [ ] Send a photo with a caption to a blocked agent: caption and path
  arrive on one line, with no second `enter` after the dialog closes.
- [ ] Send `/clear` to a Claude Code agent waiting at a dialog: it is
  refused with a hint and nothing is typed into the dialog.

## See Also

- [Development](development.md): building from source, make targets and the tree layout
- [Plugin updates](behaviour.md#plugin-updates): checks, installation, and recovery
