# Generic source projection play and replay

## Plan refinement

- **Objective:** Implement projection/ only: bounded replay scheduling, synchronous current-aggregate play, typed view mutations, explicit recursive dependencies with cycle/work budgets, stale rejection and muted best-effort pushes after durable commit.
- **System fit:** Reuse eventstore.Aggregate for replay/hash checks and view.Batch for scoped atomic mutations; consumer interfaces live here. Parent/view owner adds explicit Tombstone to close absent-delete ordering without changing ordinary Delete. Parent owns central capability inventory and transport wiring; this subtree records the exact retained-source map.
- **Reuse and simplicity:** Use standard context and serial callbacks, native version guards and one view batch. Dependencies retain independent source roots/versions; do not merge unrelated source clocks or add leases/fencing/action machinery. Replay buffers only a bounded page before external enqueue calls.
- **Evidence and unknowns:** Inspected pinned generic projection play/replay and current CLI deploy/src plus condition/push fixtures. They load current aggregates, suppress only pushes on muted/manual replay, await recursion, preserve conditions/array filters and treat no-match as no push. Their merged multi-source version semantics are not a safe Go contract; explicit dependency mutations stay independently versioned. Tombstone Body support is coordinated with the view owner.
- **Verification:** Unit tests cover source/root/revision validation, actual reference bird mapping, muting/manual action semantics, conditional/no-op pushes, dependency order/optional missing/cycles/budgets, cancellation, producer partial progress and post-commit push errors. Native tests exercise two concurrent out-of-order plays, absent tombstone before older create, batch failure and actual view ownership on disposable Mongo/PostgreSQL. Run race tests, vet and missing-infrastructure fail-closed controls.
- **Resource budget:** New subtree only, reuse parent databases and dependencies without storage/module changes. One bounded graph preparation then atomic view batch, no external effects in transaction callbacks. Local mapping informs parent inventory instead of modifying its files concurrently.

- [x] Implement consumer interfaces, bounded replay scheduler and play preparation/commit/push.
- [x] Add reference-semantic unit tests and real stale/tombstone/native regressions.
- [x] Complete capability mapping, verification evidence and parent handoff.

## Review

Implemented only projection/: consumer-owned interfaces, source-bound typed
mutation plans, budgeted recursive dependencies, bounded replay with confirmed
partial enqueue progress, and post-commit best-effort invalidations. View owner
implemented explicit Tombstone, reused here without bypassing scope checks.
Native race checks on BOTH Mongo/PostgreSQL pass2.613s, including concurrent
out-of-order plays and absent-tombstone-before-older-create, owner-failure batch
rollback, filtered conditional writes, readable committed data inside failed
push callbacks, and replay outside root-stream transactions. Unit race tests and
vet pass. Missing both native URLs intentionally fails with no skips.

README maps retained source semantics and explicit composition gaps for parent
inventory updates. HTTP/RPC/queue wiring remains the transport owner. No shared
protocol, module or storage files were edited for this owner. An independent
review delegation was unavailable because the active agent limit was reached;
parent integrated unit checks also passed.

## Plan refinement

- **Objective:** Give every logical replay an explicit run identity that stays stable across retry/pages and differs for a fresh run, from HTTP scheduler through provider deduplication.
- **System fit:** Projection owns typed RunID validation and Request identity; HTTP exposes it in input/progress; delivery hashes it inside replay request; runtime composes producers without static or random fallback. Parent owns serviceapp/integration callsites, Einstein owns new delivery native/rejection checks.
- **Reuse and simplicity:** Keep Producer.Enqueue(Request), add Request.RunID and explicit Scheduler argument, remove duplicate Envelope/static wrapper identity and random runtime producer. Use existing envelope hash and provider acceptance rules unchanged.
- **Evidence and unknowns:** Static configured RunID deduplicates later manual runs; per-enqueue random fallback cannot retry uncertain scheduling identically. New explicit IDs avoid both; provider deduplication remains provider-bounded, not exactly-once execution.
- **Verification:** Same run/root retries yield identical envelope/provider IDs; new run/root yields distinct IDs; pages and partial progress echo RunID; reject absent/mismatched run IDs; scoped unit/race/vet and projection native bothDB without failpoints.
- **Resource budget:** Coordinate projection/transport/delivery/runtimequeue files before edits; no module, eventstore, serviceapp, signer or rejection-seam changes. No cloud calls/deployments; use SDK fakes and approved disposable DBs.

- [x] Implement coherent typed replay run contract and remove fallbacks.
- [x] Update owning callers/tests/docs and pass scoped/native checks.

Replay identity verification: Go1.27.4 readonly module, full scoped race passed
projection 1.579s, transport 4.662s, delivery 5.572s, runtimequeue 4.315s; scoped
vet passed. Projection integration/race passed both disposable native databases
(4.583s), no failpoints. Tests establish same logical run retries/pages preserve
RunID and provider task/FIFO identity, fresh runs differ, missing run IDs reject
before provider calls, and HTTP acknowledgments bind returned progress to RunID.
Parent owns serviceapp/integration/CLI callsites; delivery native/rejection docs
remain Einstein-owned. Runtime worker behavior/logging was untouched.
