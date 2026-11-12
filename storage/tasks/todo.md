## Plan refinement

- **Objective:** Prevent bounded PendingRoots selection from starving later dirty roots when lexically early roots remain busy, in both PostgreSQL and Mongo.
- **System fit:** Adapters persist per-head last committed snapshot-block metadata. Current transactions remember snapshot identities, and PutBlock credits those current heads atomically. Generic BuildBlock/Append/hash/replay and projection tombstones remain unchanged.
- **Reuse and simplicity:** Reuse snapshot_heads, existing transaction lifetime and block sequence. Sort unsnapshotted roots first, then oldest committed snapshot block and root. No wall clocks, leases, global cursor, public protocol changes or migration scaffolding for this undeployed system.
- **Evidence and unknowns:** Both frontier implementations currently sort root before limit. Snapshot.Number is per-root, not a comparable last-service sequence. Mongo rootPipeline currently re-sorts after limit, so preserve frontier order while retaining Candidates root order. Coordinate candidate/frontier edits with agent01a1cdcc-bc15-75b1-bf14-d449dffd8eac.
- **Verification:** Same native fairness contract for both backends with maxRoots1 and continuously dirty first root; test both unsnapshotted and already-snapshotted waiting roots. Prove transaction rollback does not advance service credit and reopen retains it. Rerun candidate/frontier/rollback contracts and adapter race/vet checks using disposable localhost databases only.
- **Resource budget:** Small native metadata/sort changes plus one shared test contract and adapter setup. Preserve completed retry, candidate, view and tombstone work. No cloud, module, generic protocol or unrelated edits.

- [x] Preserve Mongo candidate ordering while allowing frontier pipeline order.
- [x] Persist committed snapshot service priority and fair selection in both adapters.
- [x] Pass native fairness/rollback/reopen and existing candidate checks; report limits.

## Review

Both adapters persist last committed snapshot-block priority on snapshot_heads. Current transaction snapshot root/number bookkeeping is credited only by PutBlock, only for matching current heads, and clears after a block. Snapshot replacements preserve previous priority; no-snapshot roots precede oldest credited roots, with canonical root ties and limit applied last. Mongo shared root cursor preserves caller ordering; Candidate behavior was verified before semantic changes. No public protocol/hash-byte/module changes.

Native race checks: fairness and credit contracts pass on PostgreSQL55439 and Mongo55449, including continuously dirty first root/maxRoots1 across two rounds, unsnapshotted priority, oldest completed-block priority after reopening, rollback after block+credit writes, standalone snapshot preservation, stale historical snapshots and multiple block writes within one transaction. Expanded native candidate/binary/latest/rollback checks pass PostgreSQL5.219s and Mongo6.561s; shared integration snapshots-blocks-query-frontier passes both backends8.434s. Storage ordinary race tests and vet pass.

Independent read-only review found no actionable metadata/rollback/order/stale-head/candidate issue. Coordinated design, file ownership and final handoff with agent01a1cdcc-bc15-75b1-bf14-d449dffd8eac. Completed view composite/tombstone work remains untouched.

Limit: fairness for finite waiting work does not guarantee older-root service under unbounded arrivals of higher-priority unsnapshotted roots. The selection still does a metadata join/filter/sort, not a constant-time ready queue. PostgreSQL uses the fresh-system schema baseline; no legacy migration scaffolding was introduced.
