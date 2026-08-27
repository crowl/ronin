# ronin

*ronin* is an experimental coding harness written in Go. It gives an LLM a focused terminal UI and tools to inspect, edit, and test code.

<p align="center">
  <img src="assets/ronin-demo.gif" alt="Ronin edits and tests a Go CLI">
</p>

## Features

- Interactive terminal UI and non-interactive prompt mode
- OpenAI, Gemini, and Anthropic models
- File reading, editing, shell commands, and Go-aware code navigation
- Persistent sessions with optional resume
- Project instructions through `AGENTS.md`
- Reusable skills and Lua workflows

## Quick start

Download a binary for Linux, macOS, or Windows from [GitHub Releases](https://github.com/crowl/ronin/releases), then set an API key for at least one provider:

```sh
export OPENAI_API_KEY=...
# or GEMINI_API_KEY=...
# or ANTHROPIC_API_KEY=...
```

Start Ronin in a project directory:

```sh
cd /path/to/project
ronin
```

To run from source instead, use Go 1.27.0 or newer:

```sh
git clone https://github.com/crowl/ronin.git
cd ronin
go run ./cmd/ronin
```

The default configuration selects an OpenAI model. Select another registered model with `-model`:

```sh
ronin -model anthropic:claude-sonnet-4-6
```

## Common usage

Resume the latest session for the current project:

```sh
ronin --resume
```

Work in another directory:

```sh
ronin -working_dir /path/to/project
```

Run a single prompt without opening the TUI:

```sh
ronin -prompt "summarize this project"
```

Use `Ctrl+O` to expand tool output in the TUI. Press Escape to cancel an active operation.

## Session persistence

Ronin stores sessions in a local SQLite database. `--resume` selects the most recently updated session whose working directory matches the current project.

The database is stored at:

- `$XDG_DATA_HOME/ronin/ronin.db` when `XDG_DATA_HOME` is set;
- `$HOME/.local/share/ronin/ronin.db` otherwise.

Session data is separate from configuration. Existing sessions from versions that used files under the configuration directory are not migrated.

To back up sessions, copy `ronin.db` while Ronin is not running. To reset all persisted sessions, remove `ronin.db` and its `-wal` and `-shm` companion files while Ronin is not running; Ronin recreates the database on its next start.

## Configuration

Ronin creates `config.json` in `$XDG_CONFIG_HOME/ronin`, or in `$HOME/.config/ronin` when `XDG_CONFIG_HOME` is unset. It contains the default model, reasoning level, maximum turns, tool-output summarization settings, and optional MCP servers.

### MCP servers

Ronin can start MCP servers that communicate over stdin/stdout and expose their tools to the model. Configure servers by name in `config.json`:

```json
{
  "mcp_servers": {
    "gopls": {
      "command": "gopls",
      "args": ["mcp"]
    }
  }
}
```

MCP tools are namespaced as `<server>__<tool>`, such as `gopls__go_search`. Commands run in Ronin's working directory and inherit its environment. Add an `env` object to override environment variables for a server. Ronin fails startup if a configured server cannot start, initialize, or list its tools.

Server stderr is written to one log per server under `$XDG_DATA_HOME/ronin/logs/mcp`, or `$HOME/.local/share/ronin/logs/mcp` when `XDG_DATA_HOME` is unset. Each log is truncated when Ronin starts and capped at 10 MiB.

If an MCP server returns `instructions` in its standard initialization response, Ronin includes them in the system prompt together with the server's namespaced tool names.

Provider-compatible proxies can be configured with `OPENAI_BASE_URL`, `GEMINI_BASE_URL`, or `ANTHROPIC_BASE_URL`. Each value must be an API root rather than a complete operation endpoint.

### Project instructions and skills

Ronin loads:

- a global `AGENTS.md` from the configuration directory;
- local `AGENTS.md` files from the project directory and its parents;
- skills from `<config dir>/skills/<skill name>/SKILL.md`.

### Workflows

Lua workflows coordinate fresh agent conversations that share the same working directory. Run one directly with:

```sh
ronin -working_dir /path/to/project run workflow.lua "describe the task"
```

Named workflows placed in `<config dir>/workflows` are available from the TUI. See [`testdata/workflow.lua`](testdata/workflow.lua) for a bounded plan, implement, and review loop.

## Status

Ronin is experimental. Expect rough edges and breaking changes. It is inspired by [Pi](https://pi.dev/) and built as a way to explore coding-agent design in Go.

## License

[BSD 3-Clause](LICENSE)
