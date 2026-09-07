# ronin

*ronin* is an experimental coding harness written in Go. It gives an LLM a focused terminal UI and tools to inspect, edit, and test code.

<p align="center">
  <img src="assets/ronin-demo.gif" alt="Ronin edits and tests a Go CLI">
</p>

## Features

- Interactive terminal UI and non-interactive prompt mode
- OpenAI, Gemini, Anthropic, and xAI models
- File reading, editing, shell commands, and Go/TypeScript/TSX code navigation
- Persistent sessions with optional resume
- Project instructions through `AGENTS.md`
- Reusable skills and Lua workflows

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

## OpenTelemetry

Ronin can export traces and metrics to any standard OTLP collector. Export is off by default: unset exporter selectors default to `none` in Ronin. Set `OTEL_TRACES_EXPORTER=otlp` and/or `OTEL_METRICS_EXPORTER=otlp` to enable each signal independently; `none` disables that signal. Only `otlp` and `none` are supported. No custom environment variables are required:

```sh
export OTEL_TRACES_EXPORTER=otlp
export OTEL_METRICS_EXPORTER=otlp
export OTEL_SERVICE_NAME=ronin
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
# Optional authentication (protect this value):
# export OTEL_EXPORTER_OTLP_HEADERS='authorization=Bearer ...'

ronin
```

For gRPC, use `OTEL_EXPORTER_OTLP_PROTOCOL=grpc` and typically port 4317. The default protocol is HTTP/protobuf. Standard signal-specific `OTEL_EXPORTER_OTLP_TRACES_*` and `OTEL_EXPORTER_OTLP_METRICS_*` endpoint, protocol, header, TLS, and timeout settings are supported by the exporters. HTTP base endpoints get `/v1/traces` and `/v1/metrics` appended; signal-specific endpoints must include their paths. `OTEL_RESOURCE_ATTRIBUTES` adds resource metadata. `OTEL_SDK_DISABLED=true` overrides enablement.

### Execution traces

A top-level user prompt starts a trace with `ronin.prompt_turn`, containing `ronin.cycle` spans for each model/tool cycle. These contain `ronin.request` and `ronin.tool` spans. Workflow runs have `ronin.workflow` spans, with child-agent prompt turns linked beneath the invoking workflow rather than detached into unrelated traces.

Tool spans record the exact tool name, call ID, issuing provider/model, argument size, serialized result size, duration, and outcome. Unknown tools and rejected arguments are recorded too. Argument contents, results, prompts, and raw error messages are not exported. SDK/backend attribute limits may truncate or drop metadata.

Provider/model attribution uses `gen_ai.provider.name` and `gen_ai.request.model`. Session, prompt-turn, and cycle identities are trace attributes, not metric dimensions. Prompt-turn and cycle IDs use their span IDs. Request spans include token/cache usage, estimated cost when pricing is available, completion reason, and a first-output event. `ronin.http_attempt` child spans expose HTTP retries; their duration ends at response headers, while request duration covers streaming and consumption.

### Metrics and accounting

- `ronin.request.count`, `ronin.tool.count`, `ronin.http_attempt.count`: logical requests, individual tool invocations, and actual HTTP attempts, respectively.
- `ronin.{request,tool,http_attempt,cycle,prompt_turn,workflow}.duration`: seconds, with outcome and model dimensions where applicable.
- `ronin.token.usage`: tokens by provider/model, purpose, and disjoint category (`input`, `output`, `cache_read`, `cache_write`). Here `input` excludes cached and cache-written input; trace input-token totals include them.
- `ronin.cost.estimated`: estimated USD by provider/model and purpose; unavailable pricing produces no cost increment.
- `ronin.{cycle,prompt_turn}.{requests,tool_calls,http_attempts,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens}`: per-scope histograms broken down by model.

Tool metrics also carry the exact tool name. Arguments, call IDs, and session IDs never become metric labels. Parent spans summarize direct counts and known token/cost totals. Child-agent activity is excluded from parent direct totals, avoiding double counting; inspect descendant spans for delegated work. Wall time is separate from summed operation durations.

Structured requests for compaction and workflow-output formatting are counted with purpose tags, but the current structured-response interface does not expose token usage: those spans explicitly mark usage unavailable. Parent `ronin.usage.complete` and `ronin.cost.complete` flags distinguish complete totals from known partial totals. Provider usage is recorded when a completion event supplies it; interrupted requests without such an event remain unknown. Actual response model IDs are not currently exposed by the provider interface; model tags identify the requested model.

Export is batched and bounded. Collector outages do not fail conversations; initialization failures disable telemetry with a warning, and shutdown attempts a flush for at most five seconds. The SDK defaults to recording all traces, but standard `OTEL_TRACES_SAMPLER` configuration can change sampling. Queue overflow, export failures, or collector-side sampling can lose spans: this is observability, not a durable audit log. Standard SDK batch-span and metric-export environment settings control buffering and export intervals.

## Code navigation

`code_map` and `code_find` are available in terminal/prompt sessions and workflow agents, including read-only and managed-worktree agents. They do not grant shell access.

Example tool arguments:

```json
{"path":"internal","depth":2,"limit":40}
```

Use `code_map` with a directory for files/subdirectory counts, or a file such as `internal/auth.go` for declarations and signatures. A `kind` filter can request declarations across files.

```json
{"query":"validate session","language":"go","limit":20}
```

`code_find` ranks exact names, identifier matches, paths, signatures, then matching source lines. All query words must match the same candidate; camelCase and snake_case are split. This initial implementation uses bounded in-process lexical ranking, not embeddings or SQLite FTS. It searches comments and implementation text even when parsing fails. Results include SHA-256 and source ranges: use `read_file` for current exact text before editing. `known_sha256` on `read_file` is an omission hint, not an optimistic-lock precondition.

Every call scans/hashes eligible files and reuses unchanged declarations from a separate SQLite cache under `os.UserCacheDir()/ronin/codeindex`. Caches are keyed by canonical workspace path and extraction version, not Git remote/branch; worktrees remain isolated. No cache files are written inside the workspace. Indexing runs lazily, without a watcher or background daemon. Cache errors fail only the navigation call; an error identifies a corrupt database that can be removed to rebuild. Cache files contain source text: protect them like the workspace, and delete the cache directory to remove retained data. There is no automatic cache eviction yet.

Scope is `.go`, `.ts`, `.mts`, `.cts`, and `.tsx`. Nested workspace `.gitignore` rules apply to all files (including tracked files); global Git excludes and `.git/info/exclude` are not read. Matching is implemented privately in `codeindex` with no Git library or executable required at runtime. It is case-sensitive on all platforms, supports Git-style wildcards, negation, bracket classes, and escaped characters, and prunes excluded parents before reading child rules. Differential tests use Git when available. Symlinks and common dependency/build directories are skipped. Unlike Git, generated files are not detected specially. Limits: 1 MiB/file, 64 MiB total source, 128 MiB source/structure budget, 10,000 files, 30 seconds per tool call, 100 results and 64 KiB encoded result output. Narrow the working directory if a workspace limit is exceeded. A path filter narrows results, not the refresh scope. Truncation and capped diagnostic summaries are explicit.

Structural coverage includes functions, methods, named types/classes/interfaces, Go constants/variables, TypeScript identifier bindings, and enums. It is not semantic navigation: no references, type resolution, or call graph. Docs remain searchable source text rather than attached symbol metadata. Imports/re-exports, destructuring, anonymous exports, and some type/member forms are not yet modeled. Parser diagnostics indicate potentially incomplete structure, not compiler errors; files can change after refresh, so returned hashes describe the scanned contents, not a transactionally frozen filesystem.

### Experimental parser dependencies and releases

The root `go.mod` pins unmerged fixes from [binding PR #56](https://github.com/tree-sitter/go-tree-sitter/pull/56) and [TypeScript grammar PR #365](https://github.com/tree-sitter/tree-sitter-typescript/pull/365). Parser callback ownership has regression tests. Query callbacks remain disabled because of a separate upstream lifetime bug; a conservative native timeout is retained. `f<typeof import('module')>()` remains a known grammar gap with regression coverage. Revisit these replacements when fixes merge. The implementation and regression tests live in `codeindex/` and `codeindex/syntax/`.

The release workflow now uses native CGo jobs for all six existing OS/architecture targets, with static Linux builds and Windows toolchains. `workflow_dispatch` builds/test-packages without publishing. Only Linux/arm64 has been validated locally; all other targets, especially Windows/arm64, must pass the matrix before release. No separate Tree-sitter shared library is required.

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

To run from source instead, use Go 1.27.0 or newer and a C compiler with `CGO_ENABLED=1` (Tree-sitter and its grammars are compiled into Ronin):

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

MCP tools are namespaced as `<server>__<tool>`, such as `gopls__go_search`. Commands run in Ronin's working directory and inherit its environment; add an `env` object to override environment variables for a command. Both command-based and remote servers receive Ronin's working directory as an MCP workspace root.

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

The concurrent example allows read-only design and planning on a dirty tree, but refuses to create worktrees unless the primary branch and `HEAD` are unchanged and the tree is clean, including untracked files. Managed worktree agents receive workspace-confined file tools but no arbitrary shell tool; workflow-owned Git operations remain available through the Lua API. Failed runs retain useful branches and dirty worktrees for recovery; successful runs fast-forward the primary branch and remove workflow-owned Git artifacts.

## Status

Ronin is experimental. Expect rough edges and breaking changes. It is inspired by [Pi](https://pi.dev/) and built as a way to explore coding-agent design in Go.

## License

[BSD 3-Clause](LICENSE)
