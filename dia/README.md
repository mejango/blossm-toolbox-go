# Dia services

This package ports the retained Dia core/system/comms application to the shared
Go event protocol. It has no cloud startup side effects. Configure `Service`
with a store, network, contextual ES256 signer and pinned verification key, clock,
32-byte encryption/HMAC secrets,
and the provider adapters the application actually uses. Missing adapters return
errors. The parent transport owns HTTP routes and the protected sessions view.
Wallet is a separate owner; JB/shop and Relayr payments are excluded here.

## Contracts and source map

| Original Dia domain | Go command/fact methods | State and actions |
| --- | --- | --- |
| core/session | StartSession, TerminateSession, SessionTerminated, ChangeScene, AuthenticateAccess | session start, upgrade, change-scene, terminate |
| core/account | SaveSession, IssueChallenge, SetPassword | account register, set-password |
| core/challenge | SaveSession, IssueChallenge, AnswerChallenge, RetryChallengeDelivery | challenge issue, attempt, answer, recover |
| core/principal | ChangeRoles, AddRoles, RemoveRoles, PrincipalRoles, PrincipalGroups | principal add/remove-roles, set-subject |
| core/group | AddPrincipals, AddGroupPrincipals, RemovePrincipals, GroupPrincipals, StreamGroupPrincipals | group add/remove-principals, add/remove-networks |
| core/scene | RegisterScene, DeleteScene, DeletedScene | scene register, delete |
| core/role | CreateRole, ChangeRolePermissions, RolePermissions | role create, add/remove-permissions |
| system/node | RegisterNode | node register with atomic scene/group/principal/session effects |
| system/key | RegisterKey, ReadKey, RevokeKey | key create, revoke |
| system/connection | OpenConnection | possession of API-key secret produces scoped connection JWT |
| system/updates | IssueUpdatesToken, UpdatesChannel, PublishUpdate | subject-bound channels, GRIP publication |
| comms/email | SendMailCommand, SendMail | encrypted prepared mail intent, explicit SMTP submission |
| comms/sms | SendSMSCommand, SendSMS | encrypted prepared SMS intent, explicit Twilio submission |
| root/public-key | PublicKey | PEM public key for ES256 |

The source inventory is `../../dia/services/{core,system,comms}/domains/`,
`../../dia/facts/public_key/`, and `../../dia/views/stores/sessions/`.
`models.go` replaces state schemas; `reducers.go` replaces YAML action lists,
validation and reducers. Unknown fields, duplicate JSON keys, invalid actions,
invalid references and invalid transitions fail. Typed Go inputs replace the
procedure YAML payloads; gateway permission rules are enforced in service
methods. There is no public arbitrary session-upgrade or deferred-event API.
The unfinished role-create scaffold has a real owned definition contract here.
Dynamic roles cannot expand while granted; a shared role revision guard makes
permission expansion and concurrent grants conflict atomically. grants require the caller to already
hold every delegated permission over the exact subject.

References use `{network,service,domain,id}` throughout. Role grants carry
`{id,subject}`. Scene routing reads its typed `subject`. Account creation uses
HMAC-derived identity to enforce uniqueness under concurrent registration.
Anonymous permissions and membership move to an existing account only in the
successful proof transaction. Key creation emits the correct principal root.
Builtin roles are defined once by `BuiltinRoles`.

## Store boundary

`Store` is the small consumer interface: Append, Aggregate and Query. Compose the
parent `eventstore.Store` with `Reducers()` (merge with other application domains
when necessary). Append must commit the entire batch atomically and reject
invalid state transitions. The raw aggregate version is the last event index:
`-1` means absent. The private `load` helper converts this into the next expected
count for Dia's producers; it does not change the public store API.

Events use truthful preparation timestamps. `core/operation` contains an atomic,
immutable receipt with original prepared inputs and a semantic digest. It is
committed beside the effects and never schedules, resumes or executes work.
Retries with unchanged semantic inputs can observe commitment despite revision
or timestamp drift; changed payloads conflict. Operation keys are namespaced by
full event root and action. RemoveRoles instead scopes its receipt to the caller's
session and request key, binding the target principal, explicit roles and subjects
in the canonical request. Identical retries acknowledge the original prepared
targets without reapplying removal, including after self-role removal or later
regrants. Changed targets or requested scope conflict; new requests still require
current authority. Recovery requires a currently valid authenticated session.

After any Append error, Dia reads the operation receipt once using the existing
request context. An exact committed digest proves success; a different digest
returns ErrIdempotency. An absent or unreadable receipt preserves the original
append error, including ErrUncertain. Reconciliation does not retry writes or
detach a canceled context. A receipt is not a provider delivery acknowledgment.
The parent store's `Local` flag retains its infrastructure bypass; Dia's domain
and authorization rules do not branch on local mode.

## Signing composition

`JWT` owns claims and local verification, not a concrete private key. Configure
`Signer ES256Signer` and `VerifyKey *ecdsa.PublicKey`; `Sign(ctx, claims)` passes
the request context through issuance. `LocalSigner{Key: private}` signs locally.
`KMSSigner{Client: cloudgcp.KMS{Client: sdkClient}, Key: version}` or the analogous
`cloudaws.KMS` uses the existing provider adapter's SignES256 protocol. Providers
hash the original signing input once and return 64-byte JOSE signatures.

Every signature is checked locally against VerifyKey before issuance. A changed
KMS alias/version key, malformed signature, provider error, canceled context or
claims expiring during signing cannot produce a token. Verify and PublicKey use
only pinned public trust and work with Signer unset; they never fetch provider
keys or accept a different key through remote rotation. Explicitly replace the
configured trust when rotating keys. Service methods carry their caller context
through session, challenge, connection and updates-token issuance.

`runtimeauth.Open(ctx, network)` owns runtime selection and client cleanup;
`cmd/dia` and the generic runtime reuse it. It requires an explicit `JWT_SIGNER`:

| Value | Required configuration |
| --- | --- |
| local | JWT_PRIVATE_KEY, one P-256 PKCS8 PEM private key |
| gcp | JWT_KMS_KEY, full numeric KMS key-version resource; JWT_PUBLIC_KEY, pinned P-256 PKIX PEM |
| aws | JWT_KMS_KEY, KMS key identifier; JWT_PUBLIC_KEY, pinned P-256 PKIX PEM |

Local and cloud key configurations cannot be mixed. Official default credential
chains configure provider clients; GCP clients close during runtime cleanup.
Startup parses configured trust without calling a signing endpoint. The pinned
key must match the selected provider key; a mismatch fails issuance rather than
adopting provider trust. No live KMS/IAM verification is implied by local fixtures.

## Authentication and recovery

Use `NewSessionSecret()` to generate the 32-byte secret passed to StartSession.
This is a bearer creation credential, not a predictable request identifier. An
anonymous creation retry cannot return an upgraded session. Other request keys
must be nonempty and unique per intent. Access tokens use ES256/P-256, explicit
issuer/audience, NumericDate exp/nbf, token kind, subject and ID. Authentication
checks stored termination and current principal/account/scene; deleted scene and
revoked key contexts fail. Use `AuthenticateAccess`, not raw JWT verification,
for transport and wallet authorization. `AuthorizeDelegatedContext(ctx, claims)`
reuses the live session/identity/scene validator after a gateway has authenticated
an access token, or verified a service token and bound its trusted peer to the
incoming request. Access claims retain their signed user subject; service claims
derive the user subject from their verified context without changing peer
identity. The method does not verify signatures itself and rejects stale user
state, foreign references and subjects without a selected scene. Token expiry is bounded by the persisted
session expiry; challenge and connection-update token kinds are distinct.

Scene/node retries can recover only the exact deterministic transition initiated
by the supplied still-valid token. They cannot refresh an unrelated or later
account upgrade. Session termination and password setting recognize committed
same-key results. Public key material contains no private key. API-key secrets
are HMAC-derived for authorized same-request recovery; only their hashes persist.
ReadKey omits the stored secret hash. HMAC rotation prevents old secret recovery,
while already-known secrets remain usable until key revocation.

Challenges live for three minutes, store a hashed six-digit code, enforce five
reserved proof attempts, and encrypt mail contents. Answer commits proof consumption,
account/principal/group changes and session upgrade together. At most five same-request, same-proof recovery calls
can return the committed access result while the challenge remains valid, without
repeating effects. Recovery also reserves a bounded attempt before checking the
proof, so a consumed challenge cannot become an unbounded guessing endpoint. A conflicting concurrent registration/merge fails atomically;
start a new challenge after reconciling the existing account. It never silently
replays stale prepared writes against different revisions.

Comms command methods persist an encrypted, HMAC-bound intent before provider
submission. A matching receipt can resolve an uncertain preparation response and
allow that call to submit to the provider. If preparation cannot be proven, the
provider remains uncalled. A returned intent reference alone proves neither
commitment nor delivery. They return the reference even when delivery fails. An
explicit repeated call resends the same persisted content and may duplicate
provider delivery. SMTP/Twilio acceptance is not delivery to a person. Direct
SendMail/SendSMS methods are authorized adapter calls without intent persistence;
use command methods for durable preparation. RetryChallengeDelivery similarly
makes resending explicit. No automatic durable-action protocol exists.

SMTP requires TLS (implicit TLS or verified STARTTLS). Twilio uses the real
Messages API form and Basic authentication; GRIP uses the publish JSON protocol.
HTTP clients and TLS dialing are injectable for tests. Redirects and unexpected
status responses fail rather than silently replaying credentials or delivery.

## Verification

Ordinary tests cover all reducer actions, strict decoding, ES256 signature/claim
validation, real GCP/AWS adapter signing with injected SDK fixtures, pinned-key
rotation rejection, cancellation and runtime signer selection, PBKDF2 known-answer and malformed-hash cases, AES-GCM tampering,
SMTP TLS/cancellation, Twilio/GRIP wire requests and failures, session revocation,
stale identity, challenge attempts/replay/atomic failures, account merge,
role authority, key recovery/revocation and encrypted comms retries.

Run from the module root:

```sh
go test -race ./dia
go vet ./dia
POSTGRES_URL='postgres://...' MONGODB_URI='mongodb://...?replicaSet=...' \
  go test -tags integration ./dia -run '^TestNative'
```

Native tests require both disposable databases and fail if configuration is
missing. They exercise the actual generic Store and adapters, not the unit test
store. The parent `integration/TestDiaSessionFlow` additionally proves HTTP start,
publication, protected projection and revocation on PostgreSQL and MongoDB.
Live SMTP/SMS/Fanout, deployed cloud IAM and production capacity are not claimed.

A node network label is unverified registration metadata, not proof of DNS/domain
ownership. Distinct node roots may carry the same label; roles and API keys stay
scoped to their own node root. Tokens do not acquire that label as a new audience.
Domain-ownership verification is not represented by the retained source contract.
