package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/crowl/ronin/plugin"
)

func TestNilHostIsNoop(t *testing.T) {
	var host *plugin.Host
	call := plugin.ToolCall{Name: "shell", Arguments: json.RawMessage(`{"a":1}`)}
	host.Publish(t.Context(), plugin.PromptTurnStarted{})
	args, err := host.GateToolCall(t.Context(), call)
	if err != nil || string(args) != `{"a":1}` {
		t.Fatalf("gate: %s %v", args, err)
	}
	result, err := host.FilterToolResult(t.Context(), call, json.RawMessage(`"ok"`))
	if err != nil || string(result) != `"ok"` {
		t.Fatalf("filter: %s %v", result, err)
	}
	if err := host.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := host.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPublishDeliversInOrderAndSurvivesPanics(t *testing.T) {
	var order []string
	first := &fakePlugin{name: "first", observe: func(plugin.Event) { order = append(order, "first") }}
	panicky := &fakePlugin{name: "panicky", observe: func(plugin.Event) { panic("boom") }}
	last := &fakePlugin{name: "last", observe: func(plugin.Event) { order = append(order, "last") }}
	host := plugin.NewHost(first, nil, panicky, last)
	host.Publish(t.Context(), plugin.CycleStarted{Index: 1})
	if strings.Join(order, ",") != "first,last" {
		t.Fatalf("order = %v", order)
	}
}

func TestGateChainsRewritesAndFirstDenialWins(t *testing.T) {
	var seen []string
	rewriter := &fakePlugin{name: "rewriter", gate: func(c plugin.ToolCall) (plugin.Decision, error) {
		seen = append(seen, string(c.Arguments))
		return plugin.Rewrite(json.RawMessage(`{"rewritten":true}`)), nil
	}}
	allower := &fakePlugin{name: "allower", gate: func(c plugin.ToolCall) (plugin.Decision, error) {
		seen = append(seen, string(c.Arguments))
		return plugin.Allow(), nil
	}}
	host := plugin.NewHost(rewriter, allower)
	args, err := host.GateToolCall(t.Context(), plugin.ToolCall{Arguments: json.RawMessage(`{}`)})
	if err != nil || string(args) != `{"rewritten":true}` {
		t.Fatalf("args=%s err=%v", args, err)
	}
	if strings.Join(seen, "|") != `{}|{"rewritten":true}` {
		t.Fatalf("rewrite not chained: %v", seen)
	}

	denier := &fakePlugin{name: "policy", gate: func(plugin.ToolCall) (plugin.Decision, error) { return plugin.Deny("forbidden"), nil }}
	unreached := &fakePlugin{name: "unreached", gate: func(plugin.ToolCall) (plugin.Decision, error) {
		t.Fatal("gate after denial must not run")
		return plugin.Allow(), nil
	}}
	host = plugin.NewHost(denier, unreached)
	_, err = host.GateToolCall(t.Context(), plugin.ToolCall{})
	var denied *plugin.DeniedError
	if !errors.As(err, &denied) || denied.Plugin != "policy" || denied.Reason != "forbidden" {
		t.Fatalf("err = %v", err)
	}
}

func TestGateFailsClosedOnErrorAndPanic(t *testing.T) {
	sentinel := errors.New("gate exploded")
	cases := map[string]*fakePlugin{
		"error": {name: "p", gate: func(plugin.ToolCall) (plugin.Decision, error) { return plugin.Allow(), sentinel }},
		"panic": {name: "p", gate: func(plugin.ToolCall) (plugin.Decision, error) { panic("boom") }},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := plugin.NewHost(p).GateToolCall(t.Context(), plugin.ToolCall{})
			if err == nil {
				t.Fatal("expected error")
			}
			var denied *plugin.DeniedError
			if errors.As(err, &denied) {
				t.Fatal("failure must not be reported as a denial")
			}
			if name == "error" && !errors.Is(err, sentinel) {
				t.Fatalf("cause not preserved: %v", err)
			}
		})
	}
}

func TestFilterChainsAndFailsClosed(t *testing.T) {
	upper := &fakePlugin{name: "upper", filter: func(_ plugin.ToolCall, r json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(strings.ToUpper(string(r))), nil
	}}
	suffix := &fakePlugin{name: "suffix", filter: func(_ plugin.ToolCall, r json.RawMessage) (json.RawMessage, error) {
		return append(r, '!'), nil
	}}
	out, err := plugin.NewHost(upper, suffix).FilterToolResult(t.Context(), plugin.ToolCall{}, json.RawMessage("abc"))
	if err != nil || string(out) != "ABC!" {
		t.Fatalf("out=%s err=%v", out, err)
	}
	failing := &fakePlugin{name: "failing", filter: func(plugin.ToolCall, json.RawMessage) (json.RawMessage, error) { panic("boom") }}
	if _, err := plugin.NewHost(failing).FilterToolResult(t.Context(), plugin.ToolCall{}, json.RawMessage("abc")); err == nil || !strings.Contains(err.Error(), "failing") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartDropsFailingPluginsAndCloseRunsInReverse(t *testing.T) {
	var log []string
	good := &fakePlugin{name: "good", start: func() error { log = append(log, "start good"); return nil }, close: func() error { log = append(log, "close good"); return nil }, observe: func(plugin.Event) { log = append(log, "observe good") }}
	bad := &fakePlugin{name: "bad", start: func() error { return errors.New("no") }, observe: func(plugin.Event) { t.Fatal("dropped plugin observed") }}
	later := &fakePlugin{name: "later", close: func() error { log = append(log, "close later"); return errors.New("flush failed") }}
	host := plugin.NewHost(good, bad, later)
	if err := host.Start(t.Context()); err == nil || !strings.Contains(err.Error(), `"bad"`) {
		t.Fatalf("start err = %v", err)
	}
	host.Publish(t.Context(), plugin.WorkflowStarted{})
	if err := host.Close(t.Context()); err == nil || !strings.Contains(err.Error(), "flush failed") {
		t.Fatalf("close err = %v", err)
	}
	if got := strings.Join(log, ";"); got != "start good;observe good;close later;close good" {
		t.Fatalf("log = %s", got)
	}
}

func TestContextCarriesHostAndParents(t *testing.T) {
	if plugin.FromContext(context.Background()) != nil {
		t.Fatal("expected nil host")
	}
	host := plugin.NewHost()
	ctx := plugin.NewContext(context.Background(), host)
	if plugin.FromContext(ctx) != host {
		t.Fatal("host not carried")
	}
	root := plugin.NewOperation(ctx)
	if root.ID == "" || root.ParentID != "" {
		t.Fatalf("root = %+v", root)
	}
	childCtx, parent := plugin.Begin(ctx)
	child := plugin.NewOperation(childCtx)
	if child.ParentID != parent.ID || child.ID == parent.ID {
		t.Fatalf("parent=%+v child=%+v", parent, child)
	}
}

type fakePlugin struct {
	name    string
	start   func() error
	close   func() error
	observe func(plugin.Event)
	gate    func(plugin.ToolCall) (plugin.Decision, error)
	filter  func(plugin.ToolCall, json.RawMessage) (json.RawMessage, error)
}

func (f *fakePlugin) Name() string { return f.name }
func (f *fakePlugin) Start(context.Context) error {
	if f.start == nil {
		return nil
	}
	return f.start()
}
func (f *fakePlugin) Close(context.Context) error {
	if f.close == nil {
		return nil
	}
	return f.close()
}
func (f *fakePlugin) Observe(_ context.Context, e plugin.Event) {
	if f.observe != nil {
		f.observe(e)
	}
}
func (f *fakePlugin) GateToolCall(_ context.Context, c plugin.ToolCall) (plugin.Decision, error) {
	if f.gate == nil {
		return plugin.Allow(), nil
	}
	return f.gate(c)
}
func (f *fakePlugin) FilterToolResult(_ context.Context, c plugin.ToolCall, r json.RawMessage) (json.RawMessage, error) {
	if f.filter == nil {
		return r, nil
	}
	return f.filter(c, r)
}
