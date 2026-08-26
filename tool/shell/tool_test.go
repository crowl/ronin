package shell_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/crowl/ronin/tool"
	"github.com/crowl/ronin/tool/shell"
)

func TestToolDescriptionGuidesGoSearch(t *testing.T) {
	description := shell.New(t.TempDir()).Description()
	for _, expected := range []string{"outline_package", "find_symbol", "before text search", "references", "call sites", "fields", "local declarations"} {
		if !strings.Contains(description, expected) {
			t.Errorf("Description() = %q, want %q", description, expected)
		}
	}
}

func TestToolCall(t *testing.T) {
	t.Run("captures stdout and stderr by default", func(t *testing.T) {
		res, err := callShell(t, shell.New(t.TempDir()), shell.Args{Command: "printf out; printf err >&2"})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if !res.Success {
			t.Fatalf("Success = false, exit code %d", res.ExitCode)
		}
		if res.Stdout != "out" {
			t.Fatalf("Stdout = %q, want out", res.Stdout)
		}
		if res.Stderr != "err" {
			t.Fatalf("Stderr = %q, want err", res.Stderr)
		}
	})

	t.Run("truncates output with small limit", func(t *testing.T) {
		res, err := callShell(t, shell.New(t.TempDir()), shell.Args{Command: "printf abcdef", MaxOutputBytes: 3})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if res.Stdout != "abc" {
			t.Fatalf("Stdout = %q, want abc", res.Stdout)
		}
		if !res.StdoutTruncated {
			t.Fatal("StdoutTruncated = false, want true")
		}
	})

	t.Run("empty command validation preserves structured error", func(t *testing.T) {
		_, err := shell.New(t.TempDir()).Call(context.Background(), []byte(`{"command":"   "}`))
		if err == nil {
			t.Fatal("Call() error = nil, want invalid args")
		}

		var toolErr tool.Error
		if !errors.As(err, &toolErr) {
			t.Fatalf("Call() error = %v, want tool.Error", err)
		}
		if toolErr.Code != "invalid_args" {
			t.Fatalf("toolErr.Code = %q, want invalid_args", toolErr.Code)
		}
		if !strings.Contains(toolErr.Message, "command") {
			t.Fatalf("toolErr.Message = %q, want command context", toolErr.Message)
		}
	})

	t.Run("captures requested output above default limit", func(t *testing.T) {
		res, err := callShell(t, shell.New(t.TempDir()), shell.Args{Command: "yes x | head -c 150000", MaxOutputBytes: 150000})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if len(res.Stdout) != 150000 {
			t.Fatalf("len(Stdout) = %d, want 150000", len(res.Stdout))
		}
		if res.StdoutTruncated {
			t.Fatal("StdoutTruncated = true, want false")
		}
	})

	t.Run("caps requested output above hard limit", func(t *testing.T) {
		res, err := callShell(t, shell.New(t.TempDir()), shell.Args{Command: "yes x | head -c 9000000", MaxOutputBytes: 9_000_000})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if len(res.Stdout) != 8*1024*1024 {
			t.Fatalf("len(Stdout) = %d, want hard cap", len(res.Stdout))
		}
		if !res.StdoutTruncated {
			t.Fatal("StdoutTruncated = false, want true")
		}
	})

	t.Run("timeout returns timed out result", func(t *testing.T) {
		res, err := callShell(t, shell.New(t.TempDir()), shell.Args{Command: "sleep 1", TimeoutMS: 10})
		if err != nil {
			t.Fatalf("Call() error = %v", err)
		}
		if !res.TimedOut {
			t.Fatal("TimedOut = false, want true")
		}
		if res.Success {
			t.Fatal("Success = true, want false")
		}
		if res.ExitCode != -1 {
			t.Fatalf("ExitCode = %d, want -1", res.ExitCode)
		}
	})
	t.Run("streams stdout and stderr", func(t *testing.T) {
		shellTool := shell.New(t.TempDir())
		raw, err := json.Marshal(shell.Args{Command: "printf out; printf err >&2"})
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}

		var artifacts []tool.Artifact
		var artifactsMu sync.Mutex
		result, err := shellTool.CallIncremental(t.Context(), raw, func(artifact tool.Artifact) error {
			artifactsMu.Lock()
			defer artifactsMu.Unlock()
			artifacts = append(artifacts, artifact)
			return nil
		})
		if err != nil {
			t.Fatalf("CallIncremental() error = %v", err)
		}

		res, ok := result.(shell.Result)
		if !ok {
			t.Fatalf("result type = %T, want shell.Result", result)
		}
		if res.Stdout != "out" || res.Stderr != "err" {
			t.Fatalf("result stdout/stderr = %q/%q, want out/err", res.Stdout, res.Stderr)
		}
		artifactsMu.Lock()
		gotArtifacts := append([]tool.Artifact(nil), artifacts...)
		artifactsMu.Unlock()
		if !containsShellStreamArtifact(gotArtifacts, tool.ShellStreamStdout, "out") {
			t.Fatalf("stdout artifact not found in %#v", gotArtifacts)
		}
		if !containsShellStreamArtifact(gotArtifacts, tool.ShellStreamStderr, "err") {
			t.Fatalf("stderr artifact not found in %#v", gotArtifacts)
		}
	})
}

func containsShellStreamArtifact(artifacts []tool.Artifact, stream tool.ShellStream, content string) bool {
	for _, artifact := range artifacts {
		streamArtifact, ok := artifact.(tool.ShellStreamArtifact)
		if ok && streamArtifact.Stream == stream && streamArtifact.Content == content {
			return true
		}
	}
	return false
}

func callShell(t *testing.T, shellTool *shell.Tool, args shell.Args) (shell.Result, error) {
	t.Helper()

	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	result, err := shellTool.Call(t.Context(), raw)
	if err != nil {
		return shell.Result{}, err
	}

	res, ok := result.(shell.Result)
	if !ok {
		t.Fatalf("Call() result type = %T, want shell.Result", result)
	}
	return res, nil
}
