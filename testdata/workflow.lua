local requirement = ronin.input

local function contains_non_whitespace(text)
    for i = 1, string.len(text) do
        local char = string.sub(text, i, i)
        if char ~= " " and char ~= "\n" and char ~= "\r" and
            char ~= "\t" and char ~= "\f" and char ~= "\v" then
            return true
        end
    end
    return false
end

if not contains_non_whitespace(requirement) then
    ronin.fail("A software requirement is required. Pass inline text, --input <file>, or - for stdin.")
end

-- This bound owns workflow termination; ronin.run_agent has no global call limit.
local max_cycles = 5

local models = {
    planner = "openai:gpt-5.6-sol",
    implementer = "openai:gpt-5.6-terra",
    reviewer = "openai:gpt-5.6-sol",
    evaluator = "openai:gpt-5.6-sol",
}

local function trim_trailing_whitespace(text)
    while true do
        local last = string.sub(text, -1)
        if last ~= " " and last ~= "\n" and last ~= "\r" and
            last ~= "\t" and last ~= "\f" and last ~= "\v" then
            return text
        end
        text = string.sub(text, 1, -2)
    end
end

local approved_marker = "STATUS: APPROVED"
local changes_marker = "STATUS: CHANGES_REQUIRED"

local function decision(text)
    local trimmed = trim_trailing_whitespace(text)
    local marker_start = string.len(trimmed) - string.len(approved_marker) + 1
    local marker_is_terminal_line = trimmed == approved_marker or
        (marker_start > 1 and string.sub(trimmed, marker_start - 1, marker_start - 1) == "\n")
    if not marker_is_terminal_line or string.sub(trimmed, marker_start) ~= approved_marker then
        return "changes_required"
    end

    local preceding_text = string.sub(trimmed, 1, marker_start - 1)
    if string.find(preceding_text, approved_marker, 1, true) or
        string.find(preceding_text, changes_marker, 1, true) then
        return "changes_required"
    end
    return "approved"
end

ronin.log("Planning the requirement...")
local plan = ronin.run_agent({
    model = models.planner,
    reasoning = "high",
    system = [[
Act as the planning engineer. Inspect the repository and turn the requirement
into explicit acceptance criteria and a minimal implementation plan. Identify
relevant files, existing conventions, risks, and verification commands. Do not
modify the repository.
]],
    prompt = "Software requirement:\n\n" .. requirement,
})

local feedback = "No prior review feedback. Implement the approved plan."
local implementation = nil
local review = nil
local evaluation = nil

for cycle = 1, max_cycles do
    ronin.log("Implementation cycle " .. cycle .. " of " .. max_cycles .. "...")
    implementation = ronin.run_agent({
        model = models.implementer,
        reasoning = "medium",
        system = [[
Act as the implementation engineer. Inspect the repository, validate the plan
and feedback, then implement or revise the requirement with the smallest correct
change. Follow existing project conventions. Run relevant formatting and tests.
Modify the repository as needed and report what changed and what was verified.
]],
        prompt = "Software requirement:\n\n" .. requirement ..
            "\n\nOriginal implementation plan and acceptance criteria:\n\n" .. plan.text ..
            "\n\nCycle: " .. cycle .. " of " .. max_cycles ..
            "\n\nFeedback to resolve:\n\n" .. feedback,
    })

    ronin.log("Running technical review for cycle " .. cycle .. "...")
    review = ronin.run_agent({
        model = models.reviewer,
        reasoning = "high",
        system = [[
Act as an independent technical reviewer. Do not modify the repository. Inspect
the actual working-tree diff and current test state. Check correctness,
regressions, failure behavior, test coverage, and unnecessary complexity against
the requirement and acceptance criteria. Report actionable findings with enough
detail for another engineer to fix them. End with exactly one terminal line:
STATUS: APPROVED
or
STATUS: CHANGES_REQUIRED
Approve only when no actionable technical findings remain.
]],
        prompt = "Software requirement:\n\n" .. requirement ..
            "\n\nOriginal plan and acceptance criteria:\n\n" .. plan.text ..
            "\n\nImplementation report for cycle " .. cycle .. ":\n\n" .. implementation.text,
    })

    if decision(review.text) == "approved" then
        ronin.log("Technical review approved; evaluating as the original requestor...")
        evaluation = ronin.run_agent({
            model = models.evaluator,
            reasoning = "high",
            system = [[
Act as a fresh evaluator representing the original requestor. Do not modify the
repository. Inspect the implemented behavior and current repository state.
Decide whether the original requirement and every acceptance criterion are met
from the requestor's perspective. Focus on observable outcomes, completeness,
and unresolved requirement gaps rather than repeating a general code review.
Explain any rejection with actionable feedback. End with exactly one terminal
line:
STATUS: APPROVED
or
STATUS: CHANGES_REQUIRED
]],
            prompt = "Original software requirement:\n\n" .. requirement ..
                "\n\nPlan and acceptance criteria:\n\n" .. plan.text ..
                "\n\nLatest implementation report:\n\n" .. implementation.text ..
                "\n\nTechnical review:\n\n" .. review.text,
        })

        if decision(evaluation.text) == "approved" then
            ronin.log("Requirement approved after " .. cycle .. " cycle(s).")
            ronin.log("Final implementation report:\n" .. implementation.text)
            ronin.log("Technical review:\n" .. review.text)
            ronin.log("Requestor evaluation:\n" .. evaluation.text)
            ronin.done("Workflow completed after " .. cycle .. " cycle(s).")
        end

        feedback = "Requestor evaluation requires changes:\n\n" .. evaluation.text
    else
        feedback = "Technical review requires changes:\n\n" .. review.text
    end
end

ronin.fail("Workflow exhausted " .. max_cycles ..
    " implementation cycle(s) without approval.\n\nLatest unresolved feedback:\n\n" .. feedback)
