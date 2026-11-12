# Shared views

This package consumes `eventstore.Backend` and its source-versioned projections.
It owns body operations, scoped selectors, batch composition, pagination and
formatting. It does not publish, authenticate requests, replay events, or invent
an exactly-once multi-source projection protocol.

```go
views := view.Store{
    Name: "sessions",
    Backend: backend,
    Scope: view.Scope{
        Network: "network", Service: "core", Domain: "session",
        Owner: []eventstore.Predicate{{Path: "principal.id", Value: ownerJSON}},
    },
}
```

Trusted application composition supplies the source scope and owner predicates.
Query paths address `Projection.Body`, not metadata. Both owner and request
predicates must match, using `eventstore.Matches` strict scalar equalities,
array traversal and null-versus-missing semantics. An empty owner scope fails
closed. These constraints do not authenticate the caller or grant authority to
select an arbitrary owner. Configure the store from verified application context.

`Get(ctx, id)` returns one owned, nondeleted projection or `ErrNotFound`.
`List(ctx, Query{Predicates, Offset, Limit})` returns `Page{Items, Total,
NextOffset}` in stable ID order. Limit defaults to 50 and may not exceed 10,000;
negative offsets/limits reject. Zero Limit means omitted/default in this typed
API. Total and offset count matching owned rows only. `NextOffset` is absent at
the end. Each list uses a single repeatable backend view, fetching candidate
pages of 256 and filtering at the shared owner. This keeps buffered candidates
bounded but counts/scans the source scope, not an indexed arbitrary body query.
Custom sorts retain all matching rows to sort before pagination. Neither path imposes a silent scan cap; no indexed-body-query throughput guarantee is implied.

The consumer-owned `ProjectionReader` interface adds only:

```go
Projections(ctx context.Context, network, service, domain, afterID string,
    limit int) ([]eventstore.Projection, error)
```

Its rows must be nondeleted, source-scoped, ascending by ID strictly after
`afterID`, and no more than limit. The storage owner implemented this seam for
PostgreSQL and Mongo. Listing fails explicitly if unsupported. View rechecks
scope and validates ordering; it does not silently fall back to event-root scans.

`Batch(ctx, []Mutation)` runs all writes in one `Backend.Update`, acquiring
source roots before reads. Each mutation supplies ID, complete source Root and
Version, and exactly one operation: a replacement Body, Operations, Delete,
or Tombstone with a full Body. Any error
rolls back every applied mutation and returns no partial success results.
A false If condition or stale version is a non-applied Result, not an error;
other eligible members can still apply atomically. Duplicate IDs within a batch
reject: compose their operations in one mutation instead.

Replacements represent complete current source state and may skip versions.
For aggregate views, obtain state/version from the existing
`eventstore.Store.Aggregate`, map it to an owned view body, and submit that full
replacement with the aggregate's Root and Version. This preserves generic replay
and validation ownership. Each view ID binds to one source Root. Cross-source
rebinding rejects even when a new version is greater.

Patches require `Expected` base version (`-1` for absent), since incremental
changes cannot safely skip unseen source states. Stale/equal incoming versions
return Applied=false before replaying operations. A different expected base
returns `ErrConflict`. Existing tombstones retain versions; stale replacements
cannot resurrect them. A newer complete replacement may recreate the view;
patches cannot resurrect tombstones. Delete of an absent ID is a non-applied
result, matching existing remove semantics.

For ordered projection deletion, use `Mutation{ID: id, Root: source,
Version: version, Tombstone: true, Body: ownerScopedBody}`. Tombstone requires a
full canonical object satisfying the configured owner predicates, excludes
Delete and Operations, and persists `Deleted=true` even when the view is absent.
It retains the same current-root and version guards: an older create cannot
resurrect a newer tombstone, an older tombstone cannot delete newer state, and a
newer complete replacement can legitimately recreate the view. Expected and If
remain explicit optional conditions; omit them for unconditional source-version
tombstones. Conditional mutations never upsert.
Before persistence, the resulting body must still satisfy the owner scope.

Operations compose in slice order and never mutate caller JSON:

| Kind | Behavior |
| --- | --- |
| `Set` | Replace the selected value, creating missing object parents. |
| `Unset` | Remove an object property; an existing array index becomes null. |
| `Inc` | Add a numeric value; a missing field starts at zero. Numeric strings/null reject. |
| `Push` | Append one JSON value; a missing field starts as an empty array. |
| `AddToSet` | Append only if no canonically equal JSON value exists. |
| `Pull` | Remove every canonically equal value, or objects matching explicit Where predicates. |

Paths support dotted objects, existing numeric array indexes, `$[]` for every
element, and `$[name]` for elements matching `Filters[name]` predicates. Nested
selectors compose, e.g. `parents.$[parent].children.$[child].count`. Set a parent
array first and then update its selected children in the same Operations slice.
Filters are explicit; no implicit Mongo `$` selection or arbitrary operator
compiler is claimed. Invalid paths/types/unused filters reject. Missing unset
and pull targets are no-ops. Numeric bounds, canonical equality and body encoding
reuse the existing eventstore protocol.

`Format(projection, formatter)` returns `Document{Headers, Body}`. The formatter
receives an isolated copy; its result stays under Body and cannot overwrite ID,
Root or Version. Default formatting retains the canonical body. The Go protocol
has no legacy created/modified timestamps, transaction trace or group metadata;
this package does not fabricate them.

`CSV(writer, documents, columns)` writes selected body paths followed by the
original header ID. It uses standard CSV escaping, preserves numbers, renders
compound values as canonical JSON, and shields spreadsheet formulas in string
values/column names/IDs, including leading whitespace/control and fullwidth
operators. All documents validate before writing; actual writer failure may
still leave a partial stream and is returned to the caller.

## Composite execution

`ExecuteComposite[Q, R](ctx, scope, query, readers, main)` implements the
reference `view_composite/get` delegation with typed query/response values.
`CompositeMain[Q, R]` is a function receiving context, query, a detached Scope,
and `*Components`. It returns the application response unchanged; errors return
the zero response. Authentication supplies Scope separately from query fields.

Named readers implement the two-method `ComponentReader` interface, accepting
context and Scope. `BackendComponent{Backend: backend, Name: "sessions"}` reuses Store.Get/List,
including owner filtering and scoped counts. Components may use separate
backends but share the caller source/owner scope; this API confers no authority
to switch domains or owners. Arbitrary cross-domain composition needs its own
explicit application authorization.

Inside main, call `components.Get("name", id)` or
`components.List("name", Query{...})`. These methods accept no replacement
context or scope. The registry and owner predicate bytes are copied at execution;
each adapter receives another detached scope, so callback/adapter mutation cannot
change later calls. Returned rows are checked against the bound scope. Adapters
remain trusted to scope pagination counts as well as rows.

The caller context and its values reach main and readers. Cancellation is checked
before/after calls; main and adapters must cooperate with context while running.
Readers are canceled when execution ends, including successful execution.
Component and main errors preserve their causes. Separate component reads can
use separate snapshots; no atomic cross-backend snapshot or automatic fan-out,
retry orchestration, token decoding or dependency-resolution framework is added.

## Reference ownership map

The complete framework inventory remains in
[docs/capabilities.md](../docs/capabilities.md). The references below are the
pinned Toolbox owners inspected for this package, not claims of identical
Mongoose behavior or completed HTTP/CLI transport integration.

| Reference capability | Go owner and evidence |
| --- | --- |
| `lib/view_store_mutations`, generic view-store put/delete | `Store.Batch`, `Scope`, `Mutation`, native atomic failure/conditions and scoped lifecycle tests. Body-only selectors, no-match result and owner checks are explicit; source versions replace unversioned writes. |
| `lib/compose_update` | `Apply`, six typed ops and explicit filtered arrays. Mutation tests cover parent/child composition, nonmatching parents, sibling alias safety and type failures. |
| generic view-store get and stream_ids | `Get`, `List`, projection listing seam. Query tests cover owner isolation, strict predicates, default50/max10000, count/offset/order and adapter failures. IDs are available from Page.Items; no separate unbounded stream endpoint is added. |
| `lib/json_to_csv`, generic get CSV/formatView | `Format` and `CSV`; tests cover headers/body isolation, hostile strings, arrays, quoting and writer errors. |
| projection play/replay, aggregate-fed view writes | Existing `eventstore.Store.Aggregate` and replacement Mutation with source Root/Version. Native tests cover stale/out-of-order replacements and duplicate guarded patches. Event replay/publication remains outside view/. |
| view composite get | `ExecuteComposite`, `CompositeMain`, bound `Components` and `BackendComponent` implement typed main(query, scope, named readers) execution. Composite tests prove unchanged application responses, owner filtering, scope-copy isolation, error propagation and cancellation. HTTP/RPC route adaptation remains with transport owners. |
| view gateway/channel, view-store RPC, generated projection adapter | Parent auth/HTTP/RPC/CLI owners retain these responsibilities. ViewGateway now supplies explicit scoped bootstrap, empty/update presentation and CSV hooks. Group subscription/channel notification remain separate owners; historical metadata absent from Projection is not synthesized. |

## Verification

From `blossm-go`:

```sh
go test -race ./view
go vet ./view
POSTGRES_URL='postgres://...@127.0.0.1:.../...?sslmode=disable' \
  go test -race -tags integration ./view -count=1
```

Native tests require POSTGRES_URL, fail rather than skip if missing, and create
and drop only unique schemas. PostgreSQL native checks cover owned lifecycle,
atomic late failure, expected/source version guards, concurrent replacements and
patch deduplication, multi-page listing and cancellation. Mongo implements the
same structural list protocol; this package's native evidence is PostgreSQL.

Local verification on 2026-11-12: five native PostgreSQL scenarios and ordinary
checks passed with race detection; go vet passed. An explicit missing-URL run
failed as required. Independent review found and corrected list accumulation
when Mongo retries a View callback; a dedicated replay regression now proves
that aborted-snapshot rows, counts and offsets are discarded.

Caller validation errors from Get/List/Batch and Apply satisfy
`errors.Is(err, view.ErrInvalid)`. This includes malformed predicates, versions,
mutation bodies and operations incompatible with a valid body. Backend failures,
corrupt stored patch bases, trusted scope configuration, cancellation and version
conflicts retain their original error classification. Transports may map only
this explicit sentinel to a client validation response.


## Named stores and group authority

`Store.Name` and `BackendComponent.Name` are trusted logical view names. Generic
HTTP registrations always use their route name. Producers use the same named
Store, or `PhysicalID(name, logicalID)` when writing projections directly.
Physical IDs encode the exact name and ID as separate hex strings, preserving
bytewise ID order; callers always see logical IDs. Equal source/root/ID/version
in different named views remains independent, including tombstones. Empty Name
means an explicitly unnamed store, never a fallback; the `view:` ID prefix is
reserved there and unnamed listing excludes named rows. No schema/index changes
are required, but a named scan still visits other names in its source scope.

`Scope.OwnerAny` is an OR of nonempty predicate conjunctions, ANDed with the
common `Owner` predicates and exact source scope. Set `RequireOwnerAny: true` whenever group membership is required. An empty
OwnerAny then denies all rows, even with a common tenant Owner, and the flag
survives composite/gateway scope copies. With the flag false and no groups,
only common Owner applies. At least one authority clause or required-union flag
is required; an empty OR branch rejects. Both are supplied by authentication,
not query JSON. Use scalar membership IDs (including complete group identity)
for array groups. Each predicate retains Matches' independent any-array-element
semantics; multiple predicates are not a correlated object `$elemMatch`.

## Typed queries and atomic selection

`Query.Ranges` accepts numeric JSON bounds or RFC3339 date strings with explicit
`Kind` (`number`/`date`) and inclusive bounds by default (`Exclusive` opts out).
Dates compare instants, including timezone offsets. Numbers are strict, with no
string casting. The JS `#2`/`@date` string conventions are replaced by ordinary
JSON numbers and typed date bounds, not interpreted as magic strings. Dotted
arrays match a scalar satisfying both bounds. Missing, null and wrong-type
values do not match ranges.

`Query.Text` contains explicit body Paths and a Value, matching a literal,
case-insensitive substring on any string at those paths, or an exact logical ID.
It does not interpret regex, Mongo `$text` grammar, stemming, weights or relevance
scores. That is an explicit portable search redesign, not Mongo index parity.
`Query.Sort` is ordered typed keys (`number`, `date`, `string`) with optional Desc.
Arrays use their minimum ascending/maximum descending scalar; wrong-type/null/
missing values sort last both ways. Logical ID breaks ties. String ordering is
bytewise, not locale collation. Default ordering remains logical ID. Body query
and all authority filters apply before Total, offsets and limit (default50,
maximum10000). Custom sorts require O(matching rows) memory.

`MutateWhere(ctx, query, maximum, build)` selects and mutates in one Backend.Update.
The explicit maximum must be1..10000; Offset/Limit are rejected. If matches exceed
the budget, it fails before invoking build or writing anything. Each retryable,
side-effect-free builder receives a selected projection and returns a Mutation
with the same ID/root and an event-first source Version. Expected is bound to the
selected version; an explicitly different Expected conflicts. After selected
root locks, Version must equal the committed source Count-1 visible to the update
transaction. Append the real source event before building its query mutation;
never manufacture a version from the projection row. Zero source events or a
non-current version reject before writes. A bounded validation error identifies
the root, requested version and actual frontier without including the body.

For a remote source, configure trusted `Store.SourceCount func(context.Context,
eventstore.Root) (int64, error)` to fetch the committed count for the exact root.
Nil uses local Tx.Count. The remote lookup does not join the local database
transaction; it establishes a committed source revision, not a cross-service
snapshot/fence. Getter/backend failures retain their classification. `Batch`
remains the trusted projection-consumer write path, which can legitimately
receive authoritative versions from remote aggregates. Existing operation/owner/
version/tombstone rules remain authoritative. A late error rolls back all writes.
Selection membership is evaluated at the transaction snapshot: it does not
promise serializable phantom exclusion or a multi-source fence.

## HTTP presentation and assertion mapping

Gateway query JSON directly carries typed fields. Page.Next preserves all query
fields as a relative URL; Page.Updates comes only from a trusted per-name hook.
Page.Empty carries canonical JSON from an optional hook after an empty scoped
read. `GET /view-bootstrap/{name}` requires separate bootstrap authorization,
uses a trusted per-name ID resolver, rejects caller query overrides and applies
ordinary owner/source/name checks. It does not reproduce the historical bootstrap
permission bypass. `?download=csv` exports the selected page through Format/CSV,
buffers validation before response headers, and shields spreadsheet formulas.
It uses the same read authorization; it never exports an unscoped dataset.

| JS assertion family | Current executable Go assertion anchor |
| --- | --- |
| generic/get numeric/date query conversion, typed sorting | `view/query_extended_test.go: TestTypedRangesDateTextSortPagination`, `TestTypedSortArraysMissingAndTies`, `TestExtendedQueryInvalidBeforeBackend` |
| generic/get group lookup union and common context | `TestOwnerAnyIsUnionUnderCommonAuthority`; `transport/view_query_test.go: TestViewQueryRoundtripTextPaginationGroups` proves union, conjunction and forged query rejection |
| generic/get text, skip/limit/count and next query roundtrip | `TestViewQueryRoundtripTextPaginationGroups` follows Next and checks unchanged text/range/sort plus final count/end; `TestScopedListPagination` checks10000 boundary |
| generic/get bootstrap/context and emptyFn | `TestViewBootstrapEmptyCSVHooks` checks foreign-row denial, successful scoped bootstrap, unauthenticated/query rejection, and empty response hook |
| generic/get csv download and formatter failure | `TestViewBootstrapEmptyCSVHooks` checks HTTP content type, scoped IDs, quoting/formula shielding and pre-header failure; `format_test.go` retains nested/hostile-string cases |
| generic/put/delete query-addressed selection | `view/selection_test.go: TestMutateWhereAtomicSelectionAndBudget` checks pre-write budget, group/owner narrowing, expected versions, late failure rollback and callback errors; `TestNativeViewMutateWhereAtomicSelection` reuses those state assertions natively |
| separate view store identity/index isolation | `TestViewNamedIsolationSameRootIDVersion`, `TestNativeViewNamedIsolationSameRootIDVersion` prove same-root/ID/version isolation and independent tombstones; no new physical secondary index is claimed |

Historical Mongo text index configuration, created/modified/trace headers and
update-channel subscription delivery are not established by these query tests.
The new native tests passed with race detection on Go1.27.4 against parent
localhost PostgreSQL55439 and Mongo55449 fixtures: both database subtests of
TestNativeViewMutateWhereAtomicSelection and
TestNativeViewNamedIsolationSameRootIDVersion passed (2.096s package run).
No failpoints were used; task-owned namespaces/databases were cleaned up.


### Fable2 authority/source-fence correction

Earlier passing native selection tests did not prove source-frontier integrity.
Two owned regressions first reproduced the group leak and invented version write.
`TestRequiredOwnerAnyEmptyDeniesEveryOperation` and
`TestRequiredOwnerAnySurvivesCompositeScopeCopies` now assert zero List results,
Get NotFound and no selection callbacks for required empty membership.
`TestMutateWhereRejectsInventedSourceFrontier` and
`TestQuerySourceProofPreservesFailuresAndRejectsOldFrontier` assert exact committed
frontier proof and unchanged getter/backend errors. Existing selection fixtures
now use explicit committed event version1 rather than p.Version+1; native
fixtures append the two actual source events first.

`TestNativeViewRequiredOwnerAnyEmpty` and
`TestNativeViewQueryWriteCannotPreemptProjection` cover both PostgreSQL and Mongo.
The latter uses actual Append and Engine.Play: reject synthetic1 after event0,
then apply real event1; allow a query write after committed event2, then apply
real event3. After parent restored the execution gate, all four updated native families
(MutateWhereAtomicSelection, NamedIsolationSameRootIDVersion,
RequiredOwnerAnyEmpty, QueryWriteCannotPreemptProjection) passed with race
detection on both PostgreSQL and MongoDB, Go1.27.4 (7.746s). The current full view
race suite passed16.218s, scoped gateway TestView race passed1.923s, and view vet
passed. No failpoints were used.
