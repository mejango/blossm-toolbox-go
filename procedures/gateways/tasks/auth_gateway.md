# Gateway authority, cookies and uploads

## Plan refinement

- **Objective:** Deliver composable authenticated Framework/ViewGateway/ProjectionRoutes policies, Basic/key verifier modes, optional secure command cookies and bounded multipart preparse/storage in new auth*,gateway*,upload* files only.
- **System fit:** Dia owns current session/connection validity and permission lookup; transport owns credential selection, exact route selection and safe HTTP adaptation; view/projection own scoped execution; command owns responses; cloud storage adapters own object writes. Parent composes runnable services.
- **Reuse and simplicity:** Reuse AuthenticateAccess and new Dia owner wrappers for connection/permission checks, command.RequireContext, view.ExecuteComposite, decode/write/wrap and object adapters. No parallel token validator, anonymous fallback after failed credentials, or generic cloud abstraction.
- **Evidence and unknowns:** Fable triage distinguishes component tests from actual composition. Inspected JS authentication/authorization, command gateway cookie postprocessing and uploader tests: retain bearer/cookie/basic, exact protected context, revocation priority, file/field/count limits. New fresh-system cookie contract is host-only secure cookies, explicit token expiry and CSRF/origin authority; no wildcard network cookie domains.
- **Verification:** Offline unit/race/vet with real Dia JWT/session fixtures, mounted generic handlers, exact route/resource negatives, malformed/ambiguous credentials, cookie revoke priority and cross-origin rejection, multipart byte/count bounds before storage, adapter failures and cancellation. No live cloud/database/chain calls.
- **Resource budget:** One implementation owner in new transport files and this plan; no framework.go/view_gateway.go/module edits. Delegate Dia wrappers to its owner, communicate APIs early, and test scoped package before parent full checks.

## API

Authenticator.Authenticate(request) returns detached Identity via configured Bearer,
Basic or Key verifiers; optional mode permits only absent credentials. DiaVerifier
maps access/connection claims via Dia's persisted-state checks. GatewayAuthority
constructed from exact GatewayRoute{Method,Pattern,Name} registrations returns
Framework, View and Projection callbacks; each binding must provide its relevant
Check/Scope/Projection function. Unknown registration or operation denies.
GatewayPolicy includes required contexts and exact permitted context identities,
plus a required operation/resource Check callback. DiaPermission supplies the
shared permission owner for an application-selected exact subject.

CookiePolicy provides credential selection security plus response application;
CookieGateway wraps only explicitly registered command paths, bounds buffered
JSON responses and strips tokens/revoke after secure cookies are prepared. Unsafe
cookie requests require exact allowed Origin and nonempty constant-time matched
CSRF cookie/header, verified before command execution. Revocation wins over issue.

UploadGateway authenticates before parsing multipart, bounds total bytes, file
bytes/count and field bytes/count, fully validates before any storage calls, then
supplies immutable parsed fields/file object references to a handler. Storage
callbacks use server-selected object keys; filenames never choose object paths.
GCP/AWS helpers call existing storage adapters. Partial provider writes are
reported with references for cleanup; no fabricated transactional rollback.

- [x] Implement and coordinate APIs.
- [x] Verify auth/cookie/upload failures and integration.
- [x] Record evidence and remaining application responsibilities.

## Implemented API and composition contract

All source is new `transport/auth.go`, `gateway_cookie.go`, `gateway_proxy.go`,
`upload.go`, and dedicated `auth_test.go`, `gateway_test.go`, `upload_test.go`.
No framework.go, view_gateway.go, module or command sources changed by this owner.

### Authentication and exact authority

`DiaVerifier(service,"access"|"connection")` delegates to the persisted Dia
session/scene and key revocation owners. It supplies detached full audited claims
and command.Context. `ServiceJWTVerifier(jwt,exactPeerSubject)` pins service token
kind, peer subject and the JWT's issuer/audience/verification key. A caller's
access token cannot serve as an internal peer token. JWT verification stays in
Dia, including expiry, algorithm and audience rules.

`Authenticator{Bearer,Basic,Key,Cookies,Optional}` selects exactly one credential.
Basic receives username and complete password (including colons); Key receives
X-API-Key. Both require configured verifier callbacks returning verified Identity.
No verifier means no authentication. Mixed/duplicate/empty credentials reject.
Optional allows absent credentials only; invalid supplied credentials never
silently downgrade to anonymous. Identity must include nonempty verified claims.

`NewGatewayAuthority(auth,map[GatewayRoute]GatewayPolicy)` copies registrations.
GatewayRoute fields are Method, Pattern (ServeMux path only), Name (exact dynamic
name), Operation (empty Framework, or view/projection operation). The returned
Framework/View/Projection methods directly match existing handler callbacks.
Missing operations, mismatched names, methods and patterns deny. Each policy
requires Check; context requirements reuse command.RequireContext; AllowedContexts
compares complete network/service/domain/id refs. Scope/Projection callbacks bind
only already-authorized identities to configured resources. They do not grant
permission to replace owner authority from a body/query.

For body-addressed resource routes, Check can call InspectGatewayJSON(r,&typed)
to reuse strict bounded decode while preserving bytes for the owning route.
Compare the selected resource with verified identity or call DiaPermission for
an exact configured subject/permission. DiaPermission uses Dia.RequirePermission,
not a second permission engine. Application callbacks remain responsible for
resource-specific rights; there is no permissive default Check implementation.

### Browser command gateway

CookiePolicy requires an authentication Type, allowlisted output Types and exact
HTTPS Origins. Names are __Host-<type>, Secure, HttpOnly, Path=/, SameSite=Strict,
without a Domain. This intentionally limits browser credentials to the issuing
host and same-site use; no legacy network-wide cookie domain is inferred.
Unsafe cookie requests require an allowed Origin plus constant-time matching
__Host-csrf cookie/X-CSRF-Token header. Duplicate cookies reject. CSRF cookies are
host-only Secure random32-byte values readable by same-origin client code.

`CookieGateway(next,policy,exactCommandPaths,maxResponseBytes)` wraps the existing
Framework handler, not command execution. It checks Origin/CSRF before invoking
next; supports allowlisted credentialed POST preflights; buffers bounded JSON
command responses; processes `CookieToken{type,value,expires}` (epoch seconds)
from Contribution.Tokens and type strings from Revoke. Revocation wins for the
same type. All instructions validate before any cookie is emitted; token/revoke
fields and direct Set-Cookie contribution headers are removed from the JSON.
First issuance creates a CSRF cookie if absent. Do not additionally wrap these
paths in the older bearer-only CORS helper; this opt-in wrapper owns their
credentialed browser preflight policy. Client code must send credentials and the
CSRF header on unsafe requests. Server TLS/reverse-proxy HTTPS configuration is
an application responsibility, not inferred from forwarded headers.

### Fact proxy

`FactProxy{Authorize,RPC,Path,Upstream}.Handler()` uses a fixed configured rpc.Client
and upstream path. Upstream(ctx,verifiedIdentity) supplies service-specific
credentials, rather than forwarding incoming bearer/cookie headers. Caller query
selectors remain bounded data; they cannot change the destination. Response
content-type/custom headers precede raw bytes; known hop headers, nominated hop
headers available from net/http, content-length and Set-Cookie are stripped.
Upstream HTTP errors reject before streaming with safe status/error output;
partial read/write failures abort the downstream response. The upstream RPC
request inherits downstream context, so disconnect cancels it and closes Body.
RPC's existing total response-byte and duration limits still apply.

### Multipart uploads and provider adapters

`UploadGateway{Authorize,Limits,Store,Handle}.Handler()` authorizes before parsing.
Limits specify total body, per-file and per-field byte bounds and file/field
counts (zero count forbids that category). Full multipart parsing, duplicate-field
checks and epilogue consumption finish before any object write. Memory is bounded
by total request bytes; this is a bounded preparse contract, not unbounded spooling.
Uploaded fields are data, never authentication. Handle receives verified Identity
and parsed field/object references, allowing explicit command composition.

`GCPUploadStorage(storage,bucket,prefix)` and `AWSUploadStorage(storage,bucket,prefix)`
call existing GCS/S3 adapters with server-generated random object keys; client
filenames never select destination paths. These storage helpers need configured
SDK clients and private bucket policy. On later upload failure the response
reports confirmed objects and the uncertain attempted reference. On Handle
failure confirmed references remain in the response for cleanup/recovery. Object
writes are not falsely presented as an atomic batch; callers own retry/cleanup.
No provider/storage credentials are loaded by these constructors.

## Assertion mapping

| Retained JS assertion | Go owning test |
| --- | --- |
| Authentication bearer/cookie/basic, optional absence, exact protected context | TestGatewayCredentialModesAndIsolation, TestGatewayCookieOriginCSRFAndAmbiguity, TestGatewayExactRouteJobAndViewAuthority |
| Current session/scene and revoked-key authority | TestGatewayDiaSessionAndConnectionAuthority (session and key); Dia owns existing scene-deletion fixtures |
| Same resource permission and no caller context authority | TestGatewayDiaSessionAndConnectionAuthority, TestGatewayBodyResourceAuthorizationPreservesExecutionInput, TestGatewayExactRouteJobAndViewAuthority |
| Command tokens become cookies; revocation priority | TestGatewayCookieCommandResponseAndRevocation, TestGatewayCookieFrameworkComposition |
| Multipart accepted bytes; file bytes/count and field bytes/count limits | TestGatewayMultipartPreparseBounds |
| Storage integration and uncertain recovery | TestGatewayAWSUploadAdapterUsesOfficialStorage, TestGatewayGCPUploadAdapterUsesOfficialStorage, TestGatewayUploadRecoveryAndCancellation |
| gateways/fact/get/test/composition.js raw bytes and headers | TestGatewayFactProxyForwardsRawHeaders |
| Upstream HTTP error before output | TestGatewayFactProxyRejectsBeforeStreaming |
| Downstream disconnect cancels upstream | TestGatewayFactProxyCancelsUpstreamOnDisconnect |
| Partial stream is not successful completion | TestGatewayFactProxyTruncationIsNotSuccess |

No live IAM, DNS, browser TLS or provider configuration was exercised. Parent
owns CLI generic-service generation and the executable native service flow.

## Verification record

Go1.27.4 installed binary, GOCACHE=/tmp/blossm-cloud-go-cache,
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off:

- Full `go test -race ./transport -count=1`: PASS1.813s.
- Final added protected-context/registration regression under race: PASS1.796s.
- `go vet ./transport`: PASS after final test additions.
- gofmt reports no changed formatting; plan refinement checker passes.

The first focused proxy fixture used Connection: close with a nominated custom
header; net/http consumes that Connection header before exposing the response.
The nominated-header fixture now uses Connection: X-Internal so the transport's
actual header filtering is tested; ordinary close is stripped by net/http itself.
The full gate also caught a malformed JSON test fixture introduced while adding
an import; correcting that fixture restored the job authority integration test.
No production behavior was weakened to pass either check.

## Delegated command credential plan

## Plan refinement

- **Objective:** Mint real target-bound short-lived delegated service credentials from authenticated parent command requests, and reject delegated user context whose current Dia session/scene state no longer permits use.
- **System fit:** CommandClient owns child identity and forwarded transaction metadata; transport owns service credential minting/peer verification/transaction binding; Dia owns one shared current access-state validator. Parent wires actual nested Main and separate delivery credentials.
- **Reuse and simplicity:** Reuse contextual JWT.Sign and ServiceJWTVerifier plus Dia.AuthorizeDelegatedContext; preserve the exact eight-field JWT format by binding normalized parent transaction metadata in signed jti. Receiver policy compares the same digest before execution adds the child trace. No reimplementation of persisted session rules.
- **Evidence and unknowns:** Fable2 found manually supplied test credentials did not implement actual nested delegation. CommandClient.Authorize receives trusted parent Request and target audience is fixed in the signing JWT. Parent has an approval-quota block on executable tests; source changes are allowed but test execution must receive successful approval, never be rerouted.
- **Verification:** Add tests for actual CommandClient -> Framework delegation, parent expiry clipping, principal/context mismatch, wrong peer/audience/token kind, altered transaction trace, expired/revoked session and deleted/changed scene. Record tests unrun until executable approval succeeds; no live providers.
- **Resource budget:** New transport delegated source/tests and existing owned plan only; Dia owner supplies shared validator, parent owns serviceapp integration. No module, cloud, delivery-owner or framework changes.

### Delegation implementation and current evidence

- [x] Implement real context-aware delegated signing and exact peer/target verification.
- [x] Require transaction binding before execution and shared current Dia state validation.
- [x] Prove actual signed parent Framework/Main -> CommandClient -> child Framework composition and authority negatives.
- [x] Fresh full transport race and vet after execution approval recovered.

`DelegatedCommandAuthorizer(jwt, identity, peerSubject)` returns the exact
CommandClient.Authorize callback. Configure jwt.Audience as the logical
`network/service` target, not the common network audience. The signer uses the
call context; the delegated credential keeps the original complete user context,
has peerSubject as sub, and expires at the earlier of the parent's expiry or
60 seconds. The parent must come from Framework's authenticated Main. The
callback rejects a mismatch between parent.Context and audited parent.Claims,
and asks Dia.AuthorizeDelegatedContext to check current state. It never accepts
raw caller JSON as verified identity.

`DelegatedCommandVerifier(jwt, identity, peerSubject)` validates the exact peer,
service kind, one exact target audience and current persisted user session/scene.
Its required companion `DelegatedCommandCheck` verifies POST /commands/{name}
and binds transaction ID, normalized issued time and complete ordered parent
trace using a domain-separated digest in signed jti. It restores the body for
Framework's decoder. JWT retains its exact eight-field contract. The binding is
checked before the child appends its own trace. `DelegatedCommandGateway` combines
both checks where a standalone callback is useful. These are command credentials,
not delivery-envelope, store-peer or generic job authority. Applications must
still restrict command names and resource permissions. No command payload
capability is implied by the transaction binding.

Service generation must configure a target-specific verifier, fixed outbound RPC
endpoint and logical CommandClient.Target, the signing JWT and exact peer subject,
and the shared Dia state service. Parent serviceapp owns actual registration and
Main composition; delivery credentials remain separately configured. User access
verification and delegated verification may coexist on command routes only;
only service claims invoke the mandatory DelegatedCommandCheck policy.

On 2026-11-12 the recovered approval path permitted loopback fixtures. Fresh
`go test -race ./transport -count=1` PASS (2.138s), and `go vet ./transport` PASS
with installed Go1.27.4, GOPROXY=off, GOSUMDB=off. Tests include actual signed
parent/child HTTP composition, bounded expiry, altered transaction ID/issued/path,
wrong peer/target/multiple audiences/kind/jti, substituted context, canceled
minting and revoked session rejection. Dia's owner covers scene deletion and
current-state parity in dia/boundary_test.go. No live services were called.

The same fresh full transport race run includes proxy composition tests matching
reference gateways/fact/get/test/composition.js: raw binary bytes and safe headers,
pre-stream 403 without leaking upstream content, and real downstream connection
closure canceling the upstream HTTP request. Truncation additionally fails the
client read instead of reporting completed success. CLI generation requirements
remain fixed RPC target/path, trusted Authorize and Upstream credential callbacks;
request credentials do not implicitly forward to the target.

Source review of cmd/dia confirms stage-only logStopped avoids raw provider/DB
error text, and startup cleanup cancels and joins publisher/consumer before SDK
close. Observed a shared transport.write HTTP204 body/write-header warning in the
new composition test; reported to parent owner without editing their shared file.
