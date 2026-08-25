# ronin

*ronin* is an experimental coding harness written in Go. It gives an LLM a focused terminal UI and tools to inspect, edit, and test code.

![Ronin edits and tests a Go CLI](assets/ronin-demo.gif)

[View the interactive recording on asciinema](https://asciinema.org/a/nkcfkc2mSnOYN3s7)

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

To run from source instead, use Go 1.26 or newer:

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

Resume the active session for the current project:

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

## Configuration

Ronin creates `config.json` in `$XDG_CONFIG_HOME/ronin`, or in `$HOME/.config/ronin` when `XDG_CONFIG_HOME` is unset. It contains the default model, reasoning level, maximum turns, and tool-output summarization settings.

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
