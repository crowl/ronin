package workflow

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var gitObjectID = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

func TestRunGitKeepsStderrOutOfParsedOutput(t *testing.T) {
	repo := initTestRepository(t)
	// GIT_TRACE makes git write diagnostics to stderr for every command,
	// standing in for warnings and hints from user configuration.
	t.Setenv("GIT_TRACE", "1")

	rt := newWorktreeRuntime(t.Context())
	rt.setWorkingDir(repo)

	head, err := rt.git(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	if !gitObjectID.MatchString(strings.TrimSpace(head)) {
		t.Fatalf("stdout = %q, want a bare object id", head)
	}

	preflight, err := rt.inspectPrimary()
	if err != nil {
		t.Fatalf("inspectPrimary() error = %v", err)
	}
	if preflight.Branch != "main" || preflight.Head != strings.TrimSpace(head) {
		t.Fatalf("preflight = %+v, want branch main at %s", preflight, strings.TrimSpace(head))
	}
	rt.preflight = &preflight
	if err := rt.executionGate(); err != nil {
		t.Fatalf("executionGate() error = %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(rt.manifest.RunRoot) })
	lane, err := rt.createWorktree("lane", "lane", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lane.Path, "change"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.seal(lane); err != nil {
		t.Fatalf("seal() error = %v", err)
	}
}

func TestRunGitReportsStderrOnFailure(t *testing.T) {
	repo := initTestRepository(t)
	rt := newWorktreeRuntime(t.Context())
	rt.setWorkingDir(repo)

	_, err := rt.git(repo, "rev-parse", "--verify", "refs/heads/does-not-exist")
	if err == nil {
		t.Fatal("git rev-parse of a missing ref succeeded")
	}
	if !strings.Contains(err.Error(), "fatal:") {
		t.Fatalf("error = %v, want git stderr in the message", err)
	}
}

func TestGitCommitSkipsRepositoryHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook scripts require a POSIX shell")
	}
	repo := initTestRepository(t)
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "change"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCommand(t, repo, "add", "change")

	rt := newWorktreeRuntime(t.Context())
	rt.setWorkingDir(repo)
	if _, err := rt.gitCommit(repo, "chore: bypass failing hook"); err != nil {
		t.Fatalf("gitCommit() error = %v, want hooks skipped", err)
	}
}
