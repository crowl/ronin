# *ronin*

*ronin* is an experimental coding harness written in Go. It runs an LLM-backed conversation runtime with tools for file editing and shell commands, presented through a terminal UI or non-interactive prompt. It supports skills and context files (AGENTS.md).

## Why *ronin* exists

It started as a playground for experimenting with custom tools for coding conversations, using Go.

It is heavily inspired by [Pi](https://pi.dev/), especially in the feel of the TUI and rendering model.

It is also a learning project: a way to understand coding harnesses by building one from the ground up.

## Goals

- Be a useful coding harness.
- Lean on the Go standard library and strive to keep external dependencies to a minimum.
- Be a useful playground for people who want to experiment with conversation tools and harness design in Go.

## Releases and installation

Downloadable release archives are published on the [GitHub Releases page](https://github.com/crowl/ronin/releases). Each archive name follows `ronin_<tag>_<os>_<arch>`: `linux` and `darwin` identify the operating system, while `amd64` and `arm64` identify the CPU architecture. Linux and macOS releases are `.tar.gz` archives; Windows releases are `.zip` archives and contain `ronin.exe` at the archive root.

After downloading an archive and `checksums.txt`, verify the archive from the directory containing both files:

```sh
sha256sum -c checksums.txt --ignore-missing
```

The source build reports `ronin dev`; release binaries report the tag they were built from, for example `ronin v0.1.0`:

```sh
ronin --version
```


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

### OpenAI API root override

Set `OPENAI_BASE_URL` to use an OpenAI-compatible API root with an API key:

```sh
export OPENAI_API_KEY=...
export OPENAI_BASE_URL=https://proxy.example.com/v1
```

This is an API root, not a complete endpoint. Ronin appends `/responses`, so this example sends requests to `https://proxy.example.com/v1/responses`; trailing slashes are normalized. Setting `OPENAI_BASE_URL` requires `OPENAI_API_KEY` and bypasses local ChatGPT/Codex OAuth credentials.

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

In prompt mode, assistant text is written to stdout, then the process exits when the conversation finishes.

## Lua workflows

A Lua workflow can coordinate several fresh agent conversations against the same working directory. Workflow input is optional at the runtime level and is exposed to Lua as the immutable string `ronin.input`. Supply input inline, from a file, or explicitly from stdin:

```sh
go run ./cmd/ronin -working_dir /path/to/project run testdata/workflow.lua "Add support for Gemini 4"
go run ./cmd/ronin -working_dir /path/to/project run testdata/workflow.lua --input requirement.md
cat requirement.md | go run ./cmd/ronin -working_dir /path/to/project run testdata/workflow.lua -
```

`--input` is an option of the `run` subcommand. Relative input paths resolve from the shell's current directory, not from `-working_dir`. Inline text, `--input`, and the unescaped `-` selector are mutually exclusive; conflicting or extra inputs fail rather than using implicit precedence. Use `--` before inline text that begins with a dash. File and stdin input are limited to 1 MiB, and stdin is read only when an unescaped `-` is explicit. A generic workflow may omit input, but the example requirement workflow rejects empty or whitespace-only input.

The workflow API currently provides the read-only `ronin.input` value plus `ronin.run_agent`, `ronin.read`, `ronin.log`, `ronin.done`, and `ronin.fail`. Agent calls use this form:

```lua
local result = ronin.run_agent({
    prompt = "Review the current working-tree changes",
    model = "openai:gpt-5.6-sol", -- optional; defaults to CLI/config model
    reasoning = "high",           -- optional; defaults to CLI/config level
    system = "Act as an independent reviewer.", -- optional
})

ronin.log(result.text)
```

`prompt` is required. `model` must use `<provider>:<name>` and must be registered for the configured provider. A successful call returns `{ ok = true, text = "..." }`; provider, tool, and validation failures stop the workflow with an error. `ronin.run_agent` has no global call limit, so workflows that repeat agents must define an explicit termination bound.

Every `ronin.run_agent` call starts a fresh conversation, but all agents receive the same working directory and tool access. They therefore coordinate through working-tree changes and through text explicitly included in later prompts.

### Global workflow catalog and TUI

Named workflows for interactive use are loaded once when Ronin starts from the global config directory:

```text
$XDG_CONFIG_HOME/ronin/workflows/
```

or, when `XDG_CONFIG_HOME` is unset:

```text
$HOME/.config/ronin/workflows/
```

Place flat, lower-case `.lua` files there, such as `implement.lua`. Subdirectories and non-Lua files are ignored. The filename stem is the workflow name. Restart Ronin after adding or changing catalog entries.

In the TUI, choose `/workflow:implement`, then enter or paste the input that should become `ronin.input`. Escape leaves workflow-input mode, while Enter starts the workflow. Progress is shown as a compact timeline; use Ctrl+O to expand nested agent and tool details. A compact workflow result is saved with the conversation so later prompts and resumed sessions can discuss it.

The primary conversational agent also receives a `run_workflow` tool when catalog workflows exist. You can develop a prompt in conversation and ask the agent to invoke a named workflow with it. Nested workflow agents do not receive this tool.

For implementing a software requirement, a useful bounded loop is:

1. Use a strong reasoning model once to inspect the repository and produce acceptance criteria and a plan without modifying files.
2. Give the requirement, plan, and latest feedback to an implementation agent that edits, formats, and tests the code.
3. Have an independent technical reviewer inspect the actual diff and test state.
4. Return technical findings to step 2 until the reviewer approves.
5. After technical approval, use a fresh evaluator acting as the original requestor to check observable outcomes against the requirement and acceptance criteria.
6. Return evaluator feedback to step 2, or complete when the evaluator approves.

Decision agents end their response with exactly one terminal marker:

```text
STATUS: APPROVED
```

or:

```text
STATUS: CHANGES_REQUIRED
```

The example accepts approval only when `STATUS: APPROVED` is the sole, final status marker on its own terminal line. A missing, contradictory, malformed, or non-terminal marker is treated as requiring changes. This conservative policy and an explicit `max_cycles` value prevent ambiguous approval and runaway execution.

[`testdata/workflow.lua`](testdata/workflow.lua) implements this loop with five implementation cycles at most. It reads the software requirement from `ronin.input`, assigns `gpt-5.6-sol` at high reasoning to planning, technical review, and requestor evaluation, and uses `gpt-5.6-terra` at medium reasoning for implementation. Adjust `max_cycles` or the explicit role models in the file when needed.

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

- `runtime`: active conversation runtime, tool execution, compaction, skills, and system prompt construction.
- `config`: config directory resolution and the `config.json` settings file.
- `llm`: model definitions, provider registration, streaming events, and provider implementations.
- `tool`: shared tool contracts, typed argument decoding, results, and artifacts.
- `tui`: the terminal application, command menu, rendering, themes, and presentation logic.

## Status

Experimental. Expect rough edges, missing documentation, and breaking changes.

## License

*ronin* is licensed under the BSD 3-Clause License. See [LICENSE](LICENSE) for details.
