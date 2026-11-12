# Runtime queue composition

Historical note: the initial plan and review below describing per-enqueue IDs or
`REPLAY_RUN_ID` are superseded by the later explicit `projection.Request.RunID`
refinement. Current replay identity belongs to one logical caller-selected run,
preserved across retries/pages; no process-static or random fallback exists.

## Plan refinement

- **Objective:** Wire real GCP Tasks and AWS SQS command/replay/publication delivery into runnable framework composition, with authenticated HTTP or cancelable polling and no live provider operations during tests.
- **System fit:** Reuse delivery envelopes/producers/consumer and cloud SDK adapters; serviceapp owns admission/routes and the caller owns runtime cancellation/closure. Queue acceptance remains distinct from committed processing and acknowledgment.
- **Reuse and simplicity:** Thin runtime owner reads explicit environment configuration, constructs existing provider clients, verifies Google ID tokens with the official validator, and runs existing SQSWorker.Poll. No new generic queue protocol or scheduler.
- **Evidence and unknowns:** Existing delivery tests establish stable content IDs, FIFO rules and ack-after-consume; installed official idtoken validator checks signature/audience/expiry but requires additional configured email and issuer checks. Provider IAM and live operations remain unverified. Replay defaults to distinct per-enqueue run IDs; explicit REPLAY_RUN_ID provides stable resumptions with provider-bounded deduplication.
- **Verification:** Offline injected official SDK boundaries cover missing config before client construction, OIDC claim checks, client cleanup, real delivery envelopes, fresh/stable replay IDs, SQS consume-before-ack, cancellation, bounded work timeout and retry backoff. Run scoped race and vet with pinned Go toolchain.
- **Resource budget:** Own runtimequeue subtree only; no module changes or provider calls. Reuse shared root AGENTS.md, workflow/ponytail/SKILL.md, docs/PLAN_REFINEMENT.md and tasks/lessons.md. Send API early to framework owner and report scoped evidence.

- [x] Implement configured provider composition and worker lifecycle.
- [x] Verify offline security, enqueue, replay and worker failure boundaries.
- [x] Document environment and completion evidence.

## Review

Implemented runtimequeue.Open with explicit GCP Tasks/AWS SQS configuration,
command/replay/publication bindings, exact Google OIDC producer validation and
cancelable SQS polling. No autonomous goroutines or live provider calls. Runtime
owns SDK closure; caller cancels/joins workers before Close. Worker logs contain
only controlled categories and cancellation/deadline flags, verified with a
credential-bearing error canary. Replay uses explicit stable run IDs or fresh
per-enqueue identities with documented duplicate-safe semantics.

Final Go1.27.4 checks: `GOCACHE=/tmp/blossm-runtimequeue-cache go test -race
./runtimequeue -count=1` passed2.393s; `go vet ./runtimequeue` and gofmt passed.
Seven top-level tests cover malformed config before credential discovery,
OIDC dispatch/client cleanup, replay/FIFO identity, commit-before-ack/cancellation,
worker deadline/backoff/no-ack/log redaction, exact identity claims, and official
Google validator signature rejection using an offline RSA certificate transport.
Provider IAM, live queue configuration and service deployment remain unverified.

## Plan refinement

- **Objective:** Align runtime composition with explicit projection.Request.RunID for every replay, removing ambient and random identity fallback.
- **System fit:** Projection owns typed run validation, scheduler/page identity and request shape; delivery owns envelope hashing; runtime wires the existing producers directly.
- **Reuse and simplicity:** Preserve the direct TasksReplay/SQSReplay migration already applied by the projection owner. Remove stale runtime configuration documentation and verify both provider compositions.
- **Evidence and unknowns:** Prior environment/random run semantics did not preserve exact uncertain retry identity and are superseded. Live provider deduplication remains bounded and unverified here.
- **Verification:** Offline runtime fixtures reject a missing run before enqueue, preserve identical IDs for same run/root and distinguish a deliberate new run for Tasks and SQS. Run race/vet and formatting.
- **Resource budget:** Runtimequeue only, no duplicate projection/delivery validation, no SDK or module changes and no cloud operations.

- [ ] Reconcile documentation and both provider replay assertions.
- [ ] Pass scoped verification and hand off.
