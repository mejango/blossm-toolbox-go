# MongoDB eventstore adapter

`Open(ctx, Config{URI: uri, Database: "blossm", Namespace: "events"})`
returns an `eventstore.Backend`. Both names are explicit and restricted to ASCII
letters, digits, `_` and `-`; databases are at most 63 bytes and namespaces 80.
Each instance owns its client. Call `Close(ctx)` when finished. Opening awaits
collection/index creation and rejects a standalone server. Opening never drops
data; using the same database and namespace intentionally reopens that store.

The parent owns normalization, validation, hashing, append, replay, queries and
the local-mode publication bypass. This adapter serializes the supplied protocol
values with `encoding/json`, storing those bytes as BSON binary, together with
indexed metadata. It never sends payload/state JSON through BSON document
casting. Binary snapshot/block fields round-trip through the JSON byte encoding.
Unique constraints cover event key, hash and root/number, snapshot hash and
root/number, block number/hash, publication ID and projection ID. Missing counts
return zero; missing individual records return `eventstore.ErrNotFound`.

All reader and writer callbacks use one snapshot-read, majority-write transaction
on the primary. Callbacks and cursor operations must be serial and must not
escape their callback lifetime. `LockRoots` sorts a copied input and reserves
root documents by a real write, forcing conflicting transactions to abort.
View runs once and returns errors without replaying consumer callbacks or streams.
Update callbacks must propagate operation errors, tolerate known-abort retries
and perform no external side effects. `MaxAttempts` applies only to Update,
includes the first attempt, defaults to ten and accepts 1..100. Known-abort retries wait with half-to-full
jitter under exponential ceilings of 10, 20, 40, 80, 160 and then 250ms. The
250ms cap also applies to explicitly larger attempt budgets. Waits respect
context cancellation; there is no wait after the last attempt. Retryable error
classification is unchanged: only the adapter-owned known-abort sentinel or
`TransientTransactionError` label permits replay, never code112 or error text alone.
Each driver
operation also has a 30-second default timeout; callers should set an overall context deadline.
Unknown commit acknowledgement, network failure during commit or interrupted
commit returns `eventstore.ErrUncertain` without replaying the callback. The
driver may internally retry the idempotent commit command.

Projection insert/CAS replacement executes within that transaction; equal or
older versions return false, and another source root returns `ErrConflict`.
Deletion is a versioned tombstone, preventing stale resurrection. Pending
publications sort by attempts then ID, require a positive limit, and rotate on
failure. Delivery remains a parent concern. Cursors fetch batches of 128, await
each callback, propagate its error/cancellation and close on every exit.

## Dependency

The parent-selected dependency is `go.mongodb.org/mongo-driver/v2 v2.9.0`.
No root module files were changed by this adapter task. Its native tests ran
with Go 1.27.1 and MongoDB 8.0. The official [upgrade documentation](https://www.mongodb.com/docs/drivers/go/current/reference/upgrade/)
lists Go 1.25 and MongoDB 4.4 as v2.9 minimums. The official
[v2.9.1 release](https://github.com/mongodb/mongo-go-driver/releases/tag/v2.9.1)
fixes a GridFS security issue affecting v2.9.0; this adapter does not use GridFS.
The official [v2.9.3 release](https://github.com/mongodb/mongo-go-driver/releases/tag/v2.9.3)
is available. The parent owns any pin update and root dependency resolution.

## Native verification

From `blossm-go/`:

```sh
go test -race ./vendors/mongodb
go vet ./vendors/mongodb
MONGODB_URI='mongodb://127.0.0.1:57019/?replicaSet=blossmtest&directConnection=true' \
  go test -race -tags integration ./vendors/mongodb -count=1 -v
```

The integration tag **requires** `MONGODB_URI`; absent, nonlocal or unusable
infrastructure fails rather than skips. Use a disposable localhost replica set
with `--setParameter enableTestCommands=1` for the bounded transient retry and
lost-commit-acknowledgement fault tests. Tests use unique databases, drop only
their own databases and close clients. Do not run concurrent external fault
injection against the same server: MongoDB's failCommand failpoint is global.

The task-owned fixture was MongoDB `mongo:8.0`, image digest
`sha256:d97fea17ccb8d004cd137be8dab54d70d0df11be69c9f422aefe4484c8414ab7`,
container `blossm-go-mongo-test`, bound only to `127.0.0.1:57019`.
Native checks exercise every protocol method, hash/raw JSON/binary preservation,
uniqueness, transactional rollback, opposite-order concurrent root locking,
repeatable reads, multi-batch awaited/cancelled cursors, outbox failure rotation,
concurrent versioned projections, namespaces/reopen, transient retries and
unknown commit outcome without callback replay. No cloud/deployment behavior or
capacity guarantee is claimed.

Verification completed 2026-11-12: `go vet ./storage/mongo` and ordinary
race-enabled tests passed. The final race-enabled native run passed all ten
top-level tests (eight native plus two unit), including seven constraint
subtests, in 7.227s. Running the integration tag without `MONGODB_URI` exited 1
with the required-infrastructure diagnostic, as intended. Temporary verification
modfiles were removed. Only this adapter directory was edited; parent module and
protocol files were left to their owners.

## Candidate queries and projection listing

Concrete readers implement `eventstore.CandidateReader`. Latest snapshot heads
store separate `state_terms` generated by `lib/query`; original
snapshot JSON bytes remain unchanged. A term is the JSON encoding of an exact
field-segment array and a canonical scalar. Array traversal contributes no index
segment, so arbitrary nested arrays, numeric object keys and conjunctions across
different array elements behave like shared `Matches`. Types and explicit null
remain distinct; absent paths produce no term. Dots delimit query paths, so a
literal dotted field name does not alias a nested object field. The shared owner
validates queries and still performs final matching after replay.

Heads update atomically only for a higher snapshot number. A compound multikey
index covers scope and terms for matching clean heads. A second server query
compares scoped root counters against latest `lastEvent+1`, including roots
without snapshots; all dirty roots enter replay irrespective of snapshot match.
Both reads use one transaction snapshot and return only roots, with no implicit
100-row limit. The frontier comparison scans scoped counter metadata; this is
not a claim of constant-time dirty discovery or production capacity. Clean
nonmatches are neither transferred as snapshot bodies nor replayed.

`Projections(ctx, network, service, domain, afterID, limit)` supplies the optional
view reader seam: nondeleted rows within source scope, ascending ID, strictly
after the cursor, with a positive explicit limit. Scope/deleted/ID has its own
index. The view owner applies body predicates, authorization and user page caps.

Candidate follow-up verification: full race-enabled native Mongo suite passed
in 10.212s, including the shared candidate/replay/listing matrix, metadata
rollback and a native multikey IXSCAN plan check. The same matrix passed with
PostgreSQL GIN indexes. Original constraints, bytes, retries, uncertainty,
concurrency, lifecycle and cursor tests remain passing. The fault suite used
isolated disposable port57019, avoiding the parent's concurrent port55449 tests.

Single-attempt View follow-up: a native transient getMore failure after128
already-delivered rows returns the error without repeating the View callback or
any row. A separate consumer callback-error case also returns without replay.
The full native race suite passes7.257s; unit/race tests and vet pass. Update's
transient retry, bounded exhaustion and uncertain-commit cases remain passing.

`PendingRoots` selects dirty roots with no snapshot first, then oldest committed
snapshot-block service, then canonical root key before the positive limit.
Snapshot heads persist `lastSnapshotBlock` (initially -1). Snapshot replacement
retains previous credit; the transaction records root/number writes, and
`PutBlock` credits only matching current heads via `$max`, in the same transaction.
The recorded set clears after a block. Rollback restores priority with the block
and snapshots; standalone or historical snapshot writes do not receive false
completed-block credit. The shared root cursor preserves the supplied frontier
order; candidate queries retain their own canonical root ordering.

Native shared frontier tests prove maxRoots1 progress despite a continuously
dirty first root, unsnapshotted and previously snapshotted waiting roots,
reopening, late block rollback, and exact current-head credit across multiple
blocks in a transaction. The policy assumes a finite waiting set: unsnapshotted
priority does not promise older-root service under unlimited new-root arrivals.
The frontier remains a metadata lookup/filter/sort, not a constant-time ready queue.


## Large canonical payloads

One body codec stores canonical JSON bytes inline up to 4 MiB and otherwise in
4 MiB BSON binary chunks with a manifest. Chunk writes, manifest writes and reads
use the caller's existing transaction/session. Reads validate manifest shape,
chunk order/count, exact lengths and SHA-256 before decoding the original bytes;
corrupt payloads fail rather than returning partial data. Projection replacement
and publication deletion remove their old chunks atomically. Aborted writes leave
no committed chunks. Immutable event/snapshot/block chunks remain with their
records; closing a client does not delete persisted data.

Snapshot query metadata is limited to 10,000 terms and a conservative 1 MiB term
budget. Larger metadata marks the head `unindexed`; candidate queries include
these clean roots along with matching indexed heads and every dirty root. An
oversized predicate term set likewise broadens selection to scoped clean roots.
Shared final matching removes false positives. Neither path truncates original
state or introduces candidate false negatives. Fair frontier credit is preserved.

This removes the per-document BSON limit for bodies, not transaction cache/time
or process-memory limits: the shared protocol still materializes complete wire
objects. See MongoDB's [BSON document limit](https://www.mongodb.com/docs/manual/reference/limits/)
and [transaction considerations](https://www.mongodb.com/docs/manual/core/transactions-production-consideration/).
The generic publication size bound remains independently enforced by its owner.

Chunk verification: race-enabled native tests passed in 62.602s, covering one
100-event Append with 180 KiB individual payloads and a tail/block above 16 MiB,
a 17 MiB state with conservative query matching, replacement/deletion cleanup,
rollback, and nine corruption cases. Raw adapter size tests intentionally exercise
larger stored bodies independently of generic publication admission. Existing
native contracts, candidate queries, cursor/retry behavior and both shared fair
frontier tests passed in a separate race-enabled run (10.205s). Ordinary race tests
and vet passed. Fault injection used a task-owned replica set, preserving parent
fixtures. These are local native proofs, not production capacity claims.
