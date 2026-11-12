# PostgreSQL storage

`Open(ctx, postgresURL, namespace) (*Store, error)` initializes one isolated
schema and returns an `eventstore.Backend`. The store owns its pgx pool; close it
after callbacks finish. Namespace must be explicit, valid UTF-8, free of NUL,
and at most 63 bytes. Names are quoted with `pgx.Identifier`; all values use binds.

Dependency: `github.com/jackc/pgx/v5/pgxpool@v5.12.0`. The parent module owns
`go.mod` and `go.sum`. Version 5.12.0 was verified against the official
[October 30 changelog](https://github.com/jackc/pgx/blob/v5.12.0/CHANGELOG.md),
including its security fixes. The releases/latest redirect initially returned
5.11.0; check the actual changelog/tag when verifying the current supported pin.

Storage uses `json.Marshal` wire images in bytea, without SQL JSON conversion,
field casting, normalization or numeric decoding. Metadata columns own lookup,
ordering and uniqueness. Raw JSON fields from canonical parent inputs survive
readback; snapshots and blocks retain their binary fields, including null versus
empty slices, through JSON's lossless byte encoding inside bytea. The parent owns
validation, canonical encoding/hashing, append, replay, block building and queries.

Initialization is transactional and guarded by a per-schema advisory lock.
Readers and writers use repeatable-read transactions. `LockRoots` sorts and
locks root rows; callers must acquire all roots before reading or mutating them.
Repeated calls cannot acquire an earlier root after a later one. Callbacks are
serial and scoped to their transaction. Streams fetch 100 records at a time and
release each page before invoking callbacks, permitting nested operations.

Update retries only SQLSTATE 40001 and 40P01, at most ten attempts by default with
cancellation-aware exponential backoff. Each wait is independently jittered
between half and all of its ceiling, initially 10ms and capped at 250ms.
`OpenWithRetry(ctx, url, namespace, RetryConfig{MaxAttempts: 10,
BaseDelay: 10*time.Millisecond, MaxDelay: 250*time.Millisecond})` configures
these bounds. Zero fields select defaults, negative settings and a base delay
above the cap reject before connection creation. `MaxAttempts` includes the
initial attempt; setting it to one disables retries. Exhaustion returns the last
serialization/deadlock error. Larger contention workloads may require a larger
explicit attempt budget; no finite budget promises unlimited writer progress.
Callback code must be safe to repeat and must not
perform external effects. A transport failure during COMMIT wraps
`eventstore.ErrUncertain` and is never automatically replayed. Resolve that
outcome through the parent's identical-request-key retry. Views never replay
callbacks. Database cleanup preserves the primary error.

Events enforce unique key, hash and root/number. Snapshot and block identifiers
are unique. Missing counts return zero; absent records return `ErrNotFound`.
Projections accept only newer versions from the same source root, retain
tombstones and reject attempts to rebind the ID to another root. Pending notices
sort by attempts then ID, with a positive explicit limit. Local-mode publication
bypass belongs to the parent; this adapter never publishes or calls cloud APIs.

From the module root:

```sh
go test ./vendors/postgres
go vet ./vendors/postgres
POSTGRES_URL='postgres://...@127.0.0.1:.../...?sslmode=disable' \
  go test -race -tags integration ./vendors/postgres -count=1 -v
```

The integration tag requires `POSTGRES_URL` and fails if missing. Use a disposable
localhost database with schema creation rights. Tests create/drop unique schemas
and cover initialization races, UTF-8 identifier boundaries, namespace isolation,
unique constraints, atomic rollback, opposite-order concurrent roots, stable
read/write snapshots, byte preservation, nested callbacks, ordered pending work,
projection versions and source identity, cancellation while waiting for a lock,
10,000-event batches and rollback after that batch, bounded retries, and a lost
acknowledgement after a real server commit. The acknowledgement failure is injected
at the pgx transaction boundary; it is not a real proxy/network failover test.
No throughput, cloud-provider or production failover guarantee follows.

Verification on 2026-11-12: Go 1.27.1, pgx 5.12.0, disposable PostgreSQL on
127.0.0.1:55439. The complete race-enabled tagged suite passed all 15 tests
(24.464 seconds); ordinary tests and go vet passed. The final randomized
63-byte namespace fixture passed its targeted race-enabled rerun. An explicit
empty-POSTGRES_URL control failed with the required configuration error, with
no skipped tests. Dependency resolution initially used a temporary `-modfile`
while the parent updated its module. No module files were edited by this task.

Retry follow-up: the unchanged shared eight-writer
`TestNativeContract/postgres/concurrent-counts` passed 20 consecutive
race-enabled runs with the default jittered policy. Explicit exhaustion,
disabled retries, cancellation, delay overflow/capping and unchanged uncertain
commit classification have separate regression tests. See `tasks/todo.md`.

## Candidate queries and projection listing

Concrete readers implement `eventstore.CandidateReader` with indexed latest
snapshot heads and a counter/frontier query, returning root identities only.
`lib/query` owns typed scalar terms shared with MongoDB.
Each term encodes exact field segments and a canonical scalar, flattening arrays
at any depth without converting numeric strings or treating missing fields as
null. Dot-separated query paths never alias literal dotted keys. A GIN index
supports term conjunction; scope indexes constrain roots and heads. Original
snapshot bytea is unchanged. Head replacement is atomic with snapshot insertion
and conditional on a greater snapshot number.

The query unions matching clean heads with every scoped root whose count exceeds
its latest lastEvent+1 (or zero without a snapshot). There is no implicit row
limit. Only candidates enter parent replay/final Matches; arbitrary
reducer-derived values remain discoverable on dirty/unsnapshotted roots. The
frontier comparison still reads scoped counter metadata; no constant-time or
production throughput claim follows. Snapshot state nil is indexed as the same
JSON null emitted by its existing wire serializer.

`Projections(ctx, network, service, domain, afterID, limit)` returns nondeleted
source-scoped projections in ID order strictly after the cursor, with a positive
explicit limit. ID uses C collation to align deterministic ordering with Mongo.
A partial scoped index covers live projection listing. User pagination caps,
body predicates and access policy remain with the view consumer.

Candidate follow-up verification: full race-enabled native PostgreSQL suite
passed in20.695s, preserving the retry owner's configuration and regression
checks. Shared candidate fixtures cover150 clean matches, strict scalars,
null/missing, deeply nested arrays, numeric keys, split conjunctions, latest-head
rollback, dirty/unsnapshotted derived-state replay and projection pagination.
A native EXPLAIN with sequential scans disabled proves the term predicate can
use heads_terms GIN; it is an index capability check, not a production cost claim.
Mongo runs the same semantic fixtures. No root module files were changed.

`PendingRoots` selects dirty roots with no snapshot first, then oldest committed
snapshot-block service, then canonical root key, before applying the positive
limit. `snapshot_heads.last_snapshot_block` starts at -1. Each transaction tracks
its snapshot root/number writes; `PutBlock` credits only those still-current heads
with that block number, atomically with the block and snapshot data. Credits clear
after each block, cannot move backwards, and disappear on rollback. Standalone
snapshot writes retain prior credit and do not pretend that a block completed.
The native shared frontier contract proves progress with maxRoots1 while the
lexically first root stays dirty, before/after all roots have snapshots, after
reopening, and through an aborted block. Historical snapshot inserts and multiple
blocks within one transaction cannot incorrectly refresh another head's credit.

This fairness policy applies to a finite waiting set; prioritizing unsnapshotted
roots does not promise service for older roots under an unlimited influx of new
roots. Selection still compares metadata and sorts candidates; it is not a
constant-time ready queue or a throughput guarantee. This fresh-system schema
change requires fresh disposable test schemas, not legacy migration scaffolding.
