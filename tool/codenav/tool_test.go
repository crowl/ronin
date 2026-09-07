package codenav_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crowl/ronin/tool/codenav"
)

func TestNavigationTools(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "auth.go"), []byte("package auth\nfunc ValidateSession() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		tool *codenav.Tool
		args string
		name string
	}{{codenav.NewMap(root), `{"path":"auth.go"}`, "code_map"}, {codenav.NewFind(root), `{"query":"validate session"}`, "code_find"}} {
		if tt.tool.Name() != tt.name {
			t.Fatal(tt.tool.Name())
		}
		result, err := tt.tool.Call(t.Context(), json.RawMessage(tt.args))
		if err != nil {
			t.Fatal(err)
		}
		r := result.(codenav.Result)
		if len(r.Entries) == 0 {
			t.Fatal("empty results")
		}
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "ValidateSession") {
			t.Fatal(string(raw))
		}
		if len(r.Artifacts()) == 0 {
			t.Fatal("missing UI output")
		}
		if tt.tool.Parameters() == nil {
			t.Fatal("missing schema")
		}
	}
}

func TestInvalidArgumentsAndCancellation(t *testing.T) {
	tool := codenav.NewFind(t.TempDir())
	for _, raw := range []string{`{}`, `{"query":"x","path":"../"}`, `{"query":"x","limit":101}`, `{"query":"x","language":"rust"}`, `{"query":"x","kind":"unknown"}`, `{"query":"x","extra":true}`} {
		if _, err := tool.Call(t.Context(), json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := tool.Call(ctx, json.RawMessage(`{"query":"x"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
