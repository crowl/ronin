# Plugins

Ronin's execution engine publishes lifecycle events and consults tool hooks through the `plugin` package. Plugins are Go values compiled into the binary and registered in `cmd/ronin/main.go`; they extend Ronin without changes to the runtime, model clients, or workflows. OpenTelemetry export (`telemetry.NewPlugin`) and the optional Jev tool gate (`jev.NewPlugin`) are implemented this way.

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

Register it alongside the built-in plugins in `cmd/ronin/main.go`:

```go
plugins := plugin.NewHost(telemetry.NewPlugin(), jev.NewPlugin(), denyRemoval{})
```

## Jev tool gate

`jev.NewPlugin` asks [TypeSafe Jev](https://docs.typesafe.ai) whether a tool call is relevant to the current user task. That noul is the primary verdict. Mutating tools (`shell`, `write_file`, `edit_file`) also get an irreversible-side-effect noul. Jev returns probabilities; Ronin owns the allow/deny policy.

The current user prompt is placed on `plugin.ToolCall.Task` and in the Jev state as `task`. Calls with no task skip Jev and are allowed. Record the prompt with plugin.WithTask at the start of a turn; Host.GateToolCall copies that value onto ToolCall.Task when the field is empty.

The plugin is off until you set a mode and a TypeSafe API key:

```sh
export TYPESAFE_API_KEY=tsk_...
export RONIN_JEV_MODE=shadow   # log the verdict, never deny
# export RONIN_JEV_MODE=enforce  # deny when the policy thresholds fire
```

| Variable | Default | Meaning |
| --- | --- | --- |
| `RONIN_JEV_MODE` | `off` | `off`, `shadow`, or `enforce` |
| `TYPESAFE_API_KEY` | empty | TypeSafe bearer token. Required for `shadow` and `enforce` |
| `RONIN_JEV_API_KEY_ENV` | `TYPESAFE_API_KEY` | Alternate environment variable that holds the key |
| `RONIN_JEV_ENDPOINT` | `https://api.typesafe.ai/v1/systemone` | System One HTTP endpoint |
| `RONIN_JEV_MODEL` | `jev-latest` | Model id sent in the request |
| `RONIN_JEV_TIMEOUT` | `800ms` | Per-call timeout |
| `RONIN_JEV_MIN_CONFIDENCE` | `0.55` | How sure Jev must be that a call is off-task (or irreversible) before a deny is honored |

A deny fires when P(relevant) is at most `1 - RONIN_JEV_MIN_CONFIDENCE`, or when a mutating call has P(irreversible) at least max(0.8, that threshold). API and timeout failures fail open so a TypeSafe outage cannot stall the coding loop. In `shadow` mode the call always proceeds; use it to compare Jev's verdicts against what you would have blocked by hand before turning on `enforce`.

[Back to README](../README.md)
