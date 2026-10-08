[← Talking to agents](commands.md) · [Back to README](../README.md) · [Next: Development →](development.md)

# How the sync behaves

The rules the daemon follows when it mirrors agents into topics, the options
that change them, and where its files, logs and state live. What you can write
in a topic and what gets posted there is in [commands.md](commands.md).

## Topics and statuses

- A topic is named like the row in Herdr's Agents panel: `<workspace> ·
  <agent>`, where the agent part is the custom agent name, else the tab label,
  else the agent kind (for example `V3Jobs · claude`). The terminal title is
  not used, so a topic keeps its name while the agent works through tasks.
- The status is the topic icon: ⚡ working, ✅ idle (the check Herdr shows),
  ❓ blocked, 🏆 done, 👀 unknown, 🏁 exited by default; `/options` lets you
  pick others (see [Options](#options)). The icons come from Telegram's free
  topic-icon pack; the colour is the fallback when the pack lacks an emoji,
  and an edit then leaves the icon unchanged.
- A topic is created the first time an agent appears. Herdr's pane id keeps
  topics separate when the same session is shown in two panes; the terminal
  id routes live calls, while a digest of Herdr's complete `agent_session`
  identifies that session across restarts. If Herdr reports the same session
  after a restart, the existing topic and its history stay in place even when
  the terminal id changes. A current topic needs no Telegram edit during
  adoption. A finished topic is reopened and refreshed when the matching
  session returns.
- If either the live agent or stored entry lacks a usable `agent_session`,
  continuity is best-effort: the daemon can adopt one unambiguous topic in the
  same pane with the same canonical topic name and a non-empty live working
  directory. If the stored entry already has a working directory or agent
  kind, those values must also match. Entries from mapping version 1 have no
  recorded working directory, so they can match only with a non-empty live
  directory and an exact pane/name match. The daemon prefers the sole live
  candidate over older closed generations; if several candidates remain, it
  does not guess and creates a new topic. Old topics remain subject to the
  configured cleanup sweep. A newly created entry with no recorded working
  directory cannot use this fallback, even if Herdr reports a directory later.
- When Herdr reports a different non-empty session identity, the topic is
  never inherited from another known session, even if pane, directory and
  name match. `claude --resume` therefore keeps the old topic only when Herdr
  reports the same session; a resume that starts a new session gets a new
  topic, and the old one follows the normal exit and cleanup rules.
- `mapping.json` version 1 files continue to load with their topic ids,
  statuses, mute/closed flags, timestamps and dashboard id intact. The next
  successful mapping write stores version 2 and its optional working
  directory and agent-kind metadata. An entry that still lacks a directory
  carries `legacy_no_cwd` so its version-1 fallback survives later saves.
  Those fields are refreshed when Herdr provides changed metadata; an
  unchanged mapping is not rewritten on every poll.
- When an agent's pane closes the topic gets the 🏁 icon and is closed. Topics
  of agents that vanished while the daemon was down are closed on the next
  start unless a matching session is adopted first.
- The daemon exits by itself when the Herdr socket is gone for 60 s, when the
  bot token is rejected, when another process polls the same bot, or when the
  bot is removed from the group. Losing **Manage topics** only pauses edits
  until the right is granted again.
- Every status change makes Telegram post a "changed the topic icon" notice
  into the topic. Clients learn the new icon from that notice, so the daemon
  leaves it for 20 s by default (`Keep icon notices for` in the Topics group
  of `/options`: `Keep` never deletes them, `10s` … `120s` waits that long)
  and then deletes it, which needs **Delete messages**; without that right
  the notices stay and the log says so once. A client that was asleep when
  the notice was deleted keeps drawing the old icon until it next opens the
  topic. Topic creation notices cannot be deleted and remain. The dashboard in
  General is pinned with **Pin messages**; without that right it is an
  ordinary message and the log says so once (see [The dashboard](#the-dashboard)).
- Rename a topic by hand and the change goes back to Herdr: the tab is
  renamed (`tab.rename`), which is what the Agents panel shows on its first
  line, or the custom agent name when the agent has one (`agent.rename`).
  The `<workspace> · ` prefix is optional; an empty remainder is ignored for
  a tab and clears a custom name. The topic settles on the canonical form.
- Close a topic by hand and the mirror goes quiet for that agent: no icon
  edits, no screen posts, until you reopen it. A successful same-agent
  reassociation keeps this mute. Reopening refreshes name and icon and clears
  the mute; if the agent exited meanwhile the topic gets 🏁 and is closed
  again.

## The dashboard

One message of the bot in General, pinned, shows the same text as `/status`
plus how long each agent has been in its status and a footer `updated
HH:MM`. It lists at most 40 agents and sums up the rest as `… +N more` (the
first line still counts them all), so it stays one Telegram message (4096
characters) that can be edited in place; `/status` lists every agent and
splits a long list across messages:

```
2 agents
⚡ V3Jobs · claude · 12 min
❓ herdr_tg · claude · 3 min

🔑 Claude 5h 18% ↻2 h · 7d 83% ↻1 d 4 h
🔑 Codex 7d 3% ↻4 d 2 h

updated 21:35
```

- It is created silently on the first refresh after daemon start, pinned
  silently (the "pinned a message" notice the bot causes is deleted like a
  topic edit notice) and then only edited in place. An edit of an existing
  message never notifies anybody, so the dashboard costs no sound.
- It refreshes 2 s after the last agent event, presence change or option
  change (a burst of events is one edit), and once a minute while a
  duration label moved; an edit whose text would be unchanged is skipped.
  The duration counts from the status change the daemon saw; an agent that
  was already in its status when the daemon started shows none until it
  changes. Under a minute there is no label, then `N min`, `N h M min`,
  `N d M h`.
- It keeps updating while sync is off, while writes are paused for lost
  rights and while quiet mode holds topic edits: the edit needs no admin
  right and rings nothing.
- On daemon stop the footer becomes `⏹ stopped HH:MM`. At the next start
  the stored message is pinned again (a no-op when it is still pinned) and
  refreshed; no second message is made. A message you deleted by hand is
  recreated on the next refresh.
- The message id lives in `mapping.json` as `dashboard_message_id`. A
  binary older than this feature drops the key on its next save and leaves
  one stale pinned message behind; unpin and delete it by hand.
- `Dashboard in General` in `/options` → Sync (default on) switches it;
  off unpins and deletes the message.
- Under the agents come the quota lines, one per provider with data: each
  usage window with the share spent and the time until it starts over. See
  [Quota lines](#quota-lines).

### Quota lines

The dashboard and `/status` end with the usage windows of Claude and Codex
when the daemon has numbers for them. `Quota in the dashboard` in
`/options` → Sync (default on) switches them; the lines change only
messages that are edited in place, so they never ring.

- **Codex** needs no setup. Every `token_count` event in a Codex rollout
  (`~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl`) carries the account's
  rate limits; the daemon reads the newest one among the eight most
  recently written rollouts of the last week. Rate limits belong to the
  account, so any session will do. Windows are named by length: `5h`,
  `7d`. An API-key login has no rate limits and shows no line.
- **Claude** comes from Claude Code's status line. Claude Code hands its
  status line command a JSON object with `rate_limits.five_hour` and
  `rate_limits.seven_day` (subscription logins, after the first answer of a
  session); the plugin's `usage-tap` subcommand stores those two windows
  and passes its input on unchanged, so it goes in front of your own status
  line command in `~/.claude/settings.json`:

  ```json
  "statusLine": {
    "type": "command",
    "command": "<plugin dir>/bin/herdr-tg usage-tap --out <state dir>/claude-usage.json | ~/.claude/statusline.sh"
  }
  ```

  The doctor action prints this line with both paths filled in (`claude
  quota: not set up (optional) …`). Without a status line of your own,
  use the tap alone; it prints its input, so Claude Code shows the raw
  JSON. The plugin never edits `settings.json`. The tap stores only the two
  windows and the time it saw them, never the working directory, session,
  model or cost from the same input; a write failure prints one
  `usage-tap: …` line on stderr and the status line keeps working. On
  Windows the command runs through the shell Claude Code uses there; this
  has not been checked yet.
- The sources are read at most once a minute. A window whose reset time has
  passed is dropped, since its share no longer holds; a provider with no
  window left shows no line. The time left is coarse so the dashboard is
  not edited every minute for it: `N min` under an hour, `N h` under a day,
  `N d M h` beyond. Numbers older than 30 minutes end with `· as of HH:MM`.
- A source that fails keeps its last numbers; the log has one `quota source
  failed` warning per distinct error and `quota source ready` the first
  time a provider has data.

## Capturing working screens

The daemon reads up to 400 recent lines from each working pane once a second
and merges the snapshots into in-memory history. This keeps output available
for `/screen all` and posts after it scrolls off the terminal. If Herdr refuses
a recent read with `agent_not_idle`, the daemon retries once with the visible
screen under the same read deadline. Other errors are not retried. A successful
visible read is merged into the same history; when a later recent read includes
older text, the daemon removes the overlap only when it can confirm the text is
already in history. If continuity cannot be confirmed, the history keeps a gap
marker rather than dropping uncertain lines. Successful fallbacks are logged
at DEBUG without screen text; a final failed read produces one WARN.

## Questions and buttons

- A blocked agent's screen is posted as a code block. When it ends in a
  numbered dialog with two to five real options, the post also carries one
  inline button per option, labelled with the option's number and text.
  Claude Code's `Type something.` and `Chat about this` entries are not
  option buttons and do not count towards the five; the other options keep
  their numbers, so replying with a digit still works as before. A dialog
  that offers `Type something.` (else `Chat about this`) gets a `✏️ Type
  something` button in its own row. With `Trim the input frame` on (the
  default) the status line and the mode hint Claude Code draws under the
  dialog are cut from the post; the dialog's own lines never are, so the
  buttons are found as before.
- Pressing ✏️ sends that entry's number, turns the keyboard into `✏️ waiting
  for your text`, answers `now send the text` and posts a quoted `✏️ Type
  something: send the text as your next message` with Telegram's
  `force_reply`, so the phone opens the reply box. Your next plain message
  in that topic is then typed into the agent whatever it looks like (`y`,
  `2` and `enter` included, which would otherwise be key presses), the
  button becomes `✅ ✏️ · <start of the text>` and the message gets 👀 like
  a prompt. The wait ends with that message, with any slash command (which
  runs as usual and leaves `✏️ cancelled`), when the agent exits, when a
  newer question is posted for it, or after 10 minutes (`✏️ expired`). No
  screen is read after the press: the text box the agent shows is not a
  question.
- Herdr 0.9.3 and later refuse `agent.prompt` for an agent waiting at a
  dialog (`agent_blocked`), before any input reaches the pane. The text
  after ✏️, plain text and attachment paths sent to a blocked agent are
  then typed into the dialog through `pane.send_text` and submitted with
  `enter`, as at the desk. That input has no bracketed paste, so line
  breaks are joined with spaces: a break would submit half the answer. A
  forwarded Claude command (`/clear`, `/model` …) is never typed into a
  dialog; it answers `⚠️ agent is waiting at a dialog`. Herdr 0.7.5 never
  refuses, so nothing changes there.
- A multi-select dialog (every option starts with a checkbox: `[ ]`,
  `[x]`, the `[✔]` Claude Code draws once an option is toggled, or `☐` /
  `☑`) keeps its buttons as toggles: a press sends the digit, answers
  `toggled: <n>` and leaves the keyboard in place; 1.5 s later the same
  message is redrawn from the screen, text and keyboard, so the ticks
  follow what the agent shows. Herdr may call the pane `working` for a few
  seconds after the keystroke while the dialog stays on screen; the redraw
  and further presses check the screen instead of trusting that status. A
  `✔ Submit` row submits the dialog: Claude Code ends its entries with a
  bare `Submit` row and, since a digit toggles without moving the cursor
  and `enter` toggles the row under it, the press reads the screen and
  sends the arrows from the `❯` cursor to that row followed by `enter`
  (a renderer without a `Submit` row gets a plain `enter`). The keyboard
  collapses to `✅ submitted` and the usual read is armed, so Claude Code's
  `Submit answers` / `Cancel` review arrives with its own buttons. When the
  screen no longer shows the multi-select dialog at redraw time (the agent
  moved on) the new screen is posted as usual and the old keyboard is
  retired.
- A press sends the option's digit as a key (`agent.send_keys`), answers
  with a short toast, and replaces the keyboard with a single `✅ <n> · <text>`
  button; pressing that one says `already answered`. The daemon then reads
  the screen again after the usual settle delay, so the next question of a
  multi-step dialog is posted with its own buttons.
- Buttons are removed lazily: before a newer screen is posted for the same
  agent, when the agent exits, or when a press arrives for an agent that is
  no longer blocked, has exited, or for a message that is not the latest
  question. Such a press does nothing but show a notice.
- Only operators can press; anyone else gets `not allowed`. Pressing
  `Submit` under a single-select question, or ✏️ under a question that
  offers no free-text entry, answers `unknown button` and strips the
  keyboard.
- `/close` posts its own two-button question (`Yes, close` / `No`, callback
  data `c:y` / `c:n`). One question per agent is active: a newer `/close`
  strips the buttons of the older one, which then answers `not the latest
  question`. `Yes` edits the question to `closing <label> …` and calls
  `pane.close`; `No` edits it to `not closed`; a press for an exited agent
  answers `agent has exited`; a failed close leaves `⚠️ could not close:
  <reason>` in the message.
- With `Question delay` set, the first capture of a blocked screen (after
  the usual 1.5 s) is kept in memory and the post waits N more seconds. When
  the timer fires: an agent that is no longer blocked posts nothing; a
  blocked agent whose question changed meanwhile (Herdr's `state_change_seq`
  moved) starts over with a fresh first capture; otherwise the screen is
  read again and the better capture is posted, the one with more options
  recognised, or the longer one on a tie. Buttons, the duplicate check and
  quiet mode apply to the chosen text as usual; the catch-up on leaving the
  desk never waits, and a button press restarts from a fresh first capture.

## Done posts

When an agent turns 🏆 done, or settles in idle for five seconds after a
working turn, the topic gets one silent completion post. The idle fallback
covers agents that finish an answer without reporting `done`. Duplicate
screen text is still suppressed. What the post holds is the `Done post`
option of the Posts group:

- **Screen**: the last 12 lines of the terminal, as a code block,
  without Claude Code's input frame at the bottom (the two `─` rules with the
  empty `❯` row between them, the status line and the mode hint) while
  `Trim the input frame` in the Posts group is on, so the post ends on the
  agent's last line. This is what every other post uses too (blocked
  screens, `/screen`), and it works for any agent Herdr detects: a screen
  without that frame passes through unchanged. Because the cut happens
  after the read, a `/screen N` on an idle Claude Code pane may come back
  with fewer than N lines.
- **Reply**: the agent's own last message, taken from the transcript Claude
  Code writes for itself. Herdr does not say which session a pane runs, so
  the daemon takes the pane's working directory, maps it to
  `~/.claude/projects/<cwd with every non-alphanumeric character as "-">/`
  and reads the newest `.jsonl` there from the end: the last text the agent
  wrote after your last prompt, skipping tool calls, tool results and
  subagent traffic. The text is posted as a code block, so Markdown shows as
  the agent typed it. For OpenCode, which keeps its sessions in a database
  rather than a file per session, the daemon asks Herdr for the pane's
  `agent_session` at read time and runs `opencode session export <session id>`
  on OpenCode 2.x, falling back to `opencode export <session id>` on 1.x (the
  `opencode` binary on `PATH`, one shared 10 s timeout, 16 MiB export cap per
  attempt). OpenCode cuts its output at 64 KiB when it goes into a pipe, so
  each export goes into a private file (0600) under the plugin's state
  directory, `tmp/opencode-export-*.json`, which is read back and removed
  at once; files a crashed daemon left there are removed at the next start.
  The reply is
  every text part the agent wrote after your last prompt, joined in order,
  skipping reasoning, tool calls and patches. OpenCode reports done at
  every step of a turn, so a read whose newest record is a tool call is a
  turn still running: in `Reply` and `Formatted` mode the done post waits
  and reads again up to 3 times, 5 s apart, and posts the screen if the
  turn still has not finished (log `reply still pending, screen posted`).
  The real end of the turn usually arrives first as a new done event. A new
  turn or a question in the meantime drops the wait. `Screen` mode posts at
  once; an unfinished turn only leaves out the summary line. The session value is used for
  that one lookup and never stored or logged. The pane must have a complete
  session identity matching the topic; after a session change, the screen is
  posted until Herdr reconciles the new identity. Export stderr is discarded;
  failures use short categories without session IDs or command output. An
  export timeout falls back to the screen while the parent request is alive.
  Install Herdr's OpenCode integration with `herdr integration install opencode`
  so Herdr reports `agent_session`; restart the OpenCode pane after installing
  it. Without that reference, the visible screen is posted instead.
  For Codex, the daemon asks Herdr for the pane's `agent_session` at read time
  and opens exactly that thread's rollout file,
  `~/.codex/sessions/YYYY/MM/DD/rollout-<time>-<thread id>.jsonl` (the day
  directory of the thread's creation; the thread id must be a UUID). The
  lookup never leaves the sessions directory (a link that points out of it is
  refused, and the walk does not descend into links), and the file must be a
  regular file whose first record is the `session_meta` of that thread, so a
  file that only carries the thread id in its name is refused. The search
  reads directories in batches and stops after 50,000 entries, the day
  directories included. A thread that Codex reverted continues in a new file,
  `rollout-<time>-<thread id>_<rollout id>.jsonl`, in the day directory of the
  revert; the reader looks at every day from the thread's creation to now and
  takes the newest of the thread's files, ordered by the rollout id's UUIDv7
  time (UTC), not by the local time in the file name; when several files
  exist and one id carries no time, the screen is posted. For a thread older
  than a year, or when the clock is behind the thread's creation, it walks
  the whole sessions tree instead, within the same 50,000 entries. When
  the search cannot establish the newest file (the budget runs out, a
  directory cannot be opened or listed, or a link stands where the thread's
  rollout or, in the walk, a directory could be), it posts the screen rather
  than an older file's answer; only a missing day directory counts as empty. It
  reads the file from the end, within the
  same 4 MiB budget as Claude Code, and posts the last completed turn's final
  answer, which Codex records as the turn's `task_complete` message: the
  progress notes Codex prints while it works are not part of it. A turn that is
  still running, was interrupted, was rolled back (`thread_rolled_back`, so its
  answer left the conversation) or ended without an answer has no reply, and
  neither has a pane whose session no longer matches the topic: the screen is
  posted instead. The session value and the file path (which contains the
  thread id) are used for that one lookup and never stored or logged; failures
  use short reasons without either. Install Herdr's Codex integration with
  `herdr integration install codex` so Herdr reports `agent_session`. Codex is
  read from the daemon's own home directory (`~/.codex`; `CODEX_HOME` is not
  honoured), so a Codex running inside WSL is not found.
  For Antigravity (`agy`), Herdr reports the conversation id as the pane's
  `agent_session`, and the daemon opens that conversation's transcript,
  `~/.gemini/antigravity-cli/brain/<conversation id>/.system_generated/logs/transcript.jsonl`,
  or `transcript_full.jsonl` beside it when the first is missing or holds
  nothing the reader knows. The id must be a UUID, the lookup never leaves
  the `brain` directory, and a link in place of the transcript is refused.
  It reads the file from the end within the same 4 MiB budget and posts the
  newest model answer (`PLANNER_RESPONSE` with text and no tool calls); the
  commentary Antigravity writes next to a tool call is never posted. When the
  newest record is tool output, a tool call, a new prompt or a half-written
  line, the turn is still running and the done post waits and reads again,
  as for OpenCode. A pane whose conversation no longer matches the topic
  gets the screen. The conversation id and the path are used for that one
  lookup and never stored or logged. The transcript is read from the
  daemon's own home directory; a moved `~/.gemini` is not followed.
  For Pi (`pi`), Herdr's Pi integration reports the pane's session file
  itself as `agent_session` (`kind` `path`), and the daemon opens exactly
  that file; it never picks a file by working directory, where two Pi panes
  would collide. The path must be absolute, clean and end in `.jsonl`, a
  link in place of the file is refused, and the file's first line must be a
  Pi session header, so a path to any other file yields nothing. Because the
  path comes from Pi, `PI_CODING_AGENT_DIR`, `--session-dir` and
  `--session <path>` work as they are. A Pi session is a tree: the daemon
  follows the active branch from the last line back through each entry's
  parent, so after `/tree` it never posts an answer from the abandoned
  branch. Within the same 4 MiB budget it posts the newest assistant
  message that ended the turn (no tool call, stop reason `stop` or
  `length`); thinking and the commentary Pi writes next to a tool call are
  never posted. When the newest message is a tool call, a tool result, a new
  prompt or a half-written line, the turn is still running and the done post
  waits and reads again, as for OpenCode; a turn that ended in an error or
  was aborted gets the screen, which shows Pi's message. `/new`, `/resume`
  and `/fork` in Pi report the new file, and a pane whose session no longer
  matches the topic gets the screen. Without the integration
  (`herdr integration install pi`), with `--no-session`, outside the TUI, or
  before the first message (Pi creates the file then) the screen is posted.
  The path is used for that one lookup and never stored or logged.
  For Muse Code (`muse`), Herdr reports no session, so the daemon ties the
  pane to its Muse session through the pane's processes (`pane.process_info`,
  Herdr protocol 22 or newer). Muse keeps one runtime file per live session
  under `runtime/muse/sessions/` in its data directory, naming the owning
  process (`process_generation_hint`, `pid=…`) and the working directory's
  name; a session counts only when its pid is one of the pane's processes and
  its directory name matches the pane's. A pane is never matched by the
  directory name alone, so two Muse panes in `~/a/api` and `~/b/api`, or in
  one directory, each get their own answer. After `/new` or `/clear` one
  process owns several sessions, and the one whose log was written last
  wins. The data directory is the first of `~/.local/share/muse`, `~/.muse`
  and `~/Library/Application Support/muse` that has the runtime directory
  (only the first is confirmed, on Linux); `XDG_DATA_HOME` is not honoured.
  The log is `sessions/<year>/<month>/<day>/<session id>/session.jsonl`,
  dated by the session's creation, found by walking the dated directories
  newest first and remembered per session. Within the same 4 MiB budget the
  daemon posts the final message of the newest run that ended `completed`:
  the newest `assistant_message_committed` of that run whose `phase` is not
  `commentary`; commentary is never posted. A run that has not ended (or a
  half-written line) is still running and the done post waits and reads
  again, as for OpenCode; a run that ended `failed` or `cancelled` (Esc)
  gets the screen, and so does a run Muse never closed because the process
  was killed. Windows, `--no-session-log` sessions and sessions whose
  runtime file is missing get the screen. Session ids, pids and paths are
  used for the lookup and never logged.
- **Formatted** (default): the same reply rendered for Telegram: headings become bold,
  `- ` lists become `•`, quotes get a bar, `[text](url)` becomes a link,
  inline code and fenced blocks keep their monospace, pipe tables are
  monospaced too. A fenced block never straddles two messages; long replies
  are split and stop after five messages with `… (+N chars)` at the end.
  Should Telegram reject the markup (`can't parse entities`), that part is
  sent once more as a plain code block and the log says so. An agent with
  no readable reply (any kind other than Claude Code, Codex, OpenCode,
  Antigravity, Pi and Muse,
  or a missing or stale transcript) gets the screen post instead.

`Formatted` has been the default since 2026-10-01; before that it was
`Screen`. `options.json` stores every option once the panel has saved it,
so an install that saved `Screen` keeps it: switch it in `/options` →
Posts → `Done post`.

Under every done post, in all three modes, sits the **turn summary line**
(`Turn summary line` in the Posts group, default on): one plain-text line
outside the code block, read from the same transcript, such as
`⏱ 4 min · fable-5-1 · ✏️ 3 files · ↑ 12k tokens`. It holds how long the
turn took (from your prompt to the agent's last record), the model with the
`claude-` prefix and a date suffix dropped, how many distinct files the
agent edited (`Edit`, `Write`, `MultiEdit`, `NotebookEdit`; subagent edits
excluded) and how many output tokens it wrote (summed once per API
response). A part the transcript does not know is left out; nothing known
means no line. There is no cost: Claude Code writes the cost once at the
end of the session, not per turn, and a price table would drift from what
the status line shows. Claude Code, OpenCode, Codex, Antigravity, Pi and Muse only (for OpenCode the
  edited files are distinct paths from completed `edit` and `write` tools; for
Codex the line has the duration, the model and the output tokens, but no files,
as its rollout does not name them reliably; for Antigravity the line has the
duration only, as its transcript names neither the model nor the files, and
so has Muse; for
Pi the line has the duration, the model, the distinct paths of the turn's
`edit` and `write` tool calls and the output tokens summed over the turn's
assistant messages): a pane of another kind, a
  pane without a working directory or one without a transcript directory
posts as before and the log says why at debug (`turn meta unavailable`). In `Screen` mode
the transcript is read for the line alone. A transcript last written
before the turn's first `working` status belongs to an earlier turn (two
Claude panes in one directory): its line is skipped and, in `Reply` /
`Formatted` mode, the screen is posted instead with `stale transcript` in
the `reply source unavailable` line. The daemon sees that first `working`
status a beat after the agent began, so a reply written up to a few seconds
before it still counts as this turn's.

Long `Reply` and `Formatted` posts can arrive **folded** (`Fold long
replies after` in the Posts group, default `20 lines`): every message part
with more lines than that is wrapped in Telegram's expandable quote, so the
phone shows the first few lines and an arrow that opens the rest; the
summary line stays visible under the quote. The count is lines of the
reply as the agent wrote it, before rendering and before the phone wraps
them: a list of 25 short items folds, three long paragraphs do not. `Off`
never folds. `Screen` posts are never folded, whatever the option says.

Limits worth knowing: two Claude Code panes in the same directory cannot be
told apart, so the reply of the one that wrote last wins (the stale check
above catches the case where the other pane wrote before this turn began);
other agents (Gemini, OMP) always get the screen; when no transcript or no
text is found the daemon posts the screen and logs `reply source
unavailable` with a safe category. Blocked posts are never affected: the dialog
with its buttons exists only on the screen. For an idle or done OpenCode, Codex, Antigravity, Pi or Muse agent,
a bare `/screen` tries the current session reply, rendered like `Formatted`,
whatever `Done post` says. It falls back to the screen when the reply is
unavailable, empty, or stale. The reply stops after five Telegram messages
with a truncation trailer. Claude Code and other agents keep the visible
screen for `/screen`; `/screen N` always reads the screen, and `/screen all`
uses captured history (see [commands](commands.md)). None of these commands
carries the summary line or the fold.

## Turns and reactions

A **turn** is one exchange with an agent. It starts with the first
**working** status after the previous turn ended, or at once when a prompt
from Telegram reaches an agent that is already working. It ends when the
agent turns **done**, or once it has stayed **idle** for 5 s: Herdr's
detection dips out of working for a second or two while a tool runs, so a
shorter wait would end turns that are still going. A **blocked** stretch is
part of the turn. The daemon keeps this record in memory per agent; a turn
whose start it never saw (daemon started mid-turn) has no duration.

Two things hang on it:

- **Reactions** (`React to prompts`, default off since 0.9.1). Switched
  on, a plain prompt sent from the topic gets 👀 as soon as `agent.prompt`
  accepted it, and 👌 replaces it when that turn ends. Short replies to a
  dialog, `/keys`, forwarded Claude Code commands and button presses get no
  reaction. A second prompt while the first turn still runs moves the 👌 to
  the newer message; the older keeps its 👀. Telegram notifies you of the
  bot's reaction on most phones, so in a group with sound on every prompt
  rang twice more; a week of use showed the noise outweighs the
  acknowledgement, hence the default. The log says `reaction skipped
  reason="posts.reactions off"` at debug level for every prompt while the
  option is off.
- **Short turns** (`Skip short done posts`, default `Off`). A done post is
  skipped when the turn lasted less than N seconds, measured from the turn
  start to the done status, blocked time included; the log says `screen
  skipped … reason=short_turn`. A turn with an unknown start posts. Blocked
  posts, `/screen` and the reactions are not affected, and a skipped post
  leaves no trace, so the same screen posts next time.

## Options

`/options` in the General topic answers with one message that is edited in
place as you press its buttons; only operators can press them, and every
string on it is English. A new `/options` retires the previous panel's
keyboard, `✖ Close` leaves a one-line-per-option summary behind. Option
buttons still work after a daemon restart; an Update authorization expires
and needs a fresh check.

- **Level 1** lists the groups (Sync, Quiet, Posts, Inbox, Appearance,
  Privacy, Topics) with a description each.
- **Level 2** lists the options of a group: a checkbox toggles on the spot
  (`☑` / `☐`), a choice shows its current value and opens the picker,
  `↺ Reset to defaults` restores the whole group, `‹ Back` and `✖ Close`
  navigate.
- **Level 3** is the picker for a choice: for an icon, the emoji of
  Telegram's topic-icon pack in the pack's order, eight per row, two pages,
  the current value in brackets (only those emoji can be topic icons, which
  is why there is no free-text field); for the cleanup age, one row of
  `Off 7d 14d 30d 60d 90d`; for the quiet threshold `1m 2m 3m 5m 10m 15m`;
  for the posts mode `Silent Held Normal`; for the done post `Screen Reply
  Formatted`; for the fold threshold `Off 10 20 40` lines; for the question
  delay and the short-turn threshold `Off 5s 10s 30s 60s 120s`; for the
  notice delay `Keep 10s 20s 30s 60s 120s`; for the largest inbox file
  `5MB 10MB 20MB`.

The options today:

| Option | Group | What it does |
|--------|-------|--------------|
| `Herdr → Telegram sync` | Sync | Default on. Off: the daemon creates, edits and closes no topic and posts no screen until it is on again. Messages, keys, `/screen`, `/status` and presses on existing question buttons keep working, the screen capture keeps running, daemon notices keep posting. Back on: a full resync, like the `resync` action. A daemon that starts with sync off says so in its started notice, in the `/status` header (`🔇 …`), in the `status` action line (`sync=off`) and in the log. |
| `Dashboard in General` | Sync | Default on. One pinned message in General, edited in place and never ringing: every live agent (up to 40, then `… +N more`) with its status, how long it has been in it and a link to its topic, `updated HH:MM` at the bottom. Off unpins and deletes it. See [The dashboard](#the-dashboard). |
| `Quota in the dashboard` | Sync | Default on. The Claude and Codex usage windows under the dashboard and `/status` (`🔑 Codex 7d 3% ↻4 d 2 h`). Codex is read from its session files; Claude needs the status line tap the doctor action shows. No data, no line. Off drops the lines at the next refresh. See [Quota lines](#quota-lines). |
| `Quiet while at the desk` | Quiet | Default off: every topic edit and screen post goes out at once, sounds included, so a fresh install shows the plugin at work. On: while you are at the desk, topic edits wait and screen posts are silent; everything catches up when you leave. On Linux it needs GNOME, or an X11 session with `xprintidle`; see the Presence bullet. Off means no presence check at all; `/away` and `/here` then answer that quiet mode is off. See [Quiet while at the desk](#quiet-while-at-the-desk). |
| `Away after` | Quiet | Default 3 min. Minutes without keyboard or mouse input on this machine before you count as away. A value outside the picker's list (say `45`) can be typed into `options.json` by hand. |
| `Hold topic edits` | Quiet | Default on. While at the desk no topic is created, renamed, closed, reopened or given a new icon; each of those is a Telegram service message that rings the phone. Off keeps topic edits live while at the desk. |
| `Screen posts` | Quiet | Default `Silent`. What happens to blocked and done screens while at the desk: `Silent` posts without a sound (Telegram still shows a silent banner), `Held` posts nothing until you leave, `Normal` posts as usual. |
| `Re-announce on leaving` | Quiet | Default on. When you leave, the screen of every agent still waiting for an answer is posted again with a sound, once per question. Off: only agents that have no post at all yet are posted. |
| `Done post` | Posts | Default `Formatted`. What a topic receives when its agent finishes: `Screen` posts the last 12 terminal lines in monospace; `Reply` posts the agent's last message from its Claude Code transcript (`~/.claude/projects/<cwd slug>/`, newest session file) in monospace; `Formatted` renders that message: headings and bold, `•` lists, links, inline and fenced code, tables in monospace. A reply longer than five messages is cut with `… (+N chars)`. For OpenCode the message comes from the CLI export for the pane's session; for Codex it is the final answer of the last completed turn, read from the pane's rollout file. Falls back to `Screen` for other agents or when no reply is found, see [Done posts](#done-posts). |
| `Turn summary line` | Posts | Default on. Every done post (`Screen`, `Reply` and `Formatted`) ends with one line from the agent's transcript: `⏱ 4 min · fable-5-1 · ✏️ 3 files · ↑ 12k tokens` (turn duration, model, distinct files edited, output tokens). Claude Code, OpenCode and Codex only (no file count for Codex); without a transcript the post ends as before and the log has `turn meta unavailable` at debug. A transcript written before the turn began is skipped. Off: no line and, in `Screen` mode, no transcript read. See [Done posts](#done-posts). |
| `Fold long replies after` | Posts | Default `20 lines`. A `Reply` or `Formatted` done post whose message part has more lines than this arrives collapsed in Telegram's expandable quote: the first lines and an arrow that opens the rest; the summary line stays visible under it. `Off` never folds; `Screen` posts are never folded. Any integer of lines up to 1000 can be typed into `options.json`. See [Done posts](#done-posts). |
| `Trim the input frame` | Posts | Default on. Every screen post (done and blocked screens, `/screen`, `/screen all`, the tails of the Claude Code commands, the pager's six lines) loses Claude Code's input frame at the bottom: the `─` rule, the empty `❯` row, the second rule, the status line (`… │ main ✓ │ 14%: …`) and the mode hint (`⏵⏵ auto mode on (shift+tab to cycle)` or `? for shortcuts`). The cut walks up from the bottom and stops at the first line that is none of these, so a dialog and its options are never touched, a `❯` row with typed text is left alone and a screen without the frame (Codex, any other agent) passes through unchanged. The duplicate check runs after the cut, so a screen that differs only in the status line's clock is not posted twice. Off posts the screen as captured. |
| `React to prompts` | Posts | Default off: prompts are delivered silently. On: 👀 on your message once the agent took the prompt, 👌 when that turn ends (done, or 5 s of idle). Telegram may ring for each reaction, which is why it is off. See [Turns and reactions](#turns-and-reactions). |
| `Questions in the bot's chat` | Posts | Default on. A question from an agent is posted into its topic without a sound and sent to you in the private chat with the bot with a sound: the agent's name, the dialog's options or the last six screen lines, and a link to the post. Mute the group in Telegram and only questions ring. Off: the topic post rings, nothing goes to the private chat. Needs a private chat the bot may write to; see [Silence the group](#silence-the-group). |
| `Question delay` | Posts | Default `Off`. With `5s` … `120s`: after the usual 1.5 s capture the blocked post waits that long more, is dropped when the agent left blocked meanwhile, starts over when a newer question arrived, and otherwise posts the better of the two captures (more options recognised, then the longer text). `Off` posts the first capture at 1.5 s. Any integer of seconds up to 3600 can be typed into `options.json`. See [Questions and buttons](#questions-and-buttons). |
| `Skip short done posts` | Posts | Default `Off`. With `5s` … `120s`: the done post of a turn shorter than that is skipped (blocked time included; a turn whose start the daemon never saw posts). Blocked posts and reactions are unaffected. Any integer of seconds up to 3600 can be typed into `options.json`. See [Turns and reactions](#turns-and-reactions). |
| `Accept files` | Inbox | Default on. Photos, documents, voice notes, audio and video sent to a topic are saved to the inbox and the agent is prompted with the path. Off: such messages answer `⚠️ inbox is off (/options → Inbox)`. See [Inbox](#inbox). |
| `Largest file` | Inbox | Default 20 MB, the most Telegram lets a bot download. A larger file answers `⚠️ file too big: <size> > <max>` before any download. Any integer of megabytes from 1 to 20 can be typed into `options.json`. |
| `Inbox size` | Inbox | Default 500 MB. The most all inbox files may take together. When a new file does not fit, the oldest files are deleted first; a file larger than the whole quota is refused with `⚠️ … file is too big`. Any integer of megabytes up to 100000 can be typed into `options.json`. |
| `Delete files after` | Inbox | Default 7 days. Inbox files older than that are deleted once a day, at daemon start and when the option changes. `Off` keeps them. Any integer of days can be typed into `options.json`. |
| `working` … `exited` | Appearance | The topic icon of each status and the emoji `/status` prints. Picking an emoji another status already uses answers `used by <status>` and changes nothing. A pick repaints every live topic at once (a `resync`), or when sync comes back on. |
| `Redact secrets` | Privacy | Default on. Every text the daemon posts passes the redaction step described under [Secrets in posts](#secrets-in-posts). Off: raw text, except the bot token, which is always masked. A change applies to the next post. |
| `Delete closed topics after` | Topics | Default 30 days. The topic of an exited agent is deleted once it has been closed for that long, see [Topic cleanup](#topic-cleanup). `Off` keeps every topic. A number outside the picker's list (say `45`) can be typed into `options.json` by hand; the panel shows it without a bracketed button. |
| `Keep icon notices for` | Topics | Default `20 s`. How long the bot's own topic notices ("changed the topic icon", a rename, close, reopen, the dashboard pin) stay before the daemon deletes them. Clients apply the new icon from that notice; a client that was asleep when it was deleted keeps drawing the old icon until it next opens the topic, which is why 10 s was not enough on Telegram Desktop (2026-09-06). `Keep` leaves every notice in place. A change applies to notices that arrive after it; one already waiting keeps its delay. Any integer of seconds up to 3600 can be typed into `options.json`. See [Topics and statuses](#topics-and-statuses). |

Values are saved in `options.json` next to `config.json` (mode 0600) as
`{"version": 1, "values": {"sync.enabled": true, "sync.dashboard": true,
"sync.quota": true, "quiet.enabled": true, "quiet.idle_minutes": "3", "quiet.posts": "silent",
"posts.done": "formatted", "posts.meta": true, "posts.fold": "20",
"posts.chrome": true, "posts.reactions": false, "posts.pager": true,
"posts.blocked_delay": "0", "inbox.enabled": true, "inbox.max_mb": "20",
"inbox.max_total_mb": "500", "inbox.delete_after_days": "7", "icons.working": "⚡", "privacy.redact": true,
"topics.delete_after_days": "30", "topics.notice_delay": "20", …}}`.
Missing keys take their defaults and unknown keys survive a save. The file
is read once at daemon start: edit it by hand and restart the daemon, or use
the panel, which applies a change immediately.

## Plugin updates

`/options` in General has a **Check for updates** action below the option
groups. It is an action, not a value in `options.json`. Only an operator in
the configured group may press it. The first press checks the public GitHub
releases list, including prereleases but excluding drafts, and compares
semantic versions with the installed manifest and binary. It scans every
release page within a fixed bound; a network error or incomplete scan is
shown as a failure rather than as "up to date". No GitHub token is required.
The check verifies the release's signed statement, the host binary and its
exact entry in `checksums.txt`, the release manifest version, and its minimum
Herdr version. A stable
installation is offered stable releases only; an installed prerelease may
move to a newer prerelease. Asset URLs must be `https` on `github.com`, and
downloads follow redirects only to GitHub's own and asset storage hosts.

Every release is signed offline by the maintainer. `release.txt` lists the
tag, the commit the tag points at and the `checksums.txt` lines, and
`release.txt.sig` is its SSHSIG signature by an `ssh-ed25519` key. The
updater checks the signature in Go against the keys compiled into the
running binary. It never trusts a key file from GitHub or from the
checkout. The signed digest must also match the one in `checksums.txt`.
The release then shows as `New release: vX.Y.Z · signed`. These blockers
show the release and a reason without an Update button; none of them is
reported as "up to date":

- `unsigned`: the release has no signature yet, for example in the minutes
  between publishing and signing.
- `bad_signature`: the signature does not verify, or the signed statement
  is malformed or disagrees with `checksums.txt`. Do not install that
  release; report it.
- `commit_mismatch`: a linked checkout's tag no longer points at the
  signed commit.

If a newer release is eligible, the panel shows a separate **Update** button.
That button expires after five minutes and is bound to the operator, group,
panel message, tag, and installation state. Pressing it repeats the release
and installation checks before an update worker starts. A replaced panel,
changed target, duplicate press, or press by another account cannot start
another job. There are no scheduled checks or unattended installs.

Herdr-managed GitHub installs use `herdr plugin install
permgps/herdr-telegram-agents --ref <exact-tag> --yes`. An intentional
`--ref` pin is left alone. A previous exact tag installed by this update
action is recognised from `update.json`, so it does not block a later update.
A local link stays local: its `main` branch must be clean, have the expected
GitHub origin, and be able to fast-forward to the release commit on the
remote mainline. The worker saves the old binary, stops a running daemon,
fast-forwards, and runs the release's checksum-verified install script.

The install script is given the approved SHA-256 as
`HERDR_TG_EXPECTED_SHA256` and refuses a downloaded binary that differs from
it. On a managed install that variable reaches the script only if Herdr
passes its environment to the `[[build]]` step, which is not documented.
The design does not depend on it:

- The script runs `herdr-tg version` only after a verified signature or a
  matching approved checksum. Otherwise it prints `skipped running an
  unverified binary`.
- The script writes `bin/install-receipt` (`sha256`, `approved`,
  `signature`). The worker logs it, and logs `[FIX] approved checksum did not
  reach the install script` when the receipt says `approved none`.
- The download overrides `HERDR_TG_BASE_URL` and
  `HERDR_TG_ALLOW_INSECURE_BASE` never reach the script from the daemon's
  environment.

After the install, of either kind, the worker checks the SHA-256 of the new
`bin/herdr-tg` against the approved one before anything runs it, including
reading its version. A mismatch rolls back without the new binary ever
starting.

A managed install must also resolve to the signed commit: Herdr's
`resolved_commit` after the install must equal the commit in
`release.txt`. Otherwise the worker rolls back with `commit_mismatch`, for
example when the tag moved after signing. A managed job without a signed
commit fails with `commit_unknown` before anything is touched.
Detached branches, local changes, another repository, and an incompatible
Herdr version show the release and a reason without an Update button.

The worker runs from a copy under the plugin state directory. It preserves
the prior running or stopped state. For a previously running daemon, success
requires a new daemon pid serving the target version, a healthy Herdr
connection, and a started Telegram poller. A failure after replacement makes
one rollback attempt. If rollback cannot be verified, `update.json` records
`stuck` and the panel points to manual recovery. The final outcome edits the
same General panel message, so retrying delivery cannot create a duplicate.
Checking and editing the panel does not change forum topics or create topic
service messages.

For manual recovery, inspect `update.json`, `daemon.log`, and
`update-worker.err.log` in the state directory. A managed installation can
be restored with `herdr plugin install
permgps/herdr-telegram-agents --ref <old-commit> --yes`. For a linked
checkout, keep any new local changes, then restore the recorded old commit
and the binary backup under `update-backups/<job-id>/`; run the install
script from the desired release if a fresh binary is needed. Start or
restart the daemon through the Herdr action after the files are sound.

At daemon start, when no update worker runs, the staged worker copies
(`update-worker-<job-id>`) and backups (`update-backups/<job-id>/`) of past
updates are removed; the current job keeps its own until it succeeded.

## Silence the group

The topic list shows the exact status of every agent, and every status
change is a topic edit: a Telegram service message ("X changed the topic
icon") that rings the phone in a group with sound on. The Bot API has no
silent form of it and no way to mute a group for you; the daemon deletes
its own notices after 20 seconds by default, but the push has fired by then. So the
supported setup is: **mute the group once, in Telegram, by hand**, and let
the one thing that must ring come from somewhere else.

- **Mute the group**: on the phone open the group, tap its name, then
  **Mute** (or the 🔔 in the header) and pick **Forever** or **Disable**;
  on Telegram Desktop right-click the group in the chat list and choose
  **Mute notifications** → **Disable**. Muting the group mutes every topic
  in it; a single topic can be muted the same way from its own header.
- **What still arrives, silently**: the icons keep changing exactly as
  before, the dashboard in General keeps its lines and durations current,
  done posts, `/screen` replies and daemon notices land in the topics.
  Everything is visible as soon as you open the app; nothing makes a sound.
- **What rings**: a question from an agent. With `Questions in the bot's
  chat` (the `posts.pager` option, default on) the blocked screen is posted
  into the topic without a sound and the bot sends you, in your private
  chat with it, `❓ <agent> is waiting for you`, the dialog's options or the
  last six lines of the screen, and a link that opens the topic at that
  post. The private chat is not part of the group, so it rings even though
  the group is muted, and a question rings exactly once wherever you
  listen. Answer in the topic as always: the buttons stay under the topic
  post, and nothing typed into the private chat reaches an agent.
- **Press Start once**: a bot may write to your private chat only after
  you opened it and pressed **Start**; setup does that through the
  `t.me/<bot>?start=setup_<code>` link. The daemon checks each operator's chat at
  start and when the option is switched on (log `pager reachable`); when no
  operator's chat takes messages it logs `pager unreachable: open the bot
  and press Start`, posts `⚠️ questions will ring in the topics …` into
  General and rings in the topics as before, and the `status` action line
  ends with `pager=unreachable`. The `doctor` action has an `operator chat`
  line for the same check. When every private-chat send of a question
  fails the daemon does the same for the rest of the run.
- **Quiet mode is then optional**: with the group muted there is nothing
  left for it to silence, and its catch-up rings from the bot's chat too.
  Keep it if you prefer no edits at all while you are at the desk.

`Questions in the bot's chat` off restores the old behaviour: the topic post
itself rings, and nothing is sent to the private chat.

## Quiet while at the desk

Every topic edit is a Telegram service message ("X changed the topic icon")
that rings the phone in a group with sound on, and the Bot API has no silent
form of it; the daemon deletes its own notices after 20 seconds by default,
but the push has fired by then. Quiet mode therefore holds those writes while you
are at the machine, where you see Herdr anyway, and lets Telegram catch up
when you leave. The other answer to the sound is to mute the group, see
[Silence the group](#silence-the-group).

It is off by default: a fresh install mirrors every change right away, so
you can see the plugin working. Tick `Quiet while at the desk` in
`/options` → Quiet once the service messages ring too often.

- **Presence** is the machine's input idle time, sampled every 10 seconds:
  `ioreg` (`HIDIdleTime`) on macOS, `GetLastInputInfo` on Windows. On Linux
  the daemon asks GNOME Shell first (`gdbus` call to
  `org.gnome.Mutter.IdleMonitor`, Wayland and X11), then `xprintidle` (X11;
  install it from your distribution). The source that answered is logged as
  `input idle source source=mutter|xprintidle`. Idle shorter than
  `Away after` means at the desk. Herdr's own pane focus is not used:
  another pane's agent would ring while you sit in front of it.
- **Where there is no source** (a Linux server or SSH session without
  `DISPLAY` or `WAYLAND_DISPLAY`, or neither `gdbus` nor `xprintidle`
  installed) the automatic verdict is always "away", so quiet mode never
  engages, the daemon logs one warning at start and `/away` answers that
  this machine has no input idle source. KDE Plasma on Wayland, sway and
  other Wayland desktops besides GNOME are not supported: the Mutter call
  fails there (one `presence sample failed` warning) and, with XWayland
  running, `xprintidle` sees only input to X windows, so the verdict stays
  or leans to "away" and Telegram keeps getting everything. When a source
  exists but a sample fails (a timeout, the session bus not up yet), the
  previous verdict stays, one warning `presence sample failed` is logged,
  and `presence source recovered` follows the next good sample.
- **While at the desk** (quiet on): the reconciler defers every topic write
  (create, icon, name, close, reopen) and logs `reconcile deferred: operator
  at the desk (quiet)` once per period; blocked and done screens follow
  `Screen posts` (silent by default). Everything you send to agents, `/screen`,
  `/status` and the buttons keep working. `/status` starts with `🔕 quiet: you
  are at the desk (/away to override)`.
- **When you leave** (idle passes the threshold, `/away`, or quiet mode is
  switched off in the panel): one reconcile pass creates, renames, closes and
  repaints only the topics that drifted, then every agent still blocked whose
  question never rang is posted again with a sound (from the bot's private
  chat while `Questions in the bot's chat` is on). A question rings once:
  the flag is set by any blocked post with sound and cleared when the agent
  leaves blocked, so touching the mouse for a moment and leaving again rings
  nothing new. With `Re-announce on leaving` off only agents without any post
  (a new agent, `Held` mode) are posted. The log says `quiet off: operator
  away, catching up` and `catch-up done` with the counts.
- **`/away [2h]` and `/here`** in General: `/away` forces "away" until
  `/here`, `/away 2h` (any Go duration from `1m` to `168h`) for that long,
  then the automatic verdict rules again. `/status` shows `🏃 away (manual)
  until 14:30`. Nothing is persisted: a daemon restart returns to automatic.
  In a topic both commands answer `presence commands live in General`.
- **At start** presence is sampled before the first reconcile, so a daemon
  started while you are typing edits no icon; the catch-up follows when you
  leave. The `status` action line ends with `quiet=on|away|away-manual|off`
  (`off` when the option is off or the platform has no idle source).

## Secrets in posts

Every text that leaves the daemon for Telegram (blocked and done posts,
`/screen` and `/screen all`, the `.txt` document, the follow-up of a
forwarded Claude Code command, summary footers, document names, the labels
of inline buttons, panel edits, topic names, button toasts and the sharing
panel) passes one redaction step while `Redact secrets` is on. HTML posts
(including the question sent to the private chat) are redacted between tags
only, so a masked value never swallows a closing tag, and each text run is
read unescaped (`API_KEY='…'`, not `API_KEY=&#39;…&#39;`) and escaped again
when something was masked:

- API keys and tokens keep a recognisable prefix and their last four
  characters: `sk-…a1b2` (OpenAI and Anthropic), `ghp_…9f3e` and
  `github_pat_…`, `AKIA…Q7ZX` / `ASIA…`, `xoxb-…d8c1` (Slack), `glpat-…5tq2`
  (GitLab), `AIza…w9Yc` (Google), `eyJ…7hJk` (JWT), `Bearer …k2m4`,
  `Basic …ZA==` (HTTP Basic), `sk_live_…`/`rk_test_…` (Stripe).
- Values of keys that contain `password`, `passwd`, `pwd`, `secret`,
  `token`, `api_key`, `access_key`, `private_key` or `credential` (also
  inside a longer name such as `DB_PASSWORD`, `AWS_SECRET_ACCESS_KEY`,
  `client_secret` or `?access_token=`, with `=` or `:`, and JSON keys such
  as `"password": "…"`) become `[redacted]`; the key stays. A query value
  ends at `&`. Agent replies are redacted as Markdown, before rendering, so
  a key in bold, italics or a code span (`**API_KEY**: …`,
  `` `DB_PASSWORD`=… ``) counts too, and so does a table row
  `| API_TOKEN | … |` whose value has a digit or a symbol in it (a header
  row such as `| Token | Description |` stays).
- Credentials in a URL keep the scheme and user:
  `postgres://app:[redacted]@db/app`.
- The bot token itself, any string shaped like a Telegram bot token and
  `-----BEGIN … PRIVATE KEY-----` blocks (to the `END` line, or to the end of
  the screen when it is cut) become `[redacted]`. Both token forms are found
  also when the terminal wrapped them across lines (a newline with
  indentation between two characters).

The bot token is masked even with `Redact secrets` off; the log then says
`[FIX] bot token masked while redaction is off`. Every pattern has a
minimum length, so ordinary words such as `token: none` or `password reset`
are left alone. The log records how many replacements
of which kind were made (`secrets redacted kinds="openai=1"`), never the
value. The daemon log and the Herdr side are not touched.

Error replies in the group never quote local absolute paths: a known failure
answers a fixed text (`⚠️ not a git repository`), anything else keeps its
first line with each absolute path cut to `…/<file name>`. The doctor action
keeps the full details.

## Topic cleanup

Once a day, at daemon start and as soon as `Delete closed topics after`
changes, the daemon looks for the closed topics of exited agents whose
mapping entry has not changed for longer than the chosen age (30 days by
default) and deletes them through `deleteForumTopic`, which needs the
**Delete messages** right; the entries are then forgotten. Reopening such a
topic by hand takes it out of the sweep until it is closed again, and the
clock restarts from the last change. Live agents, open topics and the
General topic are never candidates. At most 50 topics go per pass; while
eligible topics remain, the daemon schedules another pass after one minute.
If a pass deletes none, it waits five minutes before retrying. While sync is
off, while the bot cannot manage
topics or while it lacks **Delete messages** the sweep does nothing and
says so in the log (once per run for the missing right). `Off` disables it
and retains every topic and mapping entry. The mapping can grow when cleanup
is off or the bot lacks the required rights; entries are never discarded
merely to meet a size limit.

Before creating a topic or the dashboard message, the daemon saves a pending
creation marker in `mapping.json`. It clears the marker only after saving the
Telegram thread or message id. If the Telegram result is uncertain, or the
result cannot be saved, the marker prevents an automatic duplicate after a
restart. The daemon logs the affected agent key or dashboard message id with
`[FIX]`. Resolve a pending marker only after checking Telegram: retain the
existing topic or message and restore its id in the mapping when it exists;
clear the marker only when the external creation definitely did not happen.

## Inbox

Files sent to a topic land in `inbox/` under the plugin state dir
(directory mode 0700, files 0600), named `<yyyymmdd-hhmmss>-<message
id>-<safe name>`; what the agent receives and how albums and refusals work
is described under [Files from the phone](commands.md#files-from-the-phone).
The download runs on its own goroutine with a 60 s limit and never blocks
the daemon's message loop; the bytes are capped at `Largest file` even when
Telegram under-reports the size. 1.5 s after the prompt the daemon presses
`enter` once more unless the screen shows a dialog: Claude Code turns a
pasted image path into an `[Image #N]` attachment asynchronously and drops
the `enter` that came with the paste (seen 2026-09-06), and on an already
empty line the extra `enter` does nothing. The log says `inbox prompt
submitted`. Once a day, at daemon start and as soon as
`Delete files after` changes, files not modified for longer than that age
are deleted; subdirectories and files of a save in flight are left alone.
`Off` keeps every file. The sweep logs `inbox sweep deleted=<n>`.

Files from private Control recipients are stored in the same directory with a
`shared-` prefix. Together they may fill at most a quarter of `Inbox size`; a
recipient's save makes room only by deleting older `shared-` files and is
refused when the owner's files leave no room. The owner's saves may still
delete any oldest file, shared or not.

## Operators and observers

Two kinds of Telegram accounts may talk to the bot, both listed by user id
in `config.json`:

- **Operators** (`operator_ids`) drive the agents: everything in
  [Talking to agents](commands.md) is theirs. The setup wizard records the
  account that ran it as the only operator; more ids are added by editing
  the file with the daemon stopped. Questions ring in the operators' private
  chats with the bot (`Questions in the bot's chat`); observers are never
  paged.
- **Observers** (`observer_ids`) see the group like any member and may use
  `/status` and `/help` in General, nothing else: a message in a topic, a
  plain message in General, a button press or any other command is dropped
  without a reply and logged at debug with `reason=observer` (a press still
  answers `not allowed` so the phone stops spinning). The list is managed
  from General without a restart:

  | You write | What happens |
  |-----------|--------------|
  | `/observers` | operators, observers and the accounts seen recently that are neither, newest first (name, `@username`, the id, when and where), followed by `add one with /observers add <id>` |
  | `/observers add <id>` | saves the id to `observer_ids`, tells the daemon at once and answers `👁 <id> is an observer now: sees the group, may use /status and /help in General`; an operator id, a duplicate or an id that is not a positive number is refused with `⚠️ <reason>` |
  | `/observers remove <id>` | the reverse; an id that is not an observer answers `⚠️ <id> is not an observer` |

  A failed save answers `⚠️ config not saved: <reason>` and changes nothing.
  An id on both lists is an operator.
- **Strangers**, anyone else, are ignored. The first message, button press
  or command from an unknown account within ten minutes is logged at info as
  `unknown sender from_id=<id> name=… username=… thread_id=…`, which is where
  the id for `/observers add` comes from; the same accounts appear under
  `Seen recently, not allowed` in `/observers` (the last ten, kept in memory
  until the daemon restarts). Later messages from the same account within
  the window are dropped at debug only. `doctor` counts both lists in its
  config line (`1 operator, 2 observers`).

## Starting without a network

A daemon that starts before the network is up (login, wake from sleep)
keeps trying Telegram instead of exiting: nothing would start it again.
Each attempt checks the token, clears the webhook and loads the topic
icons, together at most 15 s; the waits between attempts are 2 s, 4 s, 8 s
and so on up to a minute, longer when Telegram answers 429 with a
`retry_after`. Herdr shows one notice at the first failure (`Telegram
Agents is waiting for Telegram (<reason>); retrying, see the status
action`) and one when Telegram answers (`… reached Telegram after 2m10s`);
`daemon.log` has a `telegram unreachable, retrying` warning per attempt.

While it waits, `status` answers `version=… pid=… uptime=… telegram=waiting
attempt=N` (`telegram=connecting` before the first failure) and `stop` ends
the daemon cleanly, with exit code 0 and no "could not start" notice. A
rejected token (401), a request Telegram refuses (400, 403, 404) or a
broken Herdr socket still stop the start at once with `Telegram Agents could
not start: …`.

## Files, logs and state

| File | Location | Content |
|------|----------|---------|
| `config.json` | Herdr plugin config dir (`HERDR_PLUGIN_CONFIG_DIR`), mode 0600 | bot token, chat id and title, operator ids, observer ids (`observer_ids`, written by `/observers`), log level |
| `mapping.json` | Herdr plugin state dir (`HERDR_PLUGIN_STATE_DIR`) | agent to topic mapping, the dashboard message id (`dashboard_message_id`), and pending creation markers; exited entries stay until topic cleanup confirms deletion |
| `sharing.json` | state dir, mode 0600 | recipients, grants, revisions, private topics, dashboard IDs and pending creation intents |
| `options.json` | config dir, mode 0600 | the `/options` choices |
| `inbox/` | state dir, mode 0700, files 0600 | attachments sent to topics, swept daily after `Delete files after` |
| `daemon.pid` | state dir | pid of the running daemon |
| `claude-usage.json` | state dir, mode 0600 | Claude's two usage windows and when the status line tap saw them, written by `usage-tap` from Claude Code's status line; see [Quota lines](#quota-lines) |
| `update.json` | state dir, mode 0600 | current or last update job, phases, source, versions, rollback data and notification state |
| `update.lock/` | state dir | exclusive worker ownership; a dead owner can be recovered |
| `update-worker-<job-id>` and `update-worker.err.log` | state dir | detached worker copy and its stderr; `.exe` on Windows |
| `update-backups/<job-id>/` | state dir | old linked binary kept for guarded rollback |
| `daemon.log`, `daemon.log.1`, `daemon.log.2` | state dir | JSON log, rotated at 5 MiB |
| `daemon.err.log` | state dir | stderr of the last daemon start |
| `control.sock` | state dir | the daemon's control channel for the stop, resync and status actions (a named pipe on Windows, so no file) |

The config and state directories are created 0700 and the files above 0600.
On Unix every daemon start tightens the two directories, `inbox/` and the
files named above to those modes when an older build or a manual change left
them readable by others, and logs each change (`[FIX] permissions
tightened`). Other files in those directories and symlinks are not touched.

`stop`, `resync` and `status` reach the daemon through that control channel.
A daemon from an older build that does not answer still receives SIGTERM or
SIGHUP on Unix and is killed if it answers neither. The `status` action prints
the daemon's own line: `version=… pid=… uptime=… agents=… dropped=… herdr=ok|failing
since … sync=on|off cleanup=<n>d|off quiet=on|away|away-manual|off
pager=on|off|unreachable telegram=ready` while polling has started. A
daemon still reaching Telegram answers `version=… pid=… uptime=…
telegram=connecting|waiting attempt=N` instead; see [Starting without a
network](#starting-without-a-network).

`LOG_LEVEL=debug|info|warn|error` in Herdr's environment overrides the level
saved in `config.json` (default `info`). The daemon writes JSON lines to
`daemon.log` in the state dir; the logs action renders them as
`15:04:05 INFO message key=value`. Control characters (ESC, BEL, C1) and bidi
overrides in a line, for example in a stranger's Telegram name, are shown
escaped (`\x1b`, `\u202e`) and never reach the terminal.
Message text never reaches the log, not even at debug: command lines carry
its length (`text_runes`). While Telegram is unreachable a failed poll is
warned once and then at most once per 10 minutes, with the number of
failures in between (`suppressed`); the others are at debug. When a rotation
step fails (a viewer holding `daemon.log.1` open on Windows, a full disk),
the daemon keeps appending to `daemon.log`, prints the error once to
`daemon.err.log` and tries again a minute later.
Delete `mapping.json` while the daemon is
stopped to forget every topic; the next start creates fresh ones and leaves the
old topics untouched. Entries of exited agents are no longer dropped by age;
the [topic cleanup](#topic-cleanup) deletes the topic and the entry together.
Delete `options.json` in the config dir to return every option to its
default.

## Private mirror lifecycle and recovery

A mirror binds a numeric recipient, private chat/topic, grant revision and exact
agent session. It cannot follow a replacement session merely because its name
or working directory matches. An exited session rejects input; verified resume
can reuse the topic. The moment Herdr reports a different session in the pane,
or the pane closes, exits or releases its agent, the old session stops
authorizing prompts, keys and screen reads, even before the next agent list
confirms the change and while that list keeps failing; a list that still shows
the old session restores it. The owner's group topic treats the old session
the same way in that window (a message gets "agent has exited") and follows
the agent at the next list as usual. Incomplete session identity requires owner reapproval after
restart. Private topics are never closed or reopened through supergroup APIs.
The existing exited-topic retention setting can remove old exited mirrors;
policy records remain to prevent stale work from regaining access.

Revocation and expiry deny new dispatches, cancel pending work and invalidate
buttons. Already dispatched messages and running agent work cannot be recalled.
History remains until the owner explicitly deletes the mirror or exited-topic
retention applies. Permission changes are saved before success is reported. If
saving a revocation fails, access is denied in memory, but **do not restart until
saving succeeds**: the older on-disk policy may still allow access. Metadata and
presentation changes are coalesced and flushed at shutdown.

`sharing.json` is a separate versioned, atomically replaced mode-0600 file in the
plugin state directory. Corrupt or unsupported state disables guest access and
preserves the file; owner mapping remains independent. Switching bots does not
reuse private bindings from the previous bot. An unknown topic-creation outcome
keeps a durable intent. Use the owner repair action after checking the recipient's
chat: Telegram cannot enumerate every topic whose creation reply was lost.
The overview has its own **service-repair-confirm** action for this case.

Private output is always redacted. It uses exact-session OpenCode replies where
available, otherwise the exact target's terminal screen. The Claude reader that
selects a transcript by working directory is never used for guests. Automatic
reply delivery and a bare `/screen` both exclude replies written before
activation and show the screen instead. A done post whose OpenCode turn is
still running waits like the owner's topics (up to 3 retries 5 s apart, then
the screen), except in a mirror set to `/display screen`. Explicit `/screen`
can reveal older material still visible on the screen of that session; `/screen all`
exports only screens captured since activation, not the owner's earlier buffer.

Blocked output has one notification in each active private topic; done posts are
silent. There is no additional recipient pager. Owner desk presence does not
silence recipients; global sync-off stops automatic mirror output and status
edits. Local pause and silent mode default off. Edit/pin service notices use the
existing notice delay (20 seconds by default), or Keep. Creation notices remain.
Private cleanup uses the actual chat and does not require group admin rights.
Client icon propagation and service-notice sounds still require the
[phone/Desktop acceptance checks](testing.md#private-sharing-acceptance).

Bounds: 10,000 registered contacts, 2,000 grants, 64 pending albums with ten files
each, 20 MiB per file and 40 MiB per album, four concurrent private transfers,
4,096 temporary private callbacks and 2,048 owner panel references. Private API
calls use four separate bounded queues and a two-second call budget. Ambiguous
private calls are not automatically replayed. A failed post may require another
screen request. Durable first-contact registration happens before acknowledging
Telegram updates; storage failures at this boundary can delay polling until
storage recovers. At most 30 new contacts are registered per minute; further
first contacts are refused without a reply and their updates acknowledged.
`sharing.json` is capped at 8 MiB; a first contact that would not fit is
refused like a full directory (its update acknowledged), never retried. Once the
directory is full, the "directory is full" reply goes at most once per contact
and to eight contacts in total per ten minutes.
Private event admission allows eight requests per recipient and 32 overall per
ten seconds for recipients with a grant; contacts without one share a separate
budget of eight per ten seconds and get no congestion notice. A separate
32-slot bridge queue follows. Congestion notices are bounded and best effort;
contact registration remains durable even when an agent request is rejected.
The chat menu is republished only when its command list changes. Previously
dropped or expired Telegram updates are unrecoverable.

A recipient who blocks the bot (Telegram answers 403 in the private chat) is
marked unavailable and their sends fail on their own; the daemon keeps
running. Only a 403 in the owner group stops it.

## See Also

- [Talking to agents](commands.md): what gets posted and what you can send
- [Silence the group](#silence-the-group): mute the group once, let questions ring from the bot's chat
- [Operators and observers](#operators-and-observers): who may drive the agents, who may only watch, and how strangers show up
- [README: Setup](../README.md#setup): start, stop, resync, status, logs, doctor and the test message from Herdr
- [Development](development.md): building from source and the tree layout
