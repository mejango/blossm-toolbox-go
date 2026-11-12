# Projection HTTP/RPC

`ProjectionRoutes.Handler()` returns an independent handler for
`POST /projections/{name}/play` and `POST /projections/{name}/replay`. Parent
application composition mounts it and provides `Resolve`; no existing Framework
or Dia routes were changed by this adapter.

Resolve must authenticate the request and authorize the exact operation name and
requested root ID, or whole-source replay. It returns a trusted `projection.Source`
and scoped Engine/Scheduler. The core APIs are unchanged: Engine.Play(ctx, Request)
and Scheduler.Replay(ctx, Source, after, limit). The source namespace, dependency
handlers and view writer come from trusted composition. Do not bind an unscoped
writer merely because the caller can read a source.

Play accepts `{"rootId":"id","flag":"optional"}`. Replay accepts
`{"after":"optional returned cursor","limit":100}` with limit1..10000.
Strict bounded decoding rejects extra/duplicate fields, source authority, event
triggers and claims. Manual play and replay cannot enable event-triggered pushes;
trusted event delivery continues to call the core with verified Trigger data.
Foreign-source cursors reject before scheduling.

`ProjectionClient{RPC: client}` provides Play and Replay with explicit headers.
The shared rpc.Client supplies bounded responses, cancellation and no implicit
credential discovery or effect retries. RPC calls return typed outcomes alongside
errors: Committed survives a post-commit error, Uncertain survives an uncertain
write, and replay Enqueued/After preserves confirmed progress after a failed enqueue.
Resume from After while allowing redelivery of the failed uncertain item; no
exactly-once or rollback promise is made. Error messages are generic and do not
expose backend/queue/push details. Manual requests do not send push notifications.

Verification used an in-process HTTP transport around the real handler and shared
RPC client, with injected aggregate/view/queue fixtures. `go test -mod=readonly
-race -count=1 ./transport -run TestProjection` and `go vet -mod=readonly
./transport` passed on Go1.27.1. Tests cover denied auth/root access, forged source
and Trigger input, duplicate keys/oversized bodies, cursor scope/bounds, no manual
push, cancellation, committed/uncertain effects, replay partial progress and
malformed remote acknowledgments. No live endpoint, provider or database was used.
Root composition and selected Go1.27.4/full-dependency gates remain parent work.

Acknowledgments require explicit nonnull outcome fields. The client rejects
HTTP200 null/empty objects, missing booleans/counts, invalid row outcomes,
contradictory commit/uncertainty flags, and replay cursors that fail to reflect
confirmed progress. A successful empty page or ignored/no-mutation play remains
valid when its complete explicit outcome says so. Decoding uses a temporary result;
malformed responses cannot supply trusted progress flags. Non-2xx valid committed
or uncertain outcomes and partial enqueue progress are preserved with the error.
