package workflow

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/crowl/ronin/tool/shell"
)

// WorkspaceShellPolicy keeps shell commands inside one managed worktree and
// rejects commands that can mutate Git repository metadata or escape through
// nested Git working-directory options.
type WorkspaceShellPolicy struct{}

func (WorkspaceShellPolicy) Allow(args shell.Args) error {
	if filepath.IsAbs(args.Workdir) {
		return fmt.Errorf("managed workspace shell workdir must be relative")
	}
	fields := strings.Fields(strings.TrimSpace(args.Command))
	for i, field := range fields {
		base := filepath.Base(strings.Trim(field, "'\";|&()"))
		if base != "git" {
			continue
		}
		end := len(fields)
		for j := i + 1; j < len(fields); j++ {
			if strings.ContainsAny(fields[j], ";|&") {
				end = j + 1
				break
			}
		}
		segment := fields[i+1 : end]
		for _, argument := range segment {
			argument = strings.Trim(argument, "'\";|&()")
			if argument == "-C" || strings.HasPrefix(argument, "--git-dir") || strings.HasPrefix(argument, "--work-tree") {
				return fmt.Errorf("managed workspace agents may not redirect Git outside their assigned worktree")
			}
		}
		commandName := gitSubcommand(segment)
		switch commandName {
		case "branch", "checkout", "clean", "clone", "fetch", "gc", "init", "merge", "mv", "pull", "push", "rebase", "remote", "reset", "restore", "rm", "submodule", "switch", "tag", "worktree":
			return fmt.Errorf("managed workspace agents may not run git %s; commits and file staging are allowed", commandName)
		}
	}
	return nil
}

func gitSubcommand(fields []string) string {
	for i := 0; i < len(fields); i++ {
		field := strings.Trim(fields[i], "'\";|&()")
		if field == "-c" || field == "--config-env" || field == "--exec-path" || field == "--namespace" || field == "--super-prefix" {
			i++
			continue
		}
		if strings.HasPrefix(field, "-") {
			continue
		}
		return field
	}
	return ""
}
