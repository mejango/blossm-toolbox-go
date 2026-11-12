# Offline CLI and deployment artifacts

## Plan refinement

- **Objective:** Implement offline init/config validation/generation/test/build/deployment-plan commands, Cloud Run and ECS/Fargate HTTP artifacts, examples, container build and fail-closed CI. Own only cli/, cmd/blossm/, deploy/, Dockerfile, Makefile and .github/workflows after the completed capabilities inventory; do not edit go.mod or cmd/dia.
- **System fit:** Module github.com/mejango/blossm-toolbox-go Go1.26+; parent owns actual cmd/dia HTTP runtime. Strict JSON configuration is a deliberate internal redesign. Produce reviewable artifacts with explicit prerequisites and no cloud writes; dry-run is always offline. Config selects PostgreSQL/MongoDB and references secrets without embedding credentials.
- **Reuse and simplicity:** Use Go standard library JSON, flag, os/exec and templates, one shared configuration validator and deployment renderer. Native tools own compilation/tests; preserve their exit failures. No deployment SDK or general orchestration framework.
- **Evidence and unknowns:** Pinned Toolbox372d1 source inventory complete; JavaScript dry-run is intentionally changed to offline. Verify Go/container/tool versions against official primary sources. Runtime env contract must be explicit for parent integration; generated artifacts are not deployed or provider-validated claims.
- **Verification:** Unit tests cover config rejection, deterministic generation, offline dry-run and subprocess failure. Run gofmt/vet/test/race for owned packages, CLI example regeneration, build and container when available. CI requires native integration URLs plus vet/test/race/govulncheck/container/config checks and must fail when prerequisites are absent.
- **Resource budget:** Bounded stdlib implementation with deterministic fixtures; parent handles whole-module concurrency/integration gates. No root-module edits, credentials, cloud writes or broad source changes. Replan if runtime contract cannot align.

- [x] Implement strict config and deterministic deployment plans.
- [x] Implement CLI init/validate/generate/plan/test/build with failure propagation.
- [x] Add Docker/Make/CI, reproducible examples and documentation.
- [x] Verify owned packages and report integration limits.

## Review

CLI unit/race/vet checks pass, including real Go-test failure propagation and empty-PATH offline planning. Example/config and Compose validation pass. Scoped govulncheck reports no called vulnerabilities (one imported-package and14module advisories remain outside the analyzed call paths). Docker fetched the pinned builder and modules, then correctly failed because parent-owned cmd/dia did not yet exist; full container/native/whole-module gates remain parent integration work. No cloud writes, deployment, publication or go.mod edits.

## Plan refinement

- **Objective:** Complete retained CLI procedure scaffolding, authenticated job/replay invocation, role uploads, secret versions, queue lifecycle and cloud deployment execution code. User authorizes implementation and offline fixtures, not live execution during this task.
- **System fit:** Keep strict configuration and prepared-only default; explicit future --execute opts into runner/HTTP execution. Parent owns actual domain endpoints. No IAM grants, publication or live provider writes in verification.
- **Reuse and simplicity:** Reuse current Config/Prepare/Run, stdlib exec without a shell, exact provider argv, and a single injectable runner. Use idiomatic typed procedure templates in the same module, refuse overwrites. Read secrets from named environment variables only when executing, never serialize their values in plans.
- **Evidence and unknowns:** Confirm provider command shapes using primary CLI docs. Existing adapters supply runtime semantics; generated commands do not imply resources exist. Actual deployment waits for provider readiness where supported, but application/provider live health remains unverified.
- **Verification:** Offline runner fixtures assert argv/stdin and failure propagation for every workflow, no ambient executable fallback; local HTTP fixtures assert explicit bearer auth and errors. Scaffolded files compile in a disposable module fixture; parent dependency gate remains independent.
- **Resource budget:** Bounded additions within owned CLI/deploy paths, no root go.mod edits or provider mutations. Tests inject fakes directly; no need for user cloud credentials or operational permission.

- [x] Implement scaffold and job/replay clients.
- [x] Implement provider role/secret/queue/deploy plans and explicit execution.
- [x] Verify offline execution/failure fixtures and reconcile docs/statuses.

## Operational workflow review

CLI race tests and vet passed on Go1.27.1, including isolated provider/HTTP
execution and scaffold compilation. Inventory mappings now name the actual
workflow source/tests. Go1.27.4 remains the official selected CI/container pin
(feed rechecked 2026-11-12); public toolchain download approval and selected
toolchain/full-module gates remain pending. No cloud writes occurred.

## Plan refinement

- **Objective:** Reconcile all154 current capabilities and add explicit GCP developer-token acquisition without live execution.
- **System fit:** Inventory points to actual owners/tests and distinguishes source, owner-reported passing evidence and pending root gates; CLI uses existing injected runner. JWT/Bearer and trusted Chainpoint gateway are deliberate redesigns.
- **Reuse and simplicity:** Reuse strict Config, workflow plan and runner, with exact gcloud argv; AWS retains its existing SigV4 adapter instead of inventing bearer tokens.
- **Evidence and unknowns:** Inspect new framework/job/audit/cryptography/composite files; projection remains under active ownership until source arrives. Official gcloud reference confirms audiences flag. SDK dependency checks remain pending where unrun.
- **Verification:** CLI offline tests assert no runner by default, exact execution argv, failure/invalid output propagation and AWS refusal; validate all154 source/test mappings and statuses.
- **Resource budget:** Bounded source inspection and owned CLI tests, no dependency downloads, root module edits or provider execution.

- [x] Implement and test developer-token workflow.
- [x] Reconcile current ownership, scope decisions and pending gates.

Final reconciliation:154 actual owner directories and all mapped source/test paths
validated. CLI race tests and vet passed on Go1.27.1 with injected token acquisition.
No live execution or dependency download. Parent-reported tests, independently run
CLI tests, unrun cryptography/EVM SDK tests and active projection/composite work
are separately identified; whole-port completion is not claimed.

## Plan refinement

- **Objective:** Activate repository-root Go CI configuration for the nested blossm-go module, preserving unrelated workflows and making all required failures propagate.
- **System fit:** Root .github/workflows owns GitHub discovery; job defaults select blossm-go, setup-go cache selects blossm-go/go.sum, and existing Make gates remain the single verification owner.
- **Reuse and simplicity:** Move the existing owned Go workflow to the repository root with module paths and path filters. Remove the inactive duplicate rather than maintaining two copies.
- **Evidence and unknowns:** Root has no .github directory currently; nested workflow is not discovered. Go1.27.4 remains selected; no remote workflow run or toolchain/dependency download is authorized by this edit.
- **Verification:** Parse YAML locally, assert root discovery location, workdir/cache/path filters and all required Make gates; check no continue-on-error or masked failure. Remote run and full dependency gates remain pending.
- **Resource budget:** One root workflow and relevant docs/lesson updates, local static validation only; no remote actions, publication or cloud writes.

- [x] Install root workflow and verify path/failure contracts.

Root CI YAML/path/failure-contract validation passed. Workflow definition is
repository-discoverable; no remote run or passing full module gate is claimed.

## Plan refinement

- **Objective:** Generate and validate the AWS delivery-size contract: raw SNS-to-SQS subscription and queue MaximumMessageSize at least262144 for complete Publication cap245760 bytes.
- **System fit:** eventstore owns the publication cap; runtime owner confirms raw delivery plus77-byte eventId String attribute fits SNS262144. Deployment owns explicit QueueARN and reviewed queue/subscription artifacts, not live resource state.
- **Reuse and simplicity:** Extend existing strict Publication config and deterministic Prepare artifacts/review argv; leave ECS execution and IAM policies unchanged. No duplicated runtime cap enforcement.
- **Evidence and unknowns:** Runtime owner coordinated via agent message confirms Attempts unchanged, exact-cap accept/over-cap reject before SDK endpoint. Decoder compatibility with envelopes does not establish the size guarantee; raw delivery is required.
- **Verification:** Reject disabled raw delivery, missing/mismatched ARN and undersized queues; assert generated queue attributes/subscription argv and exact checked example. Offline-only tests, no provider calls.
- **Resource budget:** Owned CLI/deploy config/tests/docs only; no dependency download, live deployment or IAM changes.

- [x] Implement delivery config/artifacts and offline regression checks.

## Plan refinement

- **Objective:** Reconcile generated job/projection/composite adapters to their now-existing owning APIs without parallel execution rules.
- **System fit:** job.Procedure, projection.Engine/Scheduler and view.ExecuteComposite own validation, mutation ordering and scope; generated code only wires typed dependencies.
- **Reuse and simplicity:** Replace early custom Job/Projection/Compose implementations with typed constructors/delegation. Preserve one module and overwrite refusal.
- **Evidence and unknowns:** Current scaffolds predate new core owners and duplicate behavior. Existing disposable module compile test covers every template.
- **Verification:** Preserve generated compile tests and add owner-type/delegation checks in the generated fixture; run CLI race/vet offline, including config sizing regressions.
- **Resource budget:** Owned scaffold/tests/docs only, no core changes, framework additions or downloads.

- [x] Delegate generated adapters and verify their owner contract.

## Final review

AWS delivery config/artifacts and shared size-budget tests pass offline; generated
adapters compile and reject missing behavior through their actual owners. No
publication preparation commands were executed. Gateway generation now also
delegates to Framework rather than maintaining separate authentication behavior.

## Plan refinement

- **Objective:** Generate validated Dia/framework deployment settings using the concrete serviceapp and runtimeauth owners, with explicit process selection and separate identity/document namespaces.
- **System fit:** CLI owns strict configuration and offline artifacts; serviceapp owns exact routes and runtimeauth owns signing. Parent owns cmd/framework, image build and final gates. Provider writes remain unauthorized.
- **Reuse and simplicity:** Extend Config/RuntimeEnv/Prepare and existing examples, reusing secret references and deterministic artifact generation. No second gateway policy or signing implementation.
- **Evidence and unknowns:** runtimeauth/signing.go requires explicit JWT_SIGNER and exclusive local versus KMS settings. Framework owner supplied SERVICE, INTERNAL_SUBJECT, IDENTITY_DATABASE_NAMESPACE and PUBLICATION_MODE. Queue settings await its owner rather than invented environment variables.
- **Verification:** Offline provider/driver matrix validates process entrypoint, secret exclusivity, exact environment and namespace isolation; malformed config fails before plan generation. Run installed Go1.27.4 CLI race/vet with child compiler pinned.
- **Resource budget:** CLI/deploy and capability records only; no module, Makefile, Docker or parent verification edits. Queue integration is a subsequent dependent step when source is available.

- [x] Add explicit runtime/signing/framework configuration and exact container commands.
- [x] Add illustrative provider examples and offline validation/plan regression tests.
- [x] Verify and report scope, pending queue/process gates and no live evidence.

Runtime review: finalized runtimequeue environment integrated after owner source arrived. Go1.27.4 CLI race PASS7.110s and vet PASS; actual CLI build of all three binaries exit0. Provider/driver plans, signing exclusivity, required delivery isolation and example decoding tested offline. Parent owns fresh native/process/container and full verification.

Replay refinement: projection.RunID is now one explicit logical run across retries and pages, never a process-static config or per-message default. Remove static delivery fields/environment, use strict owning ProjectionReplayInput and ProjectionClient response validation, and update generated Scheduler call. Offline regression must reject missing/obsolete IDs before credential access and propagate mismatched/empty acknowledgments. No cloud execution.

Replay/peer review: full Go1.27.4 CLI race PASS9.391s, vet PASS. Generated Scheduler fixture compiles with explicit RunID; runtime config rejects obsolete static replay field/env and unsafe peer identities. Owning ProjectionClient validates echoed run outcomes. Final parent full gates and review remain pending.

## Plan refinement

- **Objective:** Close Claude3's missing internal-token issuer with explicit execution and actual replay HTTP proof; correct deployment evidence and verified Cloud Run invocation prerequisites.
- **System fit:** runtimeauth.Open and dia.JWT.Sign own local/GCP/AWS signing. CLI selects configured network/internal subject, bounded lifetime and offline preparation; serviceapp's pinned ServiceJWTVerifier owns acceptance. Parent owns trust-domain decision, native/full gates and review.
- **Reuse and simplicity:** Add one internal-token command and injectable opener matching the existing owner signature. Do not duplicate signing/key parsing or invent audience/subject overrides. Preserve default offline behavior and existing external provider-token command.
- **Evidence and unknowns:** Claude3 found no configured issuer for internal replay credentials. Official Cloud Run documentation confirms default IAM invocation requires Google identity distinct from application JWT. Existing image/build evidence supersedes old missing-source failures; live IAM remains unverified.
- **Verification:** Synthetic local and injected KMS signers; real CLI mint through runtimeauth.Open drives serviceapp replay over local HTTP, wrong audience/subject reject401. Check TTL, explicit execute, config/env mismatch, cleanup and failure propagation with no secret leakage. Full CLI race/vet; no live signer/provider requests.
- **Resource budget:** CLI/deploy and evidence records only, no runtimeauth/serviceapp/module/parent docs edits. Read official invocation docs once; keep IAM changes out of execution and flag trust decision separately.

- [x] Implement bounded internal-token issuance and offline plan.
- [x] Verify real minted replay authority and signer failure paths.
- [x] Correct deployment evidence and explicit invocation prerequisites.

Cloud Run refinement: parent explicitly authorizes prepared invocation-mode configuration. Require gcp.invocation application|iam with no implicit default; examples explicitly choose application, emitting invoker-iam-disabled=true while retaining protected application routes and ingress restrictions. IAM mode emits false and requires a separate Google invocation credential/edge. Both plans display mode-specific review prerequisites; no IAM/live change occurs during preparation. Regression verifies exact manifest and warning selection.

Claude3 owned review: Go1.27.4 full CLI race PASS6.986s and vet PASS. Actual CLI-issued local service JWT drives serviceapp replay with one injected root; wrongsubject/network401, shortTTL/config constraints and injected KMS failure/cleanup covered. Explicit CloudRun invocation modes tested against generated manifests and review flags. No live operation; parent whole-port/security/native/review gates remain separate.

Claude4 refinement: permit plain HTTP only for literal loopback IPs or localhost in the existing operation URL boundary, validate RPC base before credentials, and prove minted replay through a real loopback HTTP server. Add operator retry/redrive prerequisites without claiming provisioning. Official docs prove signature redaction for X-Serverless and gcloud token cases, but do not conclusively establish all Tasks Authorization forwarding; any framework/IAM guard must be described as unsupported/unverified composition, not a universal provider fact. No live validation authorized.

Claude4 review: full offline CLI race PASS6.337s and vet PASS Go1.27.4. Actual minted replay through plain loopback HTTP succeeds; malformed/remote HTTP targets fail before credentials. Framework GCP IAM is conservatively rejected as unverified composition, with official evidence limits explicit. Dia IAM with authenticating edge retained. Recovery/redrive prerequisites tested; no live operation. Owned source frozen for parent final gates.

## Plan refinement

- **Objective:** Make CLI config/build/scaffolds usable by an independent third-party Go application without Dia source or entrypoints, retaining offline execution, provider safety and all relevant negative checks.
- **System fit:** Config v2 owns explicit application package/binary/entrypoint, plain env and secret references. Optional database/publication/delivery blocks own generated infrastructure env. Optional generic peer identities preserve same-network target checks. Dia owns namespace/business-secret policy; security owner retains generic token signing.
- **Reuse and simplicity:** Extend existing strict JSON/Prepare/runner paths, remove application-name switches and hardcoded build targets, reuse Framework in gateway template. No compatibility aliases, domain replica or second transport. Signing optional with explicit issuer/audience/subject for token issuance.
- **Evidence and unknowns:** The exported branch currently embeds Dia, serviceapp and app-specific config rules. Other owners are moving application/security source concurrently; use acknowledged APIs and preserve their files. Parent will group package paths only after handoff.
- **Verification:** Preserve provider sizing/IAM/delivery/secret/URL guards and failure propagation. Compile generated adapters in an independent module and build a real small third-party app with no Dia commands. Reject unsafe packages/entrypoints/overrides. Verify optional infra absence emits no fictional config. Run local CLI race/vet once concurrent API owners compile; no remote action.
- **Resource budget:** Own CLI except internal_token files/workflows signer seam, generic deploy examples/docs only. Parent owns module/Makefile/Docker and application-specific config integration. Coordinate schema first and limit broad tests to meaningful consumer boundaries.

- [x] Implement strict generic schema and configured application build.
- [x] Preserve generic peer and provider security assertions, coordinate consumer-only rules.
- [x] Replace generic examples/scaffolds and prove third-party consumer build/generation.
- [x] Run scoped race/vet and report exact limitations and API handoff.

Independent application review: Go1.27.4 `go test -mod=readonly -race ./cli -count=1` PASS15.208s; `go vet -mod=readonly ./cli` PASS. Real temporary third-party cmd/hello builds/runs without Dia source and compile failure propagates; all typed scaffold owners compile in a separate temporary module. Config v2 optional infrastructure, env/secret collisions, peer target/identity checks, prepared-only provider plans and raw SNS/SQS sizing gates pass. Mencius confirms application namespace/secret/signing checks moved into Dia deployment/config_test.go and owns four Dia examples. Generic examples/templates use grouped imports; Avicenna reports focused token/replay race passes. No cloud writes or root module mutations. Parent owns full split-repository/security/native/container gates.
