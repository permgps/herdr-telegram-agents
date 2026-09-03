[Back to README](../README.md) · [Next: Behaviour →](behaviour.md)

# Talking to agents

What the daemon posts into an agent's topic, what you can write back, and
what the General topic is for. The rules the mirror itself follows (topic
names, icons, exit and resume, the options) are in [behaviour.md](behaviour.md).

## What gets posted

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
dialog a press toggles the option; send `enter` to submit. The button rules
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
| `/screen` or `/screen 40` | the visible screen, or its last 40 lines (max 200) |
| `/screen all` | everything the agent printed since your last message (typed in Herdr or sent here); long output arrives as a `.txt` file |
| `/focus` | the pane is brought to the front in Herdr |
| `/clear`, `/compact [instructions]`, `/usage`, `/model [name]` | typed into the agent as its own Claude Code command; two seconds later the screen is posted as a quoted reply (`/usage` and a bare `/model` are closed with `esc` for you); only while the agent is idle |
| `/status` | `<emoji> <status> · <label> · pane <id>` |
| `/options` | a hint: the settings panel lives in General |
| `/away`, `/here` | a hint: presence commands live in General |
| `/help` | the command list |

Delivery is silent: the topic icon turning ⚡ within a few seconds shows the
agent took the prompt. A quoted `⚠️ ...` reply explains why a message did not
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

## The General topic

The **General** topic is the control panel. Other messages there are ignored,
and the commands appear in Telegram's `/` menu for the group.

| You write | What happens |
|-----------|--------------|
| `/status` | every live agent with its status emoji and a link to its topic; the first line says when quiet mode is holding edits (`🔕 …`), when you are away by hand (`🏃 …`) or when sync is off (`🔇 …`) |
| `/options` | the settings panel: sync, quiet mode, status icons, secret redaction, topic cleanup; see [Options](behaviour.md#options) |
| `/away`, `/away 2h` | you count as away until `/here`, or for that long (any Go duration from `1m` to `168h`): held topic edits and posts go out at once; see [Quiet while at the desk](behaviour.md#quiet-while-at-the-desk) |
| `/here` | presence is automatic again; the reply says the current verdict |
| `/help` | the command list |

The daemon also posts silent notices into General when it starts, stops,
loses or regains the **Manage topics** right, or gives up on the Herdr
socket.

## See Also

- [Behaviour](behaviour.md): topic naming and icons, the options panel, quiet mode, secrets, topic cleanup, logs and state
- [README: Actions](../README.md#actions): start, stop, resync, status, logs, doctor and the test message from Herdr
