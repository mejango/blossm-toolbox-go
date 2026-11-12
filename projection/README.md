# Source projection play and replay

This package consumes `eventstore.Store.Aggregate` and `view.Store.Batch` through
consumer-owned Aggregator and Writer interfaces. It does not implement a second
reducer, permission system, storage adapter or transaction protocol.

```go
engine := projection.Engine{
    Aggregates: events,
    Views: views, // view.Store with trusted source/owner scope
    Handlers: map[projection.Source]projection.Handler{
        {Network: "n", Service: "animals", Domain: "bird"}: handler,
    },
}
result, err := engine.Play(ctx, projection.Request{Root: root})
```

## Play and source revisions

A handler receives the current aggregate, optional event action and replay flag,
and returns a typed Plan of explicit-ID Mutations and Dependencies. Mutation
cannot supply Root or Version: Play binds them to the actual aggregate and checks
source identity, canonical state, safe revision and triggering-event revision.
An aggregate older than its trigger rejects. An older event may project newer
current state, matching the retained generic play semantics. Missing aggregates
reject; an unregistered top-level source explicitly returns Ignored=true.

Only trusted event-delivery composition supplies Request.Event, with matching
root, revision and action. Manual requests and scheduled replay omit Event,
so action is absent and push disabled. A transport must not copy arbitrary HTTP
body fields into this trusted trigger. Authentication/envelope decoding remains
with that owner.

The complete plan is prepared before one view.Batch. Handlers should compute
and perform explicit reads, not external writes. View owns scope checks,
conditional If predicates, named array filters, mandatory Expected base versions
for patches, duplicate-ID rejection and atomicity. Stale/no-match rows return
Applied=false and do not notify. Mutation output is isolated from handler-owned
slices/maps. A failed or uncertain batch never pushes; ErrUncertain remains in
the returned error and Result.Uncertain. Result.Committed means Batch was
acknowledged, including a batch of explicit no-ops, not that every row changed.

Use `Mutation{ID: id, Tombstone: true, Body: fullOwnerScopedObject}` for a source
deletion that can arrive before first creation. The explicit view tombstone
persists its version even when missing, blocking an older first write. Ordinary
Delete retains missing-row no-op semantics. A newer complete replacement may
recreate a tombstone; a patch cannot. Omit If/Expected for unconditional source
tombstones; supplied conditions retain normal view semantics.

## Dependencies and notifications

Dependencies specify Root, Flag and Optional. Current aggregates and registered
handlers run serially, depth first; dependency mutations precede their parent
in the final batch. Root+Flag nodes execute once. An active root is a cycle even
with a different flag. Missing optional aggregates are skipped; a later required
reference to the same missing node still fails. Missing handler registration
is always a dependency configuration error.

Limits default to64 node/edge visits and1000 prepared mutations; zero selects
defaults, negatives reject, positive values explicitly set the budget. Repeated
and missing references consume visits. Cycles, exhausted budgets and dependency
failures cause no view write. Engine configuration must remain immutable while
in use; per-play mutable state is isolated.

Every dependency output keeps its own source revision. Unrelated source clocks
are not merged into one view ID. Use independent rows and view composites, or an
explicit application contract for multi-source materialization. A view.Store
writer enforces its configured scope across the batch, rejecting cross-scope
outputs. Dependency replays are manual and do not independently push.

Engine.Push receives ID/root/version/deleted invalidations only for applied,
event-triggered, unmuted rows after successful commit acknowledgement. Muting
matches source and action and suppresses only push. Calls are serial/awaited;
failures are collected in Result.PushFailures without undoing durable success
or preventing later attempts. Cancellation after commit is likewise a push
failure, not a rollback. The caller can log/report these errors.

Notifications do not fabricate post-patch bodies or legacy timestamps, traces
and groups absent from the current protocol. A consumer can fetch current scoped
data. Push remains best effort, not a durable outbox or delivery guarantee.

## Bounded replay scheduling

`Scheduler{Roots: BackendRoots{Backend: backend}, Producer: producer}` implements
`Replay(ctx, source, runID, after, limit)`. RootStream and Producer are consumer-owned
interfaces. RootStream callbacks must be serial, awaited, strictly ordered by
Root.Key, non-replayed and stopped on error. BackendRoots uses the existing
backend View and excludes zero-event counters, including view-only reservations.

RunID is a required typed string identifying one logical replay. Reuse it for all
pages and retries, including uncertain scheduling; use a fresh ID for a new run.
ReplayResult.RunID echoes it, and each produced Request carries the same RunID.
Provider message identity includes that value. Manual Play requests may omit it.

Replay collects at most limit+1 source identities before calling Enqueue, keeping
external effects outside read transactions/stream callbacks. Limit is explicit,
1..10000. After is the returned opaque Root.Key cursor; malformed and cross-source
cursors reject. Result.Enqueued counts acknowledged enqueues; After advances
only on acknowledgement. Complete means the page reached the end and all
selected enqueues acknowledged.

On failure, confirmed partial progress is returned. Resume from After, accepting
possible redelivery of an uncertain failed enqueue. Producer acknowledgement
means scheduled, not played. There is no queue rollback, durable scheduler
receipt, lease or exactly-once claim. Each page has a separate snapshot; roots
inserted behind a cursor require a fresh pass. Seeking may scan prior/scoped-out
metadata, but buffered identities and scheduled work are bounded.

## Retained capability map

The parent owns [the central inventory](../docs/capabilities.md). References are
pinned under `tasks/worktrees/go-reference`; these rows identify implementation
and remaining composition rather than implying historical wire compatibility.

| Reference owner | Go owner and evidence |
| --- | --- |
| `procedures/projection/play` | Engine.Play, current aggregate and trusted trigger validation, manual action suppression and source/action muting. Tests port the CLI bird sound/volume example and prove stale plays cannot overwrite current rows. |
| `procedures/projection/replay` | Scheduler.Replay, scoped serial producer scheduling with limits/cursors/partial progress. Tests cover150 roots, resume, read failures and real BackendRoots-to-Play execution. |
| `cli/.../projection/deploy/src` conditions, ops and array filters | Plan/Mutation forwards view.If, Operations and explicit filters. Native tests preserve conditional no-op and filtered sibling behavior. Explicit IDs are required; handlers may use scoped view.List to select IDs within their mutation budget. No implicit unbounded query-write fanout is claimed. |
| Generated recursive replay | Explicit dependency graph, flags, optional missing data, cycles/budgets and prepare-before-commit. Independent source-versioned outputs deliberately replace unsafe implicit multi-source update merging. |
| Generated save/delete and no-content update | One view.Batch with stale/source guards, ordinary Delete and explicit Tombstone. Both native adapters prove newer deletion-before-create blocks older concurrent first writes and no-op rows do not push. |
| Generated muted push and callback failures | Applied event-triggered rows notify after commit; failures remain separate. Native push callback reads committed data before returning an error. Versioned invalidations replace unavailable legacy envelopes; channel/group expansion is the push consumer's responsibility. |
| aggregateFn/readFactFn injection | Aggregator reuses eventstore replay; typed handler closures inject explicit application read dependencies. No duplicate dynamic fact/RPC interpreter is introduced. |
| Projection server, Pub/Sub decoding, RPC routes, CLI replay command and cloud queue wiring | Parent transport/CLI/application composition binds these APIs and Producer/Push implementations. This subtree supplies no listener, authentication, cloud resources or deployment. |

## Verification

From `blossm-go`:

```sh
go test -race ./projection
go vet ./projection
POSTGRES_URL='postgres://...@127.0.0.1:55439/blossm?sslmode=disable' \
MONGODB_URI='mongodb://127.0.0.1:55449/?replicaSet=blossm' \
  go test -race -tags integration ./projection -count=1
```

Native tests require both localhost URLs and fail rather than skip when missing.
They create/drop only unique owned schemas/databases. Both adapters exercise
actual append/aggregate, out-of-order concurrent plays, absent tombstones,
scoped filtered/conditional writes, late-owner-failure rollback, readable commit
before failed push, and bounded replay with producer effects outside root reads.

Verification on2026-11-12: ordinary race checks and vet pass; full native race
suite on PostgreSQL55439 and Mongo55449 passes2.613s. An explicit missing-URLs
control fails with the required diagnostic and no skipped cases. Parent also
ran the unit race suite successfully. No module/storage/protocol changes belong
to this subtree task.
