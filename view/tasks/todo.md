## Plan refinement

- **Objective:** Implement shared scoped view reads, bounded pagination, compositional typed mutations, atomic batches, source-version guards and safe CSV/body formatting under view/ only.
- **System fit:** Consume eventstore.Backend/Reader/Tx, Matches and Canonical. Storage keeps transactions and versioned projection persistence; trusted composition supplies owner constraints. Request handlers/authentication and multi-source reconciliation remain parent owners.
- **Reuse and simplicity:** Use ordered typed operations over canonical JSON, standard encoding/csv, and one narrow optional Projections reader interface coordinated with the storage agent. No fencing or permanent receipt protocol. Keep replacements distinct from patches requiring an expected base version.
- **Evidence and unknowns:** Reference docs/capabilities.md and pinned lib/{view_store_mutations,compose_update,json_to_csv}, generic view get/put/delete and composite-get. Current Projection has source root/version/body/tombstone but no historical context/group/trace timestamps; document explicit mapping rather than fabricate fields. Adapter list implementation is delegated to existing storage owner.
- **Verification:** Table tests cover strict types, dotted/filtered nested arrays, all six operators, alias safety, query ownership, 50/default and 10000/max pages, version conflicts, batch rollback, formatting and spreadsheet formula defenses. Native PostgreSQL tests prove commit/rollback/concurrent retry and optional list integration without missing-DB skips.
- **Resource budget:** One cohesive view package with focused pure/native tests and consumer-owned projection listing. No root module, storage DDL or shared protocol edits. Replan if listing signature cannot be supplied; no full-root fallback silently pretending scalable view listing.

- [x] Implement typed mutation composition and regression tests.
- [x] Implement scoped store, batch and list using the shared backend.
- [x] Implement view/CSV formatting and capability mapping.
- [x] Pass focused ordinary/race/vet and native PostgreSQL integration checks.

## Review

Implemented only view/ with Apply, scoped Store.Get/List/Batch, ProjectionReader seam, Format/CSV and the reference ownership map in README.md. Storage owner supplied the structural projection listing methods separately; no protocol or module files changed here.

Verification: Go1.27.1, ordinary race tests and vet pass; the final PostgreSQL native suite passed in10.164s on parent localhost55439, including five real-DB scenarios and repeatable pagination while another transaction commits. The explicit missing POSTGRES_URL control fails with no skip. Twenty top-level tests cover pure and native contracts, including10kpagination, six operators and spreadsheet safety.

Focused independent review identified Mongo View callback replay retaining old Page results. Fixed by resetting within the callback, with a regression using distinct aborted and committed snapshots. No other actionable review findings. Final ordinary/race/vet verification passed after this fix; targeted PostgreSQL native pagination/repeatability rerun also passed.

Limits: List counts/filtering scan source-scoped candidate pages; order is ID-only. Native proof here is PostgreSQL, with Mongo retry behavior covered by the replay fixture. This does not claim HTTP/RPC/CLI wiring, arbitrary Mongo operators, historical metadata absent from Projection, or universal multi-source/exactly-once semantics.

## Plan refinement

- **Objective:** Fill generic view-composite execution with a typed main(query, scope, named readers) API in composite.go, preserving callback responses and scope/cancellation/error propagation.
- **System fit:** Application authentication supplies Scope, component adapters reuse Store.Get/List, and composite execution binds that authority before invoking main. Transport server/RPC remain separate inventory owners.
- **Reuse and simplicity:** Reuse Scope.validate/owns and Store reads, with one two-method component interface and a bound named-reader collection. No service registry, dependency resolver, fan-out engine or new persistence protocol.
- **Evidence and unknowns:** Pinned procedures/view_composite/get invokes main with query/context and auth-bound viewsFn and propagates its response/error. Existing view/ only exposed underlying reads, so application composition guidance did not implement this generic capability.
- **Verification:** Typed query/response roundtrip, multiple named reads with actual Store filtering, component/main errors, missing component, canceled request and cancellation during reads, defensive scope copies and query attempts to select another owner. Race tests/vet for view/.
- **Resource budget:** Scoped implementation/tests/docs only under view/, no protocol/module/storage edits or network calls. Use in-memory reader fixtures for composition because native Store filtering is already verified.

- [x] Implement bound named component readers and typed execution.
- [x] Verify scope integrity, cancellation/error propagation and response shape; update capability mapping.

### Composite review

Implemented composite.go and seven focused tests in composite_test.go. Race-enabled composite tests and go vet ./view pass. Tests use BackendComponent with real Store.Get/List filtering over deterministic reader fixtures, proving typed response/query roundtrip, owner/tombstone filtering, forged query scope rejection, detached auth bytes/registry, named component/main errors, cancellation and reader lifetime. Reference mapping updated in view/README.md; root inventory remains parent-owned. No storage, protocol, module or HTTP/RPC files changed.

## Plan refinement

- **Objective:** Add the approved explicit Tombstone mutation to prevent deletion-before-create resurrection while preserving ordinary Delete missing no-op.
- **System fit:** Tombstone carries full owner-scoped Body and source Version and uses existing PutProjection Deleted=true/version guards; projection producer chooses this operation explicitly.
- **Reuse and simplicity:** Reuse object canonicalization, owner matching, existing-root validation and atomic Batch. No new storage schema or coordination protocol.
- **Evidence and unknowns:** Ordinary missing Delete currently writes nothing, allowing older creation later. That behavior remains valid for remove semantics but cannot represent ordered projection deletion.
- **Verification:** Native missing tombstone then older create, stored tombstone/version/body, current-root conflict, invalid owner/body/ops, newer legitimate recreation and stale tombstone rejection; ordinary missing-delete behavior stays tested.
- **Resource budget:** Small view-only field/validation/write change plus focused unit/native checks. Communicate exact schema to projection owner; no module or storage changes.

- [x] Add Tombstone and regression tests; verify and hand off schema.

### Tombstone review

Added Mutation.Tombstone with json key tombstone. Full Body is mandatory, canonical object validation and owner scope checks happen before writes, Delete/Operations combinations reject, and existing root/version guards remain authoritative. Ordinary missing Delete remains a no-op. Sent exact schema/semantics to projection agent01a1cdcc-bc15-75b1-bf14-d449dffd8eac.

Race-enabled unit validation and vet pass. Native TombstoneBeforeCreate, ScopedLifecycle and AtomicFailureAndConditions pass on parent PostgreSQL55439: missing tombstone5 blocks create4, duplicate/stale tombstones do not apply, cross-root/owner takeover rejects, newer state6 recreates legitimately, and ordinary missing delete100 does not block create1. No storage/protocol/module edits.

## Plan refinement

- **Objective:** Add view.ErrInvalid for deliberate request validation and operation failures, allowing HTTP400 without relabeling backend/configuration failures.
- **System fit:** View owns caller-input classification; transport maps errors.Is(view.ErrInvalid) separately. Trusted Scope, storage, commit/cancellation and source-version conflicts retain existing semantics. Parent owns response preencoding and HTTP mapping.
- **Reuse and simplicity:** Wrap only Get/List/Batch input gates and pure operation validation. Reuse Canonical/Matches. Split pure body parsing from operation application so corrupt stored patch bases remain backend/data errors rather than client errors.
- **Evidence and unknowns:** Read-only probes reproduced negative versions and object predicates as HTTP500. Existing generic view errors lack a sentinel. Formatter/composite configuration is trusted code and is not automatically request validation.
- **Verification:** errors.Is checks for malformed predicates, identities/versions/batches, bodies/operations and computed operation overflow; injected backend failures at begin/read/lock/write/commit remain exact original causes; corrupt stored bases and invalid trusted scopes never become ErrInvalid. Ordinary/race/vet plus targeted native rollback/version tests.
- **Resource budget:** View-only code/tests/docs with small shared validation helper; parent/gateway owner maps the sentinel. No transport, storage, protocol or module edits.

- [x] Add explicit typed input validation and classification regression tests.
- [x] Verify backend error preservation and coordinate HTTP mapping.

### Typed validation review

Added ErrInvalid at caller validation gates only. Stored patch body parsing stays
separate from operation application, so malformed stored state remains an
internal/data error. Regression tests preserve backend causes at begin, read,
lock, write, commit and listing, including cancellation/conflict/uncertain errors.
Existing invalid-operation cases now require the sentinel, including overflow.
Parent confirmed HTTP mapping and response preencoding; new isolated transport
boundary test verifies predicates, negative versions and incompatible operations
return 400, while backend errors remain sanitized 500. No existing transport tests
or implementation files were edited by this task.

Verification: go test -race ./view passes; go vet ./view passes; go test -race
./transport passes with authorized local httptest listeners; native PostgreSQL
ScopedLifecycle, AtomicFailureAndConditions and TombstoneBeforeCreate pass on
55439 (3.021s). Initial combined transport run could not bind in the sandbox;
rerun with local listener authorization passed. Plan checker passes.

## Plan refinement

- **Objective:** Restore retained typed range/date/text/sort query behavior, authority group union and shared-backend named view isolation without changing the storage protocol.
- **System fit:** Auth supplies common Owner AND optional OwnerAny groups; client filters only narrow. Store owns namespacing and query evaluation; gateway supplies trusted registration name; producer adapters use the same PhysicalID helper. Existing transaction/version/tombstone and composite rules remain authoritative.
- **Reuse and simplicity:** Reuse Matches validation/equality and existing dotted CSV traversal for new typed comparisons; no Mongo operator JSON or new database engine. Encode name/id at view boundary and filter/decode listing pages; no schema changes.
- **Evidence and unknowns:** Fable triage and JS generic/get tests demonstrate numeric/date parsing, group union, text, sort and pagination. Mongo text ranking/regex, bootstrap bypass and historical trace/time headers require explicit separate decisions, not silent equivalence claims. Typed text uses literal case-insensitive substring, explicit body paths and exact ID fallback.
- **Verification:** Unit/race tests for strict types, arrays, date instants, sort ties/missing values, filtered count/offset/10000 cap, group AND/OR and defensive copies, named collisions/read/write/tombstone/list isolation and gateway authority. Native fixtures follow parent availability; no external DB access.
- **Resource budget:** Only view/, transport/view_gateway.go and view route tests. Parent informed of proposed Name/PhysicalID API; no storage/protocol/module changes. Scan-based query evaluation is explicit and bounded output remains10000.

- [x] Implement and verify authority groups and named namespaces.
- [x] Implement and verify typed query behavior and gateway boundaries.
- [x] Record JS assertion mapping, limits and verification handoff.

Query mutation refinement: parent explicitly requires MutateWhere within one
Backend.Update, with a caller-selected maximum of1..10000 rows. Pagination is
rejected for mutation selection; exceeding the maximum aborts before callbacks
or writes. Reuse extracted scan/validateBatch/mutateBatch executors, bind selected
ID/root/Expected version, and preserve snapshot predicate semantics without
claiming serializable phantom exclusion. Refactor-only view tests passed before
adding this capability. Test late operation failure rollback and owner/group
selection, not merely the happy path.

### Retained query and namespace review

Added Store.Name/PhysicalID and BackendComponent.Name without storage/schema
changes; generic gateway always supplies the registered name. OwnerAny union is
ANDed with common Owner and exact source scope, with detached authority copies.
Typed numeric/date bounds, literal case-insensitive text, typed stable multi-key
sort and complete query pagination are implemented and mapped assertion-by-
assertion in README. Explicit scalar group IDs and independent Matches array
semantics remain documented; no correlated object elemMatch is implied.

Extracted existing scan/validateBatch/mutateBatch executors first and passed
view tests before adding MutateWhere. Selection uses one Backend.Update, rejects
pagination, rejects over-budget before callbacks/writes, binds expected versions
and selected identities, and preserves late-error rollback. Predicate membership
is snapshot-scoped, not a serializable phantom promise.

Gateway adds scoped /view-bootstrap/{name}, Empty/Updates/CSV configuration and
Page.Next/Updates/Empty. Bootstrap never bypasses owner checks. Next preserves
all typed fields. CSV buffers Format/CSV before headers and checks cancellation;
formulas are shielded. Existing fixture IDs migrated to the named physical ID
helper. Parent notified to mount the new bootstrap prefix in Framework; no
framework/auth/module/storage source files edited by this task.

Go1.27.4 final full view race passes8.242s; scoped transport TestView race passes
1.852s. Earlier Go1.27.1 view race passed36.395s and refactor-only view suite passed
3.398s. Initial transport attempts hit concurrent source/import changes or local
listener sandbox restrictions; final scoped run passed with authorized localhost
listeners. New native tests cover both PostgreSQL and Mongo named isolation and
MutateWhere rollback/budget through the same assertions; execution awaits parent
fixtures, not claimed from source/build-only verification.

Final check: Go1.27.4 vet ./view ./transport passes. Integration-tag view build
passes with no tests selected (compile evidence only). Independent read-only
review found no actionable namespace/authority, pagination, selection-budget/
atomicity/version, CSV-header or bootstrap-scope issue. Files are stable for the
parent native/integrated gate.


Native follow-up: parent supplied disposable PostgreSQL55439 and Mongo55449 rs0.
Go1.27.4 race-enabled TestNativeViewMutateWhereAtomicSelection and
TestNativeViewNamedIsolationSameRootIDVersion both passed on both databases
(2.096s package run). Tests verify late-operation rollback, budget rejection
before callbacks, owner/group filtering, same-root/ID/version namespace isolation
and independent tombstones. No failures, skips or failpoints; no source fixes
required. Unique task-owned schemas/databases cleaned up successfully.

## Plan refinement

- **Objective:** Fix Fable2 zero-group authority fail-open and unproven MutateWhere source versions without changing projection delivery or storage schemas.
- **System fit:** Scope explicitly distinguishes optional from required group union; query writes prove their event-first source version before sharing the projection fence. Parent owns command/delivery remediation and blocked parent verification commands remain out of this task.
- **Reuse and simplicity:** Add RequireOwnerAny bool and optional trusted SourceCount function on Store; default to Tx.Count, with existing selected root locks and version guards. Do not introduce independent clocks or synthetic projection versions.
- **Evidence and unknowns:** Full /tmp/blossm-claude-remediation.jsonl result confirms empty union currently bypasses group checks and selectionBuilder increments row version without source proof. Remote source getters are trusted composition and cannot claim same-DB transactional snapshots.
- **Verification:** Reproduce zero groups List/Get/selection and invented source version rejection, then actual projection application; native bothDB event-first selection rollback/budget with real source events. Preserve errors from count/getter and cancellation. Only owned tests, no rerouting parent blocked commands or failpoints.
- **Resource budget:** View source/tests/docs only; shared scope copy retains bool automatically. Protocol/storage/framework unchanged. Send proposal before edits, run plan checker and focused regression then native fixtures if authorized.

- [x] Require explicit group membership and validate source frontier for query writes.
- [x] Verify regressions and update assertion mapping/native evidence.


### Fable2 correction verification

Before fixes, the two focused owned regressions failed: required empty groups
listed Total1, and a query builder applied Version1 with source Count1. Added
RequireOwnerAny, preserved through value/slice copies, and event-first exact
source proof for MutateWhere. SourceCount is an optional trusted remote getter;
local default is Tx.Count after locks. Validation errors carry bounded root and
version/frontier values, never body. Batch remains trusted projection delivery.

Fresh Go1.27.4 full view race passes16.218s; view vet passes; integration test
build-only passes. Parent restored approval before native execution resumed.
Current native-race tests pass7.746s: MutateWhereAtomicSelection, named isolation,
required empty group denial and QueryWriteCannotPreemptProjection each pass on
both PostgreSQL55439 and Mongo55449. Real Append/Engine.Play proves invented
future revision is rejected and later true projections apply, including after
a legitimate committed-revision query mutation. Updated selection native tests
seed actual source events. Scoped TestView transport race passes1.923s. No skips,
failpoints or parent-command rerouting. Namespace cleanup succeeded.

Focused read-only correction review found no actionable issue in required empty
group denial or source-version proof after locks and before writes. This is
supplementary review evidence; fresh regression/native results above establish
the exercised behavior.
