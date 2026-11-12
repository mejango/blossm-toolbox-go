# Retained external integrations

The154public Toolbox packages include Dwolla, Redis, Chainpoint and Twilio SMS
outside GCP/EVM. There is **no Slack package**. Twilio and email are retained comms
work owned by the Dia/application port. Dwolla, Redis and Chainpoint are implemented
here using the Go standard library; no root go.mod edits or live service calls.
JB/shop and Relayr payment exclusions do not exclude these packages.

## Dwolla

`dwolla.Client` implements the17reference wrapper operations through cohesive
methods rather than17files:

| Toolbox operation | Go API |
| --- | --- |
| createUnverified / createReceiveOnly / createVerifiedPersonal / createVerifiedBusiness | CreateCustomer with empty/receive-only/personal/business Type |
| updateUnverified / updateVerified | UpdateCustomer |
| deactivate / reactivate / suspend | SetCustomerStatus with deactivated/reactivated/suspended |
| customer.createDocument | UploadDocument with customers owner kind |
| certifyBusinessAuthority / getBusinessAuthorityStatus | CertifyOwnership / OwnershipStatus |
| businessAuthority.create / get / update / delete | CreateOwner / GetOwner / UpdateOwner / DeleteOwner |
| businessAuthority.createDocument | UploadDocument with beneficial-owners owner kind |

Business controller defaults retain owner/address fallbacks. Owner state maps to
`stateProvinceRegion` on the wire. This is a deliberate typed internal redesign:
responses retain provider verificationStatus naming; update fields are unified.
Credentialed OAuth client-credentials POST `/token` supplies a bearer for each
operation. No token caching, implicit retries, transfer API or automated payment
orchestration is added. Idempotency keys pass unchanged. Creation requires a
Location response. Multipart uploads preserve binary bytes/MIME and are bounded
to10MiB. Provider validation remains authoritative; local types are not a KYC or
provider-policy guarantee. HTTP status errors preserve status while excluding
provider bodies that can contain private customer data.

Source: `tasks/worktrees/go-reference/packages/vendors/dwolla`. Primary API
references: [customers](https://developers.dwolla.com/docs/api-reference/customers/create-a-customer),
[token exchange](https://developers.dwolla.com/docs/api-reference/tokens/create-an-application-access-token).
Unit transport tests inspect request method/path/auth/idempotency/JSON/multipart
and failures. No account, approval, document or money movement occurred.

## Redis

`redis.Client` implements HSET/HGETALL/EXPIRE, AUTH and optional SELECT via bounded
RESP2, with one context-bounded connection per operation and optional TLS. It is
not a general Redis driver, cluster client or connection pool. Limits are4MiB
request/bulk reply,8MiB total reply and65,536array elements; deadline defaults to
five seconds. Redis error bodies are not echoed. Missing hashes return empty maps.
A failed remote call returns an error, never a silent successful local write.

`NewMemory` explicitly selects a synchronized process-local cache. It copies map
values, merges HSET fields and preserves unexpired TTL on updates; exact expiration
removes entries. This unifies local field merging with Redis, unlike the old JS
fallback's whole-object replacement. Choosing local fallback is a visible caller
policy, not a distributed consistency guarantee. Both APIs accept string fields;
structured values must be encoded by their consumer. The old wrapper itself passed
objects directly to Redis HSET, which also expects field values.

Reference: `packages/vendors/redis/index.js`; protocol semantics from
[Redis HGETALL](https://redis.io/docs/latest/commands/hgetall/) and
[expiry behavior](https://redis.io/docs/latest/commands/expire/).
Tests exercise real loopback TCP request/reply framing, authentication/database
selection, malformed/oversized responses, error redaction, local TTL and concurrent
access. Those are wire fixtures, not a native Redis-server or failover check.

## Chainpoint

`chainpoint.Client` sends POST `/hashes` with a hashes array and retrieves GET
`/proofs` using the proofIds header. It returns matching proof handles, opaque
proof JSON and anchors_complete metadata. Missing proof returns nil. Only its
configured gateway may be used for retrieval; redirects are refused. This avoids
following untrusted returned URLs and the old SDK's hardcoded public IP fallback.

This is an explicit gateway redesign. Automatic public-IP/three-gateway discovery is deliberately retired by the
user in favor of explicitly configured trusted HTTPS gateways. The old SDK's
partial-failure suppression is also replaced: submission errors remain errors. Proof cryptographic verification was commented out in Toolbox
and is not claimed here. The protocol was traced through the installed
chainpoint-js1.4.2 `lib/{submit,get}.js` and `lib/utils/proofs.js` plus the
[upstream source/API overview](https://github.com/chainpoint/chainpoint-js).
Its v4/v5 network assumptions and gateway availability remain unverified live.
Unit transport tests cover matching hash/ID, request headers, pending/missing
proofs and rejection of foreign gateways. No hash was submitted externally.

## Shared HTTP boundary

`internal/httpwire` requires HTTPS except loopback fixtures, refuses credentials
in endpoint URLs and redirects, bounds responses to4MiB and defaults to30-second
request timeout. It retains context cancellation and HTTP status without exposing
request credentials/provider error bodies. Loopback HTTP tests cover redirects,
oversized/error responses and cancellation. Callers select trusted endpoints;
these constructors are not public untrusted-URL proxy endpoints.

## Remaining evidence and scope

These adapters are reusable implementations with scoped tests, not wired Dia
features or live-provider certification. Application composition, credential
provisioning, provider compatibility, production capacity and native Redis remain
separate gates. Chainpoint automatic discovery is a deliberate scope replacement, not a gap. The154package
inventory keeps initial state separate from current reconciled evidence; no
uninspected concurrent package is marked complete because its source exists.

Scoped verification completed with Go1.27.1: `go test -race ./cli ./cmd/blossm
./integrations/...` and corresponding `go vet` passed. Native Redis7.4.7 also
passed `go test -race -tags=redis_integration ./integrations/redis` against a
disposable authenticated loopback container: AUTH, SELECT, binary hash fields,
merge, read and expiration worked. The task-owned container was then stopped.
This does not verify TLS handshakes against a Redis TLS server or failover.

CI now includes `make native-redis`, requiring REDIS_ADDR and the pinned
Compose Redis fixture. PostgreSQL/Mongo native suites still require both of
their URLs and execute packages serially because Mongo failpoints are global.
