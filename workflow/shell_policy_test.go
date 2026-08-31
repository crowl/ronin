package workflow_test

import (
	"strings"
	"testing"

	"github.com/crowl/ronin/tool/shell"
	"github.com/crowl/ronin/workflow"
)

func TestWorkspaceShellPolicy(t *testing.T) {
	t.Parallel()
	policy := workflow.WorkspaceShellPolicy{}
	for _, command := range []string{
		"go test ./...",
		"git status --short",
		"git diff HEAD",
		"git add --all && git commit -m checkpoint",
	} {
		if err := policy.Allow(shell.Args{Command: command}); err != nil {
			t.Errorf("Allow(%q) error = %v", command, err)
		}
	}
	for _, command := range []string{
		"git -C .. status",
		"git --git-dir ../.git status",
		"git worktree add /tmp/escape",
		"git reset --hard HEAD",
		"git push origin main",
		"echo ok && git reset --hard HEAD",
	} {
		if err := policy.Allow(shell.Args{Command: command}); err == nil {
			t.Errorf("Allow(%q) error = nil", command)
		}
	}
	if err := policy.Allow(shell.Args{Command: "pwd", Workdir: "/tmp"}); err == nil || !strings.Contains(err.Error(), "relative") {
		t.Fatalf("absolute workdir error = %v", err)
	}
}
