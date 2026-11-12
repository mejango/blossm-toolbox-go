# Toolbox architecture

`github.com/mejango/blossm-toolbox-go` is an application-independent framework.
[Dia](https://github.com/mejango/dia-go) consumes its public APIs from a separate
module. Toolbox owns no Dia models, policies, application runtime or executable.
The [154-package inventory](capabilities.md) and [behavior ledger](behavior-coverage.md)
retain the original framework and application capabilities with explicit owners.

Current source-split verification is pending parent gates. Existing logs,
manifests and reviews describe their recorded pre-split source and remain
unchanged. Source links describe ownership, not successful execution. External
Dia links use the paired review branch recorded in capabilities.json; they
are not references to a required local checkout.

## Capability owners

| Directory | Responsibility |
| --- | --- |
| [procedures/command](../procedures/command), [fact](../procedures/fact), [job](../procedures/job) | Typed execution, validation, trusted caller inputs, ordered outcomes and streaming |
| [procedures/projection](../procedures/projection) | Aggregate-fed projection plans, dependencies, source revisions and replay scheduling |
| [stores/event](../stores/event) | Canonical event protocol, hashes, atomic append, replay/query, snapshots, blocks and durable publication |
| [stores/view](../stores/view) | Named/scoped projections, typed queries, atomic mutations, source-fenced selection and CSV formatting |
| [storage/postgres](../storage/postgres), [storage/mongo](../storage/mongo) | Native transactions/retries, canonical bytes, constraints, cursors and candidate indexes |
| [storage/internal/queryindex](../storage/internal/queryindex) | Shared typed scalar-leaf query terms |
| [security/token](../security/token), [security/authn](../security/authn) | Generic typed ES256 tokens/signers and authentication error boundaries; applications own claims policy |
| [security/cryptography](../security/cryptography), [security/audit](../security/audit) | Protocol cryptography and private signed/encrypted audit exports |
| [onchain/evm](../onchain/evm) | Ethereum codecs, ABI/RLP/MPT, signing and provider operations; application custody policy stays outside Toolbox |
| [transport](../transport), [rpc](../rpc) | Generic authenticated HTTP routes/clients, strict decoding, errors, cookies, proxying, uploads and cancellation |
| [delivery](../delivery) | Prepared-work consumption, acknowledgment, durable terminal rejection and recovery |
| [runtime/auth](../runtime/auth), [env](../runtime/env), [publication](../runtime/publication), [queue](../runtime/queue) | Explicit SDK/database/signing/client composition and lifecycles |
| [cloud](../cloud), [integrations](../integrations) | Provider SDK adapters and bounded external protocols |
| [cli](../cli), [cmd/blossm](../cmd/blossm), [deploy](../deploy) | Offline configuration/scaffolding, actual build/test delegation and reviewable resource plans |

Package declarations may retain short names such as `eventstore`; directory
names describe capabilities. The generic reference primitives are reusable by
any application. Dia's identity-backed document example lives in
[Dia examples/document](https://github.com/mejango/dia-go/tree/main/examples/document),
not in this module.

## Execution and committed representation

Application authentication supplies trusted identity/context; procedure handlers
prepare typed events. Append normalizes input, locks roots deterministically,
checks duplicate identity and expected event numbers, applies reducers, and
commits counters/events/outbox atomically. Publication happens after commitment.
Acknowledgment follows durable consumption. Unknown commit is explicit and never
causes an automatic transaction replay. Known-abort retries are bounded; read
callbacks are single-attempt. Local mode keeps database integrity while bypassing
publication infrastructure.

Canonical JSON rejects duplicate keys, invalid Unicode and unsafe numbers.
Safe integer values use decimal spelling; external uint256 and persisted uint64
values use strings. Timestamps normalize to UTC RFC3339Nano. Fingerprints omit
only volatile exp/iat/nbf/jti claim fields; committed event hashes retain the full
original claims. Reducer inputs/state are detached before invocation so callbacks
cannot mutate committed bytes. COMMITTED, DUPLICATE and QUEUED are distinct.
Root revision is the last zero-based event number, -1 when absent; expected event
number is zero for creation. Invalid transitions cannot be committed merely
because payload validation succeeded.

Publication payloads are bounded to240KiB before hashing/writing, including local
mode. Provider envelopes/queue limits must retain their documented overhead.
Provider acceptance is not consumer completion. Replay has explicit run identity:
retries/pages reuse it, while a new rebuild uses a new ID. Terminal deterministic
rejections are acknowledged only after their minimal immutable journal commits;
unknown commits/network failures/cancellation remain retryable. Operator redrive
and dead-letter policy remains an explicit prerequisite, not provisioned magic.

## Queries, projection fences and recovery

Event queries use strict scalar matching, array traversal and null-versus-missing
semantics. Typed candidate indexes narrow clean snapshots while dirty roots
remain candidates; complete replay performs final matching. There is no hidden
100-snapshot cap. Snapshot/block bytes remain binary, and per-root snapshot
service credit advances atomically with the block. These are internal audit
artifacts, not public Ethereum transactions.

Named views isolate logical IDs sharing a backend. Common owner scope is ANDed
with group unions; required empty unions deny all rows. View queries provide
typed number/date bounds, literal text and stable typed sorting. Query-selected
writes have an explicit maximum and prove the committed source frontier before
writing; they cannot invent projection versions. Patches bind expected revision;
missing-row tombstones prevent stale recreation. These are single-source fences,
not universal exactly-once or cross-service serializable transactions.

Command `Then` callbacks receive detached ordered outcomes, including duplicate,
queued and empty outcomes. Deterministic response construction may repeat;
fresh-only effects must check COMMITTED and remain best effort. Nested command
clients preserve stable child identity/trace; applications own delegation and
immutable intent. A child commit cannot be rolled back by its parent.

HTTP stream callbacks run serially within a repeatable view and honor cancellation.
Midstream failure aborts the response; truncation is not success. Store clients
expose typed append/count/query/history/aggregate/block calls. Signed blocks require
an explicitly configured producer signer; private export remains security/audit.
Streaming has the configured gateway deadline (currently30seconds); unbounded
connections are not promised. JSON is encoded before response headers;204 is
header-only. Application shutdown owns cancellation and joins workers before
closing their clients.

## Application boundary and evidence

Dia owns its identity/session/account/scene/role/group policy, model schemas,
communications, custody and recovery. See its
[architecture](https://github.com/mejango/dia-go/blob/main/docs/ARCHITECTURE.md).
Generic token verification/signing does not decide application permissions or
revocation policy. One network remains one signing trust domain: shared-key
signers are mutually trusted, and peer subjects are not compromise isolation.

Cloud providers and onchain adapters require explicit configuration; local tests
and SDK fixtures do not establish live provider compatibility. No deployment,
live KMS/message/custody transaction or production readiness is established by
this source split. See parent-owned [VERIFICATION.md](VERIFICATION.md) and
[PUBLICATION.md](PUBLICATION.md) for historical publication and new gate evidence.
The documentation checker may inspect both repositories via DIA_SOURCE; Go
builds must depend only on their declared public modules.
