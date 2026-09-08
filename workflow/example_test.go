package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const examplePlan = `{"tasks":[{"id":"feature","objective":"Add feature.txt with the requested behavior; preserve existing interfaces.","acceptance":["feature.txt contains implemented"],"depends_on":[],"ownership":["feature.txt"],"verification":["inspect feature.txt"],"commit_message":"feat: implement feature"}],"integration_commit_message":"fix: reconcile feature"}`

func TestExampleStartsWithPlannerBeforeDirtyTreeGate(t *testing.T) {
	t.Parallel()
	repo := initTestRepository(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("user work"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	_, err := runConcurrentWorkflowExample(t, repo, func(_ context.Context, req AgentRequest) (AgentResult, error) {
		calls++
		if req.Name != "Planning" || !req.ReadOnly || req.OutputSchema == nil || !strings.Contains(req.Prompt, "Implement the requested behavior.") || strings.Contains(req.Prompt, "design") {
			t.Errorf("first request = %#v", req)
		}
		return AgentResult{Output: []byte(examplePlan)}, nil
	})
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "must be clean") {
		t.Fatalf("calls = %d, error = %v; want planner only then dirty-tree rejection", calls, err)
	}
	content, err := os.ReadFile(filepath.Join(repo, "dirty.txt"))
	if err != nil || string(content) != "user work" {
		t.Fatalf("user file changed: %q, %v", content, err)
	}
}

func TestExamplePlannerHandoffAndNamedRepairCycles(t *testing.T) {
	t.Parallel()
	repo := initTestRepository(t)
	gitCommand(t, repo, "config", "user.name", "Test")
	gitCommand(t, repo, "config", "user.email", "test@example.com")
	baseHead := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD"))
	baseBranch := strings.TrimSpace(gitCommand(t, repo, "branch", "--show-current"))
	var mu sync.Mutex
	var names []string
	output, err := runConcurrentWorkflowExample(t, repo, func(_ context.Context, req AgentRequest) (AgentResult, error) {
		mu.Lock()
		defer mu.Unlock()
		names = append(names, req.Name)
		if req.Name == "Planning" {
			return AgentResult{Output: []byte(examplePlan)}, nil
		}
		if !strings.Contains(req.Prompt, "feature.txt contains implemented") || strings.Contains(req.Prompt, "Overall design") {
			return AgentResult{}, errors.New("missing plan contracts or obsolete design handoff")
		}
		switch req.Name {
		case "Implementing: feature (cycle 1)", "Implementing: feature (cycle 2)":
			if req.Name == "Implementing: feature (cycle 2)" && !strings.Contains(req.Prompt, "Fix lane finding") {
				return AgentResult{}, errors.New("missing lane feedback")
			}
			if err := os.WriteFile(filepath.Join(req.Workspace, "feature.txt"), []byte("implemented\n"), 0o600); err != nil {
				return AgentResult{}, err
			}
			return AgentResult{Text: "internal implementation report"}, nil
		case "Reviewing: feature (cycle 1)":
			return AgentResult{Text: "Fix lane finding\nSTATUS: CHANGES_REQUIRED"}, nil
		case "Integration repair (cycle 1)":
			if !strings.Contains(req.Prompt, "Fix acceptance finding") {
				return AgentResult{}, errors.New("missing acceptance feedback")
			}
			if err := os.WriteFile(filepath.Join(req.Workspace, "repair.txt"), []byte("reconciled\n"), 0o600); err != nil {
				return AgentResult{}, err
			}
		case "Acceptance (cycle 1)":
			return AgentResult{Text: "Fix acceptance finding\nSTATUS: CHANGES_REQUIRED"}, nil
		}
		return AgentResult{Text: "internal report\nSTATUS: APPROVED"}, nil
	})
	var done *DoneError
	if !errors.As(err, &done) {
		t.Fatalf("example error = %v, output = %s", err, output)
	}
	want := []string{"Planning", "Implementing: feature (cycle 1)", "Reviewing: feature (cycle 1)", "Implementing: feature (cycle 2)", "Reviewing: feature (cycle 2)", "Integration repair", "Integration review (cycle 1)", "Acceptance (cycle 1)", "Integration repair (cycle 1)", "Integration review (cycle 2)", "Acceptance (cycle 2)"}
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Fatalf("invocations = %q, want %q", names, want)
	}
	if strings.Contains(output, "internal report") || !strings.Contains(done.Error(), "internal report") {
		t.Fatalf("acceptance report missing from final handoff or leaked into progress: %s, %v", output, done)
	}
	if got := strings.TrimSpace(gitCommand(t, repo, "rev-parse", "HEAD")); got != baseHead {
		t.Fatalf("starting HEAD changed to %s", got)
	}
	if got := strings.TrimSpace(gitCommand(t, repo, "branch", "--show-current")); got != baseBranch {
		t.Fatalf("checkout changed to %s", got)
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("result leaked into primary worktree: %v", err)
	}
	branches := strings.Fields(gitCommand(t, repo, "branch", "--list", "ronin/*", "--format=%(refname:short)"))
	if len(branches) != 1 {
		t.Fatalf("want only result branch retained, got %v", branches)
	}
	branch := branches[0]
	if got := gitCommand(t, repo, "show", branch+":feature.txt"); got != "implemented\n" {
		t.Fatalf("result content = %q", got)
	}
	if got := gitCommand(t, repo, "show", branch+":repair.txt"); got != "reconciled\n" {
		t.Fatalf("repair content = %q", got)
	}
	head := strings.TrimSpace(gitCommand(t, repo, "rev-parse", branch))
	for _, want := range []string{"Result branch: " + branch, "Commit: " + head, "Starting branch: " + baseBranch + " (unchanged)", "1 lane commit(s) and 1 repair commit(s)"} {
		if !strings.Contains(done.Error(), want) {
			t.Errorf("handoff %q missing %q", done.Error(), want)
		}
	}
	if got := gitCommand(t, repo, "worktree", "list", "--porcelain"); strings.Count(got, "worktree ") != 1 {
		t.Fatalf("temporary worktrees remain: %s", got)
	}
}

func TestAgentDisplayNameValidation(t *testing.T) {
	t.Parallel()
	for _, function := range []string{"run_agent", "start_agent"} {
		t.Run(function, func(t *testing.T) {
			_, err := runScriptWithAgent(t, `ronin.`+function+`({ name = {}, prompt = "test" })`, func(context.Context, AgentRequest) (AgentResult, error) {
				t.Error("invalid name reached agent")
				return AgentResult{}, nil
			})
			if err == nil || !strings.Contains(err.Error(), "name must be a string") {
				t.Fatalf("invalid name error = %v", err)
			}
		})
	}
}
