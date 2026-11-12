# Retained external adapters and inventory reconciliation

## Plan refinement

- **Objective:** Account explicitly for Dwolla, Redis and Chainpoint beyond EVM/comms/cloud; no Slack package exists in the154package source. Implement bounded adapters where source/API contracts are evidenced, and reconcile inventory without false completeness.
- **System fit:** Source packages/vendors/{dwolla,redis,chainpoint} at372d1 map to integrations/{dwolla,redis,chainpoint}. Preserve operation capability, idempotency headers, context cancellation and errors; local memory cache is explicit rather than silently replacing configured remote state. No provider requests with real credentials.
- **Reuse and simplicity:** Standard-library HTTP/JSON/multipart for REST adapters; inspect actual Redis client availability before deciding on a dependency. Avoid a general SDK abstraction. Keep one owning transport error/size/redirect rule for REST adapters. Do not edit root go.mod.
- **Evidence and unknowns:** Read complete reference method surfaces and primary provider docs. Chainpoint availability and proof network compatibility remain unverified; expose configured endpoint transport only if protocol confirmed. Reference helpers replaced by standard-library operations require explicit accounting, not invented runtime modules.
- **Verification:** Local HTTP/transport fixtures cover actual request bytes, tokens, idempotency, multipart, error propagation and no secret disclosure. Native Redis checks if available; race tests for local cache. Every status distinguishes implemented unit-verified behavior from missing integration/live evidence.
- **Resource budget:** Bounded adapters for three existing external packages; avoid extra payments operations, Slack, JB/shop/Relayr, retries of uncertain external writes or new general coordination. Parent owns application wiring and complete port reconciliation.

- [x] Trace external operation surfaces and API contracts.
- [x] Implement retained adapters and local boundary tests.
- [x] Reconcile inventory and standard-library replacements with precise evidence.

## Review

Implemented Dwolla retained17operations, configured-gateway Chainpoint submission/retrieval, Redis RESP hash/expiry and explicit synchronized local cache. Scoped race/vet and loopback fixtures pass; native Redis7.4.7 authenticated/database-selection/binary/hash/expiry check passes and disposable container stopped. Automatic Chainpoint discovery remains explicit; no live external providers or full-port completion claimed. All154inventory entries now map actual Go source/test files or explicit stdlib/tooling replacements/gaps.
