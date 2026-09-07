local requirement = ronin.require_input(
    "A software requirement is required. Pass inline text, --input <file>, or - for stdin."
)

local repository = ronin.git_preflight()
local max_cycles = 5
local max_concurrency = 3

local roles = {
    planner = { model = "openai:gpt-5.6-sol", reasoning = "high" },
    implementer = { model = "openai:gpt-5.6-terra", reasoning = "medium" },
    reviewer = { model = "openai:gpt-5.6-sol", reasoning = "high" },
    integrator = { model = "openai:gpt-5.6-terra", reasoning = "medium" },
    acceptance_evaluator = { model = "openai:gpt-5.6-sol", reasoning = "high" },
}

local task_plan_schema = [[
{
  "type": "object",
  "additionalProperties": false,
  "required": ["tasks", "integration_commit_message"],
  "properties": {
    "tasks": {
      "type": "array",
      "minItems": 1,
      "maxItems": 8,
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["id", "objective", "acceptance", "depends_on", "ownership", "verification", "commit_message"],
        "properties": {
          "id": {
            "type": "string",
            "pattern": "^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$",
            "description": "Concrete lowercase kebab-case task identifier derived from the requirement."
          },
          "objective": { "type": "string", "description": "Concrete implementation objective grounded in the requirement and repository evidence." },
          "acceptance": { "type": "array", "minItems": 1, "items": { "type": "string" } },
          "depends_on": { "type": "array", "items": { "type": "string" } },
          "ownership": { "type": "array", "minItems": 1, "items": { "type": "string" } },
          "verification": { "type": "array", "minItems": 1, "items": { "type": "string" } },
          "commit_message": {
            "type": "string",
            "pattern": "^(feat|fix|refactor|test|docs|build|ci|perf|chore)(\\([a-z0-9][a-z0-9._/-]*\\))?: ([a-z]|[a-z][^\\n]*[^\\n.])$",
            "description": "Valid non-breaking Conventional Commit subject with a lowercase description and no trailing period."
          }
        }
      }
    },
    "integration_commit_message": {
      "type": "string",
      "pattern": "^(feat|fix|refactor|test|docs|build|ci|perf|chore)(\\([a-z0-9][a-z0-9._/-]*\\))?: ([a-z]|[a-z][^\\n]*[^\\n.])$",
      "description": "Valid non-breaking Conventional Commit subject for integration repairs with a lowercase description and no trailing period."
    }
  }
}
]]

ronin.log("Planning independently integrable tasks...")
local planned = ronin.run_agent({
    name = "Planning",
    model = roles.planner.model,
    reasoning = roles.planner.reasoning,
    read_only = true,
    output_schema = task_plan_schema,
    system = [[
Act as a read-only implementation planner. Do not modify repository state.
Inspect project instructions, repository state, relevant source, callers, and
tests. The original requirement is authoritative. Produce the smallest concrete
implementation plan grounded in repository evidence; preserve existing
architecture and interfaces unless change is necessary. Include boundary and
failure behavior in task objectives and acceptance criteria, and make important
assumptions and cross-task integration contracts explicit in the objectives.
Decompose the requirement only where tasks can be implemented and reviewed
in isolated Git worktrees, then squash-integrated in listed order. Use one task
when safe parallelization is not possible. Keep dependencies acyclic and only
refer to earlier task IDs. Give each task concrete acceptance, anticipated file
or subsystem ownership, and verification. Every commit_message and the
integration_commit_message must be a valid Conventional Commit with an allowed
type (feat, fix, refactor, test, docs, build, ci, perf, chore), an optional
lowercase scope, a lowercase description, and no trailing period.
]],
    prompt = "Original requirement:\n\n" .. requirement,
})

if not planned.ok or planned.output == nil or planned.output.tasks == nil then
    ronin.fail("Planner did not produce a structured task plan.")
end

local plan = planned.output
-- Give every lane the same task contracts without a separate design handoff.
local plan_sections = {}
for _, task in ipairs(plan.tasks) do
    table.insert(plan_sections, task.id .. ": " .. task.objective ..
        "\nDepends on: " .. table.concat(task.depends_on, ", ") ..
        "\nOwnership: " .. table.concat(task.ownership, ", ") ..
        "\nAcceptance:\n- " .. table.concat(task.acceptance, "\n- ") ..
        "\nVerification:\n- " .. table.concat(task.verification, "\n- "))
end
local plan_context = table.concat(plan_sections, "\n\n")
local tasks = plan.tasks
if #tasks == 0 or #tasks > 8 then
    ronin.fail("Planner must produce between 1 and 8 tasks.")
end
local by_id = {}
for index, task in ipairs(tasks) do
    -- Task IDs are validated by task_plan_schema before this output reaches Lua.
    if #task.acceptance == 0 or #task.ownership == 0 or #task.verification == 0 then
        ronin.fail("Planner produced an incomplete task: " .. task.id)
    end
    if by_id[task.id] ~= nil then
        ronin.fail("Planner produced duplicate task id: " .. task.id)
    end
    if not ronin.valid_commit(task.commit_message) then
        ronin.fail("Planner produced an invalid Conventional Commit for task " .. task.id .. ": " .. task.commit_message)
    end
    local dependencies = {}
    for _, dependency in ipairs(task.depends_on) do
        if dependencies[dependency] then
            ronin.fail("Task " .. task.id .. " repeats dependency " .. dependency)
        end
        dependencies[dependency] = true
        if by_id[dependency] == nil then
            ronin.fail("Task " .. task.id .. " depends on unknown or later task " .. dependency)
        end
    end
    task.index = index
    task.status = "queued"
    task.phase = "implementation"
    task.cycle = 1
    task.feedback = "No prior review feedback. Implement the task."
    by_id[task.id] = task
end
if not ronin.valid_commit(plan.integration_commit_message) then
    ronin.fail("Planner produced an invalid integration Conventional Commit: " .. plan.integration_commit_message)
end

-- Planning is allowed on a dirty tree. Execution begins only after
-- this gate verifies the recorded branch and HEAD are unchanged and the primary
-- worktree is clean, including untracked files.
ronin.git_execution_gate()
ronin.log("Executing " .. #tasks .. " planned task(s), up to " .. max_concurrency .. " agents at a time.")

local integration = ronin.create_worktree({ id = "integration", kind = "integration" })

local active = {}
local active_count = 0
local started_count = 0
local completed_count = 0
local integrated_count = 0
local first_failure = nil

local function dependencies_complete(task)
    for _, dependency in ipairs(task.depends_on) do
        if by_id[dependency].status ~= "approved" or not by_id[dependency].integrated then
            return false
        end
    end
    return true
end

local function remove_active(job)
    local next_active = {}
    for _, handle in ipairs(active) do
        if handle ~= job then
            table.insert(next_active, handle)
        end
    end
    active = next_active
    active_count = active_count - 1
end

local function integrate_approved_prefix()
    if active_count ~= 0 then
        return
    end
    while integrated_count < #tasks do
        local next_task = tasks[integrated_count + 1]
        if next_task.status ~= "approved" then
            break
        end
        ronin.log("Integrating approved task: " .. next_task.id)
        ronin.squash_worktree(integration.handle, next_task.workspace.handle, next_task.commit_message)
        next_task.integrated = true
        integrated_count = integrated_count + 1
    end
end

local function implementation_prompt(task)
    return "Original requirement:\n\n" .. requirement ..
        "\n\nImplementation plan and task contracts:\n\n" .. plan_context ..
        "\n\nAssigned task " .. task.id .. ":\n" .. task.objective ..
        "\n\nAcceptance criteria:\n- " .. table.concat(task.acceptance, "\n- ") ..
        "\n\nAnticipated ownership:\n- " .. table.concat(task.ownership, "\n- ") ..
        "\n\nVerification:\n- " .. table.concat(task.verification, "\n- ") ..
        "\n\nCycle: " .. task.cycle .. " of " .. max_cycles ..
        "\n\nFeedback to resolve:\n\n" .. task.feedback
end

local function start_implementation(task)
    if task.workspace == nil then
        task.workspace = ronin.create_worktree({ id = task.id, kind = "lane", from = integration.handle })
    end
    task.status = "running"
    task.phase = "implementation"
    task.job = ronin.start_agent({
        name = "Implementing: " .. task.id .. " (cycle " .. task.cycle .. ")",
        workspace = task.workspace.handle,
        model = roles.implementer.model,
        reasoning = roles.implementer.reasoning,
        system = [[
Act as the implementation engineer for one isolated task lane. You own changes
in this worktree. Inspect repository state before editing. Implement only the
assigned task while honoring the implementation plan and integration contracts.
Preserve sound changes from prior cycles. Shell commands are not available in
managed worktrees; report verification that remains to be run.
]],
        prompt = implementation_prompt(task),
    })
    active_count = active_count + 1
    table.insert(active, task.job)
end

local function start_repair(task)
    task.phase = "implementation"
    task.job = ronin.start_agent({
        name = "Implementing: " .. task.id .. " (cycle " .. task.cycle .. ")",
        workspace = task.workspace.handle,
        model = roles.implementer.model,
        reasoning = roles.implementer.reasoning,
        system = [[
Act as the implementation engineer for one isolated task lane. Resolve every
item in the supplied review feedback. Inspect the current lane state before
editing, preserve sound prior work, and report changes plus verification that
remains to be run. Shell commands are not available in managed worktrees.
]],
        prompt = implementation_prompt(task),
    })
    active_count = active_count + 1
    table.insert(active, task.job)
end

local function start_review(task, implementation)
    task.phase = "review"
    task.implementation = implementation
    task.job = ronin.start_agent({
        name = "Reviewing: " .. task.id .. " (cycle " .. task.cycle .. ")",
        workspace = task.workspace.handle,
        read_only = true,
        model = roles.reviewer.model,
        reasoning = roles.reviewer.reasoning,
        system = [[
Act as an independent read-only reviewer for one isolated task lane. Do not
modify files. Inspect the actual worktree, diff from the base, relevant callers,
and tests. Review requirement coverage, correctness, boundaries, compatibility,
concurrency, test quality, and unnecessary complexity. Report only actionable
findings. End with exactly one terminal marker and use neither elsewhere:
STATUS: APPROVED
STATUS: CHANGES_REQUIRED
]],
        prompt = "Original requirement:\n\n" .. requirement ..
            "\n\nImplementation plan and task contracts:\n\n" .. plan_context ..
            "\n\nAssigned task:\n" .. task.objective ..
            "\n\nImplementation report:\n\n" .. implementation.text,
    })
    active_count = active_count + 1
    table.insert(active, task.job)
end

while completed_count < #tasks and (active_count > 0 or first_failure == nil) do
    if first_failure == nil then
        local capacity = max_concurrency - active_count
        if capacity > 0 then
            for _, task in ipairs(tasks) do
                if capacity == 0 then break end
                if task.status == "queued" and dependencies_complete(task) then
                    start_implementation(task)
                    started_count = started_count + 1
                    capacity = capacity - 1
                end
            end
        end
    end

    if active_count == 0 then
        integrate_approved_prefix()
        if completed_count == #tasks then
            break
        end
    end

    local completed = ronin.wait_any(active)
    remove_active(completed.job)

    local task = nil
    for _, candidate in ipairs(tasks) do
        if candidate.job == completed.job then
            task = candidate
            break
        end
    end
    if task == nil then
        ronin.fail("Concurrent scheduler received an unknown job completion.")
    end

    if not completed.ok then
        task.status = "failed"
        task.failure = completed.error
        completed_count = completed_count + 1
        if first_failure == nil then first_failure = task end
    elseif task.phase == "implementation" then
        start_review(task, completed)
    elseif ronin.approved(completed.text) then
        local sealed = ronin.seal_worktree(task.workspace.handle)
        task.head = sealed.head
        ronin.log("Task " .. task.id .. " approved after " .. task.cycle .. " cycle(s).")
        task.status = "approved"
        task.review = completed.text
        completed_count = completed_count + 1
        integrate_approved_prefix()
    elseif task.cycle < max_cycles then
        task.cycle = task.cycle + 1
        ronin.log("Task " .. task.id .. " requires changes; starting cycle " .. task.cycle .. ".")
        task.feedback = completed.text
        start_repair(task)
    else
        task.status = "failed"
        task.failure = "Review remained unresolved after " .. max_cycles .. " cycles:\n" .. completed.text
        completed_count = completed_count + 1
        if first_failure == nil then first_failure = task end
    end
end

integrate_approved_prefix()
if first_failure ~= nil then
    for _, task in ipairs(tasks) do
        if task.status == "queued" then
            task.status = "skipped"
        end
    end
    ronin.fail("Lane " .. first_failure.id .. " failed. Already-running lanes were allowed to finish; no queued or dependent lanes were started.\n\n" .. first_failure.failure)
end
if completed_count ~= #tasks then
    ronin.fail("No runnable tasks remain; planner dependencies could not be satisfied.")
end

if integrated_count ~= #tasks then
    ronin.fail("Approved lanes could not be integrated in planner order.")
end
local lane_tip = ronin.worktree_head(integration.handle)

ronin.log("Running combined verification and initial integration repair...")
local initial_integration = ronin.run_agent({
    name = "Integration repair",
    workspace = integration.handle,
    model = roles.integrator.model,
    reasoning = roles.integrator.reasoning,
    system = [[
Act as the integration engineer in the combined integration worktree. Inspect
all integrated lane changes and fix concrete cross-lane defects or regressions.
Do not perform optional cleanup. Shell commands are not available in managed
worktrees; report verification that remains to be run.
]],
    prompt = "Original requirement:\n\n" .. requirement ..
        "\n\nImplementation plan and task contracts:\n\n" .. plan_context ..
        "\n\nVerify and reconcile the combined implementation.",
})

local integration_feedback = "Review the combined implementation and its verification evidence."
local latest_report = initial_integration.text
local accepted = false
for cycle = 1, max_cycles do
    ronin.log("Integration review cycle " .. cycle .. " of " .. max_cycles .. "...")
    local review = ronin.run_agent({
        name = "Integration review (cycle " .. cycle .. ")",
        workspace = integration.handle,
        read_only = true,
        model = roles.reviewer.model,
        reasoning = roles.reviewer.reasoning,
        system = [[
Act as the integration reviewer. Do not modify files. Inspect the combined lane
history, actual worktree, and verification evidence. Focus on cross-lane defects,
integration contracts, regressions, and unmet requirements. End with exactly one
terminal marker and use neither elsewhere:
STATUS: APPROVED
STATUS: CHANGES_REQUIRED
]],
        prompt = "Original requirement:\n\n" .. requirement ..
            "\n\nImplementation plan and task contracts:\n\n" .. plan_context ..
            "\n\nPrior integration feedback:\n\n" .. integration_feedback,
    })

    if ronin.approved(review.text) then
        local acceptance = ronin.run_agent({
            name = "Acceptance (cycle " .. cycle .. ")",
            workspace = integration.handle,
            read_only = true,
            model = roles.acceptance_evaluator.model,
            reasoning = roles.acceptance_evaluator.reasoning,
            system = [[
Act as a fresh read-only acceptance evaluator. The original requirement is the
source of truth. Inspect the combined implementation and tests, map every
material requested outcome to repository evidence, and reject drift or missing
observable behavior without inventing requirements. End with exactly one
terminal marker and use neither elsewhere:
STATUS: APPROVED
STATUS: CHANGES_REQUIRED
]],
            prompt = "Original requirement:\n\n" .. requirement ..
                "\n\nImplementation plan and task contracts:\n\n" .. plan_context ..
                "\n\nLatest integration report:\n\n" .. latest_report,
        })
        if ronin.approved(acceptance.text) then
            accepted = true
            latest_report = acceptance.text
            break
        end
        integration_feedback = acceptance.text
    else
        integration_feedback = review.text
    end

    if cycle < max_cycles then
        local repair = ronin.run_agent({
            name = "Integration repair (cycle " .. cycle .. ")",
            workspace = integration.handle,
            model = roles.integrator.model,
            reasoning = roles.integrator.reasoning,
            system = [[
Act as the integration engineer in the combined integration worktree. Resolve
only the concrete cross-lane review or acceptance findings. Inspect before
editing and preserve approved behavior. Shell commands are not available in
managed worktrees; report changes and verification that remains to be run.
]],
            prompt = "Original requirement:\n\n" .. requirement ..
                "\n\nImplementation plan and task contracts:\n\n" .. plan_context ..
                "\n\nIntegration feedback to resolve:\n\n" .. integration_feedback,
        })
        latest_report = repair.text
    end
end

if not accepted then
    ronin.fail("Integration repair loop exhausted without acceptance.\n\nLatest feedback:\n\n" .. integration_feedback)
end

ronin.log("Promoting accepted implementation...")
ronin.squash_repairs(integration.handle, lane_tip, plan.integration_commit_message)
ronin.promote_worktree(integration.handle)
ronin.done("Completed and fast-forwarded " .. #tasks .. " squashed lane commit(s). Integration review and acceptance approved.")
