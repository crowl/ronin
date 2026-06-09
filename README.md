# *ronin*

*ronin* is an experimental coding harness written in Go. It provides a TUI for working with an agent that can read/edit/write files and run shell commands. It supports skills and context files (AGENTS.md).

## Why *ronin* exists

It started as a playground for experimenting with custom tools for agents, using Go.

It is heavily inspired by [Pi](https://pi.dev/), especially in the feel of the TUI and rendering model.

It is also a learning project: a way to understand coding harnesses by building one from the ground up.

## Goals

- Be a useful coding harness.
- Lean on the Go standard library and strive to keep external dependencies to a minimum.
- Be a useful playground for people who want to experiment with agent tools and harness design in Go.

## Try it locally

The easiest way to try it is to clone the repository and run it from source.

```sh
git clone https://github.com/crowl/ronin.git
cd ronin
```

Set at least one provider API key before starting *ronin*. Models are registered for whichever providers have keys set:

```sh
export OPENAI_API_KEY=...
export GEMINI_API_KEY=...
export ANTHROPIC_API_KEY=...
```

### OpenAI OAuth Integration (with API Key Fallback)

*ronin* has built-in, zero-overhead support for connecting to OpenAI's ChatGPT/Codex Responses API (`chatgpt.com/backend-api/codex/responses`) using your local ChatGPT/Codex OAuth credentials.

By default, *ronin* **prioritizes** the OAuth flow and automatically searches for your local ChatGPT/Codex OAuth cache file (`auth.json`) in the standard locations:
- `~/.chatgpt-local/auth.json`
- `~/.codex/auth.json`

If found, *ronin* configures the transparent OAuth proxy, enabling you to use your subscription.

If no local `auth.json` is found, *ronin* falls back to using the standard `OPENAI_API_KEY` environment variable if configured.

To set up your local OAuth credentials, run the official login flow:
```sh
npx @openai/codex login
```
Or set `CHATGPT_LOCAL_HOME` / `CODEX_HOME` environment variables if your `auth.json` is located in a custom directory.

Then start the app:

```sh
go run ./cmd/ronin
```

The default config currently uses an OpenAI model. If you only set `GEMINI_API_KEY` or `ANTHROPIC_API_KEY`, update the configured model or pass `-model <provider>:<name>` for one of the registered models.

By default, each run starts a fresh session. To load the active session for the working directory instead, pass `--resume`:

```sh
go run ./cmd/ronin --resume
```

You can run *ronin* against a specific working directory with:

```sh
go run ./cmd/ronin -working_dir /path/to/project
```

You can also provide a prompt directly and skip the TUI:

```sh
go run ./cmd/ronin -prompt "summarize this project"
```

In prompt mode, assistant text is written to stdout, then the process exits when the agent finishes.

## Configuration

*ronin* keeps its configuration in `$XDG_CONFIG_HOME/ronin` when `XDG_CONFIG_HOME` is set, otherwise `$HOME/.config/ronin`. On first run it writes a default `config.json` there:

```json
{
  "model": { "provider": "openai", "name": "gpt-5.5" },
  "reasoning_level": "medium",
  "max_turns": 512
}
```

### Skills

Skills are loaded from `<config dir>/skills`. Each skill lives in its own directory with a `SKILL.md` file, for example `<config dir>/skills/<skill-name>/SKILL.md`.

### AGENTS.md context files

*ronin* loads a global context file from `<config dir>/AGENTS.md` when present. It also loads local `AGENTS.md` files from the working directory and its parents, ordered from the broadest parent down to the working directory.

## How it is organized

- `agent`: conversation state, tool execution, compaction, skills, and system prompt construction.
- `config`: config directory resolution and the `config.json` settings file.
- `llm`: model definitions, provider registration, streaming events, and provider implementations.
- `tool`: shared tool contracts, typed argument decoding, results, and artifacts.
- `tui`: the terminal application, command menu, rendering, themes, and presentation logic.

## Status

Experimental. Expect rough edges, missing documentation, and breaking changes.

## License

*ronin* is licensed under the BSD 3-Clause License. See [LICENSE](LICENSE) for details.
