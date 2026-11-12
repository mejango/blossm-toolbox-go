# Standalone publication

## Plan refinement

- **Objective:** Publish the verified Go module and a separately buildable Dia application to the user-selected public repositories.
- **System fit:** Toolbox owns shared behavior and the Dia runtime package; dia-go owns an independent executable/configuration pinned to Toolbox. Existing JavaScript repositories remain references.
- **Reuse and simplicity:** Extract the existing Dia runtime without behavioral changes, use thin entrypoints, mechanically replace the module path, and adapt the existing CI to the repository root.
- **Evidence and unknowns:** Original verification and reviews are retained as historical evidence. Publication-specific checks cover extraction, module relocation and independent consumption; live cloud behavior remains unverified.
- **Verification:** Run package race/vet after extraction, full unit/race/vet after relocation, build containers, smoke the independent Dia entrypoint and verify exact public remote commits.
- **Resource budget:** Reuse installed dependencies and existing native fixtures; avoid domain duplication or a broad package split. Source publication is explicitly authorized, deployment is not.

- [ ] Verify and publish the standalone module and consumer.

## CI runner refinement

Initial public CI failed before tests because MongoDB 8.0.34 rejects the hosted
Ubuntu 24.04 kernel. Keep the verified database pin and run on Ubuntu 22.04, whose
current hosted image reports kernel 6.8.0-1070-azure. This changes the fixture host,
not database semantics or test coverage. Verify the next complete GitHub run.
Sources: https://jira.mongodb.org/browse/SERVER-125742 and
https://github.com/actions/runner-images/blob/main/images/ubuntu/Ubuntu2204-Readme.md.

## Plan refinement

- **Objective:** Make Dia a genuine independent third-party application and organize related Go packages/models into navigable capability folders, with no Dia source or dependency in Toolbox.
- **System fit:** Toolbox owns generic procedures/stores/adapters/security/transport/CLI. Dia owns application domain models, policy, services, routes, composition and its Dia-backed cross-service example. Dependencies flow only from Dia to the public framework. Original JS and verified workspace snapshots remain preserved.
- **Reuse and simplicity:** Move existing implementation/tests with their owners; extract reusable JWT signing and HTTP error facilities once before relocating consumers. Keep application policy out of generic tokens. Group Go packages without separately versioned modules or compatibility aliases. Preserve runtime behavior; no rewrite of storage or protocol.
- **Evidence and unknowns:** Inspection found hidden Dia dependencies in transport, JWT runtime/CLI and the document reference app. Resolve these explicitly; move the Dia-backed reference example with Dia rather than calling its identity integration generic. Reuse all current tests, and document prior evidence as historical. No live provider verification is claimed.
- **Verification:** Enforce Toolbox absence of Dia imports/code via an automated boundary check; compile/tests/race/vet both independent modules, native PostgreSQL/Mongo and container lifecycles, CLI generated configuration/build, dependency/security checks. Verify the application with only public framework exports; temporary development workspace must not hide private API access or enter committed go.mod.
- **Resource budget:** Delegate auth/transport extraction and application moves with disjoint ownership. Parent owns package grouping/build/config/module and composed gates. Local branches preserve published mains. Prepare paired reviewable PRs; no merge or source-split main update without approval under root AGENTS release rules.

- [x] Extract reusable security/HTTP boundaries and remove framework dependency on Dia.
- [x] Move Dia models/services/runtime/routes/tests and its identity-backed example into dia-go.
- [x] Group related Go packages/models and update all imports, CLI/build/configuration/docs.
- [ ] Add independent-consumer/dependency-boundary checks and run complete relevant gates.
- [ ] Prepare paired reviewable branches/PRs with exact evidence and coordinated dependency pin.

### Verification refinement: provider error fixture

The first regrouped full race run failed the permission-error fixture because its
100ms cancellation-test deadline also bounded normal local SDK setup/response.
The permission assertion is about preserving PermissionDenied, not a100ms SLA.
Use a5s bounded deadline for permission cases, retain the existing100ms deadline
and DeadlineExceeded assertion for cancellation cases, then rerun the full race
gate after concurrent native/build work settles. No adapter/runtime change.

### Large-block verification refinement

The isolated rerun reached the final aggregate read but exhausted the shared60s
fixture budget (body_integration_test.go:80), whereas the overloaded attempt
expired during block construction. This fixture appends100x180KiB events, builds,
reads and hashes a >16MiB block, reads its snapshot and replays the full root.
There is no60s performance contract. Give only this large round-trip fixture a
bounded3-minute context through a shared fixtureWithBudget helper; keep all small
fixtures at60s, transaction/cancellation/retry tests and data assertions unchanged.
Rerun the native suite and retain both earlier failure logs as evidence.
