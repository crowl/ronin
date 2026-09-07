# ronin

*ronin* is an experimental coding harness written in Go. It gives an LLM a focused terminal UI and tools to inspect, edit, and test code.

<p align="center">
  <img src="assets/ronin-demo.gif" alt="Ronin edits and tests a Go CLI">
</p>

## Features

- Interactive terminal UI and non-interactive prompt mode
- OpenAI, Gemini, Anthropic, and xAI models
- File editing, shell commands, and Go/TypeScript/TSX code navigation
- Persistent sessions with resume, rewind, and fork
- Project instructions, reusable skills, MCP servers, and Lua workflows

## Quick start

Download a binary for Linux, macOS, or Windows from [GitHub Releases](https://github.com/crowl/ronin/releases), then set an API key:

```sh
export OPENAI_API_KEY=...
# or GEMINI_API_KEY=...
# or ANTHROPIC_API_KEY=...
# or XAI_API_KEY=...

cd /path/to/project
ronin
```

The default configuration selects an OpenAI model. To use another registered model:

```sh
ronin -model xai:grok-4.6
```

### Build from source

Requires Go 1.27.0 or newer and a C compiler with `CGO_ENABLED=1`. Tree-sitter and its grammars are compiled into Ronin.

```sh
git clone https://github.com/crowl/ronin.git
cd ronin
go run ./cmd/ronin
```

## Everyday usage

```sh
ronin --resume                           # Resume this project's latest session
ronin -working_dir /path/to/project       # Work in another directory
ronin -prompt "summarize this project"    # Run without the TUI
ronin run workflow.lua "describe the task"
```

In the TUI:

| Input | Action |
| --- | --- |
| `Ctrl+O` | Expand tool output |
| `Escape` | Cancel an active operation |
| `/rewind` | Return to before a previous prompt |
| `/fork` | Start a child session from a previous prompt |
| `/compact` | Compact conversation context |
| `!git status --short` | Run a local shell command |

Local shell commands and their output are saved in session history but **never sent to the model**. They run non-interactively with your local permissions; output may contain secrets that are saved locally. Rewind and fork change conversation history, not files.

## Configuration

Ronin creates `config.json` in `$XDG_CONFIG_HOME/ronin`, falling back to `$HOME/.config/ronin`. Configure the default model, reasoning level, provider overrides, and optional MCP servers there. API keys stay in environment variables.

Use `AGENTS.md` for project instructions and `<config dir>/skills/<name>/SKILL.md` for reusable skills. Named Lua workflows in `<config dir>/workflows` are available from the TUI.

Sessions are stored separately in `$XDG_DATA_HOME/ronin/ronin.db`, falling back to `$HOME/.local/share/ronin/ronin.db`. Telemetry export is off by default.

## Documentation

- [Configuration](docs/configuration.md) — providers, pricing, MCP servers, instructions, and skills
- [Sessions and local shell commands](docs/sessions.md) — persistence, history controls, limits, and backups
- [Workflows](docs/workflows.md) — Lua agents and managed worktrees
- [Code navigation](docs/code-navigation.md) — tools, indexing, caching, and parser limitations
- [Telemetry](docs/telemetry.md) — OTLP setup, execution traces, and metrics

## Status

Ronin is experimental. Expect rough edges and breaking changes. It is inspired by [Pi](https://pi.dev/) and built as a way to explore coding-agent design in Go.

## License

[BSD 3-Clause](LICENSE)
