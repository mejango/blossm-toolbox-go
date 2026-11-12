## 2026-11-12: An application composition suggestion is not an implemented composite

The inventory correctly retained a generic view-composite gap after Get/List
were implemented. A README saying applications can compose reads does not supply
the reference main(query, auth-bound views) capability. Add its small explicit
execution owner and test the real scope/error/response contract before marking
that capability implemented; keep HTTP server and RPC wiring separate.

## 2026-11-12: Distinguish remove semantics from ordered projection tombstones

An absent Delete no-op preserves ordinary remove behavior but cannot guard
against a later arrival of older source state. Projection deletion needs an
explicit durable Tombstone carrying authorized owner body and source version.
Test deletion before first creation as well as deletion of existing records;
never infer ordered deletion correctness only from the latter case.

## 2026-11-12: Classify validation at its input boundary

Returning plain validation errors made malformed view requests look like server
failures at transport. Add a sentinel only at known caller-input/operation gates.
Never wrap an entire backend call or classify by error text. Stored-data parsing,
trusted configuration, failed commits and genuine backend errors retain their
original classification, with executable regressions for both sides.

## 2026-11-12: Retained query semantics require assertion-level evidence

Fable's review found equality-only views could not retain the original numeric/
date/search/sort/group behavior. Map each original assertion family to a runnable
Go assertion or an explicit approved redesign. Named routes sharing a backend
must share an identity helper with writers, not assume deployment isolation.
Atomic query-addressed mutation needs a bounded shared transaction executor,
not a caller-side list/write loop presented as equivalent behavior.

## 2026-11-12: Empty required unions deny, and source fences require proof

Fable2 reproduced two missed contract boundaries: empty OwnerAny skipped group
checks, and query builders incremented projection versions without source events.
Represent required membership explicitly; never infer it from slice nilness that
copying can erase. A query mutation sharing the source projection fence must
prove its exact committed root frontier, not invent p.Version+1. Regression must
include the later actual projection, not only the immediate query write.
