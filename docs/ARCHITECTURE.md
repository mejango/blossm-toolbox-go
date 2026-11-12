# Go backend architecture

One module, `blossm.local/backend`, replaces the JavaScript runtime contract for a fresh system. Reference repositories remain untouched. Reference package source is Toolbox `18a2e086e`; the isolated checkout is `../tasks/worktrees/go-reference` at remotely verified `372d1c6a3`. Dia source is `98364adf` plus preserved working changes. The package-by-package scope ledger is [capabilities.md](capabilities.md).

## Owners and flow

| Owner | Responsibility |
| --- | --- |
| `eventstore` | Native representation/validation, hashes, atomic append outcomes, reducer replay, query refinement, snapshot/block frontiers, durable publication |
| `storage/postgres`, `storage/mongo` | Transaction isolation/retry/lifecycle, constraints, canonical binary persistence, cursors, candidate indexes, versioned projections |
| `storage/internal/queryindex` | Typed scalar-leaf terms shared by both candidate indexes |
| `command`, `rpc`, `fact` | Procedure execution, queue acceptance, best-effort callbacks, HTTP transport and streaming |
| `view` | Scoped queries, atomic versioned replacement/patch/delete batches, format/CSV |
| `dia` | Typed identity/session/account/challenge/scene/role/group, system and comms behavior, authentication and subject authority |
| `transport` | Typed HTTP routes, bounded strict decoding and protected session projection |
| `evm`, `wallet` | Standard Ethereum codecs/signing/custody, owner-authorized operations, durable signed bytes and nonce reservation, bounded reconciliation |
| `cloud/gcp`, `cloud/aws` | Official SDK operations and provider-specific delivery, acknowledgment, identity and resource contracts |
| `runtimepub`, `runtimequeue`, `runtimeauth`, `runtimeenv` | Explicit publication/delivery/signing/database clients and their lifecycles |
| `serviceapp`, `cmd/framework`, `cmd/dia` | Generic reference composition and retained Dia HTTP entrypoints |
| `delivery`, `projection` | Authenticated prepared-work consumption, durable rejection, replay scheduling and versioned projection execution |
| `cli`, `cmd/blossm`, `deploy` | Offline configuration, generation, tests/build and reviewable deployment artifacts |

A command authenticates and validates typed input, prepares events, and appends an atomic batch. The shared owner normalizes before hashing, locks roots in deterministic order, checks duplicate identity and expected next event numbers, validates reducer transitions, writes counters/events/outbox, then commits. Publication occurs afterward. Consumer acknowledgment follows its durable projection write. Failed publication leaves outbox records pending; failed deletion after acknowledgment permits duplicate delivery. Startup and periodic recovery are bounded. Local mode keeps real database transactions but creates no publication records and invokes no publisher.

Remote runtime has explicit GCP Pub/Sub or AWS SNS/SQS selection. SQS receives are not HTTP task execution, and SNS acknowledgment does not prove subscriber completion. Cloud Run background workers require allocated CPU and a warm instance; generated artifacts state this operational choice. Provider resources, permissions and live execution are never inferred from local tests.

## Internal Go v1 representation

JSON is UTF-8 with duplicate object keys and unpaired Unicode surrogate escapes rejected. Object keys are lexically sorted by Go's JSON encoder, insignificant whitespace is removed, and strings use its deterministic escaping. Numbers are finite binary64 within the safe-integer range. Integer values use plain decimal spelling; fractional values use shortest Go decimal spelling. Negative zero normalizes to zero. This is an explicit internal encoding, not an RFC8785 or historical JavaScript compatibility claim. Fixed hash vectors and normalization fixed points are tested.

Ethereum uint256 values use decimal strings; persisted uint64 nonce/block/confirmation values also use decimal strings. Timestamps are parsed RFC3339 with explicit offsets and normalized to UTC RFC3339Nano. JWT claims use integer epoch seconds and ES256 JOSE signatures. Optional fields retain the documented struct `omitempty` behavior; heterogeneous payload/claims/context fields remain validated raw JSON. Typed application decoding rejects unknown fields rather than casting or silently stripping them.

An input fingerprint hashes its normalized root/action/key/expected/timestamp/payload/context/transaction representation, excluding only top-level transaction claim fields exp, iat, nbf and jti. The event hash retains the complete original claims. Generated command keys bind a canonical trusted principal (or effective iss/aud/sub), command ID and event index, excluding the target root so retargeted retries conflict. An event hash hashes that input, allocated zero-based number, fingerprint and empty hash field. Storage retains those exact normalized values as bytes and separate searchable metadata. A repeated identical key returns DUPLICATE, including within one batch. A changed request under the same key fails. COMMITTED means confirmed database commitment; QUEUED means confirmed provider acceptance only. An unknown commit returns an explicit uncertainty error and never automatically replays the transaction callback.

Root revision is the last event number, -1 when absent. Expected is the next event number, zero for creation. Dia adapts this to next-count at its application boundary. Reducers are deterministic and are executed both before commit and during replay; invalid transitions cannot be committed merely because payload validation passed.

## Queries, projections and blocks

Queries conjoin strict scalar equalities. Paths traverse arrays without treating elements as one correlated object. Explicit null differs from missing; numeric strings differ from numbers. Indexed typed leaf terms select matching latest clean snapshots, while all dirty and unsnapshotted roots remain candidates. Full current replay performs final matching. There is no implicit 100-snapshot cap. Cursors await serial callbacks, honor cancellation and clean up within the same repeatable transaction.

Snapshots and blocks retain binary byte fields. Block construction advances durable per-root snapshot frontiers atomically with the block, never using wall-clock windows as the inclusion criterion. A bounded root batch permits continuation; late commits remain eligible. Blocks are internal audit artifacts, not public Ethereum transactions. Optional signing binds the block hash. There is no public export of private event payloads in the HTTP API.

A projection replacement includes its complete source root and revision. Older/equal versions cannot overwrite newer output, including tombstones. Patches require an expected prior view revision and atomically apply with their batch. This provides an explicit single-source contract; it does not claim exactly-once incremental side effects or solve arbitrary multi-source ordering. Application owner predicates and source scope constrain reads independently of client filters.

## Identity, recovery and external effects

Session creation uses a client-generated 32-byte secret; it cannot recover an upgraded session as an anonymous retry. Every access check verifies issuer/audience/time/signature and current session identity, termination and scene deletion. Subject-scoped role checks replace historical `privileges:none` mutation paths. Authorization is checked at admission; it does not claim cancellation of an already admitted request when an independent concurrent revocation commits.

Dia stores an operation receipt atomically with domain effects. After any append error it performs one context-bound receipt read: an exact semantic digest proves commitment, a different digest conflicts, and absence or a failed read preserves the original error. This resolves lost responses without replaying a write or assuming that a transaction error proves rollback.

Challenge consumption, deferred identity events and session upgrade commit as one multi-root batch. Proof is single-use for effects; an already consumed matching proof may recover the committed response while valid. Failed attempts are bounded. Email is encrypted in challenge state; delivery follows commitment and supports explicit retry, with possible duplicate mail. SMTP/SMS/provider acknowledgment is not exactly-once delivery. `thenFn`-style callbacks remain awaited, best-effort in-process work and can be lost after a crash. No general durable-command/action protocol is enabled.

Custodial wallet authorization binds the persisted wallet to the authenticated principal. Signed transaction bytes and the account nonce frontier commit before submission, shared across instances through the selected database. Unknown submissions retain their nonce and hash; retries broadcast identical bytes. Only canonical, sufficiently confirmed receipt evidence marks success or failure. Polling has a total deadline. Relayr payments and JB/shop are deferred by explicit scope decision, not represented by success stubs.

## Execution authority and evidence

Local compilation, native database tests, SDK protocol fixtures and generated configuration checks establish only their tested contracts. No Go deployment, live KMS signature, provider message, email/SMS, blockchain submission, publication, merge or remote-repository creation has been authorized or performed. See [VERIFICATION.md](VERIFICATION.md) for exact evidence and remaining gates.

## Callback ownership and streaming reads

The shared reducer invocation boundary copies state and all mutable event fields
before invoking application code. Passing a struct by value is insufficient for
RawMessage, slices and pointers; committed hashes and outbox records must remain
unchanged even if a reducer uses in-place working buffers. Append, current replay
and historical replay use this same boundary.

The authenticated framework replaces event context and transaction claims with
verified identity data. Command/fact validation and missing required authority
map to explicit client errors; uncertain queue acceptance remains distinct from
commit success. Store routes include `/count`, `/history`, `/roots` and
`/aggregates`, alongside `/events`, `/aggregate` and `/query`. Root and aggregate
streams use NDJSON, one repeatable read, serial callbacks and request cancellation.
An error after output aborts the response so truncation cannot look successful.
Aggregate streaming buffers root identities before replay, avoiding concurrent
cursors on one database connection; it does not buffer all aggregate bodies.

`transport.StoreClient` provides typed append, aggregate, count, query, history,
root/aggregate stream and block calls over explicit configured service URLs.
`POST /blocks` is exposed only when the framework has a configured producer
signer. It constructs snapshots and the signed block atomically, then attempts
outbox recovery even if no root changed. Private encrypted export remains the
separate `audit.Seal/Open` capability with mandatory producer signature binding.

Each canonical publication is bounded to240KiB before hashing and database
mutation, with space reserved for generated hashes, event numbers and retry
bookkeeping. The same rule applies in local mode. This is an individual-event
boundary, not a batch or aggregate-state limit. Generated AWS subscriptions use
raw SNS delivery and receiving queues sized for at least256KiB; alternate
operator configurations must preserve those delivery assumptions.

The Dia server binds request contexts to its process lifecycle. Shutdown cancels
workers and active request contexts, allows a15-second HTTP grace period, and
closes remaining HTTP connections if that period expires. It joins cancelled
context-aware publication workers before closing SDKs/database pools; Go cannot
forcibly terminate arbitrary callbacks that ignore their context. Terminal logs
record controlled lifecycle stages, never raw provider or connection-string errors.


## Generic service composition and interaction

`cmd/framework` runs `serviceapp` over the same native protocol. Identity and
application namespaces are distinct, with separate outbox ownership. Its
command/fact/job/view/projection routes consume the generic packages; `cmd/dia`
retains the application-specific identity, comms, system and custody routes.
Both executables close clients after canceled request and worker lifecycles join.

`command.Result.Then` runs after successful dispatch for all ordered outcomes,
including queued, duplicate and empty batches. It receives detached inputs and
receipts. Deterministic response construction can repeat; effects must inspect
COMMITTED explicitly and remain best effort. Callback errors preserve confirmed
outcomes. This is not a deferred durable-action protocol.

`CommandClient` derives child identity from parent ID, fixed logical target,
procedure and operation selector. Signed delegation binds the parent transaction
and trace, uses the target network/service audience, and revalidates current Dia
session/scene state. It grants command-route authority only. The copy reference
requires a source version and verified historical state so a changed source cannot
change a retry's derived child payload. Child commitment cannot be rolled back by
a later parent failure.

Remote command envelopes require a separately verified producer identity and an
explicit admission policy. Deterministic invalid/idempotency/conflict/forbidden
outcomes may be acknowledged only after a minimal immutable rejection journal
commits. Unknown commits, network errors and cancellation take precedence and
remain retryable. The reference exposes rejection inspection only to its internal
peer. Queue acceptance includes the stable envelope ID for that lookup. A fresh
confirmed rejection emits a minimal structured runtime warning; observer failures
do not undo commitment or prevent acknowledgment. Notification is best effort,
not a second durable protocol. Store-peer credentials never default to delivery credentials.

A replay run has an explicit typed RunID across scheduler, HTTP/CLI and provider
messages. Retries and pages reuse it; a new rebuild supplies a new ID. Task and
FIFO deduplication therefore do not suppress later rebuilds.

Named views isolate physical row IDs. Required group unions deny access when no
groups are present. Query-selected mutations lock selected source roots and verify
each requested version equals the committed source frontier before any writes;
they cannot invent versions that would suppress future projections. Projection
Batch remains the trusted source-bound consumer API. Missing-row tombstones
prevent an older create from resurrecting a deleted projection.

Dia JWT signing accepts a context-aware local or provider KMS signer and validates
returned ES256 signatures against a pinned public key. Runtime signer selection
is explicit; token verification and authorization remain in Dia and transport,
not provider adapters. Gateway cookies, proxying and uploads have separate bounded
and authenticated interfaces, documented under `transport`.

## Network signing trust boundary

The user explicitly selected **one trust domain per network**. Services using that
network's signing authority are mutually trusted issuers. Pinned peer subjects and
service audiences restrict correctly configured callers, but a process authorized
to sign with the shared key can mint another peer's subject; those checks are not
compromise isolation between services. Network administrators must grant signing
access only to trusted services/operators. Separate networks and their signing
keys establish separate trust domains. Per-service issuer isolation is not claimed
or silently emulated by subject strings.

Request streaming currently has the gateway's explicit 30-second deadline. A
truncated/error stream aborts and must not be interpreted as complete. Long scans
need bounded selectors/pages or an application-specific HTTP policy; there is no
claim of indefinitely open fact/store streams. Structurally malformed delivery
bodies cannot establish an authenticated content ID and remain unacknowledged;
provider redrive/dead-letter policy is an operator configuration prerequisite,
especially for FIFO groups. Deterministic failures of valid envelopes use the
rejection journal and do not block following work after acknowledgment.


Operators obtain a bounded internal service credential through
`blossm internal-token --config ... --execute`, using the same selected local/KMS
signer and exact configured network audience/internal subject. Preparation reads
no credentials and performs no signing. Provider invoker tokens and application
service JWTs are separate. GCP generated configurations explicitly select
`application` invocation (platform IAM check disabled, container authorization
retained) or `iam` (a separate Google invocation credential/edge remains required).
Generating these reviewed artifacts performs no IAM change or deployment.
