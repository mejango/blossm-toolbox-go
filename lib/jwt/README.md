# Typed ES256 tokens

`JWT[C]` and `Claims[C]` keep application context typed without importing an
application model. Verification requires the exact eight claim fields, strict
JSON, canonical base64url, pinned P-256 public trust, issuer, audience, kind and
time bounds. `Sign` uses a contextual local or KMS signer and verifies its output
against the pinned public key before issuance.

Applications own session state, revocation, roles and context policy. A valid
signature alone does not authorize an application operation. `runtimeauth.Open[C]`
constructs the signer with explicit issuer/audience and an owned cleanup callback.
Cloud fixtures exercise real adapter protocols without live provider writes.
