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
