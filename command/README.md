# Command protocol

`Procedure.Execute(ctx, Request)` is transport-independent. Consumers supply an
`Appender`, implemented by `*eventstore.Store`, required context names, optional
root requirement and validation/fill/normalize functions, and `Main`. No cloud
SDK or HTTP router is imported. The parent application owns authentication and
routing; required-context presence is not permission checking.

`Request.ID` and `Issued` must remain identical on retries. The protocol validates
context/root and JSON, then calls `Validate`, `Fill`, `Normalize`, and `Main` in
that order. Main prepares `Result.Events` and a `Contribution` response. It must
be deterministic for the immutable request envelope and keep conditional effects
out of preparation. Inputs use the existing eventstore schema. Missing event
roots inherit the request root. Generated event keys include an actor namespace,
request ID and event position. The actor is canonical authenticated
Context.principal when present; otherwise it is the effective transaction claims'
issuer/audience/subject identity. Token lifetime fields are not part of this
namespace. Without either identity, callers share the anonymous namespace.
Different actors may reuse the same request ID independently. Root is deliberately
excluded so changing the target/root under the same actor and ID conflicts with
the original event fingerprint. Explicit event keys are preserved unchanged and
leave collision policy to the application. Child request IDs are unchanged;
actor isolation occurs in their generated event keys. The command envelope owns Created and Transaction
metadata, including optional configured trace and claims, so no wall clock or
random ID changes event fingerprints on retries. Retain previously prepared
conditional inputs if application state influences them; this package does not
persist/recover arbitrary Main return values.

Responses contain per-event receipts in input order:

| Outcome | Meaning |
| --- | --- |
| COMMITTED | The atomic appender confirmed a fresh event commit. |
| DUPLICATE | An identical event already exists; this is a successful no-op. |
| QUEUED | The queue returned a nonempty provider acknowledgement ID. No commit is claimed. |

`Async` selects Queue only when configured, Local is false, and no event has an
Expected number. Conditional writes and local mode always use synchronous Append.
Async requests without a queue return a configuration error. Queue errors or
missing provider acknowledgement return `ErrQueueUncertain` and no queued
receipts: the provider may have accepted a request whose response was lost.
`Submission` contains stable events and ID; a worker calls `Consume(ctx, store,
submission)` to append it. Queue redelivery uses eventstore deduplication. Provider
acceptance does not establish worker execution, validation success or projection
completion.

`Then(ctx, []command.Receipt)` is awaited after every successful dispatch,
including empty batches, queue acceptance, duplicates and mixed batches. It receives
all outcomes in input order with detached Input/Event memory. Its contribution
replaces Body/Headers keys, appends Tokens/Revoke, and can override StatusCode.
Use the same deterministic response construction on retries; inspect each receipt's
Outcome and execute conditional side effects only for COMMITTED. QUEUED is not a
commit. A callback failure returns CallbackError plus the confirmed outcomes and
cannot undo a commit or queue acceptance. A process crash can skip the callback.
This is best-effort work, not durable actions or exactly-once effects. Store/queue
failures and unconfirmed queue acceptance do not invoke Then.

For nested calls, `transport.CommandClient{RPC, Target, Authorize}` uses explicit
addressing and target credentials. `Execute(ctx, parent, name, operation, child)`
derives the child ID from parent ID, stable logical target, command name and a
caller-chosen operation selector. Reuse the selector for retries; use distinct
selectors for independent child operations. Issued and transaction ID/trace come
from the parent. Child ID, Issued, Transaction, Context and Claims cannot override
that envelope. Authorize receives the parent to mint/forward target credentials;
the remote Framework reconstructs trusted context/claims from them. Request-body
context/claims are omitted. There are no automatic retries; an earlier child commit
is not rolled back if the parent later fails. Remote callback failure returns the
response alongside CallbackError; 204 means an empty successful response.

Transport adapters can serialize `Response` as JSON and map errors themselves.
The fixture demonstrates a real authenticated-context HTTP boundary using the
shared RPC client. No automatic retry, credential minting, cookie policy or
application authorization is hidden in this owner.

Run `go test -race ./command` and `go vet ./command` from the parent module.
Tests use real eventstore normalization/deduplication/commit code over a transaction
fixture, plus actual httptest/RPC traffic. Native database guarantees remain the
storage adapters' verification responsibility.

Generated event keys are scoped to the effective authenticated actor. Retrying
under a different principal, including upgrading an anonymous/session-only
identity to a principal, is a different actor namespace, not recovery of the
original actor's command. Applications that permit anonymous mutation must bind
and retain their intended actor or explicit event key across that product flow.
The reference service requires a principal before every document mutation; Dia
identity upgrades use their own persisted operation receipts. Refreshing volatile
token fields for the same actor preserves duplicate recovery.
