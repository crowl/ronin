# Provider validation

Normal tests use local HTTP fixtures and temporary SQLite databases; no API keys
or paid requests are required:

```sh
go test ./...
go test -race ./runtime ./llm/... ./session/...
go vet ./...
```

Coverage includes provider context-error envelopes, cancellation, transport
stalls, terminal events, retry limits, retained-history boundaries across SQLite
reopen/fork/rewind, and persistence of known results after cancellation.

## Opt-in live smoke check

Live checks are excluded from normal builds. Choose one adapter and a model that
supports reasoning disabled, set its API key, and explicitly acknowledge billing:

```sh
RONIN_LIVE_ACK=accept-provider-charges \
RONIN_LIVE_ADAPTER=openai \
RONIN_LIVE_MODEL=YOUR_MODEL \
go test -tags live ./llm -run '^TestLiveProviderSmoke$' -count=1
```

Adapters: `openai`, `anthropic`, `google`; credentials: `OPENAI_API_KEY`,
`ANTHROPIC_API_KEY`, `GEMINI_API_KEY`, respectively.

The check sends a fixed short prompt, no tools or repository contents, requests
at most 64 output tokens, and has a 90-second deadline. Existing bounded retries
still apply. This is an explicit token/time limit, **not a guaranteed dollar
cap**: use provider-side account budgets for a hard spending policy. The check
validates basic protocol compatibility, not correctness on coding tasks.

Never run live checks automatically in ordinary CI or with production secrets
in prompts. Sanitized local fixtures should remain the primary regression suite.
