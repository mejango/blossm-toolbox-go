# Multi-store aggregate composition

## Plan refinement

- **Objective:** Restore retained multi-store sorted aggregate HTTP composition and per-source historical selectors, with bounded sorted buffering and incremental unsorted output.
- **System fit:** Explicit selected StoreClients carry service scope and credentials. Existing Framework/StoreClient own authentication, historical replay and wire decoding. This combiner owns only source iteration, record metadata, ordering and callbacks.
- **Reuse and simplicity:** Reuse StoreClient.Aggregates and History; arbitrary comparator cannot assume each source is sorted, so materialize under explicit MaxRecords. Historical full scans reuse the selected StoreClient.Roots followed by History; no global discovery or new authority owner.
- **Evidence and unknowns:** Read retained tasks/worktrees/go-reference/packages/rpc/event_stores/test/http.js and current StoreClient/History. Historical context/events must survive composition; current Aggregate does not expose context. Existing JS fixtures use real HTTP/handlers with injected event readers, mirrored here.
- **Verification:** Two real HTTP Framework servers, source headers, Unicode, independent history number/time selectors, immutable selection, serial callbacks, scope mismatch, budget/error no sorted output, incremental unsorted delivery and cancellation cleanup. Scoped race/vet with readonly module, no native DB interference.
- **Resource budget:** New store_merge.go and tests only, plus this required plan. No core StoreClient/eventstore changes, global discovery, cloud calls or deployments.

- [x] Implement explicit selected-source composition.
- [x] Add retained HTTP and failure/cancellation regressions.
- [x] Run scoped race/vet and communicate API.

Review: selected-source regressions passed under race (1.668s), then complete
transport race suite passed (2.756s) and vet passed. Real Framework/Store replay
handlers run behind two httptest servers with injected read-only event readers,
matching the retained JS HTTP fixture boundary. Tests verify independent historical
time/number selection, context/Unicode, credentials, sorted budget/source failures
without partial output, full scans and incremental consumer cancellation cleanup.
No native database, cloud, module, eventstore or core StoreClient changes.
