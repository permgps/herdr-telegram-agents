[Back to README](../README.md) · [Next: Development →](development.md)

# How the sync behaves

The rules the daemon follows when it mirrors agents into topics, and where its
logs and state live.

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
- Agents are identified by pane and terminal id; a topic is created the first
  time an agent appears and reused after a restart.
- When an agent's pane closes the topic gets the 🏁 icon and is closed. If the
  same agent comes back in that pane (for example `claude --resume`), the
  finished topic is reopened and refreshed instead of a new one being made. Topics
  of agents that vanished while the daemon was down are closed on the next
  start.
- The daemon exits by itself when the Herdr socket is gone for 60 s, when the
  bot token is rejected, when another process polls the same bot, or when the
  bot is removed from the group. Losing **Manage topics** only pauses edits
  until the right is granted again.
- Every status change makes Telegram post a "changed the topic icon" notice
  into the topic. The daemon deletes its own notices right away, which needs
  **Delete messages**; without that right they stay and the log says so once.
  Topic creation notices cannot be deleted and remain.
- Rename a topic by hand and the change goes back to Herdr: the tab is
  renamed (`tab.rename`), which is what the Agents panel shows on its first
  line, or the custom agent name when the agent has one (`agent.rename`).
  The `<workspace> · ` prefix is optional; an empty remainder is ignored for
  a tab and clears a custom name. The topic settles on the canonical form.
- Close a topic by hand and the mirror goes quiet for that agent: no icon
  edits, no screen posts, until you reopen it. Reopening refreshes name and
  icon; if the agent exited meanwhile the topic gets 🏁 and is closed again.

## Questions and buttons

- A blocked agent's screen is posted as a code block. When it ends in a
  numbered dialog with two to five real options, the post also carries one
  inline button per option, labelled with the option's number and text.
  Claude Code's `Type something.` and `Chat about this` entries are not
  offered as buttons and do not count towards the five; the other options
  keep their numbers, so replying with a digit still works as before.
- A press sends the option's digit as a key (`agent.send_keys`), answers
  with a short toast, and replaces the keyboard with a single `✅ <n> · <text>`
  button; pressing that one says `already answered`. The daemon then reads
  the screen again after the usual settle delay, so the next question of a
  multi-step dialog is posted with its own buttons.
- Buttons are removed lazily: before a newer screen is posted for the same
  agent, when the agent exits, or when a press arrives for an agent that is
  no longer blocked, has exited, or for a message that is not the latest
  question. Such a press does nothing but show a notice.
- Known limit: in a multi-select dialog a press toggles the option; the
  operator submits with `enter`. Only operators can press; anyone else gets
  `not allowed`.

## Options

`/options` in the General topic answers with one message that is edited in
place as you press its buttons; only operators can press them, and every
string on it is English. A new `/options` retires the previous panel's
keyboard, `✖ Close` leaves a one-line-per-option summary behind. The buttons
carry everything they need, so a panel still works after the daemon was
restarted.

- **Level 1** lists the groups (Sync, Appearance, Privacy, Topics) with a
  description each.
- **Level 2** lists the options of a group: a checkbox toggles on the spot
  (`☑` / `☐`), a choice shows its current value and opens the picker,
  `↺ Reset to defaults` restores the whole group, `‹ Back` and `✖ Close`
  navigate.
- **Level 3** is the picker for a choice: for an icon, the emoji of
  Telegram's topic-icon pack in the pack's order, eight per row, two pages,
  the current value in brackets (only those emoji can be topic icons, which
  is why there is no free-text field); for the cleanup age, one row of
  `Off 7d 14d 30d 60d 90d`.

The options today:

| Option | Group | What it does |
|--------|-------|--------------|
| `Herdr → Telegram sync` | Sync | Default on. Off: the daemon creates, edits and closes no topic and posts no screen until it is on again. Messages, keys, `/screen`, `/status` and presses on existing question buttons keep working, the screen capture keeps running, daemon notices keep posting. Back on: a full resync, like the `resync` action. A daemon that starts with sync off says so in its started notice, in the `/status` header (`🔇 …`), in the `status` action line (`sync=off`) and in the log. |
| `working` … `exited` | Appearance | The topic icon of each status and the emoji `/status` prints. Picking an emoji another status already uses answers `used by <status>` and changes nothing. A pick repaints every live topic at once (a `resync`), or when sync comes back on. |
| `Redact secrets` | Privacy | Default on. Every text the daemon posts passes the redaction step described under [Secrets in posts](#secrets-in-posts). Off: raw text. A change applies to the next post. |
| `Delete closed topics after` | Topics | Default 30 days. The topic of an exited agent is deleted once it has been closed for that long, see [Topic cleanup](#topic-cleanup). `Off` keeps every topic. A number outside the picker's list (say `45`) can be typed into `options.json` by hand; the panel shows it without a bracketed button. |

Values are saved in `options.json` next to `config.json` (mode 0600) as
`{"version": 1, "values": {"sync.enabled": true, "icons.working": "⚡",
"privacy.redact": true, "topics.delete_after_days": "30", …}}`.
Missing keys take their defaults and unknown keys survive a save. The file
is read once at daemon start: edit it by hand and restart the daemon, or use
the panel, which applies a change immediately.

## Secrets in posts

Every text that leaves the daemon for Telegram (blocked and done posts,
`/screen` and `/screen all`, the `.txt` document, the follow-up of a
forwarded Claude Code command, the labels of inline buttons, panel edits)
passes one redaction step while `Redact secrets` is on:

- API keys and tokens keep a recognisable prefix and their last four
  characters: `sk-…a1b2` (OpenAI and Anthropic), `ghp_…9f3e` and
  `github_pat_…`, `AKIA…Q7ZX` / `ASIA…`, `xoxb-…d8c1` (Slack), `glpat-…5tq2`
  (GitLab), `AIza…w9Yc` (Google), `eyJ…7hJk` (JWT), `Bearer …k2m4`.
- `password=`, `passwd=`, `pwd=`, `secret=`, `token=` and `api_key=` values
  (also with `:`) become `password=[redacted]`; the key stays.
- The bot token itself, any string shaped like a Telegram bot token and
  `-----BEGIN … PRIVATE KEY-----` blocks (to the `END` line, or to the end of
  the screen when it is cut) become `[redacted]`.

Every pattern has a minimum length, so ordinary words such as `token: none`
or `password reset` are left alone. The log records how many replacements
of which kind were made (`secrets redacted kinds="openai=1"`), never the
value. Topic names, the daemon log and the Herdr side are not touched.

## Topic cleanup

Once a day, at daemon start and as soon as `Delete closed topics after`
changes, the daemon looks for the closed topics of exited agents whose
mapping entry has not changed for longer than the chosen age (30 days by
default) and deletes them through `deleteForumTopic`, which needs the
**Delete messages** right; the entries are then forgotten. Reopening such a
topic by hand takes it out of the sweep until it is closed again, and the
clock restarts from the last change. Live agents, open topics and the
General topic are never candidates. At most 50 topics go per pass; the rest
wait for the next one. While sync is off, while the bot cannot manage
topics or while it lacks **Delete messages** the sweep does nothing and
says so in the log (once per run for the missing right). `Off` disables it;
`mapping.json` then keeps the entries of exited agents until it holds more
than 500, when the oldest exited ones are dropped without touching Telegram
(as before the cleanup existed).

## Logs and state

`LOG_LEVEL=debug|info|warn|error` in Herdr's environment overrides the level
saved in `config.json` (default `info`). The daemon writes JSON lines to
`daemon.log` in the state dir; the logs action renders them as
`15:04:05 INFO message key=value`. Delete `mapping.json` while the daemon is
stopped to forget every topic; the next start creates fresh ones and leaves the
old topics untouched. Entries of exited agents are no longer dropped by age;
the [topic cleanup](#topic-cleanup) deletes the topic and the entry together. Delete `options.json` in the config dir to return every
option to its default.

The files themselves are listed in the README under
[Setup](../README.md#setup).

## See Also

- [README: Talking to agents](../README.md#talking-to-agents): what gets posted and what you can send
- [README: Actions](../README.md#actions): start, stop, resync, status and logs from Herdr
- [Development](development.md): building from source and the tree layout
