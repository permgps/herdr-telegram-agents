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
  ❓ blocked, 🏆 done, 👀 unknown, 🏁 exited. The icons come from Telegram's free topic-icon pack;
  the colour is the fallback when the pack lacks an emoji.
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

## Logs and state

`LOG_LEVEL=debug|info|warn|error` in Herdr's environment overrides the level
saved in `config.json` (default `info`). The daemon writes JSON lines to
`daemon.log` in the state dir; the logs action renders them as
`15:04:05 INFO message key=value`. Delete `mapping.json` while the daemon is
stopped to forget every topic; the next start creates fresh ones and leaves the
old topics untouched.

The files themselves are listed in the README under
[Setup](../README.md#setup).

## See Also

- [README: Talking to agents](../README.md#talking-to-agents): what gets posted and what you can send
- [README: Actions](../README.md#actions): start, stop, resync, status and logs from Herdr
- [Development](development.md): building from source and the tree layout
