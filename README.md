# ronin

*ronin* is an experimental coding harness written in Go. It gives an LLM a focused terminal UI and tools to inspect, edit, and test code.

<p align="center">
  <img src="assets/ronin-demo.gif" alt="Ronin edits and tests a Go CLI">
</p>

## Features

- Interactive terminal UI and non-interactive prompt mode
- OpenAI, Gemini, Anthropic, and xAI models
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
# or XAI_API_KEY=...
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
ronin -model xai:grok-4.6
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

The TUI provides these session-history commands:

- `/rewind` — choose a prior prompt, confirm, and rewind the current session to immediately before it.
- `/fork` — create a child session from immediately before a selected prior prompt.
- `/compact` — compact the current conversation context.

Rewind and fork change conversation history only. They do not restore files or modify the Git working tree. After either operation, the selected prompt is placed back in the editor for revision.

The database is stored at:

- `$XDG_DATA_HOME/ronin/ronin.db` when `XDG_DATA_HOME` is set;
- `$HOME/.local/share/ronin/ronin.db` otherwise.

Session data is separate from configuration. Existing sessions from versions that used files under the configuration directory are not migrated.

To back up sessions, copy `ronin.db` while Ronin is not running. To reset all persisted sessions, remove `ronin.db` and its `-wal` and `-shm` companion files while Ronin is not running; Ronin recreates the database on its next start.

## Configuration

Ronin creates `config.json` in `$XDG_CONFIG_HOME/ronin`, or in `$HOME/.config/ronin` when `XDG_CONFIG_HOME` is unset. It contains the default model, reasoning level, maximum turns, and optional MCP servers.

Ronin merges an embedded provider and model catalog with optional `providers` overrides in `config.json`. Overrides are keyed by provider and model name, so a small pricing correction does not require copying the full catalog:

```json
{
  "providers": {
    "openai": {
      "models": {
        "gpt-5.5": {
          "pricing": {
            "input": 2.0,
            "output": 10.0,
            "cache_read": 0.2,
            "cache_write": 2.5
          }
        }
      }
    }
  }
}
```

Pricing rates are USD per million tokens. Providers and models can be disabled with `"enabled": false`.

Custom providers use one of the built-in `openai`, `anthropic`, or `google` adapters while keeping API keys in environment variables:

```json
{
  "providers": {
    "openrouter": {
      "adapter": "openai",
      "base_url": "https://openrouter.ai/api/v1",
      "api_key_env": "OPENROUTER_API_KEY",
      "models": {
        "anthropic/claude-sonnet": {
          "context_window": 200000,
          "reasoning": {
            "mode": "effort",
            "levels": ["off", "low", "medium", "high"]
          },
          "pricing": {
            "input": 3.0,
            "output": 15.0,
            "cache_read": 0.3,
            "cache_write": 3.75
          }
        }
      }
    }
  }
}
```

The TUI only offers reasoning levels supported by the active model. Switching to a model that does not support the current level selects the nearest supported level, preferring the higher level on a tie. The status bar reports the estimated cumulative main-conversation cost; `$?` means at least one call could not be priced.

### MCP servers

Ronin can start MCP servers over stdin/stdout or connect to externally managed MCP servers over HTTP/SSE. Configure servers by name in `config.json`:

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

For a server managed outside Ronin, configure its SSE endpoint instead:

```json
{
  "mcp_servers": {
    "gopls": {
      "url": "http://127.0.0.1:3000"
    }
  }
}
```

Each server must set exactly one of `command` or `url`. Configured MCP servers are opt-in: Ronin does not connect them automatically. In the TUI, activate a server with its generated slash command, such as `/mcp:gopls`. Activation applies to the current process and future sessions started with `/new`; activating an already active server is a no-op.

For non-interactive prompts and workflow runs, use the repeatable `--mcp` flag:

```sh
ronin --mcp gopls --prompt "inspect this package"
ronin --mcp gopls --mcp github --prompt "review this repository"
ronin --mcp all run workflow.lua "input"
```

With no `--mcp` flag, no MCP servers are connected. `--mcp all` activates every configured server and cannot be combined with named selections. Unknown server names and connection, initialization, or tool-listing failures stop the non-interactive command with an error.

MCP tools are namespaced as `<server>__<tool>`, such as `gopls__go_search`. Commands run in Ronin's working directory and inherit its environment; add an `env` object to override environment variables for a command. Remote servers receive Ronin's working directory as an MCP workspace root.

Server stderr for command-based servers is written to one log per server under `$XDG_DATA_HOME/ronin/logs/mcp`, or `$HOME/.local/share/ronin/logs/mcp` when `XDG_DATA_HOME` is unset. Each log is truncated when Ronin starts and capped at 10 MiB.

If an MCP server returns `instructions` in its standard initialization response, Ronin includes them in the system prompt together with the server's namespaced tool names.

Provider API roots can be configured in the provider catalog or overridden through each provider's configured `base_url_env`. The bundled providers use `OPENAI_BASE_URL`, `GEMINI_BASE_URL`, `ANTHROPIC_BASE_URL`, and `XAI_BASE_URL`.

### Project instructions and skills

Ronin loads:

- a global `AGENTS.md` from the configuration directory;
- local `AGENTS.md` files from the project directory and its parents;
- skills from `<config dir>/skills/<skill name>/SKILL.md`.

### Workflows

Lua workflows coordinate fresh agent conversations. They can run agents sequentially in the primary working directory or concurrently in managed Git worktrees. Run one directly with:

```sh
ronin -working_dir /path/to/project run workflow.lua "describe the task"
```

Named workflows placed in `<config dir>/workflows` are available from the TUI. See [`testdata/workflow.lua`](testdata/workflow.lua) for structured planning, concurrent implementer/reviewer lanes, squash integration using Conventional Commits, and bounded integration repair.

The concurrent example allows read-only design and planning on a dirty tree, but refuses to create worktrees unless the primary branch and `HEAD` are unchanged and the tree is clean, including untracked files. Failed runs retain useful branches and dirty worktrees for recovery; successful runs fast-forward the primary branch and remove workflow-owned Git artifacts.

## Status

Ronin is experimental. Expect rough edges and breaking changes. It is inspired by [Pi](https://pi.dev/) and built as a way to explore coding-agent design in Go.

## License

[BSD 3-Clause](LICENSE)
