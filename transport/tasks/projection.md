# Projection HTTP/RPC handoff

## Plan refinement

- **Objective:** Add authenticated manual play and replay HTTP/RPC adapters only in new transport/projection_routes.go and dedicated new tests/docs. No edits to existing transport files or projection core. Reconcile internal SHA256 block protocol as the user-approved redesign.
- **System fit:** Projection owns Engine.Play and Scheduler.Replay; transport owns strict bounded request decoding, authenticated source selection and result envelopes; shared rpc.Client owns bounded requests/cancellation. Parent composes the new handler. Manual HTTP input never supplies trusted event triggers or source authority.
- **Reuse and simplicity:** Use current projection APIs without requesting core changes, existing decode/write/wrap helpers, shared RPC transport and explicit caller-provided resolver. No provider discovery, new dependencies or automatic retries.
- **Evidence and unknowns:** Core source currently exposes Engine.Play(ctx,Request) and Scheduler.Replay(ctx,Source,after,limit), with committed/uncertain outcomes and replay partial progress. Coordination contract below is additive; owner changes require adapting this adapter. Named agent messaging needs an agent ID unavailable in this context; this shared handoff records the exact API boundary for parent/Einstein.
- **Verification:** Offline HTTP/RPC fixtures verify authentication, requested-root authorization, trusted source binding, forged scope/trigger rejection, bounds, cancellation, partial enqueue outcomes, committed push failures and uncertain commits. Run owned race tests/vet without dependency downloads; root gates remain parent work.
- **Resource budget:** New transport files only plus assigned inventory docs, bounded local tests, no existing transport edits, cloud writes, root module edits or SDK downloads.

## Coordinated API contract

Uses existing projection.Engine.Play(context.Context, projection.Request) and
projection.Scheduler.Replay(context.Context, projection.Source, string, int).
No changes to core types required. New ProjectionRoutes.Handler() builds a
standalone mountable handler for POST /projections/{name}/play and /replay.
An injected resolver receives authenticated request plus untrusted operation name
and requested root ID/replay selection. It must authorize the selected operation
and root, and return the trusted Source plus scoped Engine/Scheduler. HTTP body
contains only root ID/flag or replay cursor/limit, never Source/Trigger/Claims.
Play is manual and cannot enable event-driven pushes. Core configured dependency
handlers and scoped view writer remain trusted application composition.
Error envelopes preserve committed/uncertain and confirmed replay cursor/progress;
internal errors and push error strings are not exposed. Typed RPC returns progress
alongside errors and does not retry uncertain effects automatically.

- [x] Implement standalone routes and typed RPC adapter.
- [x] Verify offline security/recovery fixtures and update mappings.

## Review

New standalone HTTP/RPC adapters use current core APIs without modifying existing
transport or projection files. Owned TestProjection race tests and transport vet
passed using Go1.27.1, cached dependencies only and no sockets/cloud requests.
Parent still mounts handler and supplies authenticated source/root authorization
and scoped Engine/Scheduler. No direct owner handshake was available; shared API
handoff uses unchanged core signatures and is documented in PROJECTION.md.

## Plan refinement

- **Objective:** Reject incomplete or contradictory projection RPC acknowledgments, including HTTP200 null/empty objects; preserve valid committed errors and replay partial progress.
- **System fit:** ProjectionClient owns wire acknowledgment validation after shared RPC decoding; core outcome semantics remain unchanged. Reconcile final view gateway/projection owner evidence separately from full module gates.
- **Reuse and simplicity:** Retain shared strict JSON decoder, add required-field presence checks and operation-specific outcome invariants at the client boundary, with no core or provider changes.
- **Evidence and unknowns:** Existing struct decoding accepts null and omitted zero fields. Play may legitimately succeed ignored or with no mutations; error responses may report committed or uncertain outcomes. Replay may successfully complete an empty page, but an incomplete successful page must fill its requested limit and advance its cursor.
- **Verification:** Offline regression matrix for both operations covers null/empty/missing/null fields, conflicting flags, negative counts, nonadvancing/foreign replay cursors, valid empty/ignored/committed cases and existing partial-error preservation. Run scoped race/vet checks without downloads.
- **Resource budget:** Owned projection transport/tests and inventory docs only, no network/provider execution or unrelated source edits.

- [x] Validate required response shape and outcome consistency.
- [x] Run regression checks and reconcile final owner evidence.

Final acknowledgment/view-gateway/Framework mount race regressions passed1.315s
on Go1.27.1; vet passed. Initial combined run could not bind sandbox loopback;
rerun with local fixture permission passed without external calls/downloads.
Required fields and consistency checks use temporary decoded candidates so invalid
responses cannot publish trusted progress. Valid committed errors and replay
partial progress remain tested. View-gateway and projection native owner evidence
is now recorded in the central inventory with attribution.
