package workflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	lua "github.com/Shopify/go-lua"
	basefsutil "github.com/crowl/ronin/fsutil"
)

var (
	workspaceIDPattern       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
	conventionalCommitHeader = regexp.MustCompile(`^(feat|fix|refactor|test|docs|build|ci|perf|chore)(\([a-z0-9][a-z0-9._/-]*\))?(!)?: (.+)$`)
)

const (
	gitCommandTimeout         = 2 * time.Minute
	gitRecoveryCommandTimeout = 15 * time.Second
	gitOutputLimit            = 1 << 20
)

type gitPreflight struct {
	Root      string
	CommonDir string
	Branch    string
	Head      string
}

type managedWorktree struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Handle          string `json:"handle"`
	Path            string `json:"path"`
	Branch          string `json:"branch"`
	Base            string `json:"base"`
	Sealed          bool   `json:"sealed"`
	SealedHead      string `json:"sealed_head,omitempty"`
	WorktreeRemoved bool   `json:"worktree_removed,omitempty"`
	BranchRemoved   bool   `json:"branch_removed,omitempty"`
}

type worktreeManifest struct {
	PromotedHead string             `json:"promoted_head,omitempty"`
	RunID        string             `json:"run_id"`
	PrimaryRoot  string             `json:"primary_root"`
	BaseBranch   string             `json:"base_branch"`
	BaseHead     string             `json:"base_head"`
	RunRoot      string             `json:"run_root"`
	Worktrees    []*managedWorktree `json:"worktrees"`
}

type worktreeRuntime struct {
	ctx             context.Context
	workingDir      string
	preflight       *gitPreflight
	manifest        *worktreeManifest
	manifestPath    string
	byHandle        map[string]*managedWorktree
	cleanupComplete bool
	promoted        bool
}

func newWorktreeRuntime(ctx context.Context) *worktreeRuntime {
	return &worktreeRuntime{ctx: ctx, byHandle: make(map[string]*managedWorktree)}
}

func (rt *worktreeRuntime) setWorkingDir(workingDir string) {
	rt.workingDir = workingDir
}

func gitPreflightFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		preflight, err := rt.inspectPrimary()
		if err != nil {
			panic(lua.RuntimeError("ronin.git_preflight: " + err.Error()))
		}
		rt.preflight = &preflight
		state.NewTable()
		setLuaStringField(state, "root", preflight.Root)
		setLuaStringField(state, "branch", preflight.Branch)
		setLuaStringField(state, "head", preflight.Head)
		return 1
	}
}

func gitExecutionGateFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		if err := rt.executionGate(); err != nil {
			panic(lua.RuntimeError("ronin.git_execution_gate: " + err.Error()))
		}
		state.NewTable()
		setLuaStringField(state, "run_id", rt.manifest.RunID)
		setLuaStringField(state, "base_head", rt.manifest.BaseHead)
		setLuaStringField(state, "branch", rt.manifest.BaseBranch)
		return 1
	}
}

func createWorktreeFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		if !state.IsTable(1) {
			panic(lua.RuntimeError("ronin.create_worktree expects a table"))
		}
		id, _, err := readAgentStringField(state, 1, "id", true)
		if err != nil {
			panic(lua.RuntimeError(strings.Replace(err.Error(), "ronin.run_agent", "ronin.create_worktree", 1)))
		}
		kind, ok, err := readAgentStringField(state, 1, "kind", false)
		if err != nil {
			panic(lua.RuntimeError(strings.Replace(err.Error(), "ronin.run_agent", "ronin.create_worktree", 1)))
		}
		if !ok {
			kind = "lane"
		}
		base := ""
		if from, hasFrom, err := readAgentStringField(state, 1, "from", false); err != nil {
			panic(lua.RuntimeError(strings.Replace(err.Error(), "ronin.run_agent", "ronin.create_worktree", 1)))
		} else if hasFrom {
			parent, err := rt.workspace(from)
			if err != nil {
				panic(lua.RuntimeError("ronin.create_worktree: " + err.Error()))
			}
			if parent.Kind != "integration" {
				panic(lua.RuntimeError("ronin.create_worktree: from must identify the integration workspace"))
			}
			base, err = rt.git(parent.Path, "rev-parse", "HEAD")
			if err != nil {
				panic(lua.RuntimeError("ronin.create_worktree: resolve integration HEAD: " + err.Error()))
			}
			base = strings.TrimSpace(base)
		}
		workspace, err := rt.createWorktree(id, kind, base)
		if err != nil {
			panic(lua.RuntimeError("ronin.create_worktree: " + err.Error()))
		}
		pushWorkspace(state, workspace)
		return 1
	}
}

func sealWorktreeFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		handle, err := requiredStringArgument(state, 1, "ronin.seal_worktree", "workspace")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		workspace, err := rt.workspace(handle)
		if err != nil {
			panic(lua.RuntimeError("ronin.seal_worktree: " + err.Error()))
		}
		tip, err := rt.seal(workspace)
		if err != nil {
			panic(lua.RuntimeError("ronin.seal_worktree: " + err.Error()))
		}
		state.NewTable()
		state.PushBoolean(true)
		state.SetField(-2, "ok")
		setLuaStringField(state, "head", tip)
		return 1
	}
}

func squashWorktreeFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		integrationHandle, err := requiredStringArgument(state, 1, "ronin.squash_worktree", "integration workspace")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		laneHandle, err := requiredStringArgument(state, 2, "ronin.squash_worktree", "lane workspace")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		message, err := requiredStringArgument(state, 3, "ronin.squash_worktree", "commit message")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		integration, err := rt.workspace(integrationHandle)
		if err != nil {
			panic(lua.RuntimeError("ronin.squash_worktree: " + err.Error()))
		}
		lane, err := rt.workspace(laneHandle)
		if err != nil {
			panic(lua.RuntimeError("ronin.squash_worktree: " + err.Error()))
		}
		head, err := rt.squash(integration, lane, message)
		if err != nil {
			panic(lua.RuntimeError("ronin.squash_worktree: " + err.Error()))
		}
		state.NewTable()
		state.PushBoolean(true)
		state.SetField(-2, "ok")
		setLuaStringField(state, "head", head)
		return 1
	}
}

func worktreeHeadFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		handle, err := requiredStringArgument(state, 1, "ronin.worktree_head", "workspace")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		workspace, err := rt.workspace(handle)
		if err != nil {
			panic(lua.RuntimeError("ronin.worktree_head: " + err.Error()))
		}
		head, err := rt.git(workspace.Path, "rev-parse", "HEAD")
		if err != nil {
			panic(lua.RuntimeError("ronin.worktree_head: " + err.Error()))
		}
		state.PushString(strings.TrimSpace(head))
		return 1
	}
}

func squashRepairsFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		handle, err := requiredStringArgument(state, 1, "ronin.squash_repairs", "workspace")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		base, err := requiredStringArgument(state, 2, "ronin.squash_repairs", "base")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		message, err := requiredStringArgument(state, 3, "ronin.squash_repairs", "commit message")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		workspace, err := rt.workspace(handle)
		if err != nil {
			panic(lua.RuntimeError("ronin.squash_repairs: " + err.Error()))
		}
		head, changed, err := rt.squashRepairs(workspace, base, message)
		if err != nil {
			panic(lua.RuntimeError("ronin.squash_repairs: " + err.Error()))
		}
		state.NewTable()
		state.PushBoolean(true)
		state.SetField(-2, "ok")
		state.PushBoolean(changed)
		state.SetField(-2, "changed")
		setLuaStringField(state, "head", head)
		return 1
	}
}

func promoteWorktreeFunction(rt *worktreeRuntime) lua.Function {
	return func(state *lua.State) int {
		handle, err := requiredStringArgument(state, 1, "ronin.promote_worktree", "workspace")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		workspace, err := rt.workspace(handle)
		if err != nil {
			panic(lua.RuntimeError("ronin.promote_worktree: " + err.Error()))
		}
		if err := rt.promote(workspace); err != nil {
			panic(lua.RuntimeError("ronin.promote_worktree: " + err.Error()))
		}
		state.PushBoolean(true)
		return 1
	}
}

func validateCommitFunction() lua.Function {
	return func(state *lua.State) int {
		message, err := requiredStringArgument(state, 1, "ronin.valid_commit", "message")
		if err != nil {
			panic(lua.RuntimeError(err.Error()))
		}
		state.PushBoolean(validateConventionalCommit(message) == nil)
		return 1
	}
}

func (rt *worktreeRuntime) inspectPrimary() (gitPreflight, error) {
	root, err := rt.git(rt.workingDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return gitPreflight{}, fmt.Errorf("working directory is not inside a Git worktree: %w", err)
	}
	root = strings.TrimSpace(root)
	branch, err := rt.git(root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) == "" {
		return gitPreflight{}, fmt.Errorf("primary worktree must be on an attached branch")
	}
	head, err := rt.git(root, "rev-parse", "HEAD")
	if err != nil {
		return gitPreflight{}, fmt.Errorf("read HEAD: %w", err)
	}
	commonDir, err := rt.git(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return gitPreflight{}, fmt.Errorf("resolve Git common directory: %w", err)
	}
	return gitPreflight{
		Root:      root,
		CommonDir: strings.TrimSpace(commonDir),
		Branch:    strings.TrimSpace(branch),
		Head:      strings.TrimSpace(head),
	}, nil
}

func (rt *worktreeRuntime) executionGate() error {
	if rt.preflight == nil {
		return fmt.Errorf("git_preflight must run before design and planning")
	}
	if rt.manifest != nil {
		return fmt.Errorf("execution gate already passed")
	}
	if err := rt.waitForGitLock(); err != nil {
		return err
	}
	current, err := rt.inspectPrimary()
	if err != nil {
		return err
	}
	if current.Root != rt.preflight.Root || current.Branch != rt.preflight.Branch || current.Head != rt.preflight.Head {
		return fmt.Errorf("primary branch or HEAD changed during design/planning: recorded %s at %s, now %s at %s", rt.preflight.Branch, rt.preflight.Head, current.Branch, current.Head)
	}
	status, err := rt.git(current.Root, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return fmt.Errorf("inspect primary worktree: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("primary worktree must be clean before execution; dirty entries:\n%s", strings.TrimRight(status, "\n"))
	}

	runID, err := randomRunID()
	if err != nil {
		return fmt.Errorf("create workflow run ID: %w", err)
	}
	runRoot, err := os.MkdirTemp("", "ronin-workflow-"+runID+"-")
	if err != nil {
		return fmt.Errorf("create workflow worktree directory: %w", err)
	}
	manifestDir := filepath.Join(current.CommonDir, "ronin-workflows", runID)
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		_ = os.RemoveAll(runRoot)
		return fmt.Errorf("create workflow manifest directory: %w", err)
	}
	rt.manifest = &worktreeManifest{
		RunID:       runID,
		PrimaryRoot: current.Root,
		BaseBranch:  current.Branch,
		BaseHead:    current.Head,
		RunRoot:     runRoot,
	}
	rt.manifestPath = filepath.Join(manifestDir, "manifest.json")
	if err := rt.writeManifest(); err != nil {
		_ = os.RemoveAll(runRoot)
		_ = os.RemoveAll(manifestDir)
		rt.manifest = nil
		return err
	}
	return nil
}

func (rt *worktreeRuntime) waitForGitLock() error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		lockPath := filepath.Join(rt.preflight.CommonDir, "index.lock")
		_, err := os.Stat(lockPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect Git index lock: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("Git index remains locked at %q", lockPath)
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-rt.ctx.Done():
			timer.Stop()
			return rt.ctx.Err()
		case <-timer.C:
		}
	}
}

func (rt *worktreeRuntime) createWorktree(id, kind, base string) (*managedWorktree, error) {
	if rt.manifest == nil {
		return nil, fmt.Errorf("execution gate has not passed")
	}
	if !workspaceIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid workspace id %q: use 1-64 lowercase letters, digits, and single hyphens", id)
	}
	if kind != "lane" && kind != "integration" {
		return nil, fmt.Errorf("kind must be lane or integration")
	}
	for _, existing := range rt.manifest.Worktrees {
		if existing.ID == id {
			return nil, fmt.Errorf("workspace id %q already exists", id)
		}
		if kind == "integration" && existing.Kind == "integration" {
			return nil, fmt.Errorf("an integration workspace already exists")
		}
	}

	if base == "" {
		base = rt.manifest.BaseHead
	}
	if _, err := rt.git(rt.manifest.PrimaryRoot, "merge-base", "--is-ancestor", rt.manifest.BaseHead, base); err != nil {
		return nil, fmt.Errorf("workspace base is not descended from the recorded primary HEAD: %w", err)
	}
	handle := "workspace:" + rt.manifest.RunID + ":" + id
	branch := "ronin/" + rt.manifest.RunID + "/" + id
	path := filepath.Join(rt.manifest.RunRoot, id)
	if _, err := rt.git(rt.manifest.PrimaryRoot, "worktree", "add", "-b", branch, path, base); err != nil {
		return nil, fmt.Errorf("create %s worktree %q: %w", kind, id, err)
	}
	workspace := &managedWorktree{ID: id, Kind: kind, Handle: handle, Path: path, Branch: branch, Base: base}
	rt.manifest.Worktrees = append(rt.manifest.Worktrees, workspace)
	rt.byHandle[handle] = workspace
	if err := rt.writeManifest(); err != nil {
		return nil, err
	}
	return workspace, nil
}

func (rt *worktreeRuntime) resolveWorkspace(handle string) (string, error) {
	workspace, err := rt.workspace(handle)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	return workspace.Path, nil
}

func (rt *worktreeRuntime) workspace(handle string) (*managedWorktree, error) {
	workspace := rt.byHandle[handle]
	if workspace == nil {
		return nil, fmt.Errorf("unknown managed workspace %q", handle)
	}
	return workspace, nil
}

func (rt *worktreeRuntime) seal(workspace *managedWorktree) (string, error) {
	if workspace.Kind != "lane" {
		return "", fmt.Errorf("only lane worktrees can be sealed")
	}
	if workspace.Sealed {
		return "", fmt.Errorf("workspace %q is already sealed", workspace.ID)
	}
	if err := rt.verifyWorkspaceBranch(workspace); err != nil {
		return "", err
	}
	if _, err := rt.git(workspace.Path, "merge-base", "--is-ancestor", workspace.Base, "HEAD"); err != nil {
		return "", fmt.Errorf("workspace branch is no longer descended from recorded base: %w", err)
	}
	if _, err := rt.git(workspace.Path, "add", "--all"); err != nil {
		return "", fmt.Errorf("stage lane changes: %w", err)
	}
	staged, err := rt.git(workspace.Path, "diff", "--cached", "--quiet")
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return "", fmt.Errorf("inspect staged lane changes: %w", err)
		}
		if _, err := rt.gitCommit(workspace.Path, "chore(ronin): seal lane "+workspace.ID); err != nil {
			return "", fmt.Errorf("commit remaining lane changes: %w", err)
		}
	} else if strings.TrimSpace(staged) != "" {
		return "", fmt.Errorf("unexpected output while inspecting staged lane changes")
	}
	count, err := rt.git(workspace.Path, "rev-list", "--count", workspace.Base+"..HEAD")
	if err != nil {
		return "", fmt.Errorf("inspect lane commits: %w", err)
	}
	if strings.TrimSpace(count) == "0" {
		return "", fmt.Errorf("lane %q produced no commits", workspace.ID)
	}
	status, err := rt.status(workspace.Path)
	if err != nil {
		return "", err
	}
	if status != "" {
		return "", fmt.Errorf("lane %q remains dirty after sealing:\n%s", workspace.ID, status)
	}
	if err := rt.rejectLaneChanges(workspace); err != nil {
		return "", err
	}
	return rt.recordSeal(workspace)
}

func (rt *worktreeRuntime) recordSeal(workspace *managedWorktree) (string, error) {
	head, err := rt.git(workspace.Path, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	workspace.SealedHead = strings.TrimSpace(head)
	workspace.Sealed = true
	if err := rt.writeManifest(); err != nil {
		workspace.Sealed = false
		workspace.SealedHead = ""
		return "", err
	}
	return workspace.SealedHead, nil
}

func (rt *worktreeRuntime) verifySeal(workspace *managedWorktree) error {
	if !workspace.Sealed || workspace.SealedHead == "" {
		return fmt.Errorf("workspace %q has no sealed commit", workspace.ID)
	}
	if err := rt.verifyWorkspaceBranch(workspace); err != nil {
		return err
	}
	head, err := rt.git(workspace.Path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(head) != workspace.SealedHead {
		return fmt.Errorf("workspace %q changed after sealing", workspace.ID)
	}
	status, err := rt.status(workspace.Path)
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("workspace %q became dirty after sealing", workspace.ID)
	}
	return nil
}

func (rt *worktreeRuntime) rejectLaneChanges(workspace *managedWorktree) error {
	changed, err := rt.git(workspace.Path, "diff", "--name-only", workspace.Base+"..HEAD")
	if err != nil {
		return fmt.Errorf("inspect lane paths: %w", err)
	}
	for path := range strings.SplitSeq(strings.TrimSpace(changed), "\n") {
		if path == "" {
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(path))
		if clean == ".git" || strings.HasPrefix(clean, ".git/") || clean == ".gitmodules" {
			return fmt.Errorf("lane %q changed protected Git metadata path %q", workspace.ID, path)
		}
	}
	return nil
}

func (rt *worktreeRuntime) squash(integration, lane *managedWorktree, message string) (string, error) {
	if integration.Kind != "integration" || lane.Kind != "lane" {
		return "", fmt.Errorf("squash requires an integration workspace and a lane workspace")
	}
	if integration.Sealed {
		return "", fmt.Errorf("integration workspace is sealed")
	}
	if !lane.Sealed {
		return "", fmt.Errorf("lane %q must be sealed before integration", lane.ID)
	}
	if err := validateConventionalCommit(message); err != nil {
		return "", err
	}
	if err := rt.verifyWorkspaceBranch(integration); err != nil {
		return "", err
	}
	if status, err := rt.status(integration.Path); err != nil {
		return "", err
	} else if status != "" {
		return "", fmt.Errorf("integration worktree is dirty before squash:\n%s", status)
	}
	if err := rt.verifySeal(lane); err != nil {
		return "", err
	}
	if _, err := rt.git(integration.Path, "merge", "--squash", "--no-commit", lane.SealedHead); err != nil {
		return "", fmt.Errorf("squash lane %q; integration worktree retained for recovery: %w", lane.ID, err)
	}
	_, err := rt.git(integration.Path, "diff", "--cached", "--quiet")
	if err == nil {
		return "", fmt.Errorf("lane %q has no changes relative to the integration branch", lane.ID)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		return "", fmt.Errorf("inspect squashed lane %q: %w", lane.ID, err)
	}
	if _, err := rt.gitCommit(integration.Path, message); err != nil {
		return "", fmt.Errorf("commit squashed lane %q: %w", lane.ID, err)
	}
	head, err := rt.git(integration.Path, "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}

func (rt *worktreeRuntime) squashRepairs(workspace *managedWorktree, base, message string) (string, bool, error) {
	if workspace.Kind != "integration" {
		return "", false, fmt.Errorf("repairs can only be squashed in the integration workspace")
	}
	if err := validateConventionalCommit(message); err != nil {
		return "", false, err
	}
	if err := rt.verifyWorkspaceBranch(workspace); err != nil {
		return "", false, err
	}
	if workspace.Sealed {
		return "", false, fmt.Errorf("integration workspace repairs are already sealed")
	}
	if status, err := rt.status(workspace.Path); err != nil {
		return "", false, err
	} else if status != "" {
		if _, err := rt.git(workspace.Path, "add", "--all"); err != nil {
			return "", false, fmt.Errorf("stage integration repairs: %w", err)
		}
		if _, err := rt.gitCommit(workspace.Path, "chore(ronin): checkpoint integration repairs"); err != nil {
			return "", false, fmt.Errorf("commit integration repairs: %w", err)
		}
	}
	if _, err := rt.git(workspace.Path, "merge-base", "--is-ancestor", base, "HEAD"); err != nil {
		return "", false, fmt.Errorf("repair base is not an ancestor of integration HEAD: %w", err)
	}
	count, err := rt.git(workspace.Path, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return "", false, fmt.Errorf("inspect integration repairs: %w", err)
	}
	if strings.TrimSpace(count) == "0" {
		head, err := rt.recordSeal(workspace)
		return head, false, err
	}
	if _, err := rt.git(workspace.Path, "reset", "--soft", base); err != nil {
		return "", false, fmt.Errorf("prepare integration repair squash: %w", err)
	}
	if _, err := rt.gitCommit(workspace.Path, message); err != nil {
		return "", false, fmt.Errorf("commit integration repair squash: %w", err)
	}
	head, err := rt.recordSeal(workspace)
	return head, true, err
}

func (rt *worktreeRuntime) promote(integration *managedWorktree) error {
	if integration.Kind != "integration" {
		return fmt.Errorf("only the integration workspace can be promoted")
	}
	if !integration.Sealed {
		return fmt.Errorf("integration repairs must be sealed with squash_repairs before promotion")
	}
	if rt.promoted {
		return fmt.Errorf("integration workspace was already promoted")
	}
	if status, err := rt.status(integration.Path); err != nil {
		return err
	} else if status != "" {
		return fmt.Errorf("integration worktree must be clean before promotion:\n%s", status)
	}
	current, err := rt.inspectPrimary()
	if err != nil {
		return err
	}
	if current.Root != rt.manifest.PrimaryRoot || current.Branch != rt.manifest.BaseBranch || current.Head != rt.manifest.BaseHead {
		return fmt.Errorf("primary branch or HEAD changed before promotion")
	}
	if status, err := rt.status(current.Root); err != nil {
		return err
	} else if status != "" {
		return fmt.Errorf("primary worktree became dirty before promotion:\n%s", status)
	}
	if err := rt.verifySeal(integration); err != nil {
		return err
	}
	if _, err := rt.git(current.Root, "merge", "--ff-only", integration.SealedHead); err != nil {
		return fmt.Errorf("fast-forward primary branch: %w", err)
	}
	rt.promoted = true
	rt.manifest.PromotedHead = integration.SealedHead
	if err := rt.writeManifest(); err != nil {
		return fmt.Errorf("promotion succeeded but recording promotion failed: %w", err)
	}
	if err := rt.cleanupSuccess(); err != nil {
		return fmt.Errorf("promotion succeeded but cleanup failed: %w", err)
	}
	rt.cleanupComplete = true
	return nil
}

func (rt *worktreeRuntime) recover() string {
	if rt.manifest == nil || rt.cleanupComplete {
		return ""
	}
	if rt.promoted {
		var b strings.Builder
		fmt.Fprintf(&b, "Promotion succeeded at %s; cleanup remains incomplete. Do not promote again.\nManifest: %s\nManual cleanup after inspection:\n", rt.manifest.PromotedHead, rt.manifestPath)
		for _, workspace := range rt.manifest.Worktrees {
			if !workspace.WorktreeRemoved {
				fmt.Fprintf(&b, "- git -C %q worktree remove %q\n", rt.manifest.PrimaryRoot, workspace.Path)
			}
			if !workspace.BranchRemoved {
				fmt.Fprintf(&b, "- git -C %q branch -D %q\n", rt.manifest.PrimaryRoot, workspace.Branch)
			}
		}
		fmt.Fprintf(&b, "Remove the run directory %q and manifest directory %q after cleanup.\n", rt.manifest.RunRoot, filepath.Dir(rt.manifestPath))
		if err := rt.writeManifest(); err != nil {
			fmt.Fprintf(&b, "Could not update manifest: %v\n", err)
		}
		return strings.TrimSpace(b.String())
	}
	var retained []string
	var cleanupErrors []string
	for _, workspace := range rt.manifest.Worktrees {
		status, err := rt.recoveryStatus(workspace.Path)
		if err != nil {
			retained = append(retained, fmt.Sprintf("worktree %s at %s; branch %s", workspace.ID, workspace.Path, workspace.Branch))
			cleanupErrors = append(cleanupErrors, err.Error())
			continue
		}
		if status != "" {
			retained = append(retained, fmt.Sprintf("dirty worktree %s at %s; branch %s", workspace.ID, workspace.Path, workspace.Branch))
			continue
		}
		if _, err := rt.recoveryGit(rt.manifest.PrimaryRoot, "worktree", "remove", workspace.Path); err != nil {
			retained = append(retained, fmt.Sprintf("worktree %s at %s; branch %s", workspace.ID, workspace.Path, workspace.Branch))
			cleanupErrors = append(cleanupErrors, err.Error())
			continue
		}
		retained = append(retained, fmt.Sprintf("branch %s", workspace.Branch))
	}
	_ = rt.writeManifest()
	sort.Strings(retained)
	var b strings.Builder
	b.WriteString("Workflow recovery artifacts retained:\n")
	for _, item := range retained {
		b.WriteString("- ")
		b.WriteString(item)
		b.WriteString("\n")
	}
	b.WriteString("Manifest: ")
	b.WriteString(rt.manifestPath)
	b.WriteString("\nManual cleanup after inspection:\n")
	for _, workspace := range rt.manifest.Worktrees {
		fmt.Fprintf(&b, "- git -C %q worktree remove %q\n", rt.manifest.PrimaryRoot, workspace.Path)
		fmt.Fprintf(&b, "- git -C %q branch -D %q\n", rt.manifest.PrimaryRoot, workspace.Branch)
	}
	if len(cleanupErrors) > 0 {
		b.WriteString("Cleanup errors:\n- ")
		b.WriteString(strings.Join(cleanupErrors, "\n- "))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func (rt *worktreeRuntime) cleanupSuccess() error {
	var errs []error
	for _, workspace := range slices.Backward(rt.manifest.Worktrees) {
		if _, err := rt.git(rt.manifest.PrimaryRoot, "worktree", "remove", workspace.Path); err != nil {
			errs = append(errs, fmt.Errorf("remove worktree %s: %w", workspace.ID, err))
			continue
		}
		workspace.WorktreeRemoved = true
		if _, err := rt.git(rt.manifest.PrimaryRoot, "branch", "-D", workspace.Branch); err != nil {
			errs = append(errs, fmt.Errorf("remove branch %s: %w", workspace.Branch, err))
		} else {
			workspace.BranchRemoved = true
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if err := os.RemoveAll(rt.manifest.RunRoot); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Dir(rt.manifestPath)); err != nil {
		return err
	}
	return nil
}

func (rt *worktreeRuntime) verifyWorkspaceBranch(workspace *managedWorktree) error {
	branch, err := rt.git(workspace.Path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) != workspace.Branch {
		return fmt.Errorf("workspace %q is not on assigned branch %q", workspace.ID, workspace.Branch)
	}
	return nil
}

func (rt *worktreeRuntime) fingerprint(path string) (string, error) {
	if path == "" {
		path = rt.workingDir
	}
	head, err := rt.git(path, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	branch, err := rt.git(path, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return "", err
	}
	status, err := rt.status(path)
	if err != nil {
		return "", err
	}
	// This detects changes, not sandbox violations. Ignored files are excluded.
	hash := sha256.New()
	fmt.Fprintf(hash, "%q\n%q\n%q\n", head, branch, status)
	root, err := rt.git(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root = strings.TrimSpace(root)
	for _, args := range [][]string{
		{"diff", "--binary", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none"},
		{"diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv", "--ignore-submodules=none"},
	} {
		ctx, cancel := context.WithTimeout(rt.ctx, gitCommandTimeout)
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
		cmd.WaitDelay = 5 * time.Second
		cmd.Stdout = hash
		err := cmd.Run()
		cancel()
		if err != nil {
			return "", fmt.Errorf("fingerprint Git diff: %w", err)
		}
		io.WriteString(hash, "\x00")
	}
	paths, err := rt.git(root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	for name := range strings.SplitSeq(paths, "\x00") {
		if name == "" {
			continue
		}
		if err := rt.ctx.Err(); err != nil {
			return "", err
		}
		filePath := filepath.Join(root, name)
		info, err := os.Lstat(filePath)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(hash, "%q %v %d\n", name, info.Mode(), info.Size())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(filePath)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(hash, "%q", target)
		case info.Mode().IsRegular():
			file, err := os.Open(filePath)
			if err != nil {
				return "", err
			}
			_, copyErr := io.Copy(hash, file)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("cannot fingerprint non-regular untracked path %q", name)
		}
		io.WriteString(hash, "\x00")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (rt *worktreeRuntime) status(path string) (string, error) {
	status, err := rt.git(path, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return "", fmt.Errorf("inspect worktree %q: %w", path, err)
	}
	return strings.TrimRight(status, "\n"), nil
}

func (rt *worktreeRuntime) recoveryStatus(path string) (string, error) {
	status, err := rt.recoveryGit(path, "status", "--porcelain=v1", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return "", fmt.Errorf("inspect worktree %q: %w", path, err)
	}
	return strings.TrimRight(status, "\n"), nil
}

func (rt *worktreeRuntime) gitCommit(path, message string) (string, error) {
	return rt.git(path,
		"-c", "user.name=Ronin Workflow",
		"-c", "user.email=workflow@ronin.local",
		"commit", "--no-gpg-sign", "-m", message,
	)
}

func (rt *worktreeRuntime) git(path string, args ...string) (string, error) {
	return rt.runGit(rt.ctx, path, args...)
}

func (rt *worktreeRuntime) recoveryGit(path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitRecoveryCommandTimeout)
	defer cancel()
	return rt.runGit(ctx, path, args...)
}

func (rt *worktreeRuntime) runGit(ctx context.Context, path string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	commandArgs := append([]string{"-C", path}, args...)
	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	cmd.WaitDelay = 5 * time.Second
	output := &boundedGitOutput{limit: gitOutputLimit}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	text, truncated := output.String()
	if err != nil {
		if truncated {
			text += "\n[git output truncated]"
		}
		return text, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(text))
	}
	if truncated {
		return text, fmt.Errorf("git %s: output exceeds %d bytes", strings.Join(args, " "), gitOutputLimit)
	}
	return text, nil
}

type boundedGitOutput struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (w *boundedGitOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(data)
	remaining := w.limit - len(w.data)
	if remaining <= 0 {
		w.truncated = w.truncated || original > 0
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		w.truncated = true
	}
	w.data = append(w.data, data...)
	return original, nil
}

func (w *boundedGitOutput) String() (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.data), w.truncated
}

func (rt *worktreeRuntime) writeManifest() error {
	data, err := json.MarshalIndent(rt.manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workflow manifest: %w", err)
	}
	if err := basefsutil.WriteFileAtomic(rt.manifestPath, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("publish workflow manifest: %w", err)
	}
	return nil
}

func validateConventionalCommit(message string) error {
	message = strings.TrimSpace(message)
	header, _, _ := strings.Cut(message, "\n")
	matches := conventionalCommitHeader.FindStringSubmatch(header)
	if matches == nil {
		return fmt.Errorf("commit message must use Conventional Commits: <type>(<scope>): <description>")
	}
	description := matches[4]
	if strings.HasSuffix(description, ".") {
		return fmt.Errorf("Conventional Commit description must not end with a period")
	}
	first := description[0]
	if first < 'a' || first > 'z' {
		return fmt.Errorf("Conventional Commit description must start with a lowercase letter")
	}
	if matches[3] == "!" && !strings.Contains(message, "\nBREAKING CHANGE: ") {
		return fmt.Errorf("breaking Conventional Commit requires a BREAKING CHANGE footer")
	}
	return nil
}

func randomRunID() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102t150405") + "-" + hex.EncodeToString(value[:]), nil
}

func pushWorkspace(state *lua.State, workspace *managedWorktree) {
	state.NewTable()
	setLuaStringField(state, "id", workspace.ID)
	setLuaStringField(state, "kind", workspace.Kind)
	setLuaStringField(state, "handle", workspace.Handle)
	setLuaStringField(state, "branch", workspace.Branch)
}

func setLuaStringField(state *lua.State, name, value string) {
	state.PushString(value)
	state.SetField(-2, name)
}
