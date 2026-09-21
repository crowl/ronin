package guard

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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

func TestNonShellAsksRelevanceOnly(t *testing.T) {
	eval := &fakeEval{answers: map[string]plugin.Answer{"relevant": {Type: "noul", Noul: 0.9}}}
	call := fileCall()
	if err := gateErr(t, NewPlugin(eval), call); err != nil {
		t.Fatal(err)
	}
	if _, ok := eval.questions["relevant"]; !ok {
		t.Fatal("relevant question missing")
	}
	if _, ok := eval.questions["file_bypass"]; ok {
		t.Fatal("non-shell call asked file_bypass")
	}
	if _, ok := eval.questions["irreversible"]; ok {
		t.Fatal("irreversible question must not be asked")
	}
	state, _ := eval.saw.(map[string]any)
	if state["current_request"] != call.Task {
		t.Fatalf("state = %#v", eval.saw)
	}
}

func TestShellAsksBothQuestions(t *testing.T) {
	eval := &fakeEval{answers: allowShell()}
	call := shellCall("go test ./...")
	if err := gateErr(t, NewPlugin(eval), call); err != nil {
		t.Fatal(err)
	}
	if _, ok := eval.questions["relevant"]; !ok {
		t.Fatal("relevant question missing")
	}
	if _, ok := eval.questions["file_bypass"]; !ok {
		t.Fatal("file_bypass question missing")
	}
	state, _ := eval.saw.(map[string]any)
	arguments, _ := state["arguments"].(map[string]any)
	if arguments["command"] != "go test ./..." || state["current_request"] != call.Task {
		t.Fatalf("state = %#v", eval.saw)
	}
}

func TestDeniesOffTaskAndFileBypass(t *testing.T) {
	cases := []struct {
		name    string
		call    plugin.ToolCall
		answers map[string]plugin.Answer
	}{
		{name: "off task", call: fileCall(), answers: map[string]plugin.Answer{"relevant": {Type: "noul", Noul: 0.1}}},
		{name: "file bypass", call: shellCall("sed -n 1,20p main.go"), answers: map[string]plugin.Answer{
			"relevant": {Type: "noul", Noul: 0.9}, "file_bypass": {Type: "noul", Noul: 0.9},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := gateErr(t, NewPlugin(&fakeEval{answers: tc.answers}), tc.call)
			var denied *plugin.DeniedError
			if !errors.As(err, &denied) || denied.Plugin != "guard" {
				t.Fatalf("denial = %v", err)
			}
		})
	}
}

func TestFailsClosed(t *testing.T) {
	cases := []struct {
		name string
		call plugin.ToolCall
		eval plugin.Decider
	}{
		{name: "missing task", call: func() plugin.ToolCall { c := fileCall(); c.Task = ""; return c }(), eval: &fakeEval{answers: allowFile()}},
		{name: "missing shell command", call: plugin.ToolCall{Name: "shell", Arguments: json.RawMessage(`{}`), Parameters: json.RawMessage(`{}`), Context: json.RawMessage(`[]`), Task: "run tests"}, eval: &fakeEval{answers: allowShell()}},
		{name: "decider error", call: fileCall(), eval: &fakeEval{err: errors.New("decider down")}},
		{name: "missing bypass answer", call: shellCall("go test ./..."), eval: &fakeEval{answers: allowFile()}},
		{name: "nil decider", call: fileCall(), eval: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var denied *plugin.DeniedError
			if err := gateErr(t, NewPlugin(tc.eval), tc.call); !errors.As(err, &denied) {
				t.Fatalf("error = %v, want denial", err)
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

func allowShell() map[string]plugin.Answer {
	return map[string]plugin.Answer{
		"relevant":    {Type: "noul", Noul: 0.94},
		"file_bypass": {Type: "noul", Noul: 0.04},
	}
}

func gateErr(t *testing.T, p *Plugin, call plugin.ToolCall) error {
	t.Helper()
	_, err := plugin.NewHost(p).GateToolCall(t.Context(), call)
	return err
}
