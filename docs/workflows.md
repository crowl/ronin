# Workflows

Lua workflows coordinate fresh agent conversations. They can run agents sequentially in the primary working directory or concurrently in managed Git worktrees. Run one directly with:

```sh
ronin -working_dir /path/to/project run workflow.lua "describe the task"
```

Named workflows placed in `<config dir>/workflows` are available from the TUI. See [`testdata/workflow.lua`](../testdata/workflow.lua) for structured planning, concurrent implementer/reviewer lanes, squash integration using Conventional Commits, and bounded integration repair.

The concurrent example allows read-only design and planning on a dirty tree, but refuses to create worktrees unless the primary branch and `HEAD` are unchanged and the tree is clean, including untracked files. Managed worktree agents receive workspace-confined file tools but no arbitrary shell tool; workflow-owned Git operations remain available through the Lua API. Failed runs retain useful branches and dirty worktrees for recovery; successful runs fast-forward the primary branch and remove workflow-owned Git artifacts.

[Back to README](../README.md)
