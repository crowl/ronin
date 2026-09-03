package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestConcurrentAgents(t *testing.T) {
	t.Parallel()

	t.Run("wait_any returns the first completed job and preserves identity", func(t *testing.T) {
		t.Parallel()
		var running atomic.Int32
		var concurrent atomic.Bool
		stdout, err := runScriptWithAgent(t, `
local slow = ronin.start_agent({ prompt = "slow" })
local fast = ronin.start_agent({ prompt = "fast" })
local first = ronin.wait_any({ slow, fast })
ronin.log(first.job)
ronin.log(first.text)
local second = ronin.wait_any({ slow })
ronin.log(second.text)
`, func(_ context.Context, req AgentRequest) (AgentResult, error) {
			if running.Add(1) > 1 {
				concurrent.Store(true)
			}
			defer running.Add(-1)
			if req.Prompt == "slow" {
				time.Sleep(75 * time.Millisecond)
			} else {
				time.Sleep(10 * time.Millisecond)
			}
			return AgentResult{Text: req.Prompt + " result"}, nil
		})
		if err != nil {
			t.Fatalf("RunFileWithAgent() error = %v", err)
		}
		if !concurrent.Load() {
			t.Fatal("agents did not overlap")
		}
		if !strings.Contains(stdout, "2\nfast result\nslow result") {
			t.Fatalf("stdout = %q, want fast completion followed by slow result", stdout)
		}
	})

	t.Run("structured output is exposed as Lua tables", func(t *testing.T) {
		t.Parallel()
		stdout, err := runScriptWithAgent(t, `
local result = ronin.run_agent({
  prompt = "plan",
  output_schema = '{"type":"object","properties":{"tasks":{"type":"array"}}}'
})
ronin.log(result.output.tasks[1].id)
`, func(_ context.Context, req AgentRequest) (AgentResult, error) {
			if req.OutputSchema == nil {
				t.Fatal("OutputSchema = nil")
			}
			return AgentResult{Text: "plan", Output: []byte(`{"tasks":[{"id":"runtime"}]}`)}, nil
		})
		if err != nil {
			t.Fatalf("RunFileWithAgent() error = %v", err)
		}
		if strings.TrimSpace(stdout) != "runtime" {
			t.Fatalf("stdout = %q, want runtime", stdout)
		}
	})

	t.Run("structured output must satisfy its schema", func(t *testing.T) {
		t.Parallel()
		_, err := runScriptWithAgent(t, `
ronin.run_agent({
  prompt = "plan",
  output_schema = '{"type":"object","required":["commit_message"],"properties":{"commit_message":{"type":"string","pattern":"^fix: "}}}'
})
`, func(_ context.Context, req AgentRequest) (AgentResult, error) {
			if req.OutputSchema == nil {
				t.Fatal("OutputSchema = nil")
			}
			return AgentResult{Text: "plan", Output: []byte(`{"commit_message":"string"}`)}, nil
		})
		if err == nil || !strings.Contains(err.Error(), "structured agent output validation failed") || !strings.Contains(err.Error(), "commit_message") {
			t.Fatalf("RunFileWithAgent() error = %v, want schema validation error", err)
		}
	})
}

func TestManagedWorktrees(t *testing.T) {
	t.Parallel()

	t.Run("dirty execution gate runs after read-only agents", func(t *testing.T) {
		t.Parallel()
		repo := initTestRepository(t)
		if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var calls atomic.Int32
		_, err := runScriptAtWorkingDirWithAgent(t, repo, `
ronin.git_preflight()
ronin.run_agent({ prompt = "design" })
ronin.run_agent({ prompt = "plan" })
ronin.git_execution_gate()
`, func(context.Context, AgentRequest) (AgentResult, error) {
			calls.Add(1)
			return AgentResult{Text: "ok"}, nil
		})
		if err == nil || !strings.Contains(err.Error(), "must be clean") || !strings.Contains(err.Error(), "dirty.txt") {
			t.Fatalf("error = %v, want dirty execution gate error", err)
		}
		if calls.Load() != 2 {
			t.Fatalf("agent calls = %d, want 2", calls.Load())
		}
	})

	t.Run("squashes lanes promotes and cleans owned artifacts", func(t *testing.T) {
		t.Parallel()
		repo := initTestRepository(t)
		_, err := runScriptAtWorkingDirWithAgent(t, repo, `
ronin.git_preflight()
ronin.git_execution_gate()
local lane = ronin.create_worktree({ id = "runtime", kind = "lane" })
local integration = ronin.create_worktree({ id = "integration", kind = "integration" })
local result = ronin.run_agent({ workspace = lane.handle, prompt = "implement" })
if not result.ok then ronin.fail(result.error) end
ronin.seal_worktree(lane.handle)
ronin.squash_worktree(integration.handle, lane.handle, "feat(workflow): add lane output")
local lane_tip = ronin.worktree_head(integration.handle)
ronin.squash_repairs(integration.handle, lane_tip, "fix(workflow): reconcile lane output")
ronin.promote_worktree(integration.handle)
ronin.done("complete")
`, func(_ context.Context, req AgentRequest) (AgentResult, error) {
			if req.Workspace == "" {
				t.Fatal("Workspace is empty")
			}
			if err := os.WriteFile(filepath.Join(req.Workspace, "lane.txt"), []byte("implemented\n"), 0o600); err != nil {
				return AgentResult{}, err
			}
			gitCommand(t, req.Workspace, "add", "lane.txt")
			gitCommand(t, req.Workspace, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "checkpoint")
			return AgentResult{Text: "implemented"}, nil
		})
		if _, ok := errorsAsDone(err); !ok {
			t.Fatalf("error = %v, want DoneError", err)
		}
		content, err := os.ReadFile(filepath.Join(repo, "lane.txt"))
		if err != nil || string(content) != "implemented\n" {
			t.Fatalf("lane.txt = %q, %v", content, err)
		}
		if got := strings.TrimSpace(gitCommand(t, repo, "log", "-1", "--pretty=%s")); got != "feat(workflow): add lane output" {
			t.Fatalf("commit subject = %q", got)
		}
		branches := gitCommand(t, repo, "branch", "--format=%(refname:short)")
		if strings.Contains(branches, "ronin/") {
			t.Fatalf("temporary branches remain:\n%s", branches)
		}
	})
}

func TestConventionalCommitValidation(t *testing.T) {
	t.Parallel()
	for _, message := range []string{
		"feat(workflow): add concurrent lanes",
		"fix: handle failed lane",
		"feat(workflow)!: change lane API\n\nBREAKING CHANGE: workflows use managed handles",
	} {
		if err := validateConventionalCommit(message); err != nil {
			t.Errorf("validateConventionalCommit(%q) error = %v", message, err)
		}
	}
	for _, message := range []string{
		"feature(workflow): add lanes",
		"feat(workflow): Add lanes",
		"feat(workflow): add lanes.",
		"feat(workflow)!: change lane API",
	} {
		if err := validateConventionalCommit(message); err == nil {
			t.Errorf("validateConventionalCommit(%q) error = nil", message)
		}
	}
}

func runScriptAtWorkingDirWithAgent(t *testing.T, workDir, script string, agent AgentFunc) (string, error) {
	t.Helper()
	scriptDir := t.TempDir()
	path := filepath.Join(scriptDir, "workflow.lua")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	err := RunFileWithAgentInputInWorkingDir(t.Context(), path, workDir, "", &output, agent)
	return output.String(), err
}

func initTestRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitCommand(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "add", "README.md")
	gitCommand(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "chore: initialize")
	return repo
}

func gitCommand(t *testing.T, dir string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", dir}, args...)
	output, err := exec.Command("git", commandArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func errorsAsDone(err error) (*DoneError, bool) {
	for err != nil {
		if done, ok := err.(*DoneError); ok {
			return done, true
		}
		type unwrapper interface{ Unwrap() error }
		wrapped, ok := err.(unwrapper)
		if !ok {
			return nil, false
		}
		err = wrapped.Unwrap()
	}
	return nil, false
}
