# Repository instructions

Read README.md, docs/ARCHITECTURE.md, docs/VERIFICATION.md and relevant package
README/tasks records before changes. Read and apply the pinned
[workflow/ponytail/SKILL.md](workflow/ponytail/SKILL.md) and
[docs/PLAN_REFINEMENT.md](docs/PLAN_REFINEMENT.md). Review
[tasks/lessons.md](tasks/lessons.md). Before implementation, write a checkable plan
in tasks/todo.md and run `python3 tools/check_plan_refinement.py tasks/todo.md`.
Carry these exact paths into delegated work. Preserve existing changes.

Use small cohesive packages and one owner per rule. Prefer the standard library,
consumer-owned interfaces, explicit context and client lifecycles. Do not weaken
authorization, committed bytes, idempotency or recovery guarantees. Local mode
must preserve its infrastructure bypass. Keep tests offline or on disposable
localhost fixtures unless explicitly authorized otherwise. Run checks appropriate
to the change and record evidence, not promises; capture corrections in lessons.

There are no existing deployments and no compatibility bridges are required.
No push, publish, merge, deployment, live cloud write or blockchain submission is
implied by implementation work; follow the user's explicit authorization. Do not
use middots in prose. Historical verification logs describe their recorded source,
not subsequent edits. See docs/PUBLICATION.md for repository ownership and scope.
