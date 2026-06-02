# Ronin

Ronin is an experimental coding harness written in Go. It provides a TUI for working with an agent that can read/edit/write files and run shell commands. It supports skills and context files (AGENTS.md).

## Why Ronin exists

Ronin started as a playground for experimenting with custom tools for agents, using Go.

It is heavily inspired by [Pi](https://github.com/pretzelai/pretzelai/tree/main/packages/pi), especially in the feel of the TUI and rendering model.

It is also a learning project: a way to understand coding harnesses by building one from the ground up.

## Goals

- Be a useful coding harness.
- Use only the Go standard library: zero external dependencies.
- Be a useful playground for people who want to experiment with agent tools and harness design in Go.

## Try it locally

Ronin is not packaged as a stable release yet. The easiest way to try it is to clone the repository and run it from source.

```sh
git clone https://github.com/crowl/ronin.git
cd ronin

export OPENAI_API_KEY=...
go run ./cmd/ronin
```

The default model currently uses OpenAI, so `OPENAI_API_KEY` is required to start the app. Google models are also registered; set `GEMINI_API_KEY` if you want to use them.

You can run Ronin against a specific working directory with:

```sh
go run ./cmd/ronin -cwd /path/to/project
```

## How it is organized

- `agent`: conversation state, tool execution, compaction, skills, and system prompt construction.
- `llm`: model definitions, provider registration, streaming events, and provider implementations.
- `tool`: shared tool contracts, typed argument decoding, results, and artifacts.
- `tui`: the terminal application, command menu, rendering, themes, and presentation logic.

## Status

Ronin is experimental. Expect rough edges, missing documentation, and breaking changes.

## License

Ronin is licensed under the BSD 3-Clause License. See [LICENSE](LICENSE) for details.
