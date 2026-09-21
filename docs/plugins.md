# Plugins

Ronin's execution engine publishes lifecycle events and consults tool hooks through the `plugin` package. Plugins are Go values compiled into the binary and registered in `cmd/ronin/main.go`; they extend Ronin without changes to the runtime, model clients, or workflows. OpenTelemetry export (`telemetry.NewPlugin`) and the default Jev tool gate (`jev.NewPlugin`) are implemented this way.

## Contract

A plugin implements `plugin.Plugin` (`Name() string`) plus any of these optional interfaces:

| Interface | Purpose |
| --- | --- |
| `Starter` | `Start(ctx) error` acquires resources before the run. A failing plugin is dropped from the host with a warning; the run continues. |
| `Closer` | `Close(ctx) error` flushes or releases resources. Ronin allows five seconds after the run ends. |
| `Observer` | `Observe(ctx, Event)` receives lifecycle events. |
| `ToolGate` | `GateToolCall(ctx, ToolCall) (Decision, error)` allows, denies, or rewrites a tool call before it runs. |
| `ToolResultFilter` | `FilterToolResult(ctx, ToolCall, json.RawMessage) (json.RawMessage, error)` transforms a successful result before the model sees it. |

Plugins are dispatched by `plugin.Host`, which is installed on the root context with `plugin.NewContext`; every engine layer reaches it with `plugin.FromContext`. A nil host is a valid no-op, so code paths without plugins need no special handling.

## Events

Events are plain data. Each started/ended pair embeds `plugin.Operation{ID, ParentID}`; parent identifiers rebuild the execution tree without shared context:

```
WorkflowStarted/Ended
  PromptTurnStarted/Ended (SessionID, Model, Cycles)
    CycleStarted/Ended (Index)
      ModelRequestStarted/FirstOutput/Ended (Purpose, Usage, StopReason)
        HTTPAttemptStarted/Ended (Attempt, StatusCode)
      ToolCallStarted/Ended (Call, ResultSize)   ToolCallDenied (Rejected, Err)
ContextCompacted, SessionSaveFailed (ParentID only)
```

Auxiliary structured requests (compaction, workflow output formatting) appear as model requests with a non-`conversation` purpose. Child-agent prompt turns started by a workflow report the workflow operation as their parent.

Observers run synchronously on the publishing goroutine, in registration order. They must return promptly and must not initiate model calls. Panics are recovered and logged; observers cannot fail a run. Events may arrive concurrently from parallel workflow agents, so observers need their own synchronization.

## Tool hooks

Gates run in registration order. `plugin.Rewrite(arguments)` replaces the arguments for subsequent gates, the tool, and the UI; `plugin.Deny(reason)` stops the chain. A denial, a returned error, or a panic all prevent execution: the model receives a tool error naming the plugin and reason, the UI shows a failed tool call, and observers see `ToolCallDenied`.

Filters run in registration order on the serialized JSON result and receive the previous filter's output. The filtered result is what is persisted and sent to the model; UI artifacts still come from the unfiltered result. A returned error or panic fails the tool call. Tool errors are not filtered.

## Writing a plugin

```go
type denyRemoval struct{}

func (denyRemoval) Name() string { return "deny-removal" }

func (denyRemoval) GateToolCall(_ context.Context, call plugin.ToolCall) (plugin.Decision, error) {
	if call.Name == "shell" && strings.Contains(string(call.Arguments), "rm -rf") {
		return plugin.Deny("recursive removal is not permitted"), nil
	}
	return plugin.Allow(), nil
}
```

Register it with the built-in plugins in `cmd/ronin/main.go`. Jev is enabled
by default; its API key is required unless the CLI's `-disable-jev` flag is set:

```go
plugins := []plugin.Plugin{telemetry.NewPlugin(), denyRemoval{}}
if !opts.disableJev {
	apiKey := strings.TrimSpace(os.Getenv("TYPESAFE_API_KEY"))
	if apiKey == "" {
		return errors.New("TYPESAFE_API_KEY is required unless -disable-jev is set")
	}
	plugins = append(plugins, jev.NewPlugin(apiKey))
}
host := plugin.NewHost(plugins...)
```

## Jev tool gate

Ronin registers `jev.NewPlugin` by default as a strict
[TypeSafe Jev](https://docs.typesafe.ai) tool gate. `TYPESAFE_API_KEY` is
required at startup unless `-disable-jev` is set. There are no other Jev
environment settings or modes.

Before every tool call, Ronin asks two Noul questions: whether the call is a
relevant next step for the current user request, and whether it could have an
irreversible side effect. Asking both questions for every tool also covers
workflows, MCP tools, and tools added later without maintaining a list of
mutating tool names. Ronin denies a call when P(relevant) is at most 0.45 or
P(irreversible) is at least 0.8.

The structured state sent to TypeSafe contains the current user request, bounded
recent model-visible conversation context, the selected tool's name,
description, and parameter schema, its proposed arguments as JSON, and the
working directory. It does not include Ronin's session identifier. This data
can contain source code, commands, paths, or secrets supplied in tool
arguments; use `-disable-jev` only when sending that data to TypeSafe is not
acceptable.

The gate fails closed. A missing request, invalid local state, malformed Jev
response, network error, rate limit, overload, or timeout denies the tool call.
HTTP 429 and 529 responses receive bounded retries within a fixed three-second
deadline. Pass `-disable-jev` to disable Jev behavior.

[Back to README](../README.md)
