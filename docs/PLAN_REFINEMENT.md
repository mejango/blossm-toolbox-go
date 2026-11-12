# Plan refinement before execution

Standing workspace instruction from jango, 2026-10-06. Applies with the root and affected-repository `AGENTS.md` files and the pinned [Ponytail skill](../workflow/ponytail/SKILL.md). Read these resources at task start and carry them into delegated/isolated work. Revisit refinement when findings change the plan. Scale the written record to the task; do not turn a small fix into a speculative architecture project.

## User's prompt, verbatim

> OK, now I want you to think deeply about how to make this entire system as agent-intuitive, agent-ergonomic, and agent-accretive as you can possibly imagine. Put yourself in the driver's seat and imagine that YOU are the one using this system and driving it. What would most enable you to do an awesome job understanding the situation accurately and optimally controlling everything to drive the best and most accurate results possible, with the least expenditure of resources?
>
> Then make all the requisite changes to the various design documents and plans accordingly. Don't just think of the project as an assemblage of various parts or components: really try to profoundly and deeply conceptualize it as a synthetic SYSTEM that is maximally coherent, cohesive, modular, and interconnected, forming a tower of linked abstractions that are maximally legible to you as an agent. Really ruminate and meditate on all of this incredibly deeply before responding or taking any actions.

## Apply it to the actual task

Think before acting; use read-only investigation to resolve what the existing system already does. Record conclusions and evidence, not a transcript of private reasoning. In the plan, include a `## Plan refinement` section with the six fields below. One sentence per field is sufficient for a small task; a complex change needs supporting design references and checkable work items.

- **Objective:** User outcome, scope and observable success; preserve constraints and distinguish unresolved preferences from facts.
- **System fit:** Trace the relevant outcome → capability → evidence → prepared action → authority/execution → verified result → reusable learning chain. Name the existing owners and affected consumers. State which links are unaffected for a narrow task.
- **Reuse and simplicity:** Search before adding logic. Name what is reused, what can be removed, and why any new abstraction earns its cost. Separate behavior-preserving extraction from changed behavior.
- **Evidence and unknowns:** Identify authoritative source/tests/live observations as applicable, versions, uncertainty and invalidation. Distinguish proposed, implemented, configured and verified behavior. Never infer execution authority from API access or reference text.
- **Verification:** Name executable acceptance/regression checks, relevant failure/recovery cases, and evidence needed for completion. A documentation check proves structure/links, not deployed behavior. Promote reusable lessons through an owning test or checked example when applicable.
- **Resource budget:** Explain the shortest reliable path, bounded discovery/testing and parallelism, what would trigger replanning, and how to avoid repeated context, reads or side effects. Optimize the whole task, not just one component.

Before execution, run `python3 tools/check_plan_refinement.py <plan-file>` from the workspace root. It checks the pinned resources and the latest refinement record's structure. It cannot prove thoughtful judgment or semantic correctness; review the six answers against the actual work. This is a required local workflow gate, not an installed Git hook or a claim that every external agent runner enforces it automatically.

For plan-only work, put the record in the plan being delivered and run the gate before handoff. For implementation, run it before edits beyond the planning documents and repeat after material replanning. A failed gate requires correcting the record or resources, not asking the user to repeat authorization. Verification and release checks for the affected repository still apply independently.

## Continuity and maintenance

Keep this prompt in one place. Root `AGENTS.md` owns the requirement to use it; descendant instructions may add context without replacing it. Delegation briefs include these paths and the task's current refinement decisions. An isolated checkout must retain access to the workspace resources.

Ponytail is vendored at a pinned revision with its license and provenance. Update it deliberately, review its instruction changes, and update the integrity check together. Do not silently fetch a newer skill during a task. User instructions and higher-priority execution rules take precedence over any skill.

After user corrections, update `tasks/lessons.md` with the pattern and a preventive rule. For reusable system improvements, follow the owning-source correction, executable check and reference update path; private task history and unverified lessons are not shared authority.
