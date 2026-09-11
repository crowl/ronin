package runtime_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
	"github.com/crowl/ronin/session"
	"github.com/crowl/ronin/session/sqlite"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type usageClient struct {
	fakeModelClient
	result    *llm.StructuredResult
	resultErr error
}

func (c *usageClient) PredictNextStructured(context.Context, llm.PredictNextStructuredRequest) (*llm.StructuredResult, error) {
	return c.result, c.resultErr
}

func TestStructuredUsagePersistenceAndTelemetry(t *testing.T) {
	for _, mode := range []string{"success", "invalid", "missing"} {
		t.Run(mode, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			old := otel.GetTracerProvider()
			otel.SetTracerProvider(provider)
			t.Cleanup(func() { otel.SetTracerProvider(old); _ = provider.Shutdown(context.Background()) })
			store, err := sqlite.Open(t.Context(), sqlite.StoreConfig{Path: filepath.Join(t.TempDir(), "usage.db")})
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			saved, err := store.Create(t.Context(), t.TempDir(), session.Metadata{})
			if err != nil {
				t.Fatal(err)
			}
			model := llm.Model{Provider: "test", Name: "model", Pricing: llm.ModelPricing{Input: 2, Output: 10, CacheRead: .2, CacheWrite: 2.5, HasInput: true, HasOutput: true, HasCacheRead: true, HasCacheWrite: true}}
			u := llm.Usage{InputTokens: 100, OutputTokens: 5, CachedTokens: 60, CacheWriteTokens: 30, TotalTokens: 105}
			client := &usageClient{fakeModelClient: fakeModelClient{model: model}, result: &llm.StructuredResult{JSON: []byte(`{}`), Usage: &u}}
			if mode == "invalid" {
				client.resultErr = errors.New("invalid output")
			}
			if mode == "missing" {
				client.result.Usage = nil
			}
			conv, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client, Session: saved, SessionStore: store, SessionCost: saved.Cost, Messages: []session.Message{llm.AssistantMessage{Usage: llm.Usage{InputTokens: 700, OutputTokens: 20}}}})
			if err != nil {
				t.Fatal(err)
			}
			ctx := llm.WithStructuredUsageRecorder(t.Context(), conv.RecordStructuredUsage)
			_, err = llm.PredictStructuredObserved(ctx, client, llm.PredictNextStructuredRequest{}, "compaction")
			if (err != nil) != (mode == "invalid") {
				t.Fatalf("error = %v", err)
			}
			got := conv.ContextUsage()
			if got.InputTokens != 700 || got.OutputTokens != 20 {
				t.Fatalf("context overwritten: %+v", got)
			}
			loaded, messages, found, err := store.Load(t.Context(), saved.ID)
			if err != nil || !found {
				t.Fatalf("load: %v", err)
			}
			if len(messages) != 0 || len(loaded.History) != 1 || loaded.History[0].Type != session.EventUsage {
				t.Fatalf("history = %+v, messages = %+v", loaded.History, messages)
			}
			want := llm.EstimateCost(model, u)
			if mode == "missing" {
				if loaded.Cost.Available || got.Cost.Available {
					t.Fatal("unknown usage reported as complete")
				}
			} else if loaded.Cost.Total != want.Total || got.Cost.Total != want.Total || !loaded.Cost.Available {
				t.Fatalf("cost persisted=%+v live=%+v want=%+v", loaded.Cost, got.Cost, want)
			}
			if err := store.Append(t.Context(), saved.ID, session.Event{Type: session.EventCompaction, Compacted: []session.Message{llm.UserMessage{Text: "summary"}}}); err != nil {
				t.Fatal(err)
			}
			after, effective, _, err := store.Load(t.Context(), saved.ID)
			if err != nil || after.Cost != loaded.Cost || len(effective) != 1 {
				t.Fatalf("compaction changed auxiliary cost or context: %+v, %v", after.Cost, err)
			}
			spans := exporter.GetSpans()
			if len(spans) != 1 {
				t.Fatalf("request spans = %d", len(spans))
			}
			if mode != "missing" {
				if telemetryAttr(spans[0].Attributes, "gen_ai.usage.cache_creation.input_tokens").AsInt64() != 30 {
					t.Fatal("missing write usage")
				}
				if telemetryAttr(spans[0].Attributes, "ronin.cache.read_share").AsFloat64() != .6 {
					t.Fatal("incorrect cache read share")
				}
			}
		})
	}
}

func TestConversationCacheIdentity(t *testing.T) {
	client := &fakeModelClient{events: []llm.PredictionEvent{llm.PredictionFinished{}}}
	conv, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client})
	if err != nil {
		t.Fatal(err)
	}
	prompt := func(c *runtime.Conversation) {
		t.Helper()
		events, errs := c.Prompt(t.Context(), "hello")
		for range events {
		}
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	prompt(conv)
	prompt(conv)
	key := client.requests[0].CacheKey
	if key == "" || client.requests[1].CacheKey != key {
		t.Fatal("cache identity not stable")
	}
	other, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client})
	if err != nil {
		t.Fatal(err)
	}
	prompt(other)
	if client.requests[2].CacheKey == key {
		t.Fatal("unrelated conversations share identity")
	}
	if err := conv.NewConversation(); err != nil {
		t.Fatal(err)
	}
	prompt(conv)
	if client.requests[3].CacheKey == key {
		t.Fatal("new conversation retained identity")
	}
	for range 2 {
		resumed, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client, Session: session.Session{ID: "persisted-session"}})
		if err != nil {
			t.Fatal(err)
		}
		prompt(resumed)
	}
	if client.requests[4].CacheKey != client.requests[5].CacheKey || client.requests[4].CacheKey == "persisted-session" {
		t.Fatal("resumed identity must be stable and opaque")
	}
}
