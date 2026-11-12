# Generic framework executable

`go run ./cmd/framework` serves the composed `serviceapp` through ordinary HTTP.
It exposes `/healthz`, Dia identity operations under `/identity/`, generic
commands/facts/jobs, the protected `documents` view, authenticated projection
replay, and `/deliveries`. New anonymous sessions establish a principal by
registering a scene before authoring documents. Wallet is not mounted here.

Required configuration:

| Variable | Meaning |
| --- | --- |
| NETWORK, SERVICE | Local identity network and document service |
| INTERNAL_SUBJECT | Exact trusted service-JWT peer subject |
| PUBLICATION_MODE | Explicit local, inline, gcp or aws |
| DATABASE_DRIVER | postgres or mongo |
| DATABASE_NAMESPACE | Document store namespace |
| IDENTITY_DATABASE_NAMESPACE | Distinct identity store namespace |
| POSTGRES_URL | PostgreSQL endpoint when selected |
| MONGODB_URI, MONGODB_DATABASE | Mongo endpoint and physical database when selected |
| DIA_HMAC_KEY, DIA_ENCRYPTION_KEY | Base64-encoded 32-byte secrets |
| JWT_SIGNER and signing keys | Shared [runtime signing](../../dia/README.md#signing-composition) configuration |

PORT defaults to8080. CORS_ORIGINS is an optional JSON array of exact origins.
Identity and document stores share endpoint configuration but use different
native namespaces. A private key is never printed in startup errors; failure
logs expose only controlled cancellation/deadline flags.

Modes:

- `local` sets Store.Local on both stores, retaining synchronous command and
  publication infrastructure bypass. Automatic view publication does not run;
  authorized explicit projection play/replay remains available.
- `inline` persists normal outboxes and projects directly through the verified
  publication adapter. Background recovery processes pending document and
  identity notices. Async commands without a configured provider queue fail
  explicitly; this mode does not simulate durable queue acceptance.
- `gcp` and `aws` use runtimepub for publication and runtimequeue for command and
  replay delivery. Missing provider queue configuration fails startup. Configure
  publication destinations per service and source namespace; do not use one
  subscription/queue as competing consumers for unrelated services.

Remote publication requires PUBLICATION_TOPIC and either GCP
PUBLICATION_SUBSCRIPTION or AWS PUBLICATION_QUEUE_URL. Queue delivery uses the
[shared delivery runtime](../../runtimequeue/README.md) configuration. GCP Tasks
receives dispatch-time Google OIDC bound to the configured issuer, audience and
verified service-account email on `/deliveries` only. Other internal routes
retain the configured service-JWT peer. AWS SQS workers consume the configured
IAM-protected delivery queue directly; HTTP delivery is denied in that mode.
AWS DELIVERY_QUEUE_URL must differ from PUBLICATION_QUEUE_URL because their
wire envelopes and consumers differ.

Fresh, durably confirmed terminal delivery rejections emit a structured warning
with only `envelopeId` and the controlled rejection `reason`. Queue acceptance
returns `EnvelopeID` for correlation and authorized rejection lookup. Duplicate
delivery lookups do not emit another warning. This callback is best-effort:
a crash after persistence can lose the warning; observation errors do not
prevent acknowledgment. The durable rejection remains the authoritative record.

Shutdown cancels request and worker contexts, shuts down HTTP (forces Close on
shutdown failure), joins publication/delivery workers, then closes clients and
backends. Workers and SDKs must honor cancellation; no background work survives
normal cleanup. No automatic generic action resumption is introduced.

Configuration, publication routing and HTTP cancellation have unit tests. Parent
native and container gates exercise the real application/store composition.
Local fixtures do not establish deployed IAM, Cloud Tasks or SQS behavior.

## Optional delegated commands

Set OUTBOUND_URL and OUTBOUND_SERVICE together to enable the application's copy
command. The destination is fixed at startup: logical target and JWT audience
are `NETWORK/OUTBOUND_SERVICE`, while the issuing peer subject is `NETWORK/SERVICE`.
OUTBOUND_URL requires HTTPS except loopback HTTP for local testing. The RPC client
rejects redirects and unsafe URL paths and uses bounded request deadlines. With
neither variable set, no outbound client or copy route is installed.

A receiver opts in with DELEGATED_SUBJECT=`NETWORK/peer-service`. It pins that
peer against the existing JWT issuer/public key and its own `NETWORK/SERVICE`
audience. This applies only to explicitly configured command routes with
transaction-binding checks, not delivery envelopes or internal store routes.
The receiver validates current delegated user session/scene state through Dia.
The minter preserves authenticated user context, caps token lifetime by the
parent token and signs the parent transaction identity in jti; no extra JWT
fields or unsigned body authority are introduced. Both services must use the
same identity network and have access to its current identity state.

The executable's peer-composition test sends a real loopback HTTP child command
and checks peer, target audience, user context and transaction identity. It also
proves a terminated user session is rejected before dispatch. This is local
credential-composition evidence, not a live remote deployment test.
