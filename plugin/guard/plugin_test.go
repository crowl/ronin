package guard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/crowl/ronin/plugin"
)

type fakeEval struct {
	answers   map[string]plugin.Answer
	err       error
	saw       any
	questions map[string]plugin.Question
}

func (f *fakeEval) Decide(_ context.Context, state any, questions map[string]plugin.Question) (map[string]plugin.Answer, error) {
	f.saw = state
	f.questions = questions
	return f.answers, f.err
}

func TestToolCallsAskRelevanceOnly(t *testing.T) {
	for _, call := range []plugin.ToolCall{fileCall(), shellCall("git worktree add ../branch branch")} {
		t.Run(call.Name, func(t *testing.T) {
			eval := &fakeEval{answers: map[string]plugin.Answer{"relevant": {Type: "noul", Noul: 0.9}}}
			if err := gateErr(t, NewPlugin(eval), call); err != nil {
				t.Fatal(err)
			}
			if len(eval.questions) != 1 || eval.questions["relevant"].Type != "noul" {
				t.Fatalf("questions = %#v, want relevance only", eval.questions)
			}
			state, _ := eval.saw.(map[string]any)
			arguments, _ := state["arguments"].(map[string]any)
			if state["current_request"] != call.Task || arguments == nil {
				t.Fatalf("state = %#v", eval.saw)
			}
			if call.Name == "shell" && arguments["command"] != "git worktree add ../branch branch" {
				t.Fatalf("arguments = %#v", arguments)
			}
		})
	}
}

func TestDeniesOffTask(t *testing.T) {
	for _, call := range []plugin.ToolCall{fileCall(), shellCall("git worktree add ../branch branch")} {
		t.Run(call.Name, func(t *testing.T) {
			err := gateErr(t, NewPlugin(&fakeEval{answers: map[string]plugin.Answer{
				"relevant": {Type: "noul", Noul: 0.1},
			}}), call)
			var denied *plugin.DeniedError
			if !errors.As(err, &denied) || denied.Plugin != "guard" {
				t.Fatalf("denial = %v", err)
			}
		})
	}
}

func TestAllowsEvaluationTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "deadline exceeded", err: context.DeadlineExceeded},
		{name: "wrapped deadline exceeded", err: fmt.Errorf("request: %w", context.DeadlineExceeded)},
		{name: "network timeout", err: fmt.Errorf("request: %w", &net.DNSError{IsTimeout: true})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := gateErr(t, NewPlugin(&fakeEval{err: tc.err}), shellCall("go test ./...")); err != nil {
				t.Fatalf("timeout blocked shell call: %v", err)
			}
		})
	}
}

type waitingEval struct{}

func (waitingEval) Decide(ctx context.Context, _ any, _ map[string]plugin.Question) (map[string]plugin.Answer, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAllowsEvaluationDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	_, err := plugin.NewHost(NewPlugin(waitingEval{})).GateToolCall(ctx, fileCall())
	if err != nil {
		t.Fatalf("expired evaluation blocked call: %v", err)
	}
}

func TestAllowsWithoutExplicitDenial(t *testing.T) {
	cases := []struct {
		name string
		call plugin.ToolCall
		eval plugin.Decider
	}{
		{name: "missing task", call: func() plugin.ToolCall { c := fileCall(); c.Task = ""; return c }(), eval: &fakeEval{answers: allowFile()}},
		{name: "missing arguments", call: func() plugin.ToolCall { c := fileCall(); c.Arguments = nil; return c }(), eval: &fakeEval{answers: allowFile()}},
		{name: "invalid arguments", call: func() plugin.ToolCall { c := fileCall(); c.Arguments = json.RawMessage(`{`); return c }(), eval: &fakeEval{answers: allowFile()}},
		{name: "missing parameters", call: func() plugin.ToolCall { c := fileCall(); c.Parameters = nil; return c }(), eval: &fakeEval{answers: allowFile()}},
		{name: "invalid context", call: func() plugin.ToolCall { c := fileCall(); c.Context = json.RawMessage(`{`); return c }(), eval: &fakeEval{answers: allowFile()}},
		{name: "missing shell command", call: plugin.ToolCall{Name: "shell", Arguments: json.RawMessage(`{}`), Parameters: json.RawMessage(`{}`), Context: json.RawMessage(`[]`), Task: "run tests"}, eval: &fakeEval{answers: allowFile()}},
		{name: "missing relevance answer", call: shellCall("go test ./..."), eval: &fakeEval{answers: map[string]plugin.Answer{}}},
		{name: "decider error", call: fileCall(), eval: &fakeEval{err: errors.New("decider down")}},
		{name: "canceled", call: fileCall(), eval: &fakeEval{err: context.Canceled}},
		{name: "nil decider", call: fileCall(), eval: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := gateErr(t, NewPlugin(tc.eval), tc.call); err != nil {
				t.Fatalf("call blocked without explicit denial: %v", err)
			}
		})
	}
}

func fileCall() plugin.ToolCall {
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

func shellCall(command string) plugin.ToolCall {
	call := fileCall()
	call.Name = "shell"
	raw, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		panic(err)
	}
	call.Arguments = raw
	return call
}

func allowFile() map[string]plugin.Answer {
	return map[string]plugin.Answer{"relevant": {Type: "noul", Noul: 0.94}}
}

func gateErr(t *testing.T, p *Plugin, call plugin.ToolCall) error {
	t.Helper()
	_, err := plugin.NewHost(p).GateToolCall(t.Context(), call)
	return err
}
