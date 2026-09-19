package jev

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crowl/ronin/plugin"
)

type fakeEval struct {
	resp Response
	err  error
	saw  any
}

func (f *fakeEval) Decide(_ context.Context, state any, _ map[string]Question) (Response, error) {
	f.saw = state
	return f.resp, f.err
}

func testPlugin(mode Mode, eval evaluator) *Plugin {
	return &Plugin{
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg:    Config{Mode: mode, Timeout: defaultTimeout, MinConfidence: defaultMinConfidence},
		client: eval,
	}
}

func TestGateSkipsWhenOffOrNoTask(t *testing.T) {
	eval := &fakeEval{resp: denyResponse()}
	if err := gateErr(t, testPlugin(ModeOff, eval), plugin.ToolCall{Name: "shell", Task: "rm everything"}); err != nil {
		t.Fatalf("off: %v", err)
	}
	if eval.saw != nil {
		t.Fatal("off mode must not call Jev")
	}

	if err := gateErr(t, testPlugin(ModeEnforce, eval), plugin.ToolCall{Name: "shell"}); err != nil {
		t.Fatalf("no task: %v", err)
	}
	if eval.saw != nil {
		t.Fatal("missing task must not call Jev")
	}
}

func TestGateSendsTaskAndReadTools(t *testing.T) {
	eval := &fakeEval{resp: allowResponse()}
	call := plugin.ToolCall{Name: "read_file", Arguments: json.RawMessage(`{"path":"main.go"}`), Task: "fix the nil panic in main.go"}
	if err := gateErr(t, testPlugin(ModeEnforce, eval), call); err != nil {
		t.Fatalf("read_file: %v", err)
	}
	state, _ := eval.saw.(map[string]any)
	if state == nil || state["task"] != call.Task || state["tool"] != "read_file" {
		t.Fatalf("state = %#v", eval.saw)
	}
}

func TestGateEnforceDeniesAndShadowAllows(t *testing.T) {
	eval := &fakeEval{resp: denyResponse()}
	call := plugin.ToolCall{Name: "shell", Arguments: json.RawMessage(`{"command":"rm -rf /"}`), WorkingDir: "/tmp", Task: "add a unit test for parseFlags"}

	err := gateErr(t, testPlugin(ModeEnforce, eval), call)
	var denied *plugin.DeniedError
	if !errors.As(err, &denied) || denied.Plugin != "jev" {
		t.Fatalf("enforce: %v", err)
	}

	if err := gateErr(t, testPlugin(ModeShadow, eval), call); err != nil {
		t.Fatalf("shadow: %v", err)
	}
}

func TestGateFailsOpenOnClientError(t *testing.T) {
	eval := &fakeEval{err: errors.New("typesafe down")}
	if err := gateErr(t, testPlugin(ModeEnforce, eval), plugin.ToolCall{Name: "write_file", Arguments: json.RawMessage(`{}`), Task: "edit README"}); err != nil {
		t.Fatalf("fail open: %v", err)
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

	c := &client{
		http:     server.Client(),
		endpoint: server.URL,
		model:    "jev-latest",
		apiKey:   "test-key",
	}
	resp, err := c.Decide(t.Context(), map[string]string{"tool": "shell"}, map[string]Question{"relevant": relevanceQuestion})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Answers["relevant"].Noul != 0.08 {
		t.Fatalf("answers = %+v", resp.Answers)
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("RONIN_JEV_MODE", "")
	t.Setenv("RONIN_JEV_ENDPOINT", "")
	t.Setenv("RONIN_JEV_MODEL", "")
	t.Setenv("RONIN_JEV_TIMEOUT", "")
	t.Setenv("RONIN_JEV_MIN_CONFIDENCE", "")
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("RONIN_JEV_API_KEY_ENV", "")

	cfg, err := loadConfig()
	if err != nil || cfg.Mode != ModeOff || cfg.Endpoint != defaultEndpoint {
		t.Fatalf("default cfg = %+v err=%v", cfg, err)
	}

	t.Setenv("RONIN_JEV_MODE", "enforce")
	t.Setenv("RONIN_JEV_ENDPOINT", "http://127.0.0.1/v1/systemone")
	t.Setenv("RONIN_JEV_MODEL", "jev-1.13.0")
	t.Setenv("RONIN_JEV_TIMEOUT", "200ms")
	t.Setenv("RONIN_JEV_MIN_CONFIDENCE", "0.7")
	t.Setenv("TYPESAFE_API_KEY", "tsk_test")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeEnforce || cfg.APIKey != "tsk_test" || cfg.MinConfidence != 0.7 || cfg.Model != "jev-1.13.0" {
		t.Fatalf("override cfg = %+v", cfg)
	}

	t.Setenv("RONIN_JEV_MODE", "maybe")
	if _, err := loadConfig(); err == nil {
		t.Fatal("expected invalid mode error")
	}
}

func TestStartRequiresKeyWhenActive(t *testing.T) {
	t.Setenv("RONIN_JEV_MODE", "shadow")
	t.Setenv("TYPESAFE_API_KEY", "")
	p := NewPlugin()
	if err := p.Start(context.Background()); err == nil {
		t.Fatal("expected missing key error")
	}

	t.Setenv("RONIN_JEV_MODE", "off")
	p = NewPlugin()
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := gateErr(t, p, plugin.ToolCall{Name: "shell", Task: "x"}); err != nil {
		t.Fatalf("off after start: %v", err)
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
		"relevant": {Type: "noul", Noul: 0.94},
	}}
}
