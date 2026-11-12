# Repository ownership correction

The initial publication below is historical. The independent-application refactor
moves all Dia models, services, runtime, HTTP policy and application tests into
dia-go. Toolbox owns only generic framework facilities and has no Dia dependency.
See README.md and docs/ARCHITECTURE.md at this revision for current ownership.
Fresh refactor verification is recorded separately; initial publication results
are not evidence for later source changes.

# Standalone repositories

`github.com/mejango/blossm-toolbox-go` is the owning shared module. Its reusable
`diaruntime` package contains the existing Dia server lifecycle and wallet wiring;
`cmd/dia` is a thin reference entrypoint. Domain and application libraries remain
in this initial shared module, as selected for the original port.

`github.com/mejango/dia-go` is an independently buildable application repository,
pinned to a specific Toolbox commit. It invokes `diaruntime` rather than carrying
a divergent copy of the domain or runtime. Neither repository claims a full
physical separation of every Dia library from the framework in this first export.

The previous workspace's full test logs, source manifests and Claude reports
remain historical evidence. Their `blossm.local/backend` package names and local
paths identify that tested snapshot. Module relocation changes import identities,
not the persisted event protocol. This publication has separate verification.

The behavioral ledger is a static reference map. To refresh it against preserved
JavaScript sources, set `BLOSSM_REFERENCE_WORKSPACE` to the original workspace
(containing `tasks/worktrees/go-reference` and `dia`) and run
`python3 docs/behavior-coverage.py --refresh`. Ordinary Go builds/tests do not need
those historical repositories. Do not relabel old verification as a new run.

Source publication does not deploy infrastructure or create a software release.

## Publication checks

The extraction passed scoped race/vet before module relocation. After relocation,
full unit and race suites each passed1,008 results, full vet/module verification
and tidy-diff passed, and both Linux HTTP container lifecycles passed6.139s.
See [machine evidence](publication-results.json). This is distinct from the earlier
workspace native verification; GitHub CI runs the full native gates at repository
root. No credentials or host binaries are included. The original MIT notice is
retained. Source publication does not imply live deployment.
