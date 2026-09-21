package jev

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crowl/ronin/plugin"
)

type fakeEval struct {
	resp      Response
	err       error
	saw       any
	questions map[string]Question
}

func (f *fakeEval) Decide(_ context.Context, state any, questions map[string]Question) (Response, error) {
	f.saw = state
	f.questions = questions
	return f.resp, f.err
}

func testPlugin(eval evaluator) *Plugin {
	return newPlugin(config{Timeout: defaultTimeout, MinConfidence: defaultMinConfidence}, eval)
}

func validCall() plugin.ToolCall {
	return plugin.ToolCall{
		Name:        "read_file",
		Description: "read a file",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Arguments:   json.RawMessage(`{"path":"main.go"}`),
		Context:     json.RawMessage(`[{"Text":"fix the panic"}]`),
		WorkingDir:  "/work",
		Task:        "fix the nil panic in main.go",
	}
}

func TestGateSendsStructuredStateAndAllQuestions(t *testing.T) {
	eval := &fakeEval{resp: allowResponse()}
	call := validCall()
	if err := gateErr(t, testPlugin(eval), call); err != nil {
		t.Fatal(err)
	}
	state, _ := eval.saw.(map[string]any)
	tool, _ := state["tool"].(map[string]any)
	arguments, _ := state["arguments"].(map[string]any)
	if state["current_request"] != call.Task || tool["name"] != call.Name || tool["description"] != call.Description {
		t.Fatalf("state = %#v", eval.saw)
	}
	if arguments["path"] != "main.go" {
		t.Fatalf("arguments = %#v", arguments)
	}
	if _, ok := eval.questions["relevant"]; !ok {
		t.Fatal("relevant question missing")
	}
	if _, ok := eval.questions["irreversible"]; !ok {
		t.Fatal("irreversible question missing for read tool")
	}
	if _, ok := state["session_id"]; ok {
		t.Fatal("session identifier must not be sent")
	}
}

func TestGateDeniesPolicyVerdict(t *testing.T) {
	err := gateErr(t, testPlugin(&fakeEval{resp: denyResponse()}), validCall())
	var denied *plugin.DeniedError
	if !errors.As(err, &denied) || denied.Plugin != "jev" {
		t.Fatalf("denial = %v", err)
	}
}

func TestGateFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		call plugin.ToolCall
		eval evaluator
	}{
		{name: "missing task", call: func() plugin.ToolCall { c := validCall(); c.Task = ""; return c }(), eval: &fakeEval{resp: allowResponse()}},
		{name: "invalid arguments", call: func() plugin.ToolCall { c := validCall(); c.Arguments = json.RawMessage(`{`); return c }(), eval: &fakeEval{resp: allowResponse()}},
		{name: "client error", call: validCall(), eval: &fakeEval{err: errors.New("typesafe down")}},
		{name: "missing answer", call: validCall(), eval: &fakeEval{resp: Response{Answers: map[string]Answer{"relevant": {Type: "noul", Noul: 0.9}}}}},
		{name: "wrong answer type", call: validCall(), eval: &fakeEval{resp: Response{Answers: map[string]Answer{"relevant": {Type: "choice"}, "irreversible": {Type: "noul"}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var denied *plugin.DeniedError
			if err := gateErr(t, testPlugin(tc.eval), tc.call); !errors.As(err, &denied) {
				t.Fatalf("error = %v, want denial", err)
			}
		})
	}
}

func TestClientRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(denyResponse())
	}))
	t.Cleanup(server.Close)

	c := &client{http: server.Client(), endpoint: server.URL, model: defaultModel, apiKey: "test-key"}
	resp, err := c.Decide(t.Context(), map[string]string{"tool": "shell"}, map[string]Question{"relevant": relevanceQuestion})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answers["relevant"].Noul != 0.08 {
		t.Fatalf("answers = %+v", resp.Answers)
	}
}

func TestClientRetriesRateLimitAndOverload(t *testing.T) {
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt++
		if attempt == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if attempt == 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(529)
			return
		}
		_ = json.NewEncoder(w).Encode(allowResponse())
	}))
	t.Cleanup(server.Close)
	c := &client{http: server.Client(), endpoint: server.URL, model: defaultModel, apiKey: "test-key"}
	if _, err := c.Decide(t.Context(), map[string]string{}, map[string]Question{}); err != nil {
		t.Fatal(err)
	}
	if attempt != 3 {
		t.Fatalf("attempts = %d, want 3", attempt)
	}
}

func TestClientHonorsRetryDelayCap(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Retry-After": []string{"60"}}}
	if got := retryDelay(resp, 1); got != maxRetryDelay {
		t.Fatalf("retry delay = %s, want %s", got, maxRetryDelay)
	}
}

func TestNewPluginRequiresAPIKey(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewPlugin did not panic for an empty API key")
		}
	}()
	NewPlugin("")
}

func TestNewPluginUsesFixedConfiguration(t *testing.T) {
	p := NewPlugin(" key ")
	if p.cfg.Endpoint != defaultEndpoint || p.cfg.Model != defaultModel || p.cfg.Timeout != defaultTimeout || p.cfg.MinConfidence != defaultMinConfidence {
		t.Fatalf("config = %+v", p.cfg)
	}
	if p.cfg.APIKey != " key " {
		t.Fatalf("api key = %q", p.cfg.APIKey)
	}
	if _, ok := p.client.(*client); !ok {
		t.Fatalf("client = %T, want *client", p.client)
	}
}

func gateErr(t *testing.T, p *Plugin, call plugin.ToolCall) error {
	t.Helper()
	_, err := plugin.NewHost(p).GateToolCall(t.Context(), call)
	return err
}

func denyResponse() Response {
	return Response{Answers: map[string]Answer{
		"relevant":     {Type: "noul", Noul: 0.08},
		"irreversible": {Type: "noul", Noul: 0.96},
	}}
}

func allowResponse() Response {
	return Response{Answers: map[string]Answer{
		"relevant":     {Type: "noul", Noul: 0.94},
		"irreversible": {Type: "noul", Noul: 0.04},
	}}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	if got := truncate("ab世界", 5); !strings.HasPrefix("ab世界", got) || !json.Valid([]byte(`"`+got+`"`)) {
		t.Fatalf("truncate = %q", got)
	}
}
