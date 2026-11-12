# Dia implementation

## Plan refinement
- **Objective:** Complete retained core/system/comms Go source and executable tests, preserving wallet ownership and making no cloud writes.
- **System fit:** Dia consumes the parent event store, owns domain validation and authorization, returns verified persisted outcomes, and exposes explicit retryable provider operations.
- **Reuse and simplicity:** Reuse eventstore protocol and standard Go crypto/HTTP/SMTP; use typed domain reducers and a small consumer store interface.
- **Evidence and unknowns:** JavaScript inventory is complete; parent Aggregate/Predicate API is being implemented concurrently. Existing source defects are corrected, not compatibility requirements.
- **Verification:** Run package tests and race tests, including actual ES256 verification, failed authorization, challenge reuse, atomic domain changes, provider errors and retries.
- **Resource budget:** Parallel disjoint auth/adapters, models/reducers and system/comms work while core session/account/group flows are implemented locally; integrate against the actual parent API.

- [x] Session creation, authentication and revocation.
- [x] Typed reducers and complete core operations.
- [x] System and external comms protocols.
- [x] Integration tests and source contract documentation.

## Review corrections
- Session creation keys are bearer creation secrets: require 32 random bytes and never use an anonymous retry to recover upgraded state. Reject stale token identity context against current session state.
- Event timestamps must reflect real preparation time. Recovery must use persisted inputs, never fabricated epoch metadata or a changed revision presented as an identical retry.

## Verification record

- Full `go test -race -tags integration ./dia -count=1` passes in 50.992s with explicit disposable PostgreSQL and Mongo replica URLs. Native journeys cover session, concurrent same-key proof, account/password, scene/group/role, node/key/connection/update-token revocation and unauthorized deletion.
- Parent HTTP `TestDiaSessionFlow` passes both stores: start, durable publication, protected projection, termination and revoked access.
- Final query-boundary change uses scalar candidate predicates plus exact-reference post-filtering; focused merge/group/dynamic-role race tests pass in 6.315s.
- `go vet ./dia`, gofmt and whitespace checks pass. Existing JavaScript Dia WIP remains intact; no go.mod edits, remote cloud/provider writes, deployment or publication.
- Independent security review identified proof-attempt reuse, consumed-proof recovery guessing and role grant/expansion races. Narrow fixes and adversarial tests passed; reviewer reported no remaining blockers within inspected scope.

Provider acceptance is not end-user delivery. Explicit comms retries can duplicate delivery; no live SMTP/Twilio/Fanout claims. Node network labels are metadata, not verified DNS ownership. No generic durable action scheduler or resumption protocol was introduced.

## Plan refinement
- **Objective:** Repair HTTP durable comms wiring and preserve explicit partial recovery results on failure, without cmd edits.
- **System fit:** Routes use Dia's existing encrypted intents; HTTP errors retain challenge tokens/references without claiming delivery or commitment.
- **Reuse and simplicity:** Reuse SendMailCommand/SendSMSCommand and existing status mapping; one error wrapper supplies narrowly typed partial results.
- **Evidence and unknowns:** Current routes bypass intents and discard partial outputs. Provider errors can occur after preparation; a returned reference alone is not commit evidence.
- **Verification:** Native PostgreSQL/Mongo real HTTP tests exercise provider failure, preserved challenge token/ref, explicit retry and changed-key-content rejection.
- **Resource budget:** One route patch and one native test file; parent owns shared HTTP envelope hook. Startup logging review remains source-only.

- [x] Wire durable routes and partial result wrapper.
- [x] Prove native HTTP recovery and report source-traced cmd risks.

HTTP recovery verification: `go test -race -tags integration ./transport -run TestNativeHTTPRecovery -count=1` passes both native adapters in 7.658s. Parent owns the shared error envelope and startup logging correction. No cmd edits or live provider calls.

## Plan refinement
- **Objective:** Make subject-based RemoveRoles retries identify the exact committed removal despite changed current roles, including self-removal.
- **System fit:** Authenticate current session first, bind requested principal/roles/subjects and caller identity to an immutable atomic receipt; acknowledge only that operation without reapplying effects or restoring authority.
- **Reuse and simplicity:** Extend existing operation receipt with optional explicit request semantics; reuse ChangeRoles authorization and prepared-event construction through one private implementation.
- **Evidence and unknowns:** Current subject expansion drifts after removal and fails payload matching. Existing atomic store commits receipts and domain effects together; no general action resumption is needed.
- **Verification:** Native tests on both stores cover identical retries, self-removal, concurrent retries, altered requests, other callers, regrants and revoked sessions; preserve existing Dia and HTTP regression checks.
- **Resource budget:** Narrow owning changes in identity/operation plus native regression tests and docs. No transport/cmd edits or cloud calls.

- [x] Bind removal request semantics and recover exact committed effects.
- [ ] Verify native retry and authorization boundaries.

## Role-removal verification
- Focused native/race role tests passed both stores in 8.245s; full Dia/transport native/race passed in 58.185s/10.836s before final receipt-root refinement.
- Independent review found target-scoped receipt lookup allowed a changed authorized target under the same request key. Receipt identity is now session+request-key scoped; target remains in the canonical request, and a native regression covers changing an authorized target.
- Final source compiles with integration tests enabled and no tests selected. Parent owns the final serialized native suite to avoid Mongo fault-injection overlap; no more broad/native runs from this thread.


## Receipt-error reconciliation verification
- Any Append error now triggers one exact receipt lookup. Matching digest resolves success; mismatched digest conflicts; absent/unreadable/canceled lookup preserves the original append error. No replay occurs.
- Focused non-native race checks passed in 3.908s for reconciliation and affected visible-commit expectations, then 1.255s for the 20-case event/request digest matrix and the comms unreadable-receipt boundary. The latter proves no provider call until preparation is established and explicit recovery sends once.
- Parent reviewed the patch and reported 753 passing unit cases. The frozen pre-fix native diagnostic passed 10/10; retry-budget exhaustion was not reproduced and Mongo source was unchanged.
- Parent owns the final consolidated native run, still pending at this documentation update. No additional tests were run by this thread.

## Plan refinement
- **Objective:** Replace concrete JWT private-key coupling with contextual local/GCP/AWS signing, pinned public verification and executable runtime composition without live calls.
- **System fit:** Dia owns ES256 signing/verification contract; existing cloud adapters own provider protocols; cmd/dia selects/configures the signer and closes GCP clients. Request cancellation reaches every issuance path. Verification remains local against configured trust.
- **Reuse and simplicity:** Reuse cloud SignES256 adapters behind one consumer interface and a key-bound adapter, with a local P256 signer. Remove JWT.Key and change Sign to accept context; update consumers together rather than retain a bridge.
- **Evidence and unknowns:** Independent Fable review identified a real composition gap. Existing cloud wire tests validate protocol conversions, but Dia must prove returned signatures match its pinned verify key and failures/cancellation do not issue tokens.
- **Verification:** Unit/race/vet, provider SDK fixtures through actual adapters into JWT, wrong-key/rotation/malformed signature tests, request-context propagation and runtime config tests. Parent updates out-of-scope transport/integration/wallet fixtures and owns full native/container gates; no provider calls.
- **Resource budget:** Narrow auth/signing and context propagation edits, explicit runtime provider selection, focused checks then independent read-only review. No eventstore/module/transport edits.

- [x] Implement contextual signer and update Dia callers.
- [x] Compose local/GCP/AWS runtime signing with pinned verification.
- [x] Prove signing and configuration boundaries and report API to parent.

Signing composition refinement: parent generic runtime also needs the exact selector. Move the already-tested cmd selection and tests into `runtimeauth.Open(ctx, network) (*dia.JWT, func(), error)` before further behavior changes. Both binaries reuse this single environment/SDK lifecycle owner. Pre-extraction cmd runtime-selection race checks passed in 2.193s; repeat the moved tests after extraction. Parent owns cmd/framework.


Signing verification checkpoint: focused Dia JWT/provider-adapter race checks passed in 6.168s; runtime selection passed before extraction (cmd/dia 2.193s) and after extraction (runtimeauth 2.306s). Dia vet passed. Independent read-only review found no blocking trust-binding, lifecycle or cancellation issue, noting cooperative signer cancellation and no live provider evidence. Full Dia unit/race plus runtimeauth/cmd vet remain active; parent owns the independent Claude follow-up and consolidated native/container gates.


Final signing handoff: full `go test -mod=readonly -race ./dia -count=1` passed in 247.553s on Go1.27.4 during parallel workspace verification. `go vet -mod=readonly ./dia` and `go vet -mod=readonly ./runtimeauth ./cmd/dia` passed. Shared `runtimeauth.Open` tests passed after extraction. Source is stable, no tests active, and no live provider/native calls were made for this signing task. Parent owns remaining full-application gates and Claude follow-up review.
