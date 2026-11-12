# Custodial wallet integration review

## Plan refinement

- **Objective:** Verify the complete authenticated custodial journey and native eventstore journal on PostgreSQL and MongoDB; fix original command-key validation and evidenced integration defects within wallet only.
- **System fit:** Dia AuthenticateAccess owns identity; wallet loads owned encrypted custody, EVM signs locally, Journal atomically commits signed bytes and nonce frontier, Submit/Reconcile persist observation evidence. Parent owns cmd/dia wiring, which is read-only here. No live blockchain or cloud operations.
- **Reuse and simplicity:** Reuse evm AES-GCM/Engine and actual eventstore.Store/native adapters. Use fake EVM RPC only at the external blockchain boundary; native fixtures prove transaction rollback/concurrency/restart rather than mocking journal behavior.
- **Evidence and unknowns:** Parent-created wallet and command wiring inspected. Full EVM uint64 persisted values use decimal strings to satisfy canonical safe53 JSON. SDK download authorization is pending; tests can be written but must not be claimed executed until dependencies are available.
- **Verification:** Empty/whitespace/overlong original request key, authenticated owner boundaries, encrypted custody persistence, high nonce serialization, atomic collision rollback, duplicate prepare/restart/uncertain send, actual signature recovery, bounded receipt reconciliation and durable observations against each native store. Read-only review of runtime wallet transport config.
- **Resource budget:** One wallet owner coordinated with parent; no cmd or module edits, no dependencies fetched, no cloud calls. Use existing local native database fixtures and bounded race checks after dependency authorization.

- [x] Review and fix wallet boundaries/journal integrity.
- [x] Add native PostgreSQL/Mongo wallet journey and atomic recovery tests.
- [x] Complete SDK and native verification; report live-chain/cloud exclusion.

Review: gofmt parses wallet files; scoped plan checker passes. Native tests and SDK-dependent unit tests remain uncompiled pending dependency installation approval. cmd/dia/wallet.go was inspected read-only; no cmd/eventstore/module files changed. Authenticated owner checks, request-key prehash validation, root bindings and nonce rollback have fixtures but not yet executed evidence.

## Plan refinement

- **Objective:** Require account-backed custody, preserve confirmed receipt evidence across submission retries, and erase best-effort private scalar backing storage.
- **System fit:** Dia authenticates current account/session; wallet owns custody admission and observation journal; EVM owns transient key cleanup. Parent owns eventstore/framework fixes.
- **Reuse and simplicity:** One shared owner guard, observation source distinction with conditional append, and one private scalar cleanup helper; reuse native wallet fixtures.
- **Evidence and unknowns:** Read-only review traced anonymous login authority loss, submit evidence regression, and big.Int zero truncation. SDK dependencies remain unavailable and no downloads authorized.
- **Verification:** Add anonymous operation rejection, account-backed native journey, confirmed submit retry persistence, and backing-word erasure tests; run offline tests if dependencies exist and report failures honestly.
- **Resource budget:** Limit changes to wallet and evm; no module changes, cloud calls or dependency downloads. One bounded offline test attempt per package after formatting.

- [x] Implement admission, evidence and key cleanup fixes.
- [x] Add regressions and report offline verification (SDK-dependent execution blocked).

Refinement update: wallet owns reciprocal account/principal binding after
AuthenticateAccess; there is no duplicate Dia helper. Fact input preparation also
needs an ErrInvalid sentinel for canonical/validation failures; parent owns HTTP
mapping. Test invalid query classification without reclassifying read failures.

Review update: implementation and native regression fixtures complete. Wallet and
EVM test setup fails on absent go-ethereum checksums with GOPROXY=off/GOSUMDB=off;
no download attempted. gofmt succeeds. SDK-dependent unit and native database tests
remain uncompiled and must run after dependency approval. Avicenna confirms the
wallet-local reciprocal policy; no AuthenticateAccount method or core login edits.

Fact verification: complete `go test -race ./fact` passed (1.275s) using local
httptest sockets with downloads disabled; `go vet ./fact` passed. Targeted invalid
query regression also passed. Native wallet setup explicitly attempted with
integration tag and failed only at the unavailable SDK dependency gate before
compilation or database operations.

## Completed SDK verification, 2026-11-12

Parent completed authorized dependency installation; verification used installed
Go 1.27.4 directly with GOTOOLCHAIN=local, GOPROXY=off, GOSUMDB=off and
-mod=readonly. Complete race unit suites passed: evm 1.862s, wallet 1.735s,
cryptography 1.297s. Scoped vet passed. Wallet integration/race passed against
disposable digest-pinned PostgreSQL 17.6 and MongoDB 8.0.34 replica set (85.290s).
No code fixes, module writes, live blockchain calls, cloud operations or deployments
were required. Earlier pending-compilation records are superseded by this result.
