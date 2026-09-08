# Workflows

Lua workflows coordinate fresh agent conversations. They can run agents sequentially in the primary working directory or concurrently in managed Git worktrees. Run one directly with:

```sh
ronin -working_dir /path/to/project run workflow.lua "describe the task"
```

Named workflows placed in `<config dir>/workflows` are available from the TUI. See [`testdata/workflow.lua`](../testdata/workflow.lua) for structured planning, concurrent implementer/reviewer lanes, squash integration using Conventional Commits, and bounded integration repair.

The concurrent example allows read-only design and planning on a dirty tree, but refuses to create worktrees unless the primary branch and `HEAD` are unchanged and the tree is clean, including untracked files. Managed worktree agents receive workspace-confined file tools but no arbitrary shell tool; workflow-owned Git operations remain available through the Lua API. Failed runs retain useful branches and dirty worktrees for recovery. Successful runs retain the accepted integration branch as the local result, remove temporary worktrees and lane branches, and leave the starting branch and checkout unchanged. Nothing is pushed automatically.

Invoking this example authorizes task-scoped staging and Conventional Commits by the workflow in managed worktrees; separate commit approval is not required. The final report identifies the result branch, final commit, lane and repair commit counts, implementation summary, verification evidence, and checks not run. Managed agents cannot run arbitrary shell commands, so review approval alone is not evidence that tests passed.

### Finalizing a result branch

After sealing integration repairs, use the branch-returning API:

```lua
ronin.squash_repairs(integration.handle, lane_tip, "fix: reconcile integration")
local result = ronin.finish_worktree(integration.handle)
ronin.done("Result branch: " .. result.branch .. "\nCommit: " .. result.head)
```

`finish_worktree` requires a clean, sealed integration workspace and an unchanged starting branch and HEAD. It retains the integration branch at the sealed commit, cleans workflow-owned temporary artifacts, and returns `branch` and `head`. It does not modify the primary checkout or discard edits made there during execution. If cleanup fails, the error and recovery report identify the retained result branch; do not rerun finalization blindly.

The existing `promote_worktree` API remains available for workflows that explicitly intend to fast-forward the starting branch instead. The example does not use it.

[Back to README](../README.md)
