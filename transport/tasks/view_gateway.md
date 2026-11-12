# View gateway delivery

## Plan refinement

- **Objective:** Deliver standalone authenticated store read/list/batch, composite read/stream and channel lookup routes in new files only, with scoped HTTP evidence.
- **System fit:** Trusted application authorization selects view.Scope for each named operation; view.Store owns source/owner filtering and atomic writes; ExecuteComposite owns bound components; transport owns strict decoding and HTTP responses. Parent mounts the handler and supplies credentials/channel policy.
- **Reuse and simplicity:** Reuse wrap/decode/write, view.Store and ExecuteComposite. No universal cloud interface, new dependency, duplicate view rules, or fabricated channel subscriptions. Add typed composite adaptation to existing execution rather than a second composition engine.
- **Evidence and unknowns:** Reference go-reference 372d1c6 gateway/view/get and channel, rpc/view_store and view_composite inspected; legacy composite stream is commented out. Current view APIs support Get/List/Batch and ExecuteComposite, with generic validation errors lacking typed classification. Channel callback must authorize its result; no live configuration inferred.
- **Verification:** Cached-only Go tests/race/vet, HTTP fixtures for auth denial, forged owner, cross-owner reads/writes, pagination, composite streaming failure/cancellation and channel scope. No cloud or DB calls, no downloads. Known missing dependencies may require file-list compilation.
- **Resource budget:** One implementation and dedicated tests plus this plan; no existing transport edits or root module changes. Bounded inspection and local tests; replan only for incompatible owner API changes.

## API coordination

ViewGateway.Authorize(request, operation, name) returns trusted view.Scope. It must authenticate and authorize read/list/batch/composite/stream/channel separately. Caller query/body has no scope field. Stores map names to eventstore.Backend. Composite callbacks receive only trusted scope; TypedViewComposite adapts typed ExecuteComposite main callbacks. Channel is an injected lookup seam, not a subscription/push implementation. Handler is independently mountable; parent owns composition. Existing view owner APIs require no changes.

- [x] Implement routes and typed composite adapter.
- [x] Verify authorization, scopes, stream failure and batch behavior.
- [x] Record exact evidence and limits.

## Delivered contract and evidence

Implemented in `../view_gateway.go`, checked by seven `TestViewGateway*` tests
in `../view_gateway_test.go`. No existing transport source was edited.

| Route | Authorization operation | Response |
| --- | --- | --- |
| GET /view-stores/{name}/{id} | read | Owned, nondeleted Projection, or 404 |
| GET /view-stores/{name}?query=JSON | list | view.Page with scoped counts and offsets |
| POST /view-stores/{name}/batch | batch | Atomic []view.Result from {mutations:[...]} |
| GET /view-composites/{name}?query=JSON | composite | Application JSON |
| GET /view-composites/{name}/stream?query=JSON | stream | NDJSON objects/arrays |
| GET /view-channels/{name}?key=value | channel | JSON string from trusted callback |

`ViewGateway.Handler() (http.Handler,error)` requires Authorize and snapshots
registries. Authorize(request,operation,name) must verify both identity and the
selected operation/name; batch permission must be distinct from read permission.
It returns view.Scope, not request claims. Missing/foreign/deleted records are
indistinguishable 404s. Callback auth errors are hidden (401, or 403 for
`dia.ErrForbidden`). Body input is bounded by existing decode (1MiB); query is
bounded to 64KiB with repeated/unknown fields rejected. Standard wrap supplies
the 30-second request deadline. Backend validation errors without typed sentinels
remain safe generic 500 responses; transport does not classify error strings.

`ViewClient{RPC:*rpc.Client}` exposes Get, List, Batch, ReadComposite,
StreamComposite and Channel. Credentials are caller-supplied HTTP headers;
no scope, discovery, retries or environment credential chain is invented.
ReadComposite accepts a typed output pointer; Store methods return typed values.
TypedViewComposite[Q,R] strictly decodes queries, binds named component readers
through view.ExecuteComposite and supports typed synchronous producers.
The shared fact stream owner validates objects/arrays, serializes callbacks,
retains ignored emit failures and closes emitters after completion. Failure after
output aborts the HTTP response; failure before output gets the safe error
response. Empty success streams have the correct media type and no records.

Reference mapping for parent inventory: view-gateway, view-gateway-get,
view-store-rpc and view-composite-rpc now have transport/RPC implementations of
the new scoped Go contract. view-gateway-channel has an authenticated lookup
seam; actual subscription, websocket/push provider and runtime mount remain
application composition work. This is not identical legacy URL/envelope,
bootstrap/group authority, text/custom-sort/CSV download or stream-ids support.
The reference composite RPC stream was commented out; this implementation adds
an explicit bounded streaming contract. No cloud/channel delivery claim.

Verification on 2026-11-12, from blossm-go, with GOCACHE=/tmp/blossm-cloud-go-cache,
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off:

- `go test -race ./transport -run TestViewGateway -count=1`: passed.
- `go test -race ./transport -count=1`: passed across the current full transport package.
- `go vet ./transport`: passed.
- HTTP/RPC fixtures exercise real owner filtering and transactional batch
  rollback in a local backend fixture. A localhost HTTP server proves truncated
  stream errors on the wire. These do not prove native database isolation or
  live provider behavior; those remain with their owning adapter checks.

The first RPC test exposed scalar output incompatible with the shared fact
stream contract. Both producer and consumer now reuse that owner; tests reject
scalars, retained callback failures and post-completion callbacks. Reusable lesson
is in transport/tasks/lessons.md.

## Startup-log handoff (read-only)

cmd/dia/main.go:225 still logs `err.Error()`. Cached parser-only synthetic probes
confirmed pgx includes query values after redacting URI userinfo and Mongo invalid
option errors include AWS_SESSION_TOKEN or proxyPassword values. For example,
`postgres://user:password@localhost/db?sslmode=invalid&token=SYNTHETIC_SECRET_CANARY`
returns an error containing the token canary; malformed Mongo option values
ending `%ZZ` retain their canary too. No real credentials or DB calls were used.
Parent fix: terminal logs should use a controlled startup stage/code (and, if
useful, concrete error type), never arbitrary Error()/wrapped causes. Preserve
causes internally for errors.Is/As. A log capture regression should assert these
synthetic canaries are absent. No cmd/dia edit was made by this owner.
