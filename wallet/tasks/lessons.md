# Wallet boundary lessons

Validate an original idempotency key before deriving a nonempty hash namespace.
Otherwise an omitted user key silently becomes a stable operation key. Keep the
rule shared between Create and Prepare and cover direct service calls as well as
HTTP validation.

EVM uint64 values must be decimal JSON strings before they enter the eventstore's
safe53 canonical protocol. Native tests should exercise values above 2^53 instead
of checking only small realistic fixture values.

Custody requires more than a valid anonymous access token: validate local full
account/principal references, persisted existence and reciprocal binding in the
shared owner gate. Keep this policy in wallet while Dia AuthenticateAccess retains
its anonymous-session contract. Native fixtures must finish account proof first.

Submission acknowledgements cannot erase confirmed receipt evidence. Preserve
confirmed observation bytes/version on submit retries; only receipt reconciliation
may supersede that evidence. Conditional appends prevent concurrent stale writes.
