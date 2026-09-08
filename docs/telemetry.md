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

Structured requests for compaction and workflow-output formatting record provider-reported usage and cost with purpose tags, including usage retained when output validation fails. Missing usage remains explicitly unavailable, rather than being treated as a free request. Auxiliary usage is journaled separately from model messages in persisted conversations; it contributes to session cost without changing the conversation context-size estimate. Workflow agents keep their own accounting, separate from their parent's direct totals. Parent `ronin.usage.complete` and `ronin.cost.complete` flags distinguish complete totals from known partial totals. Interrupted requests without reported usage remain unknown. Actual response model IDs are not currently exposed by the provider interface; model tags identify the requested model.

## Prompt caching

- Anthropic conversation requests enable automatic caching with top-level `cache_control: {"type":"ephemeral"}` (default five-minute TTL). One-off structured requests do not opt in. Cache writes incur a premium, so short or infrequently reused prefixes may not save money.
- OpenAI uses default implicit caching. Both cached reads and reported cache writes are accounted for, including newer models with cache-write pricing.
- OpenAI Responses requests to `https://api.openai.com` and xAI Responses requests to `https://api.x.ai` include a stable per-conversation `prompt_cache_key`. (xAI documents `x-grok-conv-id` for Chat Completions, which Ronin does not use.) These opaque identities improve routing opportunities but do not guarantee hits. Recognized provider names and hosts must both match; custom compatible endpoints and base-URL overrides on other hosts receive no routing hint.
- Persisted sessions retain their routing identity across resume; new conversations and forks get separate identities. Workflow-agent conversations also get distinct identities.
- Google remains on stateless Interactions, which supports implicit caching. No explicit cache objects or server-side conversation storage are introduced.

### Measuring effectiveness

1. Enable OTLP export using the configuration above. Use one provider/model for a representative multi-turn coding session. Requests must exceed that model's minimum cacheable prefix length.
2. Keep tools, system instructions, and reasoning settings stable. Run several related read/search turns promptly; for Anthropic, stay within the five-minute cache TTL. The first request may write a cache rather than read one.
3. Inspect `ronin.request` spans for `gen_ai.usage.input_tokens`, `gen_ai.usage.cache_read.input_tokens`, `gen_ai.usage.cache_creation.input_tokens`, `ronin.cache.read_share`, and `ronin.cost.estimated`. The read-share attribute is omitted when input tokens are zero.
4. For aggregate cache-read share, divide **sum of cached-read tokens by sum of total input tokens**, grouped by provider/model and purpose. Do not average per-request percentages. With `ronin.token.usage`, the denominator is `input + cache_read + cache_write`, not just the disjoint `input` category.
5. Separate cold starts, long pauses, model/tool changes, and compaction events from steady-state turns. Compaction deliberately rewrites history and can lose the old conversation-prefix cache.
6. Compare input cost including cache-write premiums and auxiliary requests, not just the hit ratio. Check completeness flags and the provider billing dashboard before drawing conclusions; telemetry estimates depend on configured prices and reported usage.

These checks make paid API requests. Unit tests use simulated responses and do not establish live hit rates. No prompts, cache keys, or tool contents are added to telemetry by the cache instrumentation.

Export is batched and bounded. Collector outages do not fail conversations; initialization failures disable telemetry with a warning, and shutdown attempts a flush for at most five seconds. The SDK defaults to recording all traces, but standard `OTEL_TRACES_SAMPLER` configuration can change sampling. Queue overflow, export failures, or collector-side sampling can lose spans: this is observability, not a durable audit log. Standard SDK batch-span and metric-export environment settings control buffering and export intervals.

[Back to README](../README.md)
