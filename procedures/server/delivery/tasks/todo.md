# Queue and projection delivery

## Plan refinement

- **Objective:** Bind existing official provider adapters to command queue acceptance, replay production and verified event-triggered projections, with authenticated consumption and durable-before-ack semantics.
- **System fit:** Command owns prepared immutable events, eventstore owns commitment/idempotency, projection owns versioned view mutation and best-effort push. New delivery owns bounded envelopes and provider dispatch/receipt handling; parent owns runnable service composition. No storage, module or core transport edits.
- **Reuse and simplicity:** Reuse cloud/gcp.Tasks, cloud/aws.SQS, command.Consume and projection.Engine.Play. Provider-specific targets and wrappers avoid a new universal cloud API. One strict envelope codec supports commands, manual replay and publications. Canonical envelopes are capped at256KiB. Shared Encode already accounts for HTML escapes; a near-limit escaped publication test checks the actual provider bytes without adding a second wire encoding. Verify trigger payload against persisted event before constructing Trigger.
- **Evidence and unknowns:** Existing adapters enqueue and expose receipts but no consumer binding. Provider enqueue errors may follow acceptance. Stable content-derived IDs and existing durable event/view guards tolerate duplicates; replay RunID distinguishes deliberate new runs. Provider deduplication is bounded, not exactly once.
- **Verification:** Offline official-SDK seam tests cover provider requests, stable IDs, uncertain enqueue, strict envelope bounds, authenticated HTTP, serial SQS commit-before-ack, failure/cancellation/no-ack, duplicate delivery, verified trigger/mismatch and distinct postcommit push errors. Run local race and vet; no live cloud operations or new SDKs.
- **Resource budget:** New delivery subtree plus new projection publication adapter/tests only. Read-only source inspection, one implementation, targeted race/vet; preserve concurrent owners. Report public APIs early and hand off stable source for parent's native composition tests.

- [x] Implement envelope, provider producers and authenticated consumers.
- [x] Implement persisted-publication trigger verification.
- [x] Verify failure/duplicate/cancellation and document composition contracts.


## Review

New delivery wrappers implement command.Queue, projection.Producer and
 eventstore.Publisher over the existing GCP Tasks/AWS SQS adapters. Consumer
admission is mandatory and detached, HTTP authentication is explicit, and SQS
receipt deletion follows confirmed consumption. Canonical envelope bounds use
the shared encoder; a discarded base64 proposal was corrected by its failing
near-limit test and the lesson records why. PublicationAdapter verifies the
persisted source event before deriving a Trigger, keeping push failures separate.

Focused race suite passed (delivery2.480s, projection passed/cached after its
initial1.320s run). Final HTTP postcommit cancellation regression and vet passed.
Formatting and diff whitespace checks passed. No live cloud calls, new SDKs,
module/storage/core transport changes or provider capacity claims. Parent has
the stable API and owns native service composition verification.

# Durable terminal delivery rejection

## Plan refinement

- **Objective:** Prevent authenticated, structurally valid but deterministically rejected queued work from permanently blocking FIFO followers, acknowledging only after a durable rejection record.
- **System fit:** Parent owns pre-enqueue authorization and native poison-followed-by-valid acceptance. Delivery classifies typed deterministic failures and journals minimal terminal facts using existing transactional projections; no protocol/storage/schema changes. Existing unknown commit, cancellation and transient retry boundaries remain authoritative.
- **Reuse and simplicity:** Add one Rejections consumer seam and a backend-backed RejectionJournal scoped by an explicit internal Root. Store only envelope ID and stable reason code in immutable version0 named projections. Lock/read/create atomically; Lookup permits exact duplicate recovery after lost acknowledgment. No payload retention, general workflow engine, lease or durable push protocol.
- **Evidence and unknowns:** Fable2 found valid messages could be enqueued then permanently denied in Consume, poisoning FIFO. Typed invalid/idempotency/conflict and explicitly configured forbidden errors are terminal; ambiguous/network/canceled/uncertain or postcommit failures are never converted. Structurally invalid envelopes have no verified identity and remain failed deliveries requiring provider dead-letter policy.
- **Verification:** Add journal exact-duplicate/mismatch/rollback/uncertainty tests, rejection-before-ack ordering, denied-then-valid SQS continuation, HTTP rejection headers, and joined network/cancel/uncertain exclusions. Parent owns real database acceptance. Test execution currently awaits successful approval review after parent's quota failure; do not reroute blocked commands or claim unrun checks.
- **Resource budget:** Delivery subtree only, existing eventstore.Tx.PutProjection and view.PhysicalID; no cloud/native fixture actions, dependency/module or existing core edits. Source/API handoff first, formatting and plan checks, then permitted focused race/vet execution.

- [x] Implement scoped immutable rejection journal and consumer terminal outcome.
- [x] Add poison continuation and transient/uncertainty regression tests.
- [x] Complete permitted verification and hand off explicit remaining gate status.


## Durable rejection review

RejectionJournal stores an immutable version0 record containing only envelope ID
and reason code in a scoped internal named projection. Per-record LockRoots plus
read/compare/create provides exact duplicate handling without a protocol change.
Consumer returns Result.Rejected only after confirmed durable Record or Lookup;
HTTP includes rejection headers and SQS advances after receipt acknowledgment.
Typed deterministic failure and configured forbidden admission are covered;
network/EOF/syscall errors, joined transient labels, cancellation, deadline,
uncertain commit and postcommit projection errors retain failure semantics.

After approval review recovered through the parent's original command path, fresh
final-source `go test -race ./delivery && go vet ./delivery` passed (race2.186s,
vet silent success). Formatting and diff whitespace checks pass. Tests include
poison-before-valid order, journal rollback/lost-ack recovery, exact concurrent
duplicates, scope isolation, minimal body contents and authenticated versus
unauthenticated rejection handling. Parent owns the real two-database serviceapp
native poison/follower acceptance gate; no native or live-cloud test was run here.
The initial approval-quota limitation is superseded only for these approved
focused checks, not evidence of any blocked command being bypassed.

# Native rejection journal acceptance

## Plan refinement

- **Objective:** Prove terminal rejection durability and queue progress on both real adapters, including exact duplicates, committed-but-unacknowledged journal recovery and cancellation without receipt deletion.
- **System fit:** New integration-tag delivery tests compose real eventstore.Store and RejectionJournal with the existing SQS SDK seam. Parent separately owns generic serviceapp acceptance. Replay owner controls envelope/tasks/sqs and existing replay assertions; this work touches only new native tests and delivery docs/tasks.
- **Reuse and simplicity:** Reuse configured disposable localhost PostgreSQL/Mongo fixtures and existing consumer/worker. Simulate a lost journal acknowledgment by wrapping a successfully committed Backend.Update, not by global server failpoints. Use per-test namespaces and remove only those resources.
- **Evidence and unknowns:** Unit transactional fixtures pass, but real projection uniqueness/transaction behavior needs adapter evidence. Both approved fixtures remain available at55439/55449. Mongo View stays single-attempt and Update retry policy remains unchanged.
- **Verification:** Integration tests fail when either database URI is missing or nonlocal; run race-enabled native tests on both. Assert poison record before ack, following valid event committed, exactduplicate recovery, journal unknown outcome noack followed by Lookup acknowledgment, and cancellation noack. Run focused unit/race and vet after source stabilizes.
- **Resource budget:** One new native_test.go plus local docs/plan only, no failpoints, no provider calls, no fixture replacement, no storage/protocol/module/runtime changes. Coordinate with replay owner and preserve parent shared infrastructure.

- [x] Add both-adapter native rejection/worker tests.
- [x] Run native/race/vet and report exact results.


## Native rejection review

Fresh race-enabled TestNativeRejectionJournal passed against PostgreSQL17.6 and
Mongo8.0.34 (package3.483s; PostgreSQL0.78s, Mongo0.80s). Both exercised poison
before a valid follower, exact duplicate/mismatch/scope/minimal-body checks,
actual committed journal followed by injected uncertain acknowledgment, and
cancellation after real journal/event commitment. Retry Lookup confirmed durable
rejection without rerunning the poisoned append; successful event redelivery
remained duplicate with one persisted event. No global failpoints were used.

The first invocation used stale historical fixture settings and failed before
assertions (PostgreSQL authentication, Mongo replica-set selection). Parent
provided current postgres credentials and Mongo rs0/directConnection settings;
the corrected invocation passed. Only unique test schemas/databases were removed,
not the parent fixtures. Ordinary delivery race checks pass (latest cached),
integration-tag vet, formatting and whitespace checks pass. Missing-environment
execution intentionally exited1 with the required-infrastructure diagnostic,
with no skipped tests. Replay owner made its changes independently; README now
uses Request.RunID per logical run and no producer-level/static/random fallback.

# Observable terminal outcomes

## Plan refinement

- **Objective:** Make terminal queued rejection discoverable to operators and submitters without disclosing payloads or changing durable acknowledgment semantics.
- **System fit:** Claude remediation3 identifies dropped SQS results, hidden Tasks response headers and opaque provider acceptance IDs. Delivery exposes stable envelope identity and a post-journal observer; parent owns structured runtime logging and authorized journal lookup integration. Command owner released the additive Acceptance field.
- **Reuse and simplicity:** The final explicit parent contract freeze supersedes crossed alternatives: Acceptance.EnvelopeID; Rejections.Record returns (Rejection,bool,error), bool meaning fresh insertion; Consumer.OnRejected(ctx,Rejection)error; Result.ObservationError. Reset freshness inside every Update attempt, with no journal schema/state change. Hook error remains separate from durable success.
- **Evidence and unknowns:** Existing journal tests establish commit-before-ack and unknown outcome retention. Lookup duplicates suppress observation; concurrent inserts emit only for the winning fresh transaction. A crash or uncertain commit can miss the best-effort notice, so no exactly-once logging guarantee is claimed. Journal lookup remains authoritative.
- **Verification:** Test acceptance IDs bind decoded envelopes, hook observes a committed journal before acknowledgment, fresh concurrent duplicates produce one observation, transaction retries reset freshness, hook errors do not prevent ack, and uncertain writes produce no false observation. Rerun unit/race/vet and both-adapter native journal tests. Parent verifies response ID to authorized lookup and runtime structured logs.
- **Resource budget:** Delivery files/tests/docs plus the released command.Acceptance field only; parent owns serviceapp/runtime logging. No protocol/storage/module/schema changes, live provider calls or new coordination protocol. Preserve replay RunID owner and failpoint-free native fixtures.

- [x] Add stable envelope acceptance identity and fresh rejection observation.
- [x] Verify observation ordering, duplicates, errors and uncertain commits.
- [x] Hand off API and final checks for runtime/native composition.


## Observable rejection review

The final frozen contract is implemented: provider Acceptance.ID remains intact,
Acceptance.EnvelopeID exposes the immutable delivery identity, Record returns a
confirmed freshness bool, and OnRejected reports only confirmed fresh rejection.
ObservationError is separate from durable consumption. No journal schema or
storage protocol changed. Callback retries clear freshness, concurrent duplicate
inserts observe once, and Lookup duplicates suppress notice. Unknown commits and
process death can miss notices, as documented; journal lookup remains authority.

Fresh final-source race tests passed delivery2.183s and command2.221s. Integration-
tag delivery vet and ordinary command vet passed. Both real adapters passed the
updated native journal/observer suite in2.105s (PostgreSQL0.21s, Mongo0.30s), using
no failpoints and preserving parent fixtures. Tests cover queue acceptance ID
binding, journal-before-hook-before-ack ordering, callback failure independent of
acknowledgment, eight concurrent duplicates, transaction-retry freshness reset,
and unknown-outcome suppression. Formatting and diff whitespace checks pass.
Parent separately reports native generic acceptance-to-authorized-lookup passing
both databases; Avicenna reports runtime structured WARN hook wiring race/vet pass.
Source handed off stable for independent review and final full gates.
