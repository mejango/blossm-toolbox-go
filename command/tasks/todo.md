# Command and fact protocol

## Plan refinement

- **Objective:** Own retained command dispatch/outcomes and fact read/stream behavior in command and fact only; RPC client is delegated to rpc subtree. No cloud dependencies or durable-action framework.
- **System fit:** Authenticated consumer context enters validation, deterministic command preparation, eventstore.Append or acknowledged queue acceptance, explicit receipts and best-effort fresh-commit callbacks. Facts use the same required-context rule and await every stream callback. Parent owns application HTTP routing/authentication and inventory.
- **Reuse and simplicity:** Reuse eventstore Input/Receipt/Outcome and Append atomicity, canonical JSON validation, standard context/HTTP/JSON codecs. Queue interface earns its cost by separating provider acceptance from commit. No environment-derived endpoint guesses or hidden retry loops.
- **Evidence and unknowns:** Read isolated reference command/post, generic RPC, fact read/stream and eventstore owners. Current root plan authorizes these capabilities. No existing deployments require compatibility bridges. SDK downloads remain separately blocked, these packages use stdlib/local packages only.
- **Verification:** Actual eventstore append via a transaction fixture plus real httptest RPC-to-command and fact streams; validation ordering/contexts, commit/duplicate/queued receipts, conditional/local synchronous paths, queue failures, callback fresh-only/failure, malformed/oversized streams, cancellation/cleanup and serial callback completion. Run race tests and vet offline.
- **Resource budget:** One RPC delegate, command/fact in this owner, no edits outside assigned subtrees. Bounded body/stream/time limits and no automatic side-effect retries. Replan only on evidence of incompatible local APIs.

- [x] Implement command protocol, queue handoff and result composition.
- [x] Implement fact read/serial streaming and RPC adapters.
- [x] Pass offline race/vet and real HTTP acceptance fixtures, report APIs.

Review: command/rpc/fact combined race tests pass with actual loopback httptest, plus final conditional-conflict command regression and scoped vet. Dependency downloads disabled. EOF cancellation review fixed buffered JSON callback completion; callbacks see the total RPC deadline until explicit Close. No cloud calls or module edits.

## Plan refinement

- **Objective:** Prevent AfterCommit mutations from corrupting returned committed receipts on callback success or failure.
- **System fit:** Eventstore owns cloning and persistence; command detaches callback evidence before handing it to user code. Callback durability/outcomes remain unchanged.
- **Reuse and simplicity:** Use exported eventstore.CloneEvent, with no duplicate cloning implementation or eventstore edits.
- **Evidence and unknowns:** Callback and response currently share nested RawMessage, Path and Expected memory. Existing real Store plus memory transaction fixture exercises readback.
- **Verification:** Mutate every nested event field in callbacks returning success/failure; Verify returned and persisted events, compare original bytes, and replay aggregate. Run command race/vet offline.
- **Resource budget:** One clone at callback handoff, one table-driven regression, no native rerun or dependency downloads.

- [x] Detach callback events and add mutation regression.
- [x] Run offline race/vet and record results.

Review: mutation regression failed on both success/failure before the clone fix,
then complete command race suite and vet passed with downloads disabled. Returned
and persisted events retain exact pre-callback bytes and valid hashes; aggregate
replay succeeds. The implementation uses only shared eventstore.CloneEvent.

## Plan refinement

- **Objective:** Restore one outcome-aware Then callback, typed nested command RPC and trusted job claims as validated by Fable review.
- **System fit:** Command owns ordered outcomes and deterministic response composition; consumers gate side effects on COMMITTED. Transport owns explicit target addressing and authenticated child propagation; job consumes injected identity. Parent owns eventstore fingerprint and broader framework composition.
- **Reuse and simplicity:** Replace AfterCommit rather than add another callback. Reuse shared CloneEvent, rpc.Client and Framework.Authorize. Stable child IDs derive from parent, target, command and operation selector; no hidden retries or credential policy.
- **Evidence and unknowns:** Reviewed Fable triage and current callers. Current fresh-only callback loses deterministic responses on duplicate; jobs lack claims. Delegated credentials must establish context/claims at the receiver, never through body fields.
- **Verification:** All callback outcomes including empty, mixed, queued and errors; cloned mutable receipts and persistence; actual two-service httptest nested identities/trace/auth, replay, child IDs, uncertain sends, context cancellation; trusted job claims despite forged body. Scoped race and vet using resolved readonly module.
- **Resource budget:** Command/job subtrees plus new transport command client/tests and agreed job injection lines only. No eventstore/module/wallet edits, cloud operations or durable-action framework.

- [x] Replace callback contract and update callers/tests/docs.
- [x] Implement child RPC and trusted job identity with real HTTP checks.
- [x] Pass race/vet and hand off API/evidence.

Review, outcome-aware contract: command race passed 1.402s, job race passed
1.685s, transport race passed 3.147s; scoped vet passed after concurrent gateway
and view edits became coherent. Added actual two-service HTTP child identity,
trace and authentication tests, uncertain queue callback suppression, all-outcome
Then response reconstruction, cloned Input/Event mutation regression, and trusted
job claims despite forged body. Go1.27.4, readonly module, downloads disabled.
Parent owns full generic/native acceptance and module-wide gates.

## Plan refinement

- **Objective:** Prevent actors from reserving each other's generated event keys while retaining same-actor request-ID retarget conflicts and explicit key overrides.
- **System fit:** Framework supplies trusted context/claims. Command namespaces generated keys by actor; eventstore still owns stable fingerprint comparison and immutable committed audit claims. Child request IDs remain unchanged.
- **Reuse and simplicity:** Reuse eventstore canonical encoding and Digest. Prefer canonical Context.principal; otherwise select only effective transaction claims iss/aud/sub. Root and volatile token fields never enter generated key identity.
- **Evidence and unknowns:** Current default keys use only request ID/index and are globally squattable. Parent already verified shared fingerprint token-refresh behavior. The parent test command hit an approval quota; do not reroute or retry that blocked command.
- **Verification:** Real Store fixture tests independent actors using the same request ID, same-actor retarget conflict, refreshed-token duplicate, principal preference, explicit-key override and child request identity stability. Formatting/plan checks now; test execution awaits parent verification authority/quota resolution.
- **Resource budget:** Command subtree only; no eventstore/framework/module changes, new dependencies or live operations. Preserve one fingerprint owner rather than reimplement token policy.

- [x] Add actor namespace and owning regression fixtures.
- [x] Record permitted verification and notify parent of remaining checks.

Implementation ready: default event-key protocol now includes canonical actor
namespace (principal preferred, otherwise iss/aud/sub), request ID and index.
Explicit keys and child request IDs are untouched. Added real Store fixture actor
separation/retarget/refresh/override tests and an external command test exercising
actual nested HTTP client identities and queued event keys. gofmt passed. Tests
not executed in this round because parent reported approval quota rejection;
no blocked test command was retried or rerouted.

Fresh verification after normal approval recovered: Go1.27.4 readonly-module
`go test -race -count=1 -timeout=3m ./command ./job ./transport` passed with command
2.007s, job 2.027s and transport 3.643s. Scoped vet passed, exit0. This includes
the new actor namespace and nested-child tests, not an earlier baseline. No native
fixtures/failpoints, cloud operations or dependency downloads were used. The prior
pending-test note is superseded by these executed results.
