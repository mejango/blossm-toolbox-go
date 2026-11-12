# Independent framework verification

Verified locally on 2026-11-12 with Go 1.27.4, `GOWORK=off` and no Dia dependency.
Runtime refactor commit: `0007d3d81d364b0c63dd8745df3c724f43b620b5`. The later
`0b43e4b605a817704def65929e770e35decf5170` changes only a native test fixture and
its plan record; Dia pins the identical production implementation at0007d3d.

| Gate | Result |
| --- | --- |
| Full unit and serial race suites | PASS |
| `go vet ./...`, final CLI scoped vet | PASS |
| Native PostgreSQL/Mongo race suite | PASS, including the large-block/chunk, rollback, restart and uncertain-outcome contracts |
| Native Redis | PASS4.567s |
| Formatting, boundary, module verify/tidy diff, CLI configuration/build | PASS |
| Generic scaffolds and independent non-Dia executable build/run | PASS |
| Final Linux CLI image and actual container execution | PASS |
| govulncheck v1.9.0 | No vulnerabilities found |

[Raw logs and hashes](separation-results.json) and the
[Go source manifest](separation-source.json) identify this checkpoint. An initial
parallel verification overloaded the permission-error and large-block fixtures.
The permission case now has a5s error-propagation budget while cancellation
retains100ms. The isolated large-block test reached final aggregate readback but
exhausted its shared60s budget, so only that complete >16MiB round trip has a
bounded3-minute fixture context. All integrity assertions and other deadlines
remain. Initial failures and final passing logs are preserved separately.

Dia's models, policy, HTTP routes, runtime and native application tests live in
its own repository. It resolved the public framework module without a local
replacement and passed its own independent and native gates; affected tests and
containers were rerun after the review's outbox ownership fix. See the paired
Dia `docs/SEPARATION.md` for exact scope.

The original154-package/59-family ledger remains intact with explicit owning
repositories. All original named tests are preserved or explicitly renamed;
application assertions moved with their owner. These are semantic mappings, not
a numerical claim of one-to-one JS coverage.

[Claude review and closure](reviews/separation-2026-11-12/README.md) record two
bounded read-only Fable5.2 reviews. The document publication finding is fixed and
proved against both native databases. No unresolved blocker was found in the
reviewed scope. No live deployment/provider/chain verification is claimed. The
paired PRs require final review and approval before merging.
