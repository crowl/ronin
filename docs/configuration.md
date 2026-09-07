# Configuration

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

## MCP servers

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

## Project instructions and skills

Ronin loads:

- a global `AGENTS.md` from the configuration directory;
- local `AGENTS.md` files from the project directory and its parents;
- skills from `<config dir>/skills/<skill name>/SKILL.md`.

[Back to README](../README.md)
