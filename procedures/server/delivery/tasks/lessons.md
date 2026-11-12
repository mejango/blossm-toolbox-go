## 2026-11-12: Inspect the owning encoder before adding a second wire format

A proposed base64 envelope assumed canonical JSON omitted HTML escaping. The
near-limit test disproved that: shared Encode uses json.Marshal and already
counts escapes in the publication bound. Inspect the existing serializer and
actual provider bytes before adding an encoding layer. Keep the regression for
a near-limit escaped publication; use one canonical envelope rather than an
unnecessary wrapper. Reconcile the plan and parent API handoff when evidence
changes the design.


## 2026-11-12: Failed business work is not always retryable queue work

Fable2 found structurally valid, authenticated messages could fail admission or
idempotency forever and block FIFO followers. Preserve failure semantics but
record deterministic terminal rejection durably before acknowledging it. Keep
network, cancellation and unknown outcomes retryable even when joined to typed
business markers. Gate poison-before-valid ordering, journal uncertainty and
minimal record contents; producer authorization alone cannot replace consumer
recovery for already queued work.


## 2026-11-12: Durable rejection needs a discoverable identity

Claude3 found that terminal rejection records were durable but invisible: SQS
workers discarded the result, Tasks consumed response headers, and submitters
received only unrelated provider acknowledgment IDs. Return the stable envelope
ID alongside provider acceptance, and expose a minimal post-journal observation
hook. Gate confirmed freshness, callback-retry reset, duplicate suppression and
hook errors independent of acknowledgment. Never call best-effort logs exactly
once or replace the authoritative journal with logging.
