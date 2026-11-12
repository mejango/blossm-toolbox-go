# Command contract lessons

A fresh-only side-effect hook cannot also reconstruct a deterministic retry
response. Use one explicit all-outcome Then contract; callback consumers inspect
COMMITTED before conditional effects and handle DUPLICATE/QUEUED deliberately.
Keep callback receipts detached using eventstore's shared clone owner, including
both submitted Input and persisted Event. Neither awaited callbacks nor child
RPC calls provide durable action recovery or rollback across services.

Generated idempotency keys must isolate authenticated actors; a globally unique
request ID is not sufficient when callers choose that ID. Derive the actor from
trusted principal context or stable issuer/audience/subject claims, exclude root
to preserve retarget conflict, and keep explicit key overrides deliberate. Token
refresh stability and full committed-claims integrity remain eventstore-owned.
