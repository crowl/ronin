package workflow

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadOnlyDetectsContentChanges(t *testing.T) {
	for _, kind := range []string{"tracked", "staged", "untracked"} {
		t.Run(kind, func(t *testing.T) {
			repo := initTestRepository(t)
			name := "README.md"
			if kind == "untracked" {
				name = "new file\n.txt"
			}
			path := filepath.Join(repo, name)
			if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "staged" {
				gitCommand(t, repo, "add", name)
			}
			_, err := runScriptAtWorkingDirWithAgent(t, repo, `ronin.run_agent({prompt="review", read_only=true})`, func(context.Context, AgentRequest) (AgentResult, error) {
				err := os.WriteFile(path, []byte("after\n"), 0600)
				if kind == "staged" {
					gitCommand(t, repo, "add", name)
				}
				return AgentResult{Text: "ok"}, err
			})
			if err == nil || !strings.Contains(err.Error(), "read-only agent changed") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestReadOnlyFingerprintScope(t *testing.T) {
	repo := initTestRepository(t)
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "ignored.txt\n")
	write("ignored.txt", "before")
	write("README.md", "dirty\n")
	rt := newWorktreeRuntime(t.Context())
	rt.setWorkingDir(repo)
	before, err := rt.fingerprint("")
	if err != nil {
		t.Fatal(err)
	}
	write("ignored.txt", "after")
	after, err := rt.fingerprint("")
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("ignored content changed fingerprint")
	}

	// Streaming diffs must include changes beyond the normal Git output limit.
	prefix := strings.Repeat("large line\n", gitOutputLimit/5)
	write("README.md", prefix+"before\n")
	before, err = rt.fingerprint("")
	if err != nil {
		t.Fatal(err)
	}
	write("README.md", prefix+"after\n")
	after, err = rt.fingerprint("")
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("large diff tail was not fingerprinted")
	}
}

func TestWorkflowCallbacksAreSerialized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.lua")
	if err := os.WriteFile(path, []byte(`
local job = ronin.start_agent({prompt="work"})
for i=1,100 do ronin.log("progress") end
ronin.wait_any({job})
`), 0600); err != nil {
		t.Fatal(err)
	}
	var active atomic.Int32
	var overlap atomic.Bool
	emit := func(Event) {
		if active.Add(1) != 1 {
			overlap.Store(true)
		}
		time.Sleep(time.Millisecond)
		active.Add(-1)
	}
	err := runFile(t.Context(), path, "", "", io.Discard, func(_ context.Context, req AgentRequest) (AgentResult, error) {
		for range 100 {
			req.Progress(AgentTextDelta{Text: "progress"})
		}
		return AgentResult{Text: "done"}, nil
	}, emit)
	if err != nil {
		t.Fatal(err)
	}
	if overlap.Load() {
		t.Fatal("event callbacks overlapped")
	}
}
