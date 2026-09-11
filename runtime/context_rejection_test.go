package runtime_test

import (
	"context"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
	"github.com/crowl/ronin/runtime"
)

func TestHTTPContextRejectionRecovery(t *testing.T) {
	for _, always := range []bool{false, true} {
		client := &contextRejectingClient{always: always}
		compactor := &fakeCompactor{messages: []llm.Message{llm.UserMessage{Text: "short"}}}
		c, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client, Compactor: compactor})
		if err != nil {
			t.Fatal(err)
		}
		events, errs := c.Prompt(t.Context(), "a sufficiently long request to ensure that the compacted version is smaller")
		collectEvents(events)
		err = <-errs
		if (err != nil) != always {
			t.Fatalf("always=%v error=%v", always, err)
		}
		if client.calls != 2 {
			t.Fatalf("calls=%d; expected exactly one retry", client.calls)
		}
	}
}

func TestContextRecoveryRejectsNonReducingCompaction(t *testing.T) {
	client := &contextRejectingClient{}
	compactor := &fakeCompactor{messages: []llm.Message{llm.UserMessage{Text: strings.Repeat("larger", 100)}}}
	c, err := runtime.NewConversation(runtime.ConversationConfig{ModelClient: client, Compactor: compactor})
	if err != nil {
		t.Fatal(err)
	}
	events, errs := c.Prompt(t.Context(), "small")
	collectEvents(events)
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "cannot reduce") {
		t.Fatalf("error = %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("calls = %d", client.calls)
	}
}

type contextRejectingClient struct {
	fakeModelClient
	calls  int
	always bool
}

func (c *contextRejectingClient) PredictNext(context.Context, llm.PredictNextRequest) (<-chan llm.PredictionEvent, <-chan error) {
	c.calls++
	events := make(chan llm.PredictionEvent, 1)
	errs := make(chan error, 1)
	if c.calls == 1 || c.always {
		errs <- llm.HTTPError("openai", 400, `{"error":{"code":"context_length_exceeded"}}`)
	} else {
		events <- llm.PredictionFinished{}
	}
	close(events)
	close(errs)
	return events, errs
}
