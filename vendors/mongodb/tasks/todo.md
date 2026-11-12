# Indexed query candidates

## Plan refinement

- **Objective:** Implement optional Candidates on both concrete readers, returning all dirty roots plus matching clean latest snapshots without truncation or false negatives.
- **System fit:** Parent owns CandidateReader, query validation, final Matches and replay; adapters own indexed metadata and native filtering in the existing repeatable transaction. PG owner handed off DDL after completing retry policy; preserve that work. Original snapshot bytes and local bypass remain unchanged.
- **Reuse and simplicity:** Reuse canonical protocol JSON, snapshot inserts, root counters and native indexes. Store queryable state separately and maintain a latest-snapshot head atomically; never scan/decode every event or replay clean nonmatches. No Mongoose casts or migration scaffolding for undeployed systems.
- **Evidence and unknowns:** Current query scans all roots; current adapters store snapshot JSON as opaque bytes. Mongo/SQL predicate traversal must preserve strict scalar types and arbitrary arrays; conservatively admit unsupported path shapes rather than omit possible matches. Exact final filtering remains shared Matches.
- **Verification:** Native candidate tests cover more than100 snapshots, conjunction across fields/elements, numeric/string/null/missing distinctions, dirty and unsnapshotted reducer-derived state, latest snapshot ordering, byte preservation and unbounded root-only output; rerun both adapter suites and static/race checks using disposable localhost infrastructure.
- **Resource budget:** One focused implementation per adapter, reuse existing fixtures and parent shared matrix. No root module edits, remote writes or unrelated retry changes. Replan if database array/path semantics cannot guarantee sound candidates.

- [x] Implement Mongo indexed heads and candidate filtering.
- [x] Implement PostgreSQL indexed heads and candidate filtering, preserving retry work.
- [x] Verify native cases and report integrated retest readiness.

## Review

Both adapters now implement CandidateReader and the optional view Projections
seam. Shared typed-leaf terms handle nested arrays without relying on divergent
database dot/jsonpath semantics; the parent explicitly accepted this refinement.
Original snapshots remain opaque byte images. Latest-head updates, counters,
projection tombstones and indexes are transactional. The frontier is a scoped
metadata comparison, not a constant-time dirty index.

Full native race suites pass: PostgreSQL20.695s, Mongo10.212s. Native index plans
confirm GIN/multikey use; shared contracts cover150 clean matches, all requested
strict predicate/frontier cases, metadata rollback, real reducer replay and
projection listing. Existing nil-state snapshots revealed and gated an indexing
regression; the helper now follows their JSON-null wire encoding. Parent has
integrated retest readiness and owns Query callback-reset work. No module or
shared eventstore files changed. Only task-owned test infrastructure is cleaned.

# Single-attempt read callbacks

## Plan refinement

- **Objective:** Never replay a Mongo View callback or an owned reader stream after a known abort; preserve bounded Update retries.
- **System fit:** Align Mongo with PostgreSQL View semantics so arbitrary consumer callbacks cannot duplicate external effects. Parent query result resets remain defensive.
- **Reuse and simplicity:** Pass an explicit attempt budget to the existing transaction runner: one for View, configured budget for Update. Preserve session, snapshot and cleanup ownership.
- **Evidence and unknowns:** View currently shares Update's retry loop. A transient getMore failure after the first cursor batch demonstrates observable duplicate callbacks without this fix.
- **Verification:** Native callback-error and transient getMore tests assert exactly one View callback and no repeated event callbacks. Run Mongo native race suite, ordinary checks and vet; existing Update retry/exhaustion checks must still pass.
- **Resource budget:** One narrow runtime edit and native regression on isolated disposable Mongo; no PostgreSQL, protocol, parent module or consumer edits.

- [x] Restrict View to one attempt and update retry documentation.
- [x] Verify callback-error/getMore failures and unchanged Update retries.

## Single-attempt read review

View passes one attempt to the shared transaction runner; Update retains its
configured retry budget. A native callback-error case delivers one row once.
A real getMore failpoint reports TransientTransactionError after128 delivered
rows, with exactly one View callback and no repeated rows. Full native race
suite passes7.257s; ordinary race tests and vet pass. Existing Update transient
retry, budget exhaustion and unknown-commit checks pass unchanged. Documentation
and the local lesson now distinguish owned read callbacks from retry-safe writes.

# Transactional payload chunks beyond BSON document size

## Plan refinement

- **Objective:** Store/recover large canonical event/snapshot/block bodies without the16MiB BSON document ceiling truncating or permanently blocking a one-root tail; preserve protocol and fair frontier metadata.
- **System fit:** Adapter-owned inline/chunked body codec serves all persisted body readers/writers in their existing session. Parent retains serialization/hash/replay. Projection replacement and publication deletion retire old chunks atomically. No off-session GridFS or storage protocol edits.
- **Reuse and simplicity:** Reuse one JSON wire image, existing metadata uniqueness, transaction retries and cursors. Small bodies stay inline; larger bodies use4MiB chunks with manifest ID/count/length/SHA256. Query terms above a bounded metadata budget use an explicit unindexed head included conservatively in candidates; full original state remains stored.
- **Evidence and unknowns:** Current InsertOne stores full Block JSON including base64 tails and snapshot copies, which can exceed BSON16MiB even with maxRoots1 and tiny reduced state. Shared terms can independently exceed a document/index bound. Fairness owner handed off lastSnapshotBlock and snapshotWrites credit updates; preserve these. Database transaction cache/time limits and process memory remain actual operational bounds, not an unbounded capacity promise.
- **Verification:** Native >16MiB small-state event tail/block roundtrip with digest verification, >16MiB state/candidate completeness, corruption length/order/hash/missing-part rejection, rollback leaves no chunks, deletion and replacement retire only owned chunks. Recheck candidate/fairness/publication/projection/native regressions and race/vet.
- **Resource budget:** Mongo subtree only plus its local plan/docs; no PG/module/shared protocol changes, remote resources or migrations. One chunk codec and bounded index metadata fallback. Use existing disposable localhost Mongo with unique test databases; preserve parent fixture and concurrent-owner source.

- [x] Implement atomic chunk body codec and on-session readers/cleanup.
- [x] Bound auxiliary query metadata without candidate false negatives.
- [x] Verify large payload/failure cases and existing native contracts.


## Chunk review

One transactional codec now handles all stored bodies, with atomic mutable-record
cleanup and verified manifests. Oversized query metadata conservatively broadens
candidates; original bytes and fair frontier credit remain intact. Native large
payload/corruption/cleanup race tests passed62.602s. Existing Mongo contracts,
candidates, listing, retry/stream and fairness tests passed10.205s on an isolated
replica set. Ordinary race tests, vet and formatting passed. Parent fixtures,
module and shared protocol were not changed. Parent may run the consolidated
suite against this stable handoff; no further Mongo source edits are planned.

## Plan refinement

- **Objective:** Give known-abort Mongo contention a bounded ten-attempt default with jitter while preserving transaction and streaming safety.
- **System fit:** Only Mongo Update retry scheduling changes; parent owns Dia concurrent-worker cleanup and end-to-end gates.
- **Reuse and simplicity:** Keep current retry/uncertainty classification and callback loop; add one delay helper and cancellation-aware wait.
- **Evidence and unknowns:** Initial native log shows five transient known aborts exhausting50ms cumulative linear wait, not missing labels or uncertain commit. No contention-success guarantee follows from a larger bounded budget.
- **Verification:** Delay ceiling/cap/cancellation unit checks; native default budget, explicit budget exhaustion, committed unknown acknowledgment without replay, single-attempt View and final-attempt no-wait checks.
- **Resource budget:** Mongo adapter/tests/docs only, installed Go1.27.4 and authorized unique localhost Mongo databases; no huge-body suites, module edits or remote operations.

- [x] Implement bounded retry policy and regression fixtures.
- [x] Run scoped unit/native race and vet, then release to parent.

Contention review: installed Go1.27.4, GOCACHE=/tmp/blossm-go-dia-cache, GOWORK=off, readonly modules. Ordinary adapter race PASS1.325s; scoped native race PASS3.564s on localhost55449 (default retry budget, explicit budget, unknown committed acknowledgment, View stream, cancellation/final-attempt tests); vet PASS. All native databases were unique and cleaned by the existing fixture. No large chunk suites, remote calls or module edits. Parent owns separate behavior commit and real Dia concurrent/full-native revalidation; this bounded policy does not promise unlimited contention recovery.
