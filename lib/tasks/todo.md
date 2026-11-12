# Shared primitive extraction

## Plan refinement
- **Objective:** Give canonical JSON, event contracts and scalar query helpers bottom-layer owners without changing bytes, errors, authorization or store workflow semantics.
- **System fit:** Storage and higher services consume lib contracts; event store retains append/replay/block verification. Dia consumes public owners. Parent owns later folder moves and global dependency gates.
- **Reuse and simplicity:** Move current implementations/tests once. lib/canonical_json owns Canonical/Encode/Digest/Decode; lib/event owns shared records/interfaces/errors/clones; lib/query owns matching and existing scalar index terms. No aliases or duplicate algorithms.
- **Evidence and unknowns:** Current storage production imports only event primitives and internal query terms. Verify and VerifyBlock need no production storage caller and remain workflow owners. Test-only native contracts may consume workflows.
- **Verification:** Compile both modules including native-tag fixtures without running native databases, run focused unit/race/vet for lib/event stores/storage and relevant consumers, compare moved test names and inspect production imports. Parent runs global/native gates after source freeze.
- **Resource budget:** Exclusive Go-file extraction ownership across exports, bounded AST-based callsite rewrite, cached Go1.27.4 and temporary two-module workspace, no module edits or network effects required.

- [x] Extract primitives and tests.
- [x] Rewrite both modules and embedded Go templates.
- [x] Verify scoped behavior and dependency direction; report exact API mapping.

## Review

Both modules compile after the rewrite. Scoped lib/event-store/storage units and race tests pass; focused vet passes after converting eight newly external positional literals to keyed fields. CLI scaffold compilation and generated owner contracts pass (7.770s). All six relocated canonical/query tests remain present; block hash binding stays with workflow verification. Production go-list proves storage internally imports only lib/event and lib/query, and lib imports only stdlib/other lib. Native fixture execution remains parent-owned: no-test runs invoked environment guards and refused missing disposable DB settings; final validation uses compile-only binaries instead. No module changes.

API mapping: Canonical/Encode/Digest/Decode -> lib/canonical_json; protocol records/errors/interfaces, Aggregate, Predicate, CandidateReader, FrontierReader, Signer, Verifier, CloneEvent and exported CloneInput -> lib/event; Matches and former queryindex State/Predicates -> lib/query. Verify/VerifyBlock, Store, HistoryOptions/HistoricalAggregate and publication-size policy remain stores/event. All consumers and Go-embedded templates use owners directly without compatibility aliases.

Verification environment: GOTOOLCHAIN=go1.27.4, GOCACHE=/tmp/blossm-go-dia-cache, default GOMODCACHE=/Users/jango/go/pkg/mod, GOWORK=/tmp/blossm-independent.work. Workspace is temporary; neither module was modified.

Final native-tag compile-only gate passed for all Toolbox test packages (excluded no-test lib/event from the batch solely to avoid output-name collision with stores/event; it already compiled). Output binaries are under /tmp/blossm-extraction-native-compile. Dia native-tag no-test compilation also passed. No native database/failpoint work ran. No tests remain active; Go source ownership released to parent.
