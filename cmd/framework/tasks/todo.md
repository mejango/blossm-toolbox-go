## Plan refinement
- **Objective:** Ship a runnable ordinary HTTP generic framework service with separate identity/document stores, explicit local/inline/GCP/AWS modes, authenticated queue/replay delivery and deterministic cleanup.
- **System fit:** Compose serviceapp, runtimeauth, runtimepub and delivery adapters. Reuse current domain/store/transport owners. Cloud Tasks receives a separate exact Google OIDC delivery verifier; internal service APIs keep pinned service JWTs.
- **Reuse and simplicity:** Extract existing cmd/dia environment/database setup to runtimeenv without behavior changes before building the new executable. Delegate new runtimequeue SDK composition to existing delivery owner; no second provider protocol implementation.
- **Evidence and unknowns:** serviceapp.New is implemented and parent owns native composition tests. Queued bearer tokens can expire before retry, so GCP dispatch-time OIDC with issuer/audience/verified-email binding is required. No live cloud calls authorized.
- **Verification:** Test shared environment validation, mode/config rejection, HTTP server shutdown and actual executable composition via parent native/container gates. SDK queue fixtures exercise authenticated delivery, cancellation/ack and cleanup. Run focused unit/race/vet and source review before handoff.
- **Resource budget:** Own cmd/framework/runtimeenv and narrow cmd/dia extraction; runtimequeue delegated separately. No eventstore/module/Makefile/container/serviceapp edits. Read root AGENTS.md, workflow/ponytail/SKILL.md, docs/PLAN_REFINEMENT.md, tasks/lessons.md and latest tasks/todo.md already applied.

- [x] Extract shared environment/database owner with passing scoped checks.
- [x] Implement config and executable lifecycle/composition.
- [x] Integrate remote queue/replay workers and verify units/docs.

## Verification checkpoint
- Shared runtimeenv extraction passed race tests before new executable work (runtimeenv1.368s, cmd/dia1.646s).
- Current framework/runtimeauth/runtimeenv race checks passed3.573s/2.303s/1.490s; scoped vet passed. Parent owns actual native/container composition and full gates.
- Read-only review found no runtime lifecycle/routing blocker. It found and prompted correction of first-hop delegated access handling at the shared Dia state boundary; focused updated checks running separately.
- Provider runtimequeue implementation and fixtures are owned by Faraday, including dispatch-time Google OIDC and cancelable SQS polling. No live cloud calls.

Final scoped source checks: framework/runtimeauth/runtimeenv race3.573s/2.303s/1.490s; full Dia race84.619s; latest first-hop delegated-context regression1.662s. Scoped vet passed. Source review confirmed first-hop correction and no remaining blocker in inspected composition/state-boundary scope. Parent owns native/container and Claude follow-up evidence. No live provider operation performed.

## Plan refinement
- **Objective:** Compose optional outbound CommandClient and incoming delegated peer verification in the executable, with no copy route when outbound is absent.
- **System fit:** Use transport's delegated authorizer/verifier and live Dia state owner. Target audience and logical client target must be the destination network/service, while the minted peer subject identifies the issuing service. Parent serviceapp owns route policy and copy Main.
- **Reuse and simplicity:** Reuse rpc.New and the existing contextual JWT signer. Configure endpoint/target explicitly and pin incoming peer; do not forward user body credentials or add JWT fields. Keep replay files with Herschel.
- **Evidence and unknowns:** Delegated transport APIs are implemented and tested by Mencius. Shared Dia first-hop access and subsequent service state checks pass. Parent is adding serviceapp.Config fields; validate concrete API before wiring.
- **Verification:** Config rejection, fixed peer/audience issuance and actual loopback CommandClient credential flow, stale-session denial, plus scoped race/vet. No live provider or remote application calls.
- **Resource budget:** cmd/framework source/tests/docs only; parent owns application/Main and full native/container review. Required resource paths remain those recorded above.

Outbound composition result: cmd/framework race tests pass in1.862s including configured real loopback delegated CommandClient, fixed peer/target/audience/context/transaction, revoked-session no-dispatch and unsafe/partial configuration rejection. Optional peers are wired to serviceapp.Config.Outbound/Delegated; absence leaves copy route unmounted. No replay files changed. Scoped vet pending at this update.

Final outbound scoped vet passed. No tests active. Parent owns consolidated native/container verification and independent Claude follow-up; source and documentation are ready for that gate.

## Plan refinement
- **Objective:** Make confirmed terminal delivery rejections observable in the runnable framework with safe envelope identity and reason fields.
- **System fit:** Delivery owns durable confirmation and callback timing; serviceapp passes the hook before constructing HTTP handlers; cmd/framework supplies the structured logger. Boole separately owns internal-token issuance so operators can inspect recorded IDs.
- **Reuse and simplicity:** Use Consumer.OnRejected through Config.OnRejected, never a second rejection classifier or post-construction mutation of a copied HTTP consumer. Keep the existing network trust-domain policy.
- **Evidence and unknowns:** Claude3 found missing operator visibility. Final frozen contract calls an error-returning hook only after a newly confirmed record; duplicate lookup suppresses it, observer failures remain separate from acknowledgment. The runtime logger returns nil; no durable log delivery or live KMS claim.
- **Verification:** Check safe structured log fields and callback wiring with scoped unit/race/vet. Delivery owner tests confirmed-only invocation and unchanged acknowledgment behavior.
- **Resource budget:** One small cmd logging hook plus tests/docs; no unrelated runtime, provider, replay or signing changes.

Frozen-contract runtime verification: cmd/framework and serviceapp scoped race tests passed (2.100s and1.554s), and scoped vet passed. The logger returns nil and emits only envelopeId/reason plus standard logging fields; the synthetic credential context is absent. Config passes the callback before HTTP consumer capture. Delivery owns fresh-record timing, duplicate suppression and ObservationError tests. No native or cloud tests were run for this change; no tests remain active.

Claude4 refinement: use one explicit JSON logger constructor shared by runtime startup and the existing safe-field test, matching cmd/dia runtime logging. No callback contract or startup error classification changes. Verify scoped framework race/vet, then freeze source.

Claude4 runtime JSON follow-up complete: production runtimeLogger(os.Stdout) and existing credential-canary/JSON-field test use the same constructor. Scoped framework race passed1.634s and vet passed. Source frozen, no tests active.
