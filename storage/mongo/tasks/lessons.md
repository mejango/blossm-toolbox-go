## 2026-11-12: Index the existing serialized representation

The parent reported PostgreSQL cursor/latest-snapshot failures after candidate
metadata was introduced. Existing low-level snapshots permit nil RawMessage,
which encoding/json writes as null; passing nil directly to the canonical
index decoder returned EOF. Build auxiliary query terms from the same logical
wire representation, retain opaque bytes, and gate nil-versus-null equivalence
in the shared helper. Do not weaken old storage/cursor tests to accommodate an
index-only regression.

## 2026-11-12: Read streams are not retry-safe owned effects

The user corrected Mongo View sharing Update's callback retry loop. Awaiting and
serializing stream callbacks does not make their external effects idempotent.
View must run once and return a known abort; only Update callbacks carry the
explicit retry-safe/no-external-effects contract. Gate this at the adapter with
a transient getMore failure after already-delivered rows, not only consumer
result resets or synthetic pre-read errors.


## 2026-11-12: Bound documents, preserve logical payloads

The user's size review exposed a block containing an entire tail exceeding BSON's
16 MiB limit even with one root and a tiny final state. Apply size analysis to
serialized containers and auxiliary query metadata, not only individual events.
Use a shared transactional chunk codec for opaque bytes and conservative query
fallback for oversized metadata. Gate with a real batch of individually admitted
small events producing a large tail, plus large-state and cleanup/corruption
checks. Publication admission is a separate owner rule, not a block-size fix.
