# Retained custodial EVM

## Plan refinement

- **Objective:** Complete offline-verifiable custodial direct EVM transactions and retained V6 forward requests inside evm only. Relayr/JB/shop are deferred.
- **System fit:** Consumers authorize wallet ownership and supply authenticated encryption and durable repository; this package binds chain/sender, signs locally, persists exact bytes before sending, and returns bounded canonical receipt evidence. Parent owns module and application composition.
- **Reuse and simplicity:** Use go-ethereum crypto, transaction, ABI, RPC and EIP712 APIs; retain pinned reference ABI and V6 provenance. A narrow injected journal owns atomic nonce uniqueness and immutable idempotency, with a local durable file implementation for usable recovery.
- **Evidence and unknowns:** Isolated retained EVM and Dia wallet reviewed. Official go-ethereum v1.17.10 release and go.mod verified, requires Go1.25+. No deployed bytecode, live RPC or cloud verification authorized.
- **Verification:** Offline fixtures exercise real cryptography/ABI, encrypted custody, chain/domain/nonce mismatch, persisted-before-send, concurrent nonce selection, uncertain sends, retry/restart, bounded receipt checks, canonical confirmations and reverted transactions; race detector and vet.
- **Resource budget:** One EVM subtree owner, no module edits. Explicit caller fee limits and bounded attempts avoid hidden payment/replacement policy. Replan if SDK compatibility or repository contracts invalidate durable recovery.

- [x] Implement custody, transaction journal and direct flow.
- [x] Implement retained ABI and V6 typed signing.
- [x] Run offline checks and document consumer contracts/dependencies.

## Review refinement

Static review identified nonce blockage from transactions that cannot meet gas requirements, and confirmation counts combining unrelated histories. Prepare now performs exact-request RPC gas preflight before nonce persistence. Reconciliation proves parent-hash ancestry under its total timeout. Consumer-requested AES-256-GCM is concrete, versioned, AAD-bound and uses random nonces. The parent owns wallet authorization, eventstore journal and observation events; FileRepository is local single-host only.

SDK installation and full Go verification await explicit dependency-download authorization after automatic approval review rejected it under the no-cloud constraint. No indirect download attempt is permitted before that authorization. ABI/address hashes pass independently; gofmt parses all Go files. Full tests must run before completion claims.

Verification so far: standard-library AES-GCM tests pass with `go test -race cipher.go cipher_test.go`, and scoped `go vet` passes. Full package cryptography/RPC/journal tests await SDK installation. The ABI and address checksums match the retained source.

## Plan refinement

- **Objective:** Compile and verify EVM, wallet and cryptography now that public dependency/toolchain downloads are explicitly approved; fix evidenced defects within these subtrees.
- **System fit:** Parent owns module resolution and full runtime checks. EVM owns offline signing/receipt fixtures, wallet owns native account/custody journal recovery, cryptography owns external MPT/RLP byte vectors.
- **Reuse and simplicity:** Run existing comprehensive fixtures first, reuse established SDK APIs, change only failing owners instead of introducing wrappers or replacing imports.
- **Evidence and unknowns:** User authorized public downloads; parent is performing tidy with Go1.27.4. Compilation waits for parent deps-ready, so previous uncompiled claims are not yet verification.
- **Verification:** Scoped unit/race and vet for all three packages, followed by wallet integration/race against local PostgreSQL and MongoDB with fake blockchain RPC. Preserve crypto golden vectors and unknown-send/restart tests. No live chain, cloud or deployment operations.
- **Resource budget:** Single dependency owner, no module writes or duplicate tidy; bounded package tests and local fixture execution after readiness. Replan only for actual SDK/API or invariant failures.

- [x] Receive dependency readiness and compile scoped packages.
- [x] Pass unit/race/vet plus both native wallet journeys; no fixes needed.
- [x] Update evidence and notify parent; scoped integration has no unresolved failures.

## Completed SDK verification, 2026-11-12

Parent completed authorized dependency installation; verification used installed
Go 1.27.4 directly with GOTOOLCHAIN=local, GOPROXY=off, GOSUMDB=off and
-mod=readonly. Complete race unit suites passed: evm 1.862s, wallet 1.735s,
cryptography 1.297s. Scoped vet passed. Wallet integration/race passed against
disposable digest-pinned PostgreSQL 17.6 and MongoDB 8.0.34 replica set (85.290s).
No code fixes, module writes, live blockchain calls, cloud operations or deployments
were required. Earlier pending-compilation records are superseded by this result.
