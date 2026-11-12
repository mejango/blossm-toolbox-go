# Custodial EVM

This package owns encrypted wallet identity, local EIP-1559 signing, durable
transaction preparation, exact-byte retries, bounded receipt reconciliation,
and the retained Juicebox V6 ERC-2771 forward-request signer. Relayr payments,
Juicebox product workflows and shop workflows are deferred.

## Dependency and verification boundary

Parent module: `github.com/mejango/blossm-toolbox-go`. Add the sole direct dependency
`github.com/ethereum/go-ethereum v1.17.10` in the parent module, then resolve its
transitive modules with `go mod tidy`. The EVM work does not edit `go.mod`.
The [official stable release](https://github.com/ethereum/go-ethereum/releases/tag/v1.17.10)
and [tagged module declaration](https://github.com/ethereum/go-ethereum/blob/v1.17.10/go.mod)
were checked on 2026-11-12. The SDK requires Go 1.25 or newer; the workspace has
Go 1.27.4 for verification. Cryptography, transaction encoding, EIP-712, ABI and RPC codecs all
come from that SDK.

Run from the parent module after dependency installation:

```sh
go test -race ./evm
go vet ./evm
```

Fixtures use temporary files, local cryptography and in-process RPC. They do
not connect to a blockchain or cloud service. Tests cover uncertain sends,
restart/idempotency, nonce conflicts, concurrent prepares, receipt budgets,
canonical confirmations, reverts, custody tampering, independent EIP-712
hashing, signature recovery, ABI selectors/round trips and provenance hashes.
These checks do not prove live deployments, provider reliability or finality.

## Consumer contract

1. Authorize the principal's wallet access and the exact maximum spend before
   invoking this package. The wallet owner/ID must come from trusted application
   storage, not directly from unauthenticated request fields.
2. Use `NewAESGCMCipher(key)` with a separately managed 32-byte key, or provide
   `Cipher.Seal/Open` using authenticated encryption and bind the
   supplied AAD. It includes a version marker, wallet ID, owner and address.
   Encryption keys must remain separate from wallet/transaction storage. Return
   newly allocated buffers; never log plaintext or key-bearing errors.
3. `CreateWallet` returns an encrypted wallet record. Persist it before using it.
   It generates keys with go-ethereum and never sends them to RPC. Key buffers
   are cleared best-effort; Go does not guarantee complete heap erasure.
4. Configure `Engine` with an `ethclient.Client`, expected chain ID, `Cipher`
   and durable `Repository`. Treat configuration and request values as immutable
   during calls. Set deadlines on prepare/submit contexts to bound transport.
5. Call `Prepare` with a stable application transaction ID, destination, integer
   wei value, calldata, gas limit and explicit fee/tip caps. The caller approves
   maximum cost `value + gas * feeCap`. Fee selection and initial gas quotation belong
   to the consumer's approval policy. `Prepare` preflights the exact request
   with RPC gas estimation and rejects simulation errors or estimates exceeding
   the approved gas limit before reserving a nonce. State may change after
   preflight, so simulation does not guarantee execution success. No automatic fee bump or replacement
   is performed. The package rejects zero destinations and does not expose
   contract-creation transactions.
6. `Prepare` checks endpoint chain identity, decrypts/signs locally and commits
   the hash plus signed bytes before returning. A repository error prevents
   submission. An ambiguous repository commit can be recovered by repeating
   the same ID and request. Changing any request field for an existing ID fails.
7. `Submit(id)` loads and verifies only stored signed bytes. On a send error,
   retain `unknown`: the node may have accepted the transaction. Repeat this ID
   to send the identical hash, or call `Reconcile`; never create a new transaction
   ID merely because submission timed out. Recovery requires no private key.
8. `Reconcile` uses a total timeout, at most 100 attempts, a polling interval
   and explicit confirmation count. Only a receipt matching the stored hash and
   canonical block with verified parent-hash ancestry can produce
   `succeeded`/`failed`. Confirmation evidence is capped at the requested threshold
   to bound the ancestry walk; its RPC work shares the total timeout. A confirmed status-zero
   receipt means failed execution (gas was still spent). Missing receipts,
   replaced/dropped transactions, noncanonical receipts and observation errors
   remain unknown. A canonical receipt below the threshold remains pending.
   Confirmations are a caller risk policy, not irreversible finality. Reconcile
   again if reorg evidence arrives. Observations contain block evidence and no
   replay-dependent current timestamps; application reducers should retain
   supplied evidence rather than inventing timestamps during replay.

`Submit` does not update application events. Consumers persist the returned
observation in their own event stream. The journal is the durable authority for
which exact signed transaction may be sent, independent of event delivery.
A crash after preparation leaves a recoverable ID even if the application never
received the return value. Consumers must retain their command ID to retry it.

## Repository and nonce policy

`Repository.Get`, `NextNonce` and `Put` are the only persistence boundary.
`Put` must be atomic and durable, with unique ID and unique
`(chainId, from, nonce)` constraints. Exact duplicates succeed; changed ID
bindings return `ErrConflict`; nonce collisions return `ErrNonceConflict`.
`NextNonce` is advisory: return the maximum of the RPC pending nonce and one
above every stored nonce for that account and chain. `Prepare` resolves races
through `Put`, with eight bounded retries. Repository errors must not expose
custody material. Custom implementations must pass equivalent concurrency and
restart tests before use.

Nonces are never released by send errors, missing receipts or failed execution.
All writers for an account must share one repository. Use dedicated accounts;
external signers can consume or replace nonces outside this journal's authority.
An abandoned prepared transaction can leave a nonce gap and block later
transactions. Resume its stored bytes. Cancellation/replacement needs an
explicit application policy and is not silently invented here. Do not delete
journal rows to clear a gap. Preserve the nonce high-water mark when designing
future journal retention.

`FileRepository{Directory: path}` supplies durable local storage on Linux/macOS.
Provision this directory durably with service-only permissions first. It uses
an exclusive process-safe file lock, atomic rename, file fsync and directory
fsync. Reads complete an ambiguous prior directory sync and validate stored
signatures/hash bindings. Do not delete/replace the lock file, share this over
NFS, or use object-store mounts. The whole journal is scanned under the lock;
use a database implementation of the same interface for larger workloads.
Filesystem/IO calls themselves are not forcibly cancellable, but lock waiting
honors context cancellation. Signed bytes contain no private key, but are
broadcast authority: protect integrity and access to the journal.

## Retained V6 forwarder

`ForwarderAddress` accepts only the eight pinned V6 chains. There is no fallback
for Polygon or arbitrary chains. Direct EVM transactions can use any explicitly
configured positive chain ID independently of this forwarder table.

`SignForwardRequest` verifies the endpoint chain, selects a block, and reads
`eip712Domain()` and `nonces(wallet.Address)` using that block's hash. It accepts
exactly the four EIP-5267 fields name/version/chain/verifying-contract with no
extensions, binds the actual chain and verifying address, and signs with the
locally decrypted wallet. The supplied deadline is uint48 and must exceed the
selected block timestamp. The contract still enforces expiration at execution.

The returned `ForwardCall` has the forwarder destination, native value, execute
calldata, sender nonce, signature, digest and observation block hash. Pass its
`To`, `Value` and `Data` to `Engine.Prepare` with the authorized gas payer and
appropriate direct gas/fee limits. The request tuple excludes nonce while the
EIP-712 message includes it, exactly as the retained ABI requires. Concurrent
forward-request signatures do not reserve onchain forwarder nonces: serialize
forwarded operations per sender and reconcile execution before signing the
next. Contract replay protection rejects a consumed nonce. A signature alone
is not a durable submission; the actual direct transaction journal owns that.

Targets must trust the forwarder. These APIs do not claim target trust, current
runtime bytecode or live deployment identity based on an address table. The
actual domain and nonce reads are required even with pinned source evidence.

## Provenance

Addresses derive from `Bananapus/deploy-all-v6` revision
`c62b3ac81bfc5603b6771e8ad801eb2236022947`, artifact path
`deployments/<directory>/ERC2771Forwarder.json`:

| Chain | Directory | Artifact SHA256 |
| --- | --- | --- |
| 1 | ethereum | fe4b86b2945907e5e8e467c894e1dc5653a116a51a4c561bd536c2e5bc47d1c8 |
| 10 | optimism | 22b2afda37e1e21fd37ab00e12cc6512d824d083ca193dd30a207378e83e988d |
| 8453 | base | 2d445573481b9afa3555e6e5fa12dea53fad7a7283f5acb742c97274c0f352fd |
| 42161 | arbitrum | 5e466bce4fc88915038ec59c0136d9764aa627755d2fa14a995900ef51cb0f95 |
| 11155111 | sepolia | 4acf67e1b2fd496fcdfe0af8ad351e9f10449714617b26055cfa56f9a04b7991 |
| 84532 | base_sepolia | cec05b9232236b937fd5715372517c32f97f11cefdd6c685e52100aff7684b14 |
| 11155420 | optimism_sepolia | b26a655cb20d7c111568b84b19d5f3d551d20fe21754f2d11a1667a112482268 |
| 421614 | arbitrum_sepolia | cf7a8d6da3a7a5c79d5a460ca6c9895ac72081921c3d0b81ff6720aab80351fa |

All addresses are `0x3ba60b60933916a7c87d0860dcee62a0ce34e3e2`.
Sorted compact JSON `[chainId, lowercaseAddress]` pairs have SHA256
`2529ebbf4c7a7855d1d91e210fa75630ba451aa0185790d54cd17e2778703932`.

The full [retained ABI](forwarder.abi.json) is copied from the isolated reference
package, preserving all 17 entries and their parameter order. Its matching
reference is OpenZeppelin Contracts 5.4.0, artifact SHA256
`d67d9f8322423f8efb19c3447a928ec20d917837ed8809a64cefac6c374ce089`.
Sorted compact serialized entries joined with a newline hash to
`0bb81fff2d9988faf96902ab3910e3af3ca306a40c1baebb8d19a1f262a59f0b`.
The pinned V6 artifacts record OpenZeppelin 5.6.1 source hashes and expose the
same ABI. Their constructor name is Juicebox; the signer reads the actual
name/version rather than hardcoding them. Upstream MIT terms are retained in
[LICENSE.OpenZeppelin](LICENSE.OpenZeppelin).

The reference records were inspected and preserved; no live blockchain RPC or
cloud calls were made. Official SDK metadata reads do not establish deployed
contract compatibility. ABI and address projection integrity checks run offline.

Verification on 2026-11-12: full package race tests and vet passed with Go 1.27.4
and go-ethereum v1.17.10. Wallet native race tests also passed against local
PostgreSQL 17.6 and MongoDB 8.0.34, with blockchain RPC replaced by explicit
fixtures. These results verify local implementation behavior, not live deployments.
