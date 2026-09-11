# Sessions

Ronin stores sessions in a local SQLite database. `--resume` selects the most recently updated session whose working directory matches the current project.

The TUI provides these session-history commands:

- `/rewind` — choose a prior prompt, confirm, and rewind the current session to immediately before it.
- `/fork` — create a child session from immediately before a selected prior prompt.
- `/compact` — compact the current conversation context.

Rewind and fork change conversation history only. They do not restore files or modify the Git working tree. After either operation, the selected prompt is placed back in the editor for revision.

The database is stored at:

- `$XDG_DATA_HOME/ronin/ronin.db` when `XDG_DATA_HOME` is set;
- `$HOME/.local/share/ronin/ronin.db` otherwise.

Session data is separate from configuration. The current database format is version 2. Older database formats are rejected without migration or deletion. Back up the old database and choose a fresh data directory, or explicitly reset it using the instructions below.

To back up sessions, copy `ronin.db` while Ronin is not running. To reset all persisted sessions, remove `ronin.db` and its `-wal` and `-shm` companion files while Ronin is not running; Ronin recreates the database on its next start.

## Recovering compacted context

The model can use `conversation_history` to retrieve model-visible messages
omitted by compaction. Compaction facts carry `history:` references; retrieval
also supports text search, search pagination, and byte-offset paging of long
messages. Each call returns at most 20 messages and 32 KiB of content, with at
most 8 KiB per message.

Retrieval never reads local `!` command events, unrelated sessions, or parent
sessions. Rewind and fork persist an explicit retained archive alongside their
context snapshot; discarded branches are excluded, including after restart.
A reference is not authorization: unavailable or
discarded references return no matches.

Compaction remains bounded and lossy, but original retained messages remain
recoverable. User requirements receive priority, and known tool outputs preserve
status fields and selected failure diagnostics separately from text excerpts.
Context budgeting includes instructions and tool schemas, reserves output space,
and calibrates conservative estimates upward from reported input usage. It is
not an exact tokenizer; provider overflow recovery remains the final safeguard.

## Local shell commands

In the TUI, submit text beginning with `!` to run a local shell command, for example:

```text
!git status --short
!go test ./... 2>&1 | tail -30
```

Commands run non-interactively in Ronin's working directory. Output streams into
scrollback, and completion shows the exit code or error. Use the normal cancel
key to stop execution. A bare `!` does nothing. While a shell command is running,
other prompts and commands are rejected rather than queued.

Commands, captured stdout/stderr, and completion status are saved in session
history and restored on resume, but **never sent to the model**, including during
compaction. Rewind retains this local execution audit; a fork starts a separate
local audit. An execution without a saved completion is shown as interrupted on
resume. Rewinding does not undo filesystem changes made by commands.

Capture is limited to 128 KiB per output stream, with visible truncation. Commands
use the shell tool's existing timeout (five minutes). Interactive programs and
persistent shell state (such as `cd` affecting later commands) are not supported.
Shell commands execute with your normal local permissions, and their output may
contain secrets that will be stored in the session database.

[Back to README](../README.md)
