package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crowl/ronin/llm"
)

func TestRunFile(t *testing.T) {
	t.Parallel()

	t.Run("base library does not expose dofile, loadfile, pcall, xpcall, or rawset", func(t *testing.T) {
		t.Parallel()

		for _, expr := range []string{
			"dofile(\"x.lua\")",
			"loadfile(\"x.lua\")",
			"pcall(function() ronin.done(\"finished\") end)",
			"xpcall(function() ronin.fail(\"broken\") end, function(err) return err end)",
			"rawset(ronin, \"input\", \"changed\")",
		} {
			t.Run(expr, func(t *testing.T) {
				t.Parallel()

				_, err := runScript(t, expr)
				if err == nil {
					t.Fatalf("RunFile() error = nil, want runtime error")
				}
				if !strings.Contains(err.Error(), "attempt to call") {
					t.Fatalf("RunFile() error = %v, want call failure", err)
				}
			})
		}
	})

	t.Run("log and normal completion", func(t *testing.T) {
		t.Parallel()

		stdout, err := runScript(t, `ronin.log("hello")
ronin.log({ answer = 42, ok = true })
`)
		if err != nil {
			t.Fatalf("RunFile() error = %v", err)
		}
		if !strings.Contains(stdout, "hello") {
			t.Fatalf("stdout = %q, want hello", stdout)
		}
		if !strings.Contains(stdout, "\"answer\": 42") || !strings.Contains(stdout, "\"ok\": true") {
			t.Fatalf("stdout = %q, want table log output", stdout)
		}
	})

	t.Run("done stops later statements", func(t *testing.T) {
		t.Parallel()

		stdout, err := runScript(t, `ronin.log("before")
ronin.done("finished")
ronin.log("after")
`)
		if err == nil {
			t.Fatal("RunFile() error = nil, want DoneError")
		}
		var doneErr *DoneError
		if !errors.As(err, &doneErr) {
			t.Fatalf("RunFile() error type = %T, want *DoneError", err)
		}
		if doneErr.Message != "finished" {
			t.Fatalf("DoneError.Message = %q, want %q", doneErr.Message, "finished")
		}
		if !strings.Contains(stdout, "before") {
			t.Fatalf("stdout = %q, want before log", stdout)
		}
		if strings.Contains(stdout, "after") {
			t.Fatalf("stdout = %q, did not expect after log", stdout)
		}
	})

	t.Run("fail stops later statements", func(t *testing.T) {
		t.Parallel()

		stdout, err := runScript(t, `ronin.log("before")
ronin.fail("broken")
ronin.log("after")
`)
		if err == nil {
			t.Fatal("RunFile() error = nil, want FailureError")
		}
		var failureErr *FailureError
		if !errors.As(err, &failureErr) {
			t.Fatalf("RunFile() error type = %T, want *FailureError", err)
		}
		if failureErr.Message != "broken" {
			t.Fatalf("FailureError.Message = %q, want %q", failureErr.Message, "broken")
		}
		if !strings.Contains(stdout, "before") {
			t.Fatalf("stdout = %q, want before log", stdout)
		}
		if strings.Contains(stdout, "after") {
			t.Fatalf("stdout = %q, did not expect after log", stdout)
		}
	})

	t.Run("syntax error", func(t *testing.T) {
		t.Parallel()

		_, err := runScript(t, `local value =`)
		if err == nil {
			t.Fatal("RunFile() error = nil, want syntax error")
		}
		if _, ok := err.(*DoneError); ok {
			t.Fatalf("RunFile() error type = %T, want parse error", err)
		}
		if !strings.Contains(err.Error(), "parse workflow script") {
			t.Fatalf("RunFile() error = %v, want parse workflow script message", err)
		}
	})

	t.Run("runtime error", func(t *testing.T) {
		t.Parallel()

		_, err := runScript(t, `ronin.missing()`)
		if err == nil {
			t.Fatal("RunFile() error = nil, want runtime error")
		}
		var doneErr *DoneError
		var failureErr *FailureError
		if errors.As(err, &doneErr) || errors.As(err, &failureErr) {
			t.Fatalf("RunFile() error type = %T, want runtime error", err)
		}
		if !strings.Contains(err.Error(), "attempt to call") {
			t.Fatalf("RunFile() error = %v, want call failure", err)
		}
	})

	t.Run("runtime error mentioning done is not a done signal", func(t *testing.T) {
		t.Parallel()

		_, err := runScript(t, `ronin.done = "not controlled"
ronin.done()`)
		if err == nil {
			t.Fatal("RunFile() error = nil, want runtime error")
		}
		if _, ok := errors.AsType[*DoneError](err); ok {
			t.Fatalf("RunFile() error type = %T, want runtime error", err)
		}
		if !strings.Contains(err.Error(), "attempt to call") {
			t.Fatalf("RunFile() error = %v, want original runtime error", err)
		}
	})
}

func TestRunFileWithAgent(t *testing.T) {
	t.Parallel()

	t.Run("ronin.read returns metadata and content", func(t *testing.T) {
		workDir := t.TempDir()
		content := "hello, workflow\n"
		if err := os.WriteFile(filepath.Join(workDir, "note.txt"), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		stdout, err := runScriptAtPath(t, workDir, `local file = ronin.read("note.txt")
ronin.log(file.ok)
ronin.log(file.path)
ronin.log(file.size)
ronin.log(file.content)
ronin.log(file.sha256)
`)
		if err != nil {
			t.Fatalf("RunFileWithAgent() error = %v", err)
		}
		if !strings.Contains(stdout, "true") || !strings.Contains(stdout, "note.txt") || !strings.Contains(stdout, content) {
			t.Fatalf("stdout = %q, want read result", stdout)
		}
		if !strings.Contains(stdout, fmt.Sprintf("%x", sha256.Sum256([]byte(content)))) {
			t.Fatalf("stdout = %q, want sha256 log", stdout)
		}
	})

	t.Run("ronin.read supports absolute paths", func(t *testing.T) {
		otherDir := t.TempDir()
		absPath := filepath.Join(otherDir, "external.txt")
		if err := os.WriteFile(absPath, []byte("external"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		stdout, err := runScriptAtPath(t, t.TempDir(), fmt.Sprintf(`local file = ronin.read(%q)
ronin.log(file.path)
ronin.log(file.content)
`, absPath))
		if err != nil {
			t.Fatalf("RunFileWithAgent() error = %v", err)
		}
		if !strings.Contains(stdout, "external") {
			t.Fatalf("stdout = %q, want external content", stdout)
		}
		if !strings.Contains(stdout, filepath.ToSlash(absPath)) {
			t.Fatalf("stdout = %q, want absolute display path", stdout)
		}
	})

	t.Run("ronin.read resolves relative paths from explicit workflow working directory", func(t *testing.T) {
		scriptDir := t.TempDir()
		workDir := t.TempDir()
		content := "from workflow dir\n"
		if err := os.WriteFile(filepath.Join(workDir, "note.txt"), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		scriptPath := filepath.Join(scriptDir, "workflow.lua")
		if err := os.WriteFile(scriptPath, []byte(`local file = ronin.read("note.txt")
ronin.log(file.path)
ronin.log(file.content)
`), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		var output strings.Builder
		err := RunFileWithAgentInWorkingDir(t.Context(), scriptPath, workDir, &output, nil)
		if err != nil {
			t.Fatalf("RunFileWithAgentInWorkingDir() error = %v", err)
		}
		stdout := output.String()
		if !strings.Contains(stdout, content) {
			t.Fatalf("stdout = %q, want workflow-dir content", stdout)
		}
		if !strings.Contains(stdout, "note.txt") {
			t.Fatalf("stdout = %q, want relative file path", stdout)
		}
	})

	t.Run("ronin.read rejects invalid paths and files", func(t *testing.T) {
		workDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(workDir, "binary.bin"), []byte{0x00, 0x01, 0x02}, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(workDir, "big.txt"), bytes.Repeat([]byte("a"), int(maxReadBytes)+1), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		if err := os.Mkdir(filepath.Join(workDir, "dir"), 0o700); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}

		cases := map[string]struct {
			script string
			want   string
		}{
			"missing path":    {script: `ronin.read()`, want: "path is required"},
			"non-string path": {script: `ronin.read(123)`, want: "path must be a string"},
			"empty path":      {script: `ronin.read("   ")`, want: "path is required"},
			"nul path":        {script: `ronin.read("a" .. string.char(0) .. "b")`, want: "path must not contain NUL bytes"},
			"missing file":    {script: `ronin.read("missing.txt")`, want: "file does not exist"},
			"directory":       {script: `ronin.read("dir")`, want: "path is a directory"},
			"binary file":     {script: `ronin.read("binary.bin")`, want: "file appears to be binary"},
			"too large":       {script: `ronin.read("big.txt")`, want: "file exceeds maximum size"},
		}

		for name, tc := range cases {
			t.Run(name, func(t *testing.T) {
				_, err := runScriptAtPath(t, workDir, tc.script)
				if err == nil {
					t.Fatal("RunFileWithAgent() error = nil, want runtime error")
				}
				if !strings.Contains(err.Error(), "ronin.read:") {
					t.Fatalf("RunFileWithAgent() error = %v, want ronin.read prefix", err)
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("RunFileWithAgent() error = %v, want %q", err, tc.want)
				}
			})
		}
	})

	t.Run("returns agent result table", func(t *testing.T) {
		t.Parallel()

		stdout, err := runScriptWithAgent(t, `local result = ronin.run_agent({ prompt = "hello" })
ronin.log(result.ok)
ronin.log(result.text)
`, func(context.Context, AgentRequest) (AgentResult, error) {
			return AgentResult{Text: "assistant text"}, nil
		})
		if err != nil {
			t.Fatalf("RunFileWithAgent() error = %v", err)
		}
		if !strings.Contains(stdout, "true") || !strings.Contains(stdout, "assistant text") {
			t.Fatalf("stdout = %q, want agent result", stdout)
		}
	})

	t.Run("passes spec fields", func(t *testing.T) {
		t.Parallel()

		var got AgentRequest
		_, err := runScriptWithAgent(t, `ronin.run_agent({
  prompt = " hello ",
  model = "openai:gpt-5.5",
  reasoning = "high",
  system = "reviewer",
  future = true,
})`, func(_ context.Context, req AgentRequest) (AgentResult, error) {
			got = req
			return AgentResult{Text: "ok"}, nil
		})
		if err != nil {
			t.Fatalf("RunFileWithAgent() error = %v", err)
		}
		if got.Prompt != "hello" {
			t.Fatalf("Prompt = %q, want hello", got.Prompt)
		}
		if got.Model.Provider != "openai" || got.Model.Name != "gpt-5.5" {
			t.Fatalf("Model = %#v, want openai:gpt-5.5", got.Model)
		}
		if got.ReasoningLevel != llm.ReasoningLevelHigh {
			t.Fatalf("ReasoningLevel = %q, want high", got.ReasoningLevel)
		}
		if got.System != "reviewer" {
			t.Fatalf("System = %q, want reviewer", got.System)
		}
	})

	t.Run("multiple calls are independent", func(t *testing.T) {
		t.Parallel()

		var prompts []string
		stdout, err := runScriptWithAgent(t, `local first = ronin.run_agent({ prompt = "one" })
local second = ronin.run_agent({ prompt = "two" })
ronin.log(first.text .. ":" .. second.text)
`, func(_ context.Context, req AgentRequest) (AgentResult, error) {
			prompts = append(prompts, req.Prompt)
			return AgentResult{Text: req.Prompt + "-result"}, nil
		})
		if err != nil {
			t.Fatalf("RunFileWithAgent() error = %v", err)
		}
		if got, want := strings.Join(prompts, ","), "one,two"; got != want {
			t.Fatalf("prompts = %q, want %q", got, want)
		}
		if !strings.Contains(stdout, "one-result:two-result") {
			t.Fatalf("stdout = %q, want both results", stdout)
		}
	})

	t.Run("missing prompt fails", func(t *testing.T) {
		t.Parallel()

		_, err := runScriptWithAgent(t, `ronin.run_agent({})`, func(context.Context, AgentRequest) (AgentResult, error) {
			return AgentResult{}, nil
		})
		if err == nil || !strings.Contains(err.Error(), "prompt is required") {
			t.Fatalf("RunFileWithAgent() error = %v, want prompt required", err)
		}
	})

	t.Run("agent error fails", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("provider failed")
		_, err := runScriptWithAgent(t, `ronin.run_agent({ prompt = "hello" })`, func(context.Context, AgentRequest) (AgentResult, error) {
			return AgentResult{}, wantErr
		})
		if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
			t.Fatalf("RunFileWithAgent() error = %v, want agent error", err)
		}
	})

	t.Run("more than ten agent runs are allowed", func(t *testing.T) {
		t.Parallel()

		calls := 0
		_, err := runScriptWithAgent(t, `for i = 1, 11 do
  ronin.run_agent({ prompt = "hello" })
end`, func(context.Context, AgentRequest) (AgentResult, error) {
			calls++
			return AgentResult{Text: "ok"}, nil
		})
		if err != nil {
			t.Fatalf("RunFileWithAgent() error = %v", err)
		}
		if calls != 11 {
			t.Fatalf("agent calls = %d, want 11", calls)
		}
	})
}

func TestRunFileWithAgentInput(t *testing.T) {
	t.Parallel()

	t.Run("exposes supplied immutable input", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "workflow.lua")
		if err := os.WriteFile(path, []byte(`ronin.log(ronin.input)`), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		var output strings.Builder
		if err := RunFileWithAgentInputInWorkingDir(t.Context(), path, t.TempDir(), "supplied requirement", &output, nil); err != nil {
			t.Fatalf("RunFileWithAgentInputInWorkingDir() error = %v", err)
		}
		if got, want := output.String(), "supplied requirement\n"; got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}

		if err := os.WriteFile(path, []byte(`ronin.input = "changed"`), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		err := RunFileWithAgentInputInWorkingDir(t.Context(), path, t.TempDir(), "supplied requirement", &output, nil)
		if err == nil || !strings.Contains(err.Error(), "ronin.input is read-only") {
			t.Fatalf("RunFileWithAgentInputInWorkingDir() error = %v, want read-only error", err)
		}

		if err := os.WriteFile(path, []byte(`setmetatable(ronin, {})`), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		err = RunFileWithAgentInputInWorkingDir(t.Context(), path, t.TempDir(), "supplied requirement", &output, nil)
		if err == nil || !strings.Contains(err.Error(), "protected metatable") {
			t.Fatalf("RunFileWithAgentInputInWorkingDir() error = %v, want protected metatable error", err)
		}
	})

	t.Run("require_input returns supplied input", func(t *testing.T) {
		t.Parallel()

		path := filepath.Join(t.TempDir(), "workflow.lua")
		if err := os.WriteFile(path, []byte(`ronin.log(ronin.require_input("input required"))`), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		var output strings.Builder
		if err := RunFileWithAgentInputInWorkingDir(t.Context(), path, t.TempDir(), " supplied input ", &output, nil); err != nil {
			t.Fatalf("RunFileWithAgentInputInWorkingDir() error = %v", err)
		}
		if got, want := output.String(), " supplied input \n"; got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})

	for name, script := range map[string]string{
		"missing message":    `ronin.require_input()`,
		"non-string message": `ronin.require_input(42)`,
	} {
		t.Run("require_input rejects "+name, func(t *testing.T) {
			t.Parallel()

			_, err := runScript(t, script)
			if err == nil || !strings.Contains(err.Error(), "ronin.require_input message") {
				t.Fatalf("RunFile() error = %v, want require_input message error", err)
			}
		})
	}

	for name, tc := range map[string]struct {
		text string
		want bool
	}{
		"terminal approval":        {text: "review complete\nSTATUS: APPROVED", want: true},
		"approval with whitespace": {text: "STATUS: APPROVED \t\r\n", want: true},
		"approval in prose":        {text: "review says STATUS: APPROVED", want: false},
		"contradictory markers":    {text: "STATUS: CHANGES_REQUIRED\nSTATUS: APPROVED", want: false},
		"repeated approval":        {text: "STATUS: APPROVED\nSTATUS: APPROVED", want: false},
		"changes required":         {text: "STATUS: CHANGES_REQUIRED", want: false},
		"empty":                    {text: "", want: false},
	} {
		t.Run("approved handles "+name, func(t *testing.T) {
			t.Parallel()

			stdout, err := runScriptAtPath(t, t.TempDir(), `ronin.log(ronin.approved(%q))`, tc.text)
			if err != nil {
				t.Fatalf("RunFile() error = %v", err)
			}
			if got, want := strings.TrimSpace(stdout), fmt.Sprint(tc.want); got != want {
				t.Fatalf("approved output = %q, want %q", got, want)
			}
		})
	}

	for name, script := range map[string]string{
		"missing text":    `ronin.approved()`,
		"non-string text": `ronin.approved(true)`,
	} {
		t.Run("approved rejects "+name, func(t *testing.T) {
			t.Parallel()

			_, err := runScript(t, script)
			if err == nil || !strings.Contains(err.Error(), "ronin.approved text") {
				t.Fatalf("RunFile() error = %v, want approved text error", err)
			}
		})
	}

	t.Run("existing runners expose empty input", func(t *testing.T) {
		t.Parallel()

		stdout, err := runScript(t, `ronin.log(type(ronin.input))
ronin.log(ronin.input)`)
		if err != nil {
			t.Fatalf("RunFile() error = %v", err)
		}
		if got, want := stdout, "string\n\n"; got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})
}

func TestConcurrentWorkflowExampleRequiresGit(t *testing.T) {
	t.Parallel()

	t.Run("missing input fails before git preflight", func(t *testing.T) {
		t.Parallel()
		var calls int
		var output strings.Builder
		err := RunFileWithAgentInputInWorkingDir(
			t.Context(),
			filepath.Join("..", "testdata", "workflow.lua"),
			t.TempDir(),
			"",
			&output,
			func(context.Context, AgentRequest) (AgentResult, error) {
				calls++
				return AgentResult{}, nil
			},
		)
		var failureErr *FailureError
		if !errors.As(err, &failureErr) {
			t.Fatalf("error = %v, want FailureError", err)
		}
		if calls != 0 {
			t.Fatalf("agent calls = %d, want 0", calls)
		}
	})

	t.Run("requires a git worktree before design", func(t *testing.T) {
		t.Parallel()
		var calls int
		_, err := runConcurrentWorkflowExample(t, t.TempDir(), func(context.Context, AgentRequest) (AgentResult, error) {
			calls++
			return AgentResult{}, nil
		})
		if err == nil || !strings.Contains(err.Error(), "not inside a Git worktree") {
			t.Fatalf("error = %v, want Git worktree error", err)
		}
		if calls != 0 {
			t.Fatalf("agent calls = %d, want 0", calls)
		}
	})
}

func runConcurrentWorkflowExample(t *testing.T, workingDir string, agent AgentFunc) (string, error) {
	t.Helper()
	var output strings.Builder
	err := RunFileWithAgentInputInWorkingDir(
		t.Context(),
		filepath.Join("..", "testdata", "workflow.lua"),
		workingDir,
		"Implement the requested behavior.",
		&output,
		agent,
	)
	return output.String(), err
}

func runScript(t *testing.T, script string) (string, error) {
	t.Helper()
	return runScriptWithAgent(t, script, nil)
}

func runScriptAtPath(t *testing.T, workDir, script string, args ...any) (string, error) {
	t.Helper()

	if len(args) > 0 {
		script = fmt.Sprintf(script, args...)
	}

	path := filepath.Join(workDir, "workflow.lua")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var output strings.Builder
	err := RunFileWithAgent(t.Context(), path, &output, nil)
	return output.String(), err
}

func runScriptWithAgent(t *testing.T, script string, agent AgentFunc) (string, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "workflow.lua")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var output strings.Builder
	err := RunFileWithAgent(t.Context(), path, &output, agent)
	return output.String(), err
}
