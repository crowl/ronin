# OpenTelemetry

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

## Execution traces

A top-level user prompt starts a trace with `ronin.prompt_turn`, containing `ronin.cycle` spans for each model/tool cycle. These contain `ronin.request` and `ronin.tool` spans. Workflow runs have `ronin.workflow` spans, with child-agent prompt turns linked beneath the invoking workflow rather than detached into unrelated traces.

Tool spans record the exact tool name, call ID, issuing provider/model, argument size, serialized result size, duration, and outcome. Unknown tools and rejected arguments are recorded too. Argument contents, results, prompts, and raw error messages are not exported. SDK/backend attribute limits may truncate or drop metadata.

Provider/model attribution uses `gen_ai.provider.name` and `gen_ai.request.model`. Session, prompt-turn, and cycle identities are trace attributes, not metric dimensions. Prompt-turn and cycle IDs use their span IDs. Request spans include token/cache usage, estimated cost when pricing is available, completion reason, and a first-output event. `ronin.http_attempt` child spans expose HTTP retries; their duration ends at response headers, while request duration covers streaming and consumption.

## Metrics and accounting

- `ronin.request.count`, `ronin.tool.count`, `ronin.http_attempt.count`: logical requests, individual tool invocations, and actual HTTP attempts, respectively.
- `ronin.{request,tool,http_attempt,cycle,prompt_turn,workflow}.duration`: seconds, with outcome and model dimensions where applicable.
- `ronin.token.usage`: tokens by provider/model, purpose, and disjoint category (`input`, `output`, `cache_read`, `cache_write`). Here `input` excludes cached and cache-written input; trace input-token totals include them.
- `ronin.cost.estimated`: estimated USD by provider/model and purpose; unavailable pricing produces no cost increment.
- `ronin.{cycle,prompt_turn}.{requests,tool_calls,http_attempts,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens}`: per-scope histograms broken down by model.

Tool metrics also carry the exact tool name. Arguments, call IDs, and session IDs never become metric labels. Parent spans summarize direct counts and known token/cost totals. Child-agent activity is excluded from parent direct totals, avoiding double counting; inspect descendant spans for delegated work. Wall time is separate from summed operation durations.

Structured requests for compaction and workflow-output formatting are counted with purpose tags, but the current structured-response interface does not expose token usage: those spans explicitly mark usage unavailable. Parent `ronin.usage.complete` and `ronin.cost.complete` flags distinguish complete totals from known partial totals. Provider usage is recorded when a completion event supplies it; interrupted requests without such an event remain unknown. Actual response model IDs are not currently exposed by the provider interface; model tags identify the requested model.

Export is batched and bounded. Collector outages do not fail conversations; initialization failures disable telemetry with a warning, and shutdown attempts a flush for at most five seconds. The SDK defaults to recording all traces, but standard `OTEL_TRACES_SAMPLER` configuration can change sampling. Queue overflow, export failures, or collector-side sampling can lose spans: this is observability, not a durable audit log. Standard SDK batch-span and metric-export environment settings control buffering and export intervals.

[Back to README](../README.md)
