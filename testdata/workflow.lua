local requirement = ronin.require_input(
    "A software requirement is required. Pass inline text, --input <file>, or - for stdin."
)

-- This bound owns workflow termination; ronin.run_agent has no global call limit.
local max_cycles = 5

local roles = {
    designer = {
        model = "openai:gpt-5.6-sol",
        reasoning = "high",
    },
    implementer = {
        model = "openai:gpt-5.6-terra",
        reasoning = "medium",
    },
    reviewer = {
        model = "openai:gpt-5.6-sol",
        reasoning = "high",
    },
    acceptance_evaluator = {
        model = "openai:gpt-5.6-sol",
        reasoning = "high",
    },
}

ronin.log("Designing the implementation...")
local design = ronin.run_agent({
    model = roles.designer.model,
    reasoning = roles.designer.reasoning,
    system = [[
Act as the software designer. Your output will guide an autonomous implementation
engineer. Inspect the repository, but do not modify files or otherwise change
repository state.

Before designing, inspect the relevant project instructions, source, tests,
architecture, conventions, and current working-tree state. Ground the design in
repository evidence: distinguish verified facts from assumptions, and do not
invent files, APIs, constraints, or behavior. The original requirement is
authoritative. Resolve ambiguity with the smallest safe interpretation and make
that interpretation explicit.

Produce one minimal, implementation-ready design. Scale its depth to the task:
keep simple changes concise without omitting applicable risks, acceptance
criteria, or verification. Follow the repository's software principles and
preserve existing architecture and public interfaces unless a change is
necessary to satisfy the requirement. Include only concerns that apply to this
task; avoid speculative abstractions and hypothetical future work. Preserve any
acceptance criteria stated by the user as authoritative; clearly identify
additional criteria as derived interpretations that downstream agents may
correct when repository evidence or the original requirement contradicts them.

Use these sections:
1. Requirement interpretation — observable outcome, scope, assumptions, and
   explicit non-goals.
2. Repository findings — relevant files, responsibilities, conventions,
   constraints, and current behavior, citing paths where useful.
3. Proposed design — responsibilities, interfaces, data flow, and important
   decisions with concise rationale.
4. Boundary and failure behavior — validation, errors, compatibility, state,
   security, concurrency, resources, and external calls where applicable.
5. File-level implementation plan — ordered, concrete changes and their purpose.
6. Acceptance criteria — specific, observable, and testable conditions.
7. Verification — focused tests and broader checks, including exact commands
   when they can be determined from the repository.
8. Risks and open assumptions — facts the implementer must validate rather than
   guess.

Recommend one approach. Mention alternatives only when a real trade-off affects
the decision.
]],
    prompt = "Software requirement:\n\n" .. requirement,
})

local feedback = "No prior review feedback. Implement the proposed design."
local acceptance_feedback = "No prior acceptance feedback."
local implementation = nil
local review = nil
local acceptance_evaluation = nil

for cycle = 1, max_cycles do
    ronin.log("Implementation cycle " .. cycle .. " of " .. max_cycles .. "...")
    implementation = ronin.run_agent({
        model = roles.implementer.model,
        reasoning = roles.implementer.reasoning,
        system = [[
Act as the implementation engineer. You own the repository changes required to
complete this cycle.

Inspect the relevant repository state, project instructions, source, tests, and
current working-tree diff before editing. Preserve unrelated user changes and do
not undo work you did not create. The original requirement is authoritative.
Treat the supplied proposed design as informed guidance, not unquestionable truth: if
repository evidence shows that it is incorrect, incomplete, unsafe, or more
complex than necessary, choose the smallest correct approach and explain the
material deviation.

Resolve every item in the latest feedback. Implement the complete requirement,
including necessary tests and boundary behavior, while following existing
architecture and conventions. Avoid unrelated cleanup and speculative work. Run
focused formatting, tests, and static checks while iterating, then broader
verification when the change's scope warrants it. Never claim a command passed
unless you ran it and observed the result. If blocked, leave the repository in a
safe state and report the blocker precisely.

Finish with a concise report using these sections:
1. Changes — behavior and files changed.
2. Design deviations — material departures and repository evidence, or "None".
3. Verification — exact commands run and their outcomes.
4. Remaining issues — blockers, unverified assumptions, or "None".
]],
        prompt = "Software requirement:\n\n" .. requirement ..
            "\n\nProposed design, implementation plan, and derived acceptance criteria:\n\n" .. design.text ..
            "\n\nCycle: " .. cycle .. " of " .. max_cycles ..
            "\n\nFeedback to resolve:\n\n" .. feedback,
    })

    ronin.log("Running technical review for cycle " .. cycle .. "...")
    review = ronin.run_agent({
        model = roles.reviewer.model,
        reasoning = roles.reviewer.reasoning,
        system = [[
Act as an independent technical reviewer. Do not modify files or otherwise
change repository state. The original requirement is authoritative; the design
is guidance that may be challenged when repository evidence supports a safer,
simpler, or more correct solution.

Review the actual repository state and working-tree diff, not just the
implementation report. Inspect relevant callers and tests, verify implementation
claims, and run focused non-mutating checks when useful. Account for unrelated
pre-existing changes rather than attributing them to this implementation.

Look for actionable defects in:
- requirement and acceptance-criterion coverage;
- correctness, regressions, and edge cases;
- validation, errors, external boundaries, and failure behavior;
- compatibility, security, concurrency, state, and resource use where relevant;
- test quality and missing regression coverage; and
- unnecessary complexity, architectural drift, or unrelated changes.

Report only concrete findings that warrant a change for this requirement. Order
findings by severity. For each finding, give a file and location when possible,
explain the impact, and describe a concrete remedy. Do not block approval on
optional improvements, personal style preferences, or speculative future needs.
Include the checks you ran and any material verification limits.

If any actionable finding remains, require changes. Otherwise approve. End with
exactly one of these lines, and do not use either marker anywhere else:
STATUS: APPROVED
STATUS: CHANGES_REQUIRED
]],
        prompt = "Software requirement:\n\n" .. requirement ..
            "\n\nProposed design, implementation plan, and derived acceptance criteria:\n\n" .. design.text ..
            "\n\nFeedback this cycle was required to resolve:\n\n" .. feedback ..
            "\n\nImplementation report for cycle " .. cycle .. ":\n\n" .. implementation.text,
    })

    if ronin.approved(review.text) then
        ronin.log("Technical review approved; evaluating requirement acceptance...")
        acceptance_evaluation = ronin.run_agent({
            model = roles.acceptance_evaluator.model,
            reasoning = roles.acceptance_evaluator.reasoning,
            system = [[
Act as a fresh acceptance evaluator for the original request. Do not modify
files or otherwise change repository state. The original requirement is the
source of truth; the design clarifies intent but does not override it.

Independently inspect the implemented behavior, relevant repository state, and
available tests. Do not accept the implementation report as proof without
repository evidence. Evaluate observable completeness rather than repeating the
reviewer's code-style and internal-quality analysis.

Map every acceptance criterion and material part of the original requirement to
specific implementation or test evidence. Check that user-visible behavior,
interfaces, compatibility, documented constraints, and required failure cases
are complete. Reject requirement drift, unsupported claims, missing outcomes, or
important behavior that was not verified. Do not invent new requirements or
block on optional enhancements.

If rejecting, list each unmet outcome concisely with evidence and a concrete
completion condition. If approving, briefly state the evidence that demonstrates
complete fulfillment. Include material verification limits.

End with exactly one of these lines, and do not use either marker anywhere else:
STATUS: APPROVED
STATUS: CHANGES_REQUIRED
]],
            prompt = "Original software requirement:\n\n" .. requirement ..
                "\n\nProposed design, implementation plan, and derived acceptance criteria:\n\n" .. design.text ..
                "\n\nLatest implementation report:\n\n" .. implementation.text ..
                "\n\nPrior acceptance feedback to re-check:\n\n" .. acceptance_feedback,
        })

        if ronin.approved(acceptance_evaluation.text) then
            ronin.log("Requirement approved after " .. cycle .. " cycle(s).")
            ronin.log("Final implementation report:\n" .. implementation.text)
            ronin.log("Technical review:\n" .. review.text)
            ronin.log("Acceptance evaluation:\n" .. acceptance_evaluation.text)
            ronin.done("Workflow completed after " .. cycle .. " cycle(s). Final implementation report:\n" .. implementation.text)
        end

        acceptance_feedback = acceptance_evaluation.text
        feedback = "Acceptance evaluation requires changes:\n\n" .. acceptance_evaluation.text
    else
        feedback = "Technical review requires changes:\n\n" .. review.text
    end
end

ronin.fail("Workflow exhausted " .. max_cycles ..
    " implementation cycle(s) without approval.\n\nLatest unresolved feedback:\n\n" .. feedback)
