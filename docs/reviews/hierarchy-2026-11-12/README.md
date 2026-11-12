# Hierarchy source review

Two user-authorized Claude Fable 5.2 reviews used only Read/Glob/Grep, restricted
mode, no command tools, no MCP, no session persistence and bounded budgets.
Recorded costs were $1.6728166 and $0.9463996. These are source reviews, not test
execution, live infrastructure verification or a comprehensive security audit.

The first review identified library tests importing vendor adapters and stale
current documentation. Cloud JWT signing composition tests moved to tests/signing
with all assertions retained, and the checker now enforces lib-only internal
imports in lib tests. Documentation paths and permitted-edge diagrams were fixed.
The follow-up closed all accepted findings with no remaining concrete defect in
that scope. The parent verified checker fixtures and focused signing unit/race/vet.
No production Go bytes changed in the review fixes.

Exact prompts, reports, model usage and coverage limits are retained alongside
this record. Historical separation review reports remain unchanged.

A third bounded review covered the separately discovered Mongo contention fix
and reported no runtime correctness defect. It identified test gaps for prompt
cancellation, varying jitter and the upper configuration bound. The owner added
those assertions and passed scoped race/vet. Cost was $0.8270614. The parent then
reran full gates; initial failure evidence is retained in Dia. The ten-repeat
real concurrent Mongo journey passed in81.824s before the final public-pin run.
The reviewer did not execute tests. Final gate evidence is recorded separately.
