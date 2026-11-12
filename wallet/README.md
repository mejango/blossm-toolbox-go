# Authenticated custodial wallets

Service uses Dia AuthenticateAccess for each operation and compares the complete
principal reference against the encrypted wallet owner before decrypting or
submitting. Create persists encrypted keys and returns only ID/address. Prepare
requires a nonempty original request key of at most 256 bytes before hashing its
owner/wallet namespace. The same request key must keep the same intent. Submit and
Reconcile accept the prepared transaction ID and reject another wallet or chain.
Session termination remains effective because access is authenticated each time.
All custody operations require local core account/principal references and persisted
reciprocal AccountState.Principal / PrincipalState.Subject bindings. Anonymous
sessions must complete account registration or login before creating custody.

Journal implements evm.Repository through eventstore.Store. Immutable signed
bytes/hash and the account/chain nonce frontier commit atomically in one native
database transaction. Optimistic root versions plus monotonic nonce reducers
resolve concurrent allocations. A losing collision cannot leave a transaction
row, count change or publication. Exact duplicate records succeed, changed
records conflict, and failed/unknown sends never release nonces. EVM nonces and
observation block/confirmation uint64 fields use decimal JSON strings, preserving
values beyond the eventstore's safe53 numeric range.

Submit records unknown provider outcomes without turning them into failures.
Once receipt reconciliation confirms success or failure, a submission retry returns
and preserves that evidence; only reconciliation can supersede it with chain evidence.
Signed bytes remain the recovery authority even if observation persistence
fails, for example because the caller's context expired. Retry/reconcile from
the durable prepared ID. No decryption is needed for an existing preparation or
resubmission. Identical repeated observations do not append new events. Receipt
replay uses supplied block evidence, not current timestamps in reducers.

Handler provides bounded JSON-only POST endpoints under /commands/wallet:
create, prepare, submit and reconcile. Authentication comes from Bearer tokens.
Idempotency-Key is required for create/prepare. EVM amounts, gas, block numbers,
confirmations and nonce representation use explicit decimal strings at the wire
boundary. Handler requests have a 30-second context budget; reconciliation also
has a smaller fixed polling budget. No decrypted keys are returned.

Verification commands, once the parent installs the approved go-ethereum SDK:

```sh
go test -race ./evm ./wallet
go vet ./evm ./wallet
go test -race -tags=integration -timeout=5m ./wallet
```

The native suite requires POSTGRES_URL and MONGODB_URI pointing to local loopback
fixtures. MongoDB must be a replica set. It creates isolated namespaces, exercises
actual Dia session/principal authentication and actual Ethereum signing, and
fakes only blockchain RPC. Tests cover owner/session rejection, request-key
validation, native rollback after the signed row is written, nonce collisions,
restart with unavailable decryption, uncertain sends, exact-byte retry, high
uint64 values, successful receipts, failed receipts and observation replay.
These tests do not establish live RPC, deployed chain or cloud behavior.

Verified on 2026-11-12 with Go 1.27.4 and go-ethereum v1.17.10: EVM,
wallet and cryptography unit race tests and scoped vet passed. Wallet native race
tests passed on disposable PostgreSQL 17.6 and MongoDB 8.0.34 replica-set fixtures
(85.290s), including account proof, reciprocal owner binding, atomic nonce rollback,
restart/unknown sends and confirmed-observation preservation across retries.
No live chain or cloud operations were used.
