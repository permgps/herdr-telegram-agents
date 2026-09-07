[Back to README](../README.md) · [Next: Behaviour →](behaviour.md)

# Talking to agents

What the daemon posts into an agent's topic, what you can write back, and
what the General topic is for. The rules the mirror itself follows (topic
names, icons, exit and resume, the options) are in [behaviour.md](behaviour.md).

## What gets posted

When an agent turns **blocked** (a question or an approval dialog) the daemon
waits 1.5 s and posts the last 25 lines of the screen into its topic. With
`Questions in the bot's chat` on (the default) that post is silent and the
bot sends you, in your private chat with it, `❓ <agent> is waiting for you`
with the dialog's options (or the last six screen lines) and a link to the
post, with a sound; with it off the topic post itself rings. Either way you
answer in the topic. See [Silence the group](behaviour.md#silence-the-group)
for why. When it turns **done** the topic gets the last 12 lines of the
screen, or the agent's last reply when `Done post` in `/options` says so (see
[Done posts](behaviour.md#done-posts)); under it, one line with the turn's
duration, model, edited files and output tokens from the Claude Code
transcript (`Turn summary line`), and a reply longer than `Fold long replies
after` arrives collapsed behind an arrow. Every screen post ends on the agent's
last line: Claude Code's input frame at the bottom (the `─` rules with the
empty `❯` row, the status line and the mode hint) is cut while `Trim the
input frame` in `/options` → Posts is on, which is the default; a dialog and
its options are never cut, and a screen without the frame passes through
unchanged (see [Done posts](behaviour.md#done-posts)). A post identical to
the previous one for that agent is skipped. Agents that
are already blocked when the daemon starts are posted too. Two options of
the Posts group trim this: `Question delay` waits N more seconds and posts
the better of two captures, or nothing when you answered in Herdr meanwhile;
`Skip short done posts` drops the done post of a turn shorter than N seconds
(see [Turns and reactions](behaviour.md#turns-and-reactions)).

When the blocked screen ends in a numbered dialog with two to five real
options (a Claude Code question, an approval prompt, a picker), the post
carries one button per option, `1️⃣ Yes`, `2️⃣ No, and tell Claude …`.
Pressing a button sends that number to the agent, exactly like replying `2`;
the button turns into `✅ 2 · …` and, if the agent asks a follow-up question,
it arrives with its own buttons. Buttons that no longer apply (you answered in
Herdr, the agent moved on, an older question) show a short notice instead of
acting. Claude Code's `Type something.` and `Chat about this` entries get no
button: reply with text, or with their digit, instead. In a multi-select
dialog a press toggles the option and the post is redrawn with the ticks;
`✔ Submit` submits it. The button rules
in full are under [Questions and buttons](behaviour.md#questions-and-buttons).

While you are at the machine, quiet mode changes the sound of these posts or
holds them until you leave; see
[Quiet while at the desk](behaviour.md#quiet-while-at-the-desk).

## Commands in a topic

Anything you write in a topic reaches the agent:

| You write | The agent gets |
|-----------|----------------|
| plain text | typed as a prompt and submitted (`agent.prompt`) |
| `y`, `n`, `yes`, `no`, `1`..`9`, `enter`, `ok`, `esc` while the agent is blocked | the matching key (`agent.send_keys`); in any other status these are prompts. Pressing a button under the question sends its number the same way |
| `/keys esc enter` | raw key names |
| `/screen` or `/screen 40` | the visible screen, or its last 40 lines (max 200); the input frame is cut afterwards, so an idle Claude Code pane may answer with fewer than 40 lines |
| `/screen all` | everything the agent printed since your last message (typed in Herdr or sent here); long output arrives as a `.txt` file |
| `/focus` | the pane is brought to the front in Herdr |
| `/git status`, `/git diff`, `/git diff staged`, `/git log [N]` | `git status --short --branch`, `git diff HEAD`, `git diff --cached` or `git log --oneline --decorate -n N` (default 10, at most 50) run by the daemon in the agent's working directory (`cwd` from `agent.list`), colour and pager off, 10 s timeout. Up to 3600 characters come back as a quoted code block; longer output as a `<repo>-<sub>-<hhmmss>.patch` (diff) or `.txt` file with a caption naming the argv and the line count (5 MB cap, `truncated` when cut). Empty output answers `clean`, `no changes` or `no commits`. Anything else after `/git` (a path, a flag, another subcommand) answers `usage: /git status \| diff [staged] \| log [N]`; nothing typed on the phone reaches git. Failures: `⚠️ not a git repository: <cwd>`, `⚠️ git is not installed`, `⚠️ git timed out`, `⚠️ Herdr reports no working directory`. Secret redaction applies to the output like to any post |
| `/stop` | `esc` through `agent.send_keys`, in any status: Claude Code cancels the running turn or dismisses the open dialog; the reply is `⏹ sent esc` |
| `/interrupt` | `ctrl+c` through `agent.send_keys`, in any status: a hard interrupt; the reply is `⛔ sent ctrl+c` |
| `/close` | the question `Close <label>? The pane and its tab go away.` with `Yes, close` / `No` buttons; `Yes` closes the pane through `pane.close` (the tab goes with it when it held nothing else) and the topic gets 🏁 through the usual exit path; `No` keeps everything. Only the latest question of an agent acts; see [Questions and buttons](behaviour.md#questions-and-buttons) |
| `/clear`, `/compact [instructions]`, `/usage`, `/model [name]` | typed into the agent as its own Claude Code command; two seconds later the screen is posted as a quoted reply (`/usage` and a bare `/model` are closed with `esc` for you); only while the agent is idle |
| `/status` | `<emoji> <status> · <label> · pane <id>` |
| `/options` | a hint: the settings panel lives in General |
| `/away`, `/here`, `/new`, `/observers` | a hint: these commands live in General |
| `/help` | the command list |

A prompt gets no reply; the topic icon turns ⚡ meanwhile. With `React to
prompts` ticked in `/options` (off by default) the message gets 👀 once
`agent.prompt` accepted it and 👌 when that turn ends (done, or idle for
5 s); short replies, `/keys` and button presses get no reaction. A quoted `⚠️
...` reply explains why a message did not
get through (agent gone, socket down). Messages in the topic of an
exited agent get `agent has exited`. Only the configured group and the operator
ids from setup are accepted; everything else is dropped and logged.

## Claude Code commands

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

## Agent control

`/stop` and `/interrupt` send one key through `agent.send_keys` and answer
with a quoted confirmation; the next blocked or done screen arrives through
the usual post, there are no buttons for them. On Claude Code `esc` cancels
the running turn (the partial output stays on screen) or dismisses an open
dialog, so `/stop` is the soft cancel; `ctrl+c` once interrupts what runs
and twice in a row exits Claude Code, so `/interrupt` is the hard one (the
key name `ctrl+c` was verified against Herdr 0.7.5 on 2026-09-04). Both
are accepted whatever the status: sending `esc` to an idle agent does
nothing, which is the point of not guarding it. Other agent kinds get the
same keys and react their own way.

`/close` always asks first. `Yes, close` calls `pane.close` on the agent's
pane, never `tab.close`: a tab with one pane disappears with it, a split
tab keeps its other panes. The question is edited to `closing <label> …`
and, when Herdr reports the pane closed, the topic gets 🏁 and closes like
after any exit. `No` edits it to `not closed`. A second `/close` retires the
first question's buttons; pressing a retired one answers `not the latest
question`, pressing after the agent exited answers `agent has exited`.

## `/screen all`

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

## Files from the phone

A photo, document, voice note, audio track or video sent into a topic is
downloaded (off the daemon's message loop, so other messages keep flowing),
saved under the plugin state dir as `inbox/<yyyymmdd-hhmmss>-<message
id>-<name>` (the sender's file name reduced to letters, digits, dots,
hyphens and underscores; `photo.jpg`, `voice.ogg`, `audio.mp3`, `video.mp4`
or `file.<ext>` when there is none; a taken name gets `-2`, `-3` …) and the
agent is prompted with the caption, a blank line and the absolute path. An
album (several photos sent together) is collected for a second after its
last part and becomes one prompt with one path per line; the first caption
wins. The message gets the same 👀 / 👌 reactions as a typed prompt when
`React to prompts` is on. While
the agent is blocked the path is typed into the dialog like any plain text
would be.

Refusals are quoted replies: `⚠️ inbox is off (/options → Inbox)`, `⚠️ file
too big: 25 MB > 20 MB` (the `Largest file` option; Telegram lets bots
download 20 MB at most), `⚠️ download failed: <reason>`. When one part of an
album fails the others are still delivered, followed by `⚠️ 1 of 3 files
failed: <reason>`. An agent that exits during the download gets nothing; the
reply says where the file was kept. Stickers, animations, locations and
contacts are ignored. Files older than `Delete files after` (7 days by
default) are deleted once a day; see [Inbox](behaviour.md#inbox).

## The General topic

The **General** topic is the control panel. Other messages there are ignored,
and the commands appear in Telegram's `/` menu for the group.

| You write | What happens |
|-----------|--------------|
| `/status` | every live agent with its status emoji, a link to its topic and, once known, how long it has been in that status (`· 12 min`); the first line says when quiet mode is holding edits (`🔕 …`), when you are away by hand (`🏃 …`) or when sync is off (`🔇 …`). The same text, with an `updated HH:MM` footer, is the pinned dashboard; see [The dashboard](behaviour.md#the-dashboard) |
| `/options` | the settings panel: sync, quiet mode, status icons, secret redaction, topic cleanup; see [Options](behaviour.md#options) |
| `/away`, `/away 2h` | you count as away until `/here`, or for that long (any Go duration from `1m` to `168h`): held topic edits and posts go out at once; see [Quiet while at the desk](behaviour.md#quiet-while-at-the-desk) |
| `/here` | presence is automatic again; the reply says the current verdict |
| `/new <workspace> [kind]` | opens an unfocused tab in that workspace (`tab.create`, Herdr's default directory and label) and starts an agent in its root pane (`agent.start`). The workspace is matched by label, case-insensitive: an exact match wins, else a unique prefix (`/new wor` for `Work`); labels may contain spaces. The last word is the kind only when Herdr knows it (`pi`, `claude`, `codex`, `gemini`, `cursor`, `devin`, `agy`, `cline`, `omp`, `mastracode`, `opencode`, `copilot`, `kimi`, `kiro`, `droid`, `amp`, `grok`, `hermes`, `kilo`, `qodercli`, `maki`), default `claude`; no arguments reach the agent. The first reply is `starting <kind> in <workspace> …`, the second, up to a minute later, `started <kind> in <workspace> (pane <id>)` or `⚠️ <kind> did not start in <workspace>: <reason>`; the topic appears through the ordinary sync. A bare `/new`, an unknown or an ambiguous label answer with the workspace list |
| `/observers`, `/observers add <id>`, `/observers remove <id>` | the operator and observer lists plus the unknown accounts seen recently (name, `@username`, id, when and where); add or remove an observer, saved to `config.json` and applied at once. An observer may use `/status` and `/help` here and nothing else; see [Operators and observers](behaviour.md#operators-and-observers) |
| `/help` | the command list |

`/git`, `/stop`, `/interrupt` and `/close` written in General answer with a
hint that they live in an agent's topic.

The daemon also posts silent notices into General when it starts, stops,
loses or regains the **Manage topics** right, gives up on the Herdr socket,
or cannot write to any operator's private chat while `Questions in the
bot's chat` is on (`⚠️ questions will ring in the topics …`). Above them
sits the pinned dashboard: one message, edited in place, with every live
agent, its status and how long it has been in it.

## See Also

- [Behaviour](behaviour.md): topic naming and icons, the dashboard, the options panel, silencing the group, quiet mode, secrets, operators and observers, topic cleanup, logs and state
- [README: Actions](../README.md#actions): start, stop, resync, status, logs, doctor and the test message from Herdr
