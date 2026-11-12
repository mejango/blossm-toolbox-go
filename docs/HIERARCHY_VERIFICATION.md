# Hierarchy refactor verification

This record covers the restored JS naming and downward Go dependency hierarchy,
not the earlier independent-application split or a live deployment.

Toolbox passes formatting, application boundary and layer checks, all eleven layer
checker fixtures, go vet, full unit and race suites, native PostgreSQL/MongoDB and
Redis contracts, module verification/tidy-diff, generated configuration and CLI
build. The pinned vulnerability checker reports no vulnerabilities. The rebuilt
Linux CLI container passes actual execution and its container integration test.

Native MongoDB passed in88.665s, including the large binary round trips.
The image is sha256:392eccbd0be8e19f4f5aacebe44e8136869470f4daadfc115b1fce404f4e7800.
Exact logs are in verification-logs/hierarchy; hierarchy-source.json records
source and gate hashes. Prior split evidence remains an unchanged snapshot.

All448 named baseline Go tests/fuzz/benchmarks across both repositories remain.
The154-package/59-family ledger preserves310 prior assertion descriptors and
422 JS source/test references. Counts prove retained references, not a claim of
one-to-one semantic equivalence. Source review independently compared moved
protocol declarations, cloning and canonicalization without finding a concrete
behavior change. The CLI generates imports from current owners.

Dia independent public-pin verification and the bounded Claude follow-up remain
pending at this source commit; their results will be recorded before review
completion. No new dependency versions or live cloud operations were introduced.

Claude review found a library test dependency gap and stale documentation paths.
The KMS composition fixture now lives in tests/signing; lib-only dependencies
are enforced for library test files too. All11 checker fixtures and focused
JWT/signing unit/race/vet pass. Production Go bytes match d21c5e8 exactly; the
previous full native/container results therefore cover the unchanged runtime.
The source manifest will be finalized with the completed independent Dia gates.
