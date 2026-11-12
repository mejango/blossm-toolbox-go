# Toolbox capability inventory for the Go port

**Current status: retained scope locally verified.** Parent confirms frozen-source
Go1.27.4 unit/race PASS1008 test/subtest outcomes each and native PASS1211,
zero failed/skipped tests, native exit0. Vet, formatting, module checks,
configuration, builds, security, Redis and both local container flows pass.
The [fifth bounded Fable review](reviews/fable-2026-11-12/claude-remediation-5.txt)
found no blocker in the remaining deltas; review observations and fresh gates
are closed. No runtime/config changes followed the frozen251-file manifest.

These are overlapping suite outcomes, not distinct-test counts or one-to-one
JavaScript assertion parity. No live deployment, provider or chain verification
is claimed. JB/shop and Relayr payments remain explicitly excluded; direct
custody and V6 remain included. The user selected one signing trust domain per
network, without per-service compromise isolation among shared-key signers.

See [final verification](VERIFICATION.md), [machine evidence](verification-results.json),
[review closure](reviews/fable-2026-11-12/closure.md) and [behavior coverage](behavior-coverage.md).
The current status above supersedes pending-gate notes in the historical
checkpoints below. Archived753/927 and earlier1005/1208 results are not final counts.

Reference: clean Toolbox checkout `tasks/worktrees/go-reference`, commit
`372d1c6a3f4699b8714faf15a81dff0b6d8a49a0`, inspected 2026-11-12.
Package source is the released `18a2e086e` source; the later commit updates the release guide.
This document inventories **154 public packages**, with one current Go owner per package and the original proposed owner retained in JSON.
All Go capabilities were **unimplemented initially**. The package table and machine JSON
now reconcile actual Go source/test files, explicit standard-library replacements and gaps.
Source mappings do not claim whole-module verification or live compatibility.

**No live evidence:** the initial inventory was read-only; later owned CLI/adapters have
scoped offline test evidence recorded below. Source inspection and local fixtures do not
establish operational readiness. No live provider/chain transaction, deployment or
publication was performed. Historical release checks are reference evidence only. The user confirms there are no existing deployments.

The [machine inventory](capabilities.json) records exact names, versions, manifest hashes,
owned source/test files, test commands, selected literal test titles, Go owners and explicit
initial `unimplemented` baseline, current implementation status and `none` live evidence. Test titles are a navigation aid, not a complete enumeration
of parameterized test cases or proof that tests pass. The scope is tracked, non-private
`@blossm/` manifests, including the nested `@blossm/http-dependency-cli` template. The private
workspace root is excluded. No public package is silently dropped.

## Scope and ownership

Use cohesive packages in one Go module, not 154 one-function Go modules. Owner names below
are original design boundaries; the package table and JSON `go_owner` now identify
actual implementation directories.
`internal/value` means ordinary shared value utilities, preferably standard-library calls;
it does not require preserving each JavaScript helper as a public Go API.

- `protocol` owns event envelopes, canonical bytes and commitments. Storage, snapshots,
  blocks, RPC and application producers consume that one contract.
- `event` owns append outcomes, replay, aggregate queries and frontier contracts;
  `event/outbox` owns bounded retry delivery; `event/block` owns snapshot/block construction.
- `storage/mongo` owns Mongo persistence and lifecycle, including event and view adapters.
  The Go plan also requires `storage/postgres`; PostgreSQL is not among these 154 packages.
- `command`, `fact`, `job`, `projection` and `view` own procedure behavior;
  `httpapi`/`httpapi/gateway` adapt transports. `rpc` owns clients and operation addressing.
- `auth`, `crypto`, `validation`, `fault`, `config` and `observability` own shared rules.
- `cli` owns generation, configuration, local tests, deployment artifact preparation,
  replay/execute/secret workflows and adapters, not just a command-name dispatcher.
- `cloud/gcp`, `integration/*` and `evm` own concrete integration behavior. AWS is additional
  Go-plan scope and has no Toolbox package or passing evidence in this reference.

JB/shop application behavior and Relayr payments are explicitly excluded. This does **not**
exclude the four retained EVM packages or direct custodial-wallet behavior. V6 forwarder
signing is included; it is separate from the excluded JB application. Dwolla, Chainpoint,
Redis and Twilio remain in the framework inventory: excluding Relayr is not authorization
to omit unrelated integrations. Dia core/system/comms and wallet procedures require their
own application inventory; the 154-package count does not establish application completeness.

## Current semantics the implementation must resolve and preserve

Paths in this section are relative to the pinned Toolbox root. The package table links to
that checkout. These are inspected source contracts, with named test sources for the Go
port's acceptance work. Intended Go improvements are labeled separately.

### Canonical bytes and event commitments

`packages/lib/cononical_string/index.js` exports `fast-json-stable-stringify` **2.1.0**.
The misspelled npm name is real. This is sorted-key JavaScript JSON serialization, not RLP,
not Go's default JSON encoder, and not a claim of RFC 8785 compliance. Arrays retain order;
booleans/null/numbers/strings remain distinct; own `__proto__` keys survive. Numeric-looking
keys sort lexically (`"10"` before `"2"`). Undefined object values are omitted and undefined
array elements become null. BigInt and cycles fail. Lone surrogate code units remain escaped
and distinct from U+FFFD; astral Unicode remains significant. JavaScript number formatting,
UTF-16 key ordering and Go's HTML escaping/Unicode decoding need deliberate cross-language
fixtures. A Go port must either reproduce the accepted byte domain or explicitly define
and test a narrower fresh-system input contract; silently replacing bytes is incorrect.

`lib/hash` computes **Keccak-256**, lowercase hexadecimal, over canonical JSON UTF-8 bytes.
It is not SHA3-256. Chained updates append canonical encodings without separators.
`hash("a").update("b")` differs from hashing one combined JSON string. Pinned vectors include
`hash(true) = 6273151f959616268004b58dbb21e5c851b7b8d04498b4aabee12291d22fc034`.
`lib/hash_string` is the separate non-cryptographic `string-hash` helper;
`lib/operation_hash` hashes the JSON representation of `[typeof part, part]` tuples to keep
operation component boundaries/types distinct. Do not substitute this for event commitments.

`lib/create_event` generates root/topic/time/version and a hex-encoded idempotency namespace
from supplied key or UUID, optional root, action/domain/service, path operation hashes and
canonical payload. The idempotency field is not simply the caller's original key.
`generic/post_transaction` normalizes transaction metadata **before hashing**, reserves root
counts in sorted-root order, allocates zero-based numbers and validates supplied expected
numbers. It rejects destination domain/service/network mismatches. Payload defaults only
when undefined; false/zero/null must not be converted to `{}`. Header commitments include
payload/context/optional group changes/transaction hashes; the event hash commits headers,
including created/committed times, nonce, sequence and idempotency.

Mongo normalizes `tx` with its schema before computing `tHash`; Go should own an explicit
non-mutating typed normalization contract. Claims timestamps are ISO strings. Persisted
readback must reproduce committed values and hashes; copying schema coercion after hashing
would preserve a defect, not an integrity guarantee.

Evidence: `lib/cononical_string/test/bytes.js`, `lib/hash/test/commitments.js`,
`lib/create_event/test`, `lib/operation_hash/test`,
`procedures/event_stores/generic/post_transaction/test`,
`procedures/event_stores/mongodb/server/normalize_transaction.js`,
`server/test/transaction_claims.js` and `server/test/integration/transaction_serialization.js`.

### Commands, outcomes and durable publication

`command/post` checks required contexts/root, then runs validate, fill, normalize, main.
Main receives aggregate/query/fact/stream/count/nested-command readers and `logEventsFn`.
Returned events are grouped by service/domain; a batch with any `correctNumber` is synchronous,
otherwise queueing is requested. Batches execute independently, not in a distributed transaction.
An earlier nested command or external effect is not rolled back when the parent fails.

Outcomes retain input order and contain the original event plus status. A transport result
with delivery `queued` yields `queued`; otherwise explicit per-event receipts yield
`committed`/`duplicate`, and absent receipts yield `unknown`. HTTP success, async configuration
or missing receipts do not prove commit. Event outcomes and RPC delivery constants are
separate contracts. `thenFn({outcomes})` runs after event dispatch and can merge response,
headers, tokens, revocations and status. It is an in-process callback, not durable orchestration
or lost-response recovery. Default response is 202 with events, 200 with response, otherwise
204; event responses include `_id`/`_tx`, and token outputs use `_tokens`/`_revoke`.

`generic/post` prechecks stored and within-request idempotency duplicates, persists the
remaining events transactionally and returns one outcome per input when requested. Concurrent
uniqueness conflicts still need atomic storage enforcement; do not assume the precheck alone
eliminates races or gives every raced duplicate a successful receipt.

Non-local event-store composition requires transaction normalization and durable publication.
Mongo saves event and notice in one transaction. Notice identity is the persisted event hash,
also present in transmitted metadata. Delivery processes one bounded batch, default 100,
requiring a positive safe integer. It sorts by attempts/created/key, delivers, then deletes
acknowledged notices. Failure increments attempts, retains work and continues the batch;
the raw dispatcher ultimately throws its first failure. The server catches/logs recovery
failure at startup and POST/block boundaries, preserving successful committed responses.
There is no autonomous retry timer and no exactly-once external delivery claim. A stalled
publisher can still delay the awaited recovery attempt.

Local JavaScript mode skips publication and real Mongo transaction wrapping. Preserve the
explicit local infrastructure/publication bypass. **Go-plan change:** use real local database
atomicity for integrity; do not misdescribe that as unchanged JavaScript transaction behavior.
Deferred durable-action/fencing protocols are not part of this reference contract.

Evidence: `command/post/test`, `consts/event_outcome`, `consts/rpc_delivery`,
`generic/post/test`, `generic/server/test`, `generic/deliver_pending/test`,
`mongodb/save_events/test`, `mongodb/create_transaction/test`,
`mongodb/server/test/integration/publications.js` and `startup.js` (event-store paths).

### Aggregate replay and current query discovery

Replay selects the latest snapshot by descending snapshot number then creation time, with
optional `created <= timestamp`. It streams only events with number greater than the snapshot's
`lastEventNumber`, ascending number, optionally bounded by timestamp/event limit. Missing
reducers fail. Result is an envelope with `state`, headers, context, groups and recent tx IDs,
not plain state; no reconstructed state yields no aggregate. Context collisions become false,
with identity comparison using root/service/network. Added groups append; removals compare
root/service/network. Recent distinct transaction IDs are capped at ten.

Query input is an array of `{key, value}` conjunctions. Key must be a nonempty string; value
must be string/number/boolean/null. Matching uses own dotted object properties, recursively
visits array elements, preserves falsy values and uses strict terminal equality. Missing
and null are different. It does not traverse inherited properties or string internals.
Separate predicates can match different array elements; this is not an implicit `$elemMatch`.

For a nonempty query, **candidate roots are the union of**:

1. snapshot matches for `$and: [{"state.<key>": value}, ...]`, projected to `headers.root`,
   with `limit: null` to override the adapter's default 100;
2. all roots whose current count exceeds latest snapshot `lastEventNumber + 1`, including
   roots without snapshots (frontier defaults to -1).

Deduplicate roots, replay their current aggregate, then reapply all predicates. Historical
snapshot matches can produce extra candidates; replay removes stale matches. Dirty roots
cover reducer-derived state that first matches after the snapshot. An empty query streams
all roots and bypasses snapshot lookup. There is no generic 100-result cap. Correctness
requires sound adapter snapshot matching and frontier enumeration, not a scan of historical
payloads requiring all fields in one event. This avoids replaying unrelated clean roots but
still scans dirty-root metadata and does not establish production capacity or a transactionally
consistent multi-root result under concurrent writes.

The GET adapter parses `pairs` JSON for queries; direct root lookup can suppress not-found
throwing with the literal query value `notFoundThrows=false`. Replay options are not all
exposed by that HTTP root handler merely because the aggregate function accepts them.

Evidence: `generic/query/test/candidates.js`, `completeness.js`, `index.js`,
`generic/aggregate/test/index.js`, `mongodb/root_stream/index.js`,
`mongodb/find_snapshots/index.js`, `mongodb/server/test/integration/query.js` and `frontier.js`.

### Snapshots, blocks and binary commitments

Block progress is **sequence-frontier based, never a timestamp window**. Enumerate
`unsnapshottedOnly` roots, oldest updated first with root tie-breaking; replay all unconsumed
events for each selected root. Timestamps in block start/end are metadata. A late commit with
an old event timestamp must still enter a later block. The frontier compares count lengths
to zero-based last-event numbers plus one. Snapshot number starts at zero, and the nonce is
`<root>_<snapshot-number>`. Snapshot `lastEventNumber` advances only after saved replay.

Genesis has block number zero, previous hash `hash("~")`, fixed start/end
`2020-01-01T05:00:00.000Z` / `.001Z`, empty trie roots and RLP-encoded empty arrays, signed
header hash and base64 public key. The genesis call returns without consuming roots.
Subsequent blocks link the previous header hash. Each snapshot commits context, groups,
state, previous snapshot hash (or `hash("~")`), event root/count/byte size and last event number.

The so-called Merkle tree is a **Merkle Patricia trie** (`merkle-patricia-tree` BaseTrie),
not a binary Merkle tree. Keys/values are UTF-8 buffers of the supplied strings. Event pairs
use `hash(event.hash)` as key and canonical event (or encrypted payload) as value; snapshot
pairs use `hash(root)` and canonical snapshot export; tx pairs use `hash(tx.id)` and canonical
arrays of event hashes. Trie roots are base64; pair collections are RLP bytes. Header sizes
are actual byte lengths. Preserve Buffer/binary round trips. Private export encrypts event
payload and snapshot state; it does not encrypt every field or the persisted snapshot state.
The block's snapshot export omits some fields of the stored snapshot, including groups/txIds.

The handler's `blockLimit` is 100 **roots**, not events or bytes. Increment is 10. Within a
transaction callbacks are sequential and the allowance is 90 roots; continuation is flagged
only when another candidate callback hits that threshold. Local concurrent callbacks use
parallelism 10 and the tests bound admission within 100. A root can contain arbitrarily many
unconsumed events, so this is not a memory/byte cap. Snapshots and block must save atomically
in a transaction; a full block schedules another build after commit. Empty non-genesis blocks
are possible. Continuation scheduling failure remains separate from publication recovery.

Evidence: `generic/create_block_transaction/test/frontier.js`, `limit.js`, `commitments.js`,
`index.js`; `generic/create_block/test`; `lib/merkle_tree/test`, `lib/rlp/test`;
`mongodb/server/test/integration/frontier.js`. The Go owner needs fixed cross-language
trie/RLP/hash vectors plus rollback, same-timestamp roots, late commits and restart checks.

### Authentication, authorization and token lifecycle

Bearer/Basic header parsing is case-sensitive; bearer wins over cookie. Basic is opt-in and
splits at the first colon. Strict/object protection propagates authentication failures;
other modes may tolerate them. Object protection matches context root/service/network.
JWTs use three canonical base64url segments; verification precedes decoding and
algorithm/audience checks. Audience is comma-separated. The source uses date-string
`exp`/`nbf`/`iat`, **not NumericDate**; exact expiration and future not-before are rejected.
Decode alone does not authenticate.

Authorization accepts **any** matching service/domain/privilege tuple. `"none"` bypasses
lookup; an empty requirements array denies when passed to authorize. Roles are filtered by
network/subject context. Permission cache identity includes principal, network and context,
with five-minute expiry. Session termination/scene deletion require separate fact checks.
Ordinary gateway routes attach authorization for literal `"strict"`; object authentication
protection alone does not imply permission/revocation checking. The port must explicitly
resolve that policy, not accidentally widen access while consolidating middleware.

Public-key verification caches by URL for five minutes, maximum 100 entries; expired-key
fetch errors propagate. Connection-token caching refreshes invalid/expired credentials.
Redis can fall back to process-local memory when unavailable, which is not a distributed
revocation or durable-state guarantee. Token families include basic/network/operation/node
external/connection tokens and CLI development credentials; do not reduce scope to JWT encode.

Evidence: `lib/{authenticate,authorize,tokens_from_req,role_permissions,verify_access_token,
connection_token}/index.js` and tests; `lib/jwt/src` and `test/{create,validate,decode}.js`;
`middleware/{authentication,authorization}`; `cli/utils/permissions_lookup/test/index.js`;
`cli/utils/{terminated_session_check,deleted_scene_check}`; gateway server composition.

### Projection, view writes and queries

Play decodes Pub/Sub metadata and loads the current aggregate; direct play ignores supplied
action. Muting suppresses push, not projection execution. Replay enumerates roots and queues
plays; it does not clear stale rows. The generated adapter supports conditional updates,
array filters, deletions, multiple operations and recursive aggregate replay. Operations can
run concurrently. Push failures are logged separately. No source-version fence, atomic
multi-row projection or exactly-once incremental mutation is established by this source.
**Go-plan change:** source-versioned replacement writes for an evidenced single-source
projection; multi-source aggregation requires an explicit contract, not an implied universal fix.

`lib/view_store_mutations` owns body/query prefixing, context/groups, trace flattening,
formatter output under `body` and mutation results. Write selectors retain ID/body conditions;
context is mapped to metadata, not automatically added to the selector. Internal callers
own write authorization. Delete requires ID or nonempty query and configured context, then
combines all selectors. No-match PUT returns 204; DELETE returns deleted count. Mongo disables
upsert for conditional/positional updates, awaits initialization and enforces unique view ID.
Read pagination has explicit bounded policy (maximum 10,000), not a measured capacity claim.

Evidence: `projection/{play,replay}/test`; `cli/blossm/src/projection/deploy/src/index.js`;
`cli/blossm/test/projection_{conditions,selection,push}.js`;
`lib/view_store_mutations/test/{write,remove}.js`; `view_stores/generic/{get,put,delete}/test`;
`view_stores/mongodb/test/{index,integration}.js` (procedure prefixes where applicable).

### CLI and integrations

The CLI covers init, configuration, procedure scaffolding/deployment/testing, replay, job
execution, roles, secrets and queues. Shared assembly generates runtime/config/build/Compose
artifacts and deployed procedure adapters. JavaScript `test` can install dependencies and
copy modules/locks. **JavaScript `deploy --dry-run` still submits Cloud Build work.**
**Go redesign:** requested dry-run must be offline and visibly produce a deployment plan,
not emulate this source side effect. Test/build subprocess failures must propagate.

GCP adapters cover KMS encryption/signing, bucket storage, encrypted-file secrets (KMS plus
bucket objects, not Secret Manager), Pub/Sub, Tasks, Scheduler, metadata identity tokens and
logging. Tasks support explicit bearer or OIDC with URL-origin audience. Twilio wraps SMS.
Dwolla exposes customer and business-authority lifecycle/documents, not a proven full payment
journey. Chainpoint submits hashes and retrieves matching proof records; commented verification
exports are not capabilities. Redis's memory fallback must remain explicit.

Evidence: `cli/blossm/test/{dispatch,scaffolds,configuration,gateway_suites,deploy_cleanup,
test_runner,resource_commands}.js`, `cli/utils/merge_cli_template/test`, vendor-local tests.
GCP/AWS IAM, scheduling, publication, SMS and cloud deployment need separate live evidence;
none is provided here. AWS is required new implementation scope, not a reference capability.

### EVM and custodial wallet boundary

V6 lookup pins eight chains (1, 10, 8453, 42161, 11155111, 84532, 11155420, 421614) to
`0x3ba60b60933916a7c87d0860dcee62a0ce34e3e2`; other IDs fail. Deployment provenance is pinned
in its README to `c62b3ac81bfc5603b6771e8ad801eb2236022947`. This is source provenance, not
live bytecode verification. Providers require explicit absolute HTTP(S) RPC URLs.

ERC-2771 signing reads EIP-712 domain and checks chain ID, reads signing-account nonce,
signs locally and returns encoded `execute` calldata. Value/gas/nonce are uint256 and deadline
is uint48. Domain name/version/verifying contract come from RPC. The helper does not enforce
sender/key equality, target trust, deadline policy, submission or recovery. Key-parser errors
are redacted. Local cryptography with mocked RPC and ABI encoding tests do not establish
successful chain execution. Evidence: each `onchain/evm/*/test/index.js`, nested ABI/address
package tests and READMEs.

Direct custodial wallet creation/encrypted key storage/signing/submission/receipt recovery
remain required application work. Dia wallet source is incomplete: address event-name mismatch,
incorrect aggregate/key/root assumptions, broken transaction composition, nondurable submission
recovery and time-dependent reducers were identified in the companion read-only investigation.
Do not label the four Toolbox helpers a completed wallet. JB/shop and Relayr remain excluded.

## Package-by-package ownership and reference evidence

Every row retains its initial unimplemented baseline in JSON. Current status reflects only
the actual source/test mappings listed there; live-provider evidence remains **none**. Descriptions originate
from package manifests and serve as capability labels, not implementation guarantees. Source
links lead to entrypoints; the JSON lists all owned source files and reference tests. Nested
workspace files belong to their own package. `tests` means test source is present, not run.

| Public package | Capability | Current Go owner | Current status | Reference evidence |
| --- | --- | --- | --- | --- |
| `@blossm/array-difference` | Finds the items different between two arrays. | `dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/array_difference/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/array_difference/test/index.js) |
| `@blossm/array-intersection` | Finds the items in common between two arrays. | `dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/array_intersection/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/array_intersection/test/index.js) |
| `@blossm/authenticate` | Authenticate requests. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/authenticate/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/authenticate/test/index.js) |
| `@blossm/authentication-middleware` | Authentication middleware for the network. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/middleware/authentication/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/middleware/authentication/test/index.js) |
| `@blossm/authorization-middleware` | Authorization middleware for the network. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/middleware/authorization/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/middleware/authorization/test/index.js) |
| `@blossm/authorize` | Authorize requests. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/authorize/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/authorize/test/index.js) |
| `@blossm/basic-token` | Gets a basic token to communicate with a network. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/basic_token/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/basic_token/test/index.js) |
| `@blossm/chainpoint` | Anchors a hash to a blockchain. | `integrations/chainpoint` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/chainpoint/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/chainpoint/test/index.js) |
| `@blossm/channel-name` | A properly formatted channel name. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/channel_name/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/channel_name/test/index.js) |
| `@blossm/cli` | A command line tool for interacting with blossm. | `cli` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/blossm/src/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/blossm/src/command/init/template/test/unit/main.js) |
| `@blossm/cli-root-dir` | Finds the nearest root blossm directory. | `cli` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/cli_root_dir/index.js), placeholder |
| `@blossm/command` | A command server. | `command` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/command/server/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/command/server/test/index.js) |
| `@blossm/command-gateway` | A gateway server that manages command issuing access. | `transport` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/command/server/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/command/server/test/index.js) |
| `@blossm/command-gateway-post` | A command gateway server POST handler. | `transport` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/command/post/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/command/post/test/index.js) |
| `@blossm/command-post` | A command handler server POST handler. | `command` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/command/post/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/command/post/test/committed_outcomes.js) |
| `@blossm/command-rpc` | Issues a command. | `rpc` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/command/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/command/test/index.js) |
| `@blossm/compose-update` | Composes an update from many independently specified parts. | `view` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/compose_update/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/compose_update/test/index.js) |
| `@blossm/connection-token` | Opens a connection to get a token to another node. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/connection_token/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/connection_token/test/index.js) |
| `@blossm/cononical-string` | Creates a string of an object that is the same regardless of object property order. | `eventstore` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/cononical_string/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/cononical_string/test/bytes.js) |
| `@blossm/contract-abis-trusted-forwarder` | Pinned OpenZeppelin ERC2771Forwarder ABI for onchain operations | `evm` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/onchain/evm/contractAbis/trustedForwarder/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/onchain/evm/contractAbis/trustedForwarder/test/index.js) |
| `@blossm/contract-addresses-trusted-forwarder` | Pinned Juicebox V6 ERC2771Forwarder deployment addresses | `evm` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/onchain/evm/contractAddresses/trustedForwarder/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/onchain/evm/contractAddresses/trustedForwarder/test/index.js) |
| `@blossm/cors-middleware` | Cors middleware for the network. | `transport` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/middleware/cors/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/middleware/cors/test/index.js) |
| `@blossm/create-event` | Creates a normalized event. | `eventstore` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/create_event/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/create_event/test/commitments.js) |
| `@blossm/crypt` | Crypt functions for hashing and comparing. | `dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/crypt/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/crypt/test/index.js) |
| `@blossm/datetime` | Date and time functionality. | `eventstore` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/datetime/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/datetime/test/components_from_moment.js) |
| `@blossm/deleted-scene-check` | Checks to see if the scene has been deleted. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/deleted_scene_check/index.js), placeholder |
| `@blossm/deploy-cli-template` | Deploys a template to cloud build. | `cli` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/deploy_cli_template/index.js), placeholder |
| `@blossm/duration-consts` | Constants that describe durations. | `eventstore` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/consts/duration/index.js), placeholder |
| `@blossm/dwolla` | Dwolla payment functionality. | `integrations/dwolla` | implemented-scoped-tests | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/dwolla/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/dwolla/test/business_authority/create.js) |
| `@blossm/email-validator` | Email address validation. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/email_validator/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/email_validator/test/validate.js) |
| `@blossm/enqueue-operation` | Enqueues an operation. | `rpc` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/enqueue_operation/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/enqueue_operation/test/index.js) |
| `@blossm/erc2771` | Local ERC-2771 request signing for Juicebox V6 forwarders | `evm` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/onchain/evm/erc2771/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/onchain/evm/erc2771/test/index.js) |
| `@blossm/error-middleware` | Error middleware for the network. | `transport` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/middleware/error/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/middleware/error/test/index.js) |
| `@blossm/errors` | A library of errors that can be thrown by the network. | `transport` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/errors/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/errors/test/bad_request.js) |
| `@blossm/event-outcome-consts` | Event write outcome constants. | `eventstore` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/consts/event_outcome/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/consts/event_outcome/test/index.js) |
| `@blossm/event-store` | An event store server. | `eventstore` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/server/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/server/test/index.js) |
| `@blossm/event-store-aggregate` | An event store server aggregate function. | `eventstore` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/aggregate/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/aggregate/test/index.js) |
| `@blossm/event-store-aggregate-stream` | An event store GET handler for streaming aggregates. | `eventstore` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/aggregate_stream/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/aggregate_stream/test/index.js) |
| `@blossm/event-store-count` | An event store GET handler for getting the count for a root. | `eventstore` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/count/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/count/test/index.js) |
| `@blossm/event-store-create-block` | An event store POST handler for creating a block. | `eventstore` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/create_block/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/create_block/test/index.js) |
| `@blossm/event-store-create-block-transaction` | An event store POST handler transaction for creating a block. | `eventstore` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/create_block_transaction/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/create_block_transaction/test/commitments.js) |
| `@blossm/event-store-create-publication` | Creates an event notification for durable publication. | `eventstore` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/create_publication/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/create_publication/test/index.js) |
| `@blossm/event-store-deliver-pending` | Retries pending deliveries and removes acknowledged records. | `eventstore` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/deliver_pending/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/deliver_pending/test/index.js) |
| `@blossm/event-store-get` | An event store GET handler. | `eventstore` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/get/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/get/test/index.js) |
| `@blossm/event-store-post` | An event store POST handler. | `eventstore` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/post/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/post/test/index.js) |
| `@blossm/event-store-post-transaction` | An event store POST handler transaction. | `eventstore` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/post_transaction/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/post_transaction/test/commitments.js) |
| `@blossm/event-store-query` | Am event store server query function. | `eventstore` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/query/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/query/test/candidates.js) |
| `@blossm/event-store-root-stream` | An event store GET handler for streaming roots. | `eventstore` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/root_stream/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/generic/root_stream/test/index.js) |
| `@blossm/event-store-rpc` | Access to an event store. | `transport` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/event_store/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/event_store/test/index.js) |
| `@blossm/event-stores-rpc` | Access to many event stores simultaneously. | `transport` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/event_stores/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/event_stores/test/http.js) |
| `@blossm/execute-cli-template` | Queues a procedure for execution. | `cli` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/execute_cli_template/index.js), placeholder |
| `@blossm/express-middleware` | Express middleware for the network. | `cmd/dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/middleware/express/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/middleware/express/test/index.js) |
| `@blossm/fact` | A generic fact server. | `fact` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/fact/server/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/fact/server/test/index.js) |
| `@blossm/fact-gateway` | A gateway server that manages fact access. | `transport` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/fact/server/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/fact/server/test/index.js) |
| `@blossm/fact-gateway-get` | A fact gateway server GET handler. | `transport` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/fact/get/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/fact/get/test/composition.js) |
| `@blossm/fact-get` | A fact server GET handler. | `fact` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/fact/get/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/fact/get/test/index.js) |
| `@blossm/fact-rpc` | Gets a fact. | `fact` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/fact/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/fact/test/index.js) |
| `@blossm/fact-stream` | A fact server GET stream handler. | `fact` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/fact/stream/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/fact/stream/test/backpressure.js) |
| `@blossm/format-mongodb-schema` | Formats a schema. | `storage/mongo` | retired-language-tooling | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/format_mongodb_schema/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/format_mongodb_schema/test/index.js) |
| `@blossm/gcp-kms` | Tools for asymetricly decrypting a payload. | `cloud/gcp` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/kms/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/kms/test/client.js) |
| `@blossm/gcp-log-transports` | Destinations to publish logs to. | `cloud/gcp` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/log_transports/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/log_transports/test/sdk.js) |
| `@blossm/gcp-pubsub` | Publishes to PubSub. | `cloud/gcp` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/pubsub/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/pubsub/test/index.js) |
| `@blossm/gcp-queue` | Queues and task. | `cloud/gcp` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/queue/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/queue/test/client.js) |
| `@blossm/gcp-schedule` | Schedules a job. | `cloud/gcp` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/schedule/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/schedule/test/contract.js) |
| `@blossm/gcp-secret` | Tools for accessing a secret. | `cloud/gcp` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/secret/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/secret/test/concurrency.js) |
| `@blossm/gcp-storage` | Tools for accessing stored files. | `cloud/gcp` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/storage/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/storage/test/download.js) |
| `@blossm/gcp-token` | Create a token for an audience within the network. | `cloud/gcp` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/token/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/gcp/token/test/index.js) |
| `@blossm/get-token` | Gets a token for a development environment. | `cli` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/get_token/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/get_token/test/index.js) |
| `@blossm/hash` | Keccak-256 hash of canonical JSON values. | `eventstore` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/hash/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/hash/test/commitments.js) |
| `@blossm/hash-string` | Hashes a string. | `cli` | retired-language-tooling | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/hash_string/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/hash_string/test/index.js) |
| `@blossm/hmac` | Creates HMAC-SHA256 authentication codes. | `dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/hmac/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/hmac/test/index.js) |
| `@blossm/http-dependency-cli` | A template for creating dependency mock servers. | `cli` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/merge_cli_template/template/http_dependency/index.js), placeholder |
| `@blossm/init-cli-template` | Initializes a deployable template. | `cli` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/init_cli_template/index.js), placeholder |
| `@blossm/job` | A generic job server. | `job` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/job/server/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/job/server/test/index.js) |
| `@blossm/job-post` | A job server POST handler. | `job` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/job/post/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/job/post/test/index.js) |
| `@blossm/job-rpc` | Triggers a job. | `job` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/job/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/job/test/index.js) |
| `@blossm/json-to-csv` | Converts JSON to a downloadable CSV. | `view` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/json_to_csv/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/json_to_csv/test/index.js) |
| `@blossm/jwt` | JWT token creating and validating. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/jwt/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/jwt/test/create.js) |
| `@blossm/key-claims` | Establishes the claims from the key. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/key_claims/index.js), placeholder |
| `@blossm/logger` | Logging tools. | `cmd/dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/logging/logger/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/logging/logger/test/debug.js) |
| `@blossm/merge-cli-template` | Merges the contents of a file with a template. | `cli` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/merge_cli_template/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/merge_cli_template/test/build_database_names.js) |
| `@blossm/merkle-tree` | Computes a merkle tree. | `cryptography` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/merkle_tree/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/merkle_tree/test/index.js) |
| `@blossm/mongodb-database` | Read and write from a MongoDB database. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/mongodb/db/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/mongodb/db/test/aggregate.js) |
| `@blossm/mongodb-event-store` | A mongodb event store server. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/server/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/server/test/index.js) |
| `@blossm/mongodb-event-store-count` | A mongodb event store server function to get the count of a root. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/count/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/count/test/index.js) |
| `@blossm/mongodb-event-store-create-transaction` | A mongodb event store server transaction function. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/create_transaction/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/create_transaction/test/index.js) |
| `@blossm/mongodb-event-store-event-stream` | A mongodb event store server event stream function. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/event_stream/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/event_stream/test/index.js) |
| `@blossm/mongodb-event-store-find-events` | A mongodb event store server function to get events. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/find_events/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/find_events/test/index.js) |
| `@blossm/mongodb-event-store-find-one-snapshot` | A mongodb event store server function to get a snapshot. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/find_one_snapshot/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/find_one_snapshot/test/index.js) |
| `@blossm/mongodb-event-store-find-snapshots` | A mongodb event store server function to get snapshots. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/find_snapshots/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/find_snapshots/test/index.js) |
| `@blossm/mongodb-event-store-idempotency-conflict-check` | A mongodb event store server idempotency check function. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/idempotency_conflict_check/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/idempotency_conflict_check/test/index.js) |
| `@blossm/mongodb-event-store-latest-block` | A mongodb event store server function to get the latest block. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/latest_block/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/latest_block/test/index.js) |
| `@blossm/mongodb-event-store-publish-pending` | Publishes pending MongoDB event notifications. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/publish_pending/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/publish_pending/test/index.js) |
| `@blossm/mongodb-event-store-reserve-root-counts` | A mongodb event store server function that allocated event numbers. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/reserve_root_counts/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/reserve_root_counts/test/index.js) |
| `@blossm/mongodb-event-store-root-stream` | A mongodb event store server function to stream all roots. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/root_stream/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/root_stream/test/index.js) |
| `@blossm/mongodb-event-store-save-block` | A mongodb event store server block saving function. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/save_block/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/save_block/test/index.js) |
| `@blossm/mongodb-event-store-save-events` | A mongodb event store server event saving function. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/save_events/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/save_events/test/duplicates.js) |
| `@blossm/mongodb-event-store-save-snapshot` | A mongodb event store server function that saves a snapshot of event aggregate roots. | `storage/mongo` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/save_snapshot/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/event_stores/mongodb/save_snapshot/test/index.js) |
| `@blossm/mongodb-view-store` | A mongodb view store server. | `view` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_stores/mongodb/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_stores/mongodb/test/index.js) |
| `@blossm/network-token` | Creates a token to access an network. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/network_token/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/network_token/test/index.js) |
| `@blossm/network-url` | Returns a url for a network. | `rpc` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/network_url/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/network_url/test/index.js) |
| `@blossm/node-external-token` | Creates a token for calling external services on behalf of the node. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/node_external_token/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/node_external_token/test/environment.js) |
| `@blossm/nonce` | Create a nonce. | `dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/nonce/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/nonce/test/index.js) |
| `@blossm/normalize-cli` | Normalizes arguments passed in to smarket | `cli` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/normalize_cli/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/normalize_cli/test/prompts.js) |
| `@blossm/operation-hash` | Returns a hash representing an operation. | `rpc` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/operation_hash/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/operation_hash/test/encoding.js) |
| `@blossm/operation-short-name` | Creates a short name for an operation. | `rpc` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/operation_short_name/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/operation_short_name/test/index.js) |
| `@blossm/operation-token` | Creates a token to access an operation. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/operation_token/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/operation_token/test/index.js) |
| `@blossm/operation-url` | Returns a url for an operation. | `rpc` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/operation_url/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/operation_url/test/index.js) |
| `@blossm/permissions-lookup` | Looks up permissions for a role. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/permissions_lookup/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/permissions_lookup/test/index.js) |
| `@blossm/phone-number` | Phone number formatting. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/phone_number/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/phone_number/test/format.js) |
| `@blossm/projection` | An projection server. | `projection` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/projection/server/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/projection/server/test/index.js) |
| `@blossm/projection-play` | A projection server POST handler for playing a root. | `projection` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/projection/play/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/projection/play/test/index.js) |
| `@blossm/projection-replay` | A projection server POST handler for replaying. | `projection` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/projection/replay/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/projection/replay/test/index.js) |
| `@blossm/projection-rpc` | Replays a projection. | `transport` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/projection/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/projection/test/index.js) |
| `@blossm/providers` | Read-only viem public clients from explicit HTTP RPC URLs | `evm` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/onchain/evm/providers/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/onchain/evm/providers/test/index.js) |
| `@blossm/random-char` | Returns a random char. | `dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/random_char/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/random_char/test/random_char.js) |
| `@blossm/random-int` | Returns a random integer within the given bounds. | `dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/random_int/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/random_int/test/random_int.js) |
| `@blossm/random-int-of-length` | Creates a random int that has the specified number of characters. | `dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/random_int_of_length/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/random_int_of_length/test/random_int_of_length.js) |
| `@blossm/random-string-of-length` | Creates a random string that has the specified number of characters. | `dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/random_string_of_length/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/random_string_of_length/test/random_int_of_length.js) |
| `@blossm/redis` | Redis cache interactions. | `integrations/redis` | implemented-scoped-tests | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/redis/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/redis/test/index.js) |
| `@blossm/request` | Http access. | `rpc` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/request/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/request/test/http.js) |
| `@blossm/rlp` | Recursive length prefix encoding and decoding. | `cryptography` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/rlp/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/rlp/test/index.js) |
| `@blossm/robo-say` | Adds character to a string. | `cli` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/robo_say/index.js), placeholder |
| `@blossm/role-permissions` | Finds the permissions associated with the given roles. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/role_permissions/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/role_permissions/test/index.js) |
| `@blossm/rpc` | Calls a remote procedure within the network. | `rpc` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/generic/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/generic/test/delivery.js) |
| `@blossm/rpc-delivery-consts` | RPC delivery constants. | `rpc` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/consts/rpc_delivery/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/consts/rpc_delivery/test/index.js) |
| `@blossm/server` | Exposes an express function to run. | `cmd/dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/server/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/server/test/http.js) |
| `@blossm/service-name-consts` | Constants that confine a service name. | `cli` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/consts/service_name/index.js), placeholder |
| `@blossm/stream-rpc` | Calls one or many remote stream procedure within the network. | `rpc` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/stream/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/stream/test/http.js) |
| `@blossm/terminated-session-check` | Checks to see if the session is terminated. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/terminated_session_check/index.js), placeholder |
| `@blossm/test-cli-template` | Tests a template. | `cli` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/cli/utils/test_cli_template/index.js), placeholder |
| `@blossm/tokens-from-req` | Parses the jwt Bearer token and the Basic Auth token from the Authorization header of the request. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/tokens_from_req/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/tokens_from_req/test/index.js) |
| `@blossm/trim-string` | Trims a string to a fixed length and optionally adds three dots to the end. | `cli` | retired-language-tooling | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/trim_string/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/trim_string/test/index.js) |
| `@blossm/twilio-sms` | Sms functionality. | `dia` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/twilio/sms/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/vendors/twilio/sms/test/index.js) |
| `@blossm/url-encode-query-data` | Encodes query data into a url. | `rpc` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/url_encode_query_data/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/url_encode_query_data/test/index.js) |
| `@blossm/uuid` | Creates a uuid. | `dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/uuid/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/uuid/test/index.js) |
| `@blossm/uuid-validator` | Validates a uuid. | `dia` | stdlib-replacement | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/uuid_validator/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/uuid_validator/test/index.js) |
| `@blossm/validate-command` | Validates the paramaters found in command requests. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/validate_command/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/validate_command/test/error_paths.js) |
| `@blossm/validation` | Value validation for common types. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/validation/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/validation/test/boolean.js) |
| `@blossm/validator` | Value validation for common and custom types. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/validator/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/validator/test/boolean.js) |
| `@blossm/verify-access-token` | Verifies that the signature is correct given a message and a public key. | `dia` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/verify_access_token/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/verify_access_token/test/index.js) |
| `@blossm/view-composite` | A generic view composite server. | `view` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_composite/server/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_composite/server/test/index.js) |
| `@blossm/view-composite-get` | A view composite server GET handler. | `transport` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_composite/get/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_composite/get/test/index.js) |
| `@blossm/view-composite-rpc` | Gets a composite view. | `transport` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/view_composite/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/view_composite/test/index.js) |
| `@blossm/view-gateway` | A gateway server that manages view access. | `transport` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/view/server/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/view/server/test/index.js) |
| `@blossm/view-gateway-channel` | A view gateway server GET handler for channel subscriptions. | `transport` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/view/channel/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/view/channel/test/index.js) |
| `@blossm/view-gateway-get` | A view gateway server GET handler. | `transport` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/view/get/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/gateways/view/get/test/index.js) |
| `@blossm/view-store` | A view store server. | `view` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_stores/generic/server/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_stores/generic/server/test/index.js) |
| `@blossm/view-store-delete` | A view store DELETE handler. | `view` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_stores/generic/delete/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_stores/generic/delete/test/index.js) |
| `@blossm/view-store-get` | A view store GET handler. | `view` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_stores/generic/get/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_stores/generic/get/test/csv_download.js) |
| `@blossm/view-store-id-stream` | A view store GET handler for streaming view ids. | `view` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_stores/generic/stream_ids/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_stores/generic/stream_ids/test/index.js) |
| `@blossm/view-store-mutations` | Shared view mutation mapping and results independent of HTTP and persistence adapters. | `view` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/view_store_mutations/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/lib/view_store_mutations/test/remove.js) |
| `@blossm/view-store-put` | A view store PUT handler. | `view` | implemented-source | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_stores/generic/put/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/procedures/view_stores/generic/put/test/index.js) |
| `@blossm/view-store-rpc` | Interact with a view store. | `transport` | implemented-redesign | [source](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/view_store/index.js), [tests](https://github.com/blossm-network/toolbox/blob/372d1c6a3f4699b8714faf15a81dff0b6d8a49a0/packages/rpc/view_store/test/index.js) |

## Inventory verification and completion boundary

Coverage check: 154 distinct names and manifest paths match all tracked public manifests at
the pinned commit. Every entry has an actual `go_owner`, its original `proposed_go_owner`, initial `baseline_go_status=unimplemented`,
a reconciled current implementation disposition, `live_evidence=none`, and included framework scope. All indexed source/test paths exist.
Twelve scripts are echo-only placeholders (ten report no test specified, two constants
packages say no tests). Counting successful script exit codes as behavioral coverage would
overstate verification. The initial reference inventory ran no runtime tests; later scoped CLI/adapters
verification is recorded separately below.

The required root plan refinement check passed. This document and JSON complete the focused
inventory task only. The retained Go scope has baseline local implementation and verification evidence,
including failure/recovery regressions. Live provider/chain and deployment verification
remain explicitly unperformed; local completion does not imply operational readiness.

## Reconciliation after CLI and external-adapter work

The JSON's `implementation` object maps **every package** to actual existing Go source and
test files and records a per-package decision/gap. `implemented-source` means source and
test definitions exist, not that this inventory independently ran the owner's tests.
`implemented-redesign` and `stdlib-replacement` explicitly replace a JavaScript/internal
contract; they do not assert historical wire compatibility. `retired-language-tooling`
removes a Node/Mongoose/display helper whose job is now owned by typed/native code.
`partial-*` and `gap` identify remaining behavior. No row is silently excluded.

Historical CLI/external-adapter scoped unit/race/vet checks used Go1.27.1.
The user subsequently authorized downloads; Go1.27.4 is installed and the parent
reports initial unit tests passing across all Go packages. Go1.27.4 remains the
CI/container pin, sourced from the [official release feed](https://go.dev/dl/?mode=json)
checked 2026-11-12. The earlier permission blocker is resolved. Pre-remediation baseline non-native gates passed on the security-patched dependency
graph; the baseline consolidated native run passed927 results, command exit0. Historical CLI tests prove actual
subprocess failure propagation, empty-PATH offline preparation, deterministic examples,
required authentication/publication settings and Cloud Run background-progress settings.
Typed scaffolds compile, authenticated job/replay clients use isolated HTTP transports,
and injectable provider runners cover role/secret/queue/deployment workflows with
explicit future --execute and offline default preparation. No live cloud write occurred.
External adapters have actual request/response and loopback TCP/HTTP tests. See
[CLI](../cli/README.md), [deployment evidence](../deploy/README.md) and
[external operation map](../integrations/README.md). Initial full-module unit/compilation and scoped runtime/native evidence now exist;
baseline consolidated native verification passed. These checks establish neither retained semantic completion nor live operational readiness.

Current Go command callbacks use `Result.Then`, awaited for all input-ordered
outcomes, including duplicate, queued and empty results. Response construction
runs on retries; side effects must explicitly check `Committed`. This callback
is not durable orchestration, and its failure does not undo confirmed receipts.

Current generic command/fact/job/events/aggregate/query routes are implemented in
`transport/framework.go`, with required authenticated identity injection and stream-failure
handling. CORS and job HTTP/RPC tests are present; the parent reports passing scoped checks.
`audit/export.go` implements AES256-GCM private exports with mandatory ES256-P1363 signatures
and configured producer verification. Internal SHA256 JSON blocks deliberately replace historical MPT/RLP block exports
by user decision; lack of legacy block composition is not an implementation gap. MPT/RLP primitives and JavaScript golden vectors now exist in
`cryptography/`; the parent now reports their Go1.27.4 scoped race/vet passing.
External primitive byte parity does not turn the intentionally redesigned internal
SHA256 block format into the legacy block wire format.

Application authentication deliberately uses one Go ES256 JWT with epoch-second dates and
Bearer transport. Legacy JWT wire families are redesigned; explicit gateway Basic/key/cookie credential boundaries are implemented separately in `transport/auth.go` and `transport/gateway_cookie.go`. Their validation is not implied by the JWT redesign.
Authenticated internal service claims are injected by composition. Outbound GCP identity
tokens and AWS SigV4 signing are separate provider contracts. CLI `token` prepares offline;
explicit `--execute` invokes injected gcloud identity-token acquisition with one audience.
AWS has no fabricated bearer-token equivalent.

Projection orchestration now has standalone authenticated HTTP/RPC adapters in
`transport/projection_routes.go`; [the adapter contract](../transport/PROJECTION.md)
records trusted resolver scope, manual-trigger restrictions and progress/error handling.
Owned route/RPC race tests and transport vet passed on Go1.27.1. The projection owner reports both-database native race success2.613s. Framework
now mounts explicitly configured Projections/Views, with scoped authorization tests.
Count/history/root/aggregate stream routes and typed StoreClient are now mapped;
the parent reports both-adapter native streaming race success, including the signed Block route/RPC. View gateway/composite HTTP/RPC is now
implemented and owner-tested, with independent final route/client race fixtures
passing1.315s alongside the projection acknowledgment regressions.

Active CI configuration now lives at the actual repository root in
[go-verify.yml](../.github/workflows/go-verify.yml), with nested-module working
directory/cache paths and failure-propagating Make gates. YAML/path checks passed;
no remote workflow run is claimed. Actual Linux container build, CLI and local
HTTP lifecycle smoke now pass per parent. Initial Go1.27.4 checks and final
security-patched dependency gates remain separate baseline evidence; later remediation requires fresh gates; no live environment was verified.

Stdlib replacements are concrete: time/Duration for dates/constants, crypto/rand for retained
nonce/challenge/secret callers, crypto/hmac and crypto/pbkdf2 for authentication primitives,
net/mail and typed validation for input contracts, net/url for explicit RPC addressing,
net/http/httptest for servers and test dependencies, log/slog for logging, errors.Is/As for
error ownership, typed slices/maps for domain set operations, and flag/filepath for CLI
arguments/paths. UUIDv4-specific shape, Mongoose schema compilation, JS decorative output
and noncryptographic operation-short-name hashing are deliberately retired in the fresh Go
contract. This does not authorize dropping security, recovery or external integration work.

Retained vendors beyond cloud/EVM/comms are Dwolla, Redis and Chainpoint; all have actual
adapters under integrations/. No Slack package exists. Dwolla exposes the17retained
customer/beneficial-owner operations through cohesive typed methods. Redis has remote
hash/expiry operations and an explicit local cache; it never silently falls back after a
configured remote failure. Chainpoint supports a configured gateway and reports errors;
automatic public-IP discovery is deliberately retired in favor of the configured trusted
HTTPS gateway. No successful live endpoint behavior is asserted. None of these is excluded
by the JB/shop/Relayr scope decision.

## Historical scoped evidence and open gates

ProjectionClient now rejects HTTP200 null/empty objects, missing/null required fields,
contradictory outcome flags/counts and nonadvancing/foreign replay cursors. Valid
committed errors and confirmed replay progress remain available alongside errors.
Final projection/view-gateway/Framework-mount race fixtures passed1.315s on Go1.27.1;
transport vet passed. Projection owner reports both-database native race pass2.613s.

The parent reports durable comms HTTP failure/retry tests passing7.658s against both
databases, native signed Block HTTP/RPC success, mutating-reducer and trusted-event-context
regression success, and240KiB complete-publication local/remote precommit rollback checks.
These are attributed parent evidence, not independently rerun full-module gates.

AWS plans require matching QueueARN, RawMessageDelivery=true and SQS maximum message
size>=262144. Separate preparation artifacts/argv configure exact topic/queue targets;
no live resource state is inferred. Runtime reports240KiB accepted and240KiB+1 rejected
before its SDK endpoint; Attempts is retained. Raw delivery is needed for that budget.
Job/projection/composite scaffolds now delegate to their actual core owners.

Reducer aliasing has parent-reported native regression evidence. Complete-block MongoDB
size/fairness regressions pass in the final Mongo native run67.836s; the
publication-size cap is a separate guarantee from block chunk persistence. Native wallet tests include anonymous scene
and unbound-principal rejection; the parent reports both-database wallet success. Dia subject-scoped role removal, group streaming and SSE/GRIP handshake are
covered by the completed local Dia/transport suites. Channel lookup
implementation does not itself prove a deployed subscription or push channel.
Download authorization is resolved. Baseline consolidated native verification passed, command exit0; remediation gates remain pending. No live cloud/chain writes, token issuance or provider verification
were performed; database and container fixtures are local test evidence.

## Baseline Go1.27.4 verification, before semantic remediation

The following are parent/owner-reported results, not independent full reruns by this
inventory agent. These results predate ongoing semantic remediation and do not verify later source changes. Fresh parent gates and independent follow-up review remain pending. Earlier Go1.27.1 scoped results remain historical.

| Gate | Reported evidence | Boundary |
| --- | --- | --- |
| Baseline unit and race suites |753 tests/subtests pass each on Go1.27.4 |25 test packages plus one package with no test files, not skipped tests |
| Formatting, vet, module verify, build/config | Baseline PASS | Parent-reported pre-remediation graph |
| EVM, wallet, cryptography | Scoped race and vet pass on Go1.27.4 | SDK fixtures and golden vectors, no live chains |
| Native custodial wallet | Final PostgreSQL and MongoDB pass,15.040s | Includes anonymous/foreign/revoked authority checks and journal atomicity; no live transfers |
| runtimepub/Open and cloud | Full package race/vet pass on Go1.27.4 | Synthetic credentials and local HTTP/gRPC fixtures; no IAM/provider readiness claim |
| cmd/dia | Compile/unit pass; runtime owner records1.608s | Includes complete runtime/EVM/wallet wiring, distinct from process deployment |
| Linux amd64 container | Actual image build, CLI smoke and HTTP session lifecycle smoke pass | PostgreSQL on disposable Docker fixture network; create/retry/terminate/revoke smoke1.133s |
| Final govulncheck | PASS, No vulnerabilities found | Patched dependency graph |
| Final consolidated native | PASS,927 test/subtest results, command exit0 | Zero failed/skipped tests, no live environment verified |
| Native Redis and tidy diff | PASS1.280s; tidy diff clean | Pre-remediation local checks |

Dependency downloads were user-approved, tidy completed, and MongoDB Go driver was
upgraded to v2.9.3. The [container test source](../integration/container_test.go)
and [runtime owner record](../runtimepub/README.md) locate the implementation and
scoped evidence. SDK-download permission and missing-toolchain statements from the
earlier inventory are superseded, not current blockers. No successful deployment,
publication or live provider/chain verification is claimed.

Pre-remediation non-native checkpoint: full fmt/vet/module verify/build/config, unit/race and
security gates pass on Go1.27.4 after Dia any-error exact-receipt reconciliation.
The final linux/amd64 image is
`sha256:83147a303e653cd881a300c874a1c0bd8c766062a3894b13aa8e12309a82ac30`,
with CLI and HTTP lifecycle smoke passing1.133s. The previous native run had one
Dia same-key concurrency failure;10 frozen-old-binary controls passed but do not
close it. The final consolidated native run passed927 results with zero failures/skips;
Dia76.288s, Mongo67.836s, PostgreSQL14.870s and wallet15.040s. Native Redis
reran successfully1.280s and tidy diff is clean. See [the verification record](VERIFICATION.md)
for exact reproduction commands and checkpoint distinctions.

Baseline local gate success is supported by [archived machine evidence](verification-baseline/verification-results.json),
including exact unit/race/native package results and raw-log hashes. All154 initial
baselines remain preserved; no live-provider evidence is invented.

## Remediation checkpoint, scoped evidence only

Gateway authority, cookies, fact proxy lifecycle and bounded uploads now map to
`transport/{auth,gateway_cookie,gateway_proxy,upload}.go` and their actual fixtures.
The owner reports full transport race PASS1.813s, final protected-context regression
race PASS1.796s and vet PASS. The [gateway API and JS assertion matrix](../transport/tasks/auth_gateway.md)
records exact route policies, required resource checks, pinned peer identity, secure
cookies, explicit upload limits and fixed proxy destinations. Final whole-port
gates and independent review remain pending.
The behavior matrix records proxy header forwarding, rejection before streaming,
disconnect cancellation, truncated-stream errors and upload recovery assertions.

The gateway scaffold now constructs `serviceapp.App` through `serviceapp.New`,
using the agreed typed configuration. All generated kinds compile in one module;
the fixture confirms invalid application configuration fails. CLI race PASS7.273s
and vet PASS used installed Go1.27.4, including generated-code subprocesses.
Generic process composition, cross-service acceptance, final full gates and
independent follow-up review remain separate requirements. No live writes occurred.

Latest CLI runtime checkpoint: Go1.27.4 CLI race PASS7.110s, vet PASS and actual
CLI build of `blossm`, `dia`, `framework` exited0. Strict configuration now selects
runtime/signing/delivery settings and prepares explicit `/framework` container
entrypoints with separate identity/document namespaces. Provider examples remain
illustrative and require real resources. Parent reports generic both-database
flow PASS5.019s in `integration/framework_flow_test.go`; final process/container
and whole-port gates remain pending. Formerly open behavior-family rows now
point to concrete newer assertions where present; a family association is not a
one-to-one proof of all historical JS branches. Independent semantic review is
still required before whole-port completion.

The [framework executable contract](../cmd/framework/README.md) is now stable;
its owner reports scoped runtime race/vet passing. `/healthz` is mounted.
Local mode retains infrastructure bypass without automatic view publication;
inline mode uses normal outboxes and verified projection, with async work
explicitly unavailable without a provider queue. Shared runtimeenv/runtimeauth
serve both executables. The [delivery runtime](../runtimequeue/README.md) records
GCP OIDC and AWS worker authority; distinct AWS delivery/publication queues are
enforced by both executable configuration and offline CLI validation.

Fresh gateway checkpoint superseding the earlier scoped run: owner reports full
transport race PASS2.138s and vet PASS on Go1.27.4. Actual signed delegated-command
fixtures now verify parent Main -> CommandClient -> child authority, exact target
and peer, transaction trace binding, revocation and expiry ceiling. Proxy, cookie
and upload assertions are included in that run. Final whole-port gates remain
pending; this is local fixture evidence only.

Replay/peer CLI checkpoint: full CLI race PASS9.391s and vet PASS on Go1.27.4.
Replay now requires one explicit request `runId` across retries and pages; static
delivery config/environment defaults are rejected. The owning ProjectionClient
validates required fields and echoed run identity. Optional framework peers bind
fixed HTTPS destination/service and exact same-network incoming peer, while the
runtime derives target audience and issuing subject. New regression fixtures
cover missing/duplicate IDs, malformed/foreign acknowledgments and unsafe peer
configuration. No live requests or deployments were made.

Current parent checkpoint after remediation: Go1.27.4 unit and race suites each
PASS992 test/subtest outcomes; vet PASS. These overlapping counts are not semantic
parity evidence. Native verification and independent Claude review are running,
so whole-port completion remains pending. The earlier753/927 results above remain
historical baselines and are not relabeled as current.

The inspected generic copy flow now requires explicit `sourceVersion` and uses
verified historical state. Its native test edits the source after the first child
commit, then verifies retry returns the same child ID, original text and duplicate
outcome. Additional native negatives require401 for delegated-command credentials
on facts, jobs, view listing and view reads. These new assertions are indexed;
their latest native execution remains pending the parent report.

Claude3 CLI closure checkpoint: internal-token now reuses runtimeauth.Open and
JWT.Sign with configured internal subject/network audience and TTL1s..5m.
Preparation never opens a signer or reads credentials. Actual CLI local issuance
successfully schedules replay through serviceapp HTTP with injected store/producer;
wrong-subject/network credentials return401. Synthetic KMS failures propagate
without token output and close signer resources. Full CLI race PASS6.986s and
vet PASS on Go1.27.4; no live signing, provider or native database claim for these
fixtures.

Cloud Run generation now requires explicit application/iam invocation mode,
matching provider-check annotation and review prerequisites. The application mode
retains container authorization; IAM mode requires separate Google invocation
credentials or an authenticating edge. [Deployment documentation](../deploy/README.md#cloud-run-invocation-and-application-credentials)
links the verified official contracts. Three-binary and historical failed-build
documentation is corrected. The user selected one signing trust domain per network, recorded in ARCHITECTURE.md.
Whole-port gates and final acceptance remain parent-owned and pending.

Claude4 follow-up checkpoint: full CLI race PASS6.337s and vet PASS on Go1.27.4.
Actual CLI-minted replay now uses plain loopback HTTP; remote plaintext and
malformed targets reject before credential access. Framework GCP IAM invocation
is rejected as an unverified intact-token composition. Official sources establish
specific signature-redaction cases, not universal stripping of every Tasks token;
[deployment evidence](../deploy/README.md#cloud-run-invocation-and-application-credentials)
records that distinction. Application mode remains supported and checked, while
Dia IAM mode explicitly requires an authenticating edge. Prepared operator plans
now require retry/redrive/retention decisions and disclaim DLQ provisioning.
Fourth review found no blocker; final whole-port gates and parent acceptance
remain pending. No live changes or provider validation occurred.

## Final frozen-source verification

Parent confirms manifest251 files, SHA256
`50dd9c9b76d88d6dcf913090009747f015ec02864a58f74e763b3759b652de16`,
with no subsequent runtime/config changes.

| Gate | Final result |
| --- | --- |
| Unit |1008 test/subtest outcomes, zero failures/skips |
| Race |1008 test/subtest outcomes, zero failures/skips |
| Native PostgreSQL/MongoDB |1211 test/subtest outcomes, zero failures/skips, exit0 |
| Vet, formatting, module checks, config, builds, security | PASS |
| Native Redis and two local container flows | PASS |
| Fifth bounded independent Fable review | No blocker in reviewed deltas |

No local verification gate remains outstanding for the retained frozen scope.
All154 initial unimplemented baselines and explicit package owners remain in the
machine inventory. The matrix is an assertion navigation/review record, not a
claim that every historical JS branch has an individually paired Go assertion.
The reviewer did not execute tests or re-audit unchanged domains/SDKs.

JB/shop and Relayr payments remain excluded; direct custody and V6 are retained.
One signing trust domain per network is intentional. Streams have bounded
deadlines; malformed delivery requires operator retry/redrive policy. Framework
GCP IAM remains conservatively unsupported; application invocation retains
container authorization. No live deployments, IAM changes, payments, blockchain
transactions or provider compatibility claims follow from these local results.
