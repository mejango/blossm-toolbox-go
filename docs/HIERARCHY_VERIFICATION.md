# Hierarchy verification, 2026-11-12

The final runtime revision is d45c8b4e036cf190b2aaccedc9efdfa78701f640.
Go 1.27.4 passed formatting, ownership/layer checks and 11 checker fixtures,
vet, complete unit/race suites, native PostgreSQL/MongoDB/Redis, module integrity,
generated configuration, build, vulnerability and actual CLI container gates.
Final Mongo native tests passed in 100.462s and PostgreSQL in 31.019s. The rebuilt
CLI container test passed in 1.063s; its image is
sha256:50319c91dbffd082feee35fd53202931b66414d4c8338b4d098bc67a6e25b6e5.
Both hosted CI checks also passed on this runtime revision; later documentation
commits have their own CI status.

Dia independently resolves this public revision with GOWORK=off, no replace and
no committed workspace. Its full checks, native flows and both container
lifecycles pass; see the paired application's HIERARCHY_VERIFICATION.md.

All 448 baseline named Go test/fuzz/benchmark declarations remain across the two
repositories, with five new retry regressions. The ledger retains 154 package
identities, 59 families, 422 JS source/test references and 310 prior assertion
descriptors. Counts establish retained references, not one-to-one semantic parity.

## Findings and recovery

The structural moves preserved production behavior. The first Claude review
found library tests depending on vendors and stale current docs. Cloud signing
composition tests now live under tests/signing; lib-only internal imports are
enforced for library tests too. Eleven checker fixtures and signing unit/race/vet
pass; a follow-up source review closed these findings.

Final native Dia verification then exposed an existing contention limit: five
known-aborted Mongo attempts exhausted 50ms of linear waits before a concurrent
winner's receipt became visible. This was not an uncertain-commit replay. A test
fatal also left a worker logging after teardown; the fixture now drains both
results before requiring all-success. The initial failing log is retained in Dia.

A separate behavior commit changes Mongo's default to ten total attempts with
half-to-full jittered exponential ceilings from 10ms to 250ms. Nine waits total
655–1310ms, excluding database work and cleanup. Explicit 1..100 budgets, no
final wait, cancellation, single-attempt View and unknown-commit non-replay remain.
Exhaustion still returns an error; success under unbounded contention is not
promised. Native fault tests force nine aborts then exactly one committed write.
The real concurrent Dia Mongo journey passed ten repetitions in 81.824s. A bounded
Claude review found no runtime defect and prompted stronger cancellation, jitter
and configuration-bound tests, all of which pass. Full gates were rerun afterward.

## Evidence and reproduction

Exact logs are in verification-logs/hierarchy. Files named final record the final
framework runtime; earlier logs record their earlier checkpoints. The source
manifest hierarchy-source.json covers current Go sources and gate inputs. Review
prompts, findings, costs and read bounds live under reviews/hierarchy-2026-11-12.
Historical source-split evidence remains unchanged.

Use the pinned fixtures in deploy/compose.integration.json and initialize Mongo
as shown in .github/workflows/go-verify.yml. Supply POSTGRES_URL, MONGODB_URI and
REDIS_ADDR for disposable localhost fixtures, then run:

```sh
GOWORK=off make fmt-check vet test race native native-redis modules config-check build
GOWORK=off make vuln
GOWORK=off make container IMAGE=blossm-toolbox:hierarchy
python3 docs/behavior-coverage.py --check
```

No deployment, live provider write or blockchain transaction was performed.
Public module/security-data access and localhost SDK fixtures are not live cloud
compatibility evidence. The new PR remains subject to final user approval.
