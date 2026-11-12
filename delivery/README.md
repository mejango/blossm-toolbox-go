# Prepared command and projection delivery

`TasksCommands` and `SQSCommands` implement `command.Queue` using the existing
`cloud/gcp.Tasks` and `cloud/aws.SQS` adapters. Each wrapper has a `Target` field
of type `TasksTarget` or `SQSTarget`. Targets own provider routing/configuration;
clients and credentials remain caller-owned. No import discovers credentials.

`TasksReplay` and `SQSReplay` implement `projection.Producer` and contain only
provider Target configuration. Supply an explicit `projection.RunID` in each
`projection.Request.RunID`; preserve it for retries and every page of the same
logical replay run, and choose a distinct ID for a deliberately new run. The
request root/flag and RunID determine stable envelope identity. There is no
producer-level/environment static ID or random fallback. `projection.RunID`
owns validation. Replay requests must omit `Event`; a queue caller cannot
synthesize an event trigger.
`TasksPublications` and `SQSPublications` implement `eventstore.Publisher` for
outbox-to-queue delivery, with retry Attempts excluded from envelope identity.
A successful enqueue acknowledges provider acceptance, not completed execution.
Command acceptance includes `ID` (provider acknowledgment) and `EnvelopeID`
(the stable content identity). The submitter can use EnvelopeID with the
application's authorized `/delivery-rejections/{id}` lookup, without deriving
it from a Tasks task name or SQS MessageId.

`Encode(Envelope)` generates the version and content-derived identity; `Decode`
checks both, rejects unknown/duplicate JSON fields and trailing data, and allows
exactly one of Command, Replay or Publication. Canonical JSON is capped at256KiB (`MaxEnvelopeBytes`), including HTML escaping
already counted by the shared encoder. The actual Cloud Tasks body is tested
against this bound with a near-limit escaped publication. Oversized input is
rejected without truncation. Provider queue settings must permit the bound.
A command batch that exceeds the envelope bound must run synchronously or be
prepared as separately authorized atomic batches; delivery never silently splits
an atomic submission. Provider queue settings must permit the configured bound.

Cloud Tasks uses a stable name derived from the target URL, service account,
audience and envelope identity. AlreadyExists acknowledges that named delivery;
changing the target does not collide with another route on the same queue.
SQS FIFO requires `GroupID` and uses envelope identity as deduplication ID;
standard queues omit FIFO attributes. Provider deduplication expires, so durable
event keys and projection source versions remain the authority for duplicates.
Provider errors or absent acknowledgments return `ErrEnqueueUncertain` and
`command.ErrQueueUncertain`; retry the identical work. Preflight invalid input
and already-canceled contexts return without a provider call.

## Consumer composition

Construct `Consumer` with `Commands` (command.Appender), `Projections`
(*projection.Engine), and/or `Publications` (*projection.PublicationAdapter).
Only configure work types that the endpoint is meant to consume. Every consumer
requires `Admit func(context.Context, Envelope) error`: check every command root,
source network/service/domain, allowed action, and replay/publication source.
Admission observes a detached body image; modifying it cannot alter effects.
Trusted queue writers remain an explicit application/IAM boundary.

`Handler(authorize func(*http.Request) error)` constructs an HTTP POST endpoint.
The callback must verify issuer, audience and allowed service/route authority,
using the application's authentication owner. A bearer header alone is not
proof. Authentication precedes body consumption, and admission precedes effects.
Register the handler at the exact configured Cloud Tasks destination. It returns
204 only after confirmed command receipts, successful projection work, or a
durably journaled terminal rejection (explicit response headers below). Malformed
input, failed authentication, unjournaled failure, cancellation and uncertain
commit never return success. Failure bodies do not expose payloads or internals.

`SQSWorker{Queue, Consumer}.Poll(ctx, receiveRequest)` receives one provider batch,
consumes serially, and acknowledges each exact receive receipt only after success.
It returns the confirmed acknowledgment count and first error. A durable terminal
rejection counts as acknowledged and processing continues. Other failure leaves
this and remaining messages unacknowledged; no implicit delete or Nack occurs.
Choose visibility timeout for the work or renew it externally. Cancellation
after commit prevents acknowledgment and may cause safe redelivery. Delete errors
also leave acknowledgment uncertain. There is no lease/fencing/exactly-once claim.

`projection.PublicationAdapter{Backend, Engine, OnPushFailure}` checks publication
identity/size, event hash and fingerprint, then reads the source event by key and
verifies its full hash binding. Only then does it derive source root, number and
action for `Engine.Play`. Backend must read the source event store. The optional
observer receives best-effort push failures after durable success; `Consume`
also returns them in `projection.Result`. They do not trigger rollback claims or
queue redelivery. Successful ignored sources, empty plans and stale version
no-ops are acknowledgeable. No general durable push protocol is introduced.

## Verification and limits

Offline tests exercise existing SDK injection seams and actual authenticated
HTTP consumption: stable names/dedup IDs, destination isolation, replay RunID,
uncertain enqueue, scope denial, malformed/oversized envelopes, cancellation,
commit-before-ack ordering, duplicate receipts and ack failure. A near-limit
publication of HTML characters proves bounded actual Tasks bytes. Publication
tests verify stored-source binding, tampering/missing-source rejection, uncertain
view commits and distinct postcommit push failures. Parent composition owns
native database-to-queue-to-projection acceptance tests. No live cloud behavior
or deployment/IAM correctness is claimed by these tests.

Provider limits checked against [Cloud Tasks quotas](https://docs.cloud.google.com/tasks/docs/quotas)
and [SQS SendMessage](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SendMessage.html):
each permits up to1MiB; this adapter deliberately uses smaller bounded payloads.


Focused verification: `go test -race ./delivery ./projection` passed (delivery
2.480s; projection passed then cached). The additional HTTP cancellation-after-
commit regression passed separately. `go vet ./delivery ./projection`, formatting
and diff whitespace checks passed. These checks are offline; parent native
composition evidence is recorded separately.


## Durable terminal rejections

Configure `Consumer.Rejections` with `RejectionJournal{Backend, Scope}` to resolve
poison messages. `Scope` is an explicit internal root, for example network/service
of the consumer, domain `delivery-rejection`, ID `commands`. The journal uses an
internal named projection and a per-envelope lock. Do not register that reserved
named view in public view routes. Backend and client lifetime remain caller-owned.

`Rejections` exposes `Lookup(ctx, envelopeID)` and
`Record(ctx, Rejection) (Rejection, bool, error)`. The bool is true only for a
newly inserted, confirmed record; it is false for exact duplicates and all
errors. Transaction callback retries reset it before each attempt.
Records contain only `EnvelopeID` and a stable `Reason`: `invalid`, `idempotency`,
`conflict` or `forbidden`. There is no event body, token, claims, error message or
user identity in the record. Records have no automatic TTL: removing them changes redelivery behavior.
Version0 records are immutable. Exact duplicate
records succeed; mismatched records fail. Parent applications may expose Lookup
through their existing authenticated internal route; this package adds no public
journal endpoint.

Consumer checks an existing rejection before attempting work. Otherwise typed
invalid input, idempotency and conflict errors are terminal candidates. Set
`Forbidden func(error) bool` to recognize the application's authorization error;
it is not inferred from error strings. This includes a valid envelope rejected
by Admit. Network/transport errors, transient labels, cancellation, deadline and
unknown-commit markers take precedence even when joined to a terminal sentinel.
Postcommit projection errors cannot be relabeled as rejection. Classification
must describe deterministic business failure, never provider availability.

A candidate is acknowledged only after an exact durable journal record returns.
`Result.Rejected` then contains that record; the original operation is not called
successful. HTTP204 includes `X-Blossm-Rejection: <envelopeID>` and
`X-Blossm-Rejection-Reason: <code>`. SQS deletes the exact receipt and continues to
the next message. A rejection does not claim rollback of earlier, independently
committed deliveries. Changing admission policy does not resurrect an identical
rejected envelope; a changed request has a different content identity.

On journal failure or uncertain commit, the original error and journal error
remain returned, with no acknowledgment or same-call uncertainty recovery. A
later delivery can confirm the committed rejection through Lookup. Nil Rejections
keeps the prior retry/error behavior; production composition must supply a journal
to avoid permanent deterministic poison. Unauthenticated HTTP requests and
malformed envelopes are never archived: without a verified envelope ID there is
no safe journal key. Configure provider dead-letter handling for those messages.

Rejection regression sources cover immutable/scoped minimal records, concurrent
exact duplicates, rollback and lost-ack recovery, joined transient exclusions,
authentication versus admission, explicit HTTP rejection headers, and SQS poison
followed by valid work. Native journal behavior is covered below; parent separately checks generic
serviceapp composition.


Final durable-rejection checks: `go test -race ./delivery && go vet ./delivery`
passed on the final source (race2.186s). Formatting and diff whitespace checks
passed. The transactional fixture tests are separate from the parent's native
serviceapp verification; no live provider validation was performed.


Native rejection acceptance: with both `POSTGRES_URL` and `MONGODB_URI` pointing
to disposable localhost databases, run
`go test -race -tags integration ./delivery -run '^TestNativeRejectionJournal$'`.
Missing or nonlocal settings fail immediately instead of skipping. The suite
passed PostgreSQL17.6 and Mongo8.0.34 in3.483s, using isolated test namespaces and
no failpoints. It proves poison-before-valid progress, exact duplicate recovery,
minimal scoped records, uncertainty after an actual journal commit, and no
acknowledgment after cancellation following journal/event commitment. The latter
cases recover through real database reads on redelivery. Provider receive/delete
calls remain SDK-seam fixtures; these tests do not call live SQS.


## Rejection observation

Set `Consumer.OnRejected func(context.Context, Rejection) error` to emit a
structured warning containing only envelope ID and reason. It runs after the
journal confirms a fresh insertion and before receipt acknowledgment. It does
not run for Lookup duplicates, concurrent duplicate inserts, aborted attempts
or uncertain writes. No payload, claims, token or application error text is
passed to this hook.

The callback is awaited and must return promptly. Its error is reported only in
`Result.ObservationError`; the durable rejection remains successful and transport
acknowledgment proceeds. The journal record is authoritative, and the submitter's
Acceptance.EnvelopeID remains the authorized lookup key even when observation
fails. Logging is best-effort, not a durable action: process death or an uncertain
commit can miss a notice, and later duplicate recovery intentionally does not
emit a fresh-record notice. There is no exactly-once logging guarantee.

The runtime composition owns logger configuration. The delivery package does not
choose a logging sink or expose an unauthenticated rejection lookup endpoint.


Final observability checks: `go test -race ./delivery ./command` passed
(2.183s/2.221s), integration-tag delivery vet and command vet passed, and the
updated two-database native journal/observer suite passed in2.105s. These prove
stable acceptance identity, journal-before-observation-before-ack ordering,
concurrent duplicate suppression, transaction retry freshness reset, observer
failure independent of ack, and no observation on an uncertain commit. Parent
composition separately verifies the accepted envelope ID resolves through the
authorized rejection lookup and binds the safe structured runtime logger.
