# HTTP transport boundaries

`Framework`, `Authenticator`, `GatewayAuthority`, view/projection adapters and RPC
clients accept application-owned verification and policy callbacks. `Identity`
carries authenticated context and claims; request bodies do not supply authority.
`ServiceJWTVerifier[C]` pins a generic token's service kind and exact peer subject.

`Decode`, `Write`, `Wrap` and `NDJSON` are shared HTTP facilities available to
third-party application adapters. `security/authn` owns shared error categories;
application errors must wrap or deliberately specialize those sentinels to retain
HTTP classification. `PartialResult()` preserves explicitly public recovery data,
not a claim of commitment. Existing uncertainty and partial-stream rules remain.

Dia routes, session projection, persisted-state verifier, permission adapter and
delegated user-context checks live in the independent Dia application's `httpapi`
package. Earlier task records describe their pre-separation location.
