# Runtime queue composition

`Open(ctx, "gcp" | "aws")` creates SDK clients and exposes `Commands`, `Replay`,
`Publications`, and `DeliveryVerifier`. It never starts a goroutine, creates a
queue, or enqueues work. Pass the adapters into `serviceapp.Config`. Start
`Run(ctx, app.Delivery, logger)` for SQS consumption; GCP's worker is the app's
HTTP `/deliveries` endpoint and `Run` returns immediately. Cancel workers, await
their return, then call the idempotent `Close`. Unknown providers and incomplete
configuration fail before credential discovery. Local mode supplies no queues
and explicitly denies HTTP delivery; synchronous execution belongs to the app.

| Provider | Environment | Meaning |
| --- | --- | --- |
| GCP | `DELIVERY_TASKS_QUEUE` | Full `projects/.../locations/.../queues/...` name |
| GCP | `DELIVERY_URL` | Absolute HTTPS delivery endpoint |
| GCP | `DELIVERY_SERVICE_ACCOUNT` | Exact producer service-account email |
| GCP | `DELIVERY_AUDIENCE` | Explicit OIDC audience, identical at dispatch and verification |
| AWS | `DELIVERY_QUEUE_URL` | HTTPS SQS queue URL |
| AWS | `DELIVERY_GROUP_ID` | Required for FIFO queues, forbidden for standard queues, at most 128 bytes |
| AWS | `DELIVERY_TIMEOUT` | Optional duration from `1s` through `11h`, default `1m` |

Use the official SDK credential sources; AWS also requires its configured
region. Google HTTP deliveries require the official ID-token signature and
lifetime validation, a Google Accounts issuer, the exact configured audience,
and the configured email with Boolean `email_verified: true`. No caller-supplied
claims bypass these checks. AWS HTTP delivery is denied: SQS consumption relies
on queue IAM and the mandatory consumer admission policy. Configure queue writers
and workload-specific dead-letter policy independently.

SQS receives one message at a time. `DELIVERY_TIMEOUT` bounds receive, processing
and acknowledgment together. Long polling is at most 20 seconds and at most one
third of that deadline. Visibility is the rounded-up timeout plus 30 seconds.
There is no visibility extension: work that exceeds the deadline is canceled and
left unacknowledged. Failures back off from 1 to 30 seconds; shutdown interrupts
backoff and provider requests. Empty responses are also rate bounded. Only the
existing delivery consumer's confirmed durable result permits acknowledgment.
Handlers must honor cancellation. Neither SQS nor this runtime promises exactly
once delivery.

Replay requires projection.Request.RunID from the scheduler/caller. A logical run
uses the same explicit ID across pages and retries; a deliberate new run uses a
new ID. There is no process-wide REPLAY_RUN_ID setting and no per-message random
fallback. Runtime wires TasksReplay/SQSReplay directly; envelope hashing and
provider task/FIFO identities include the replay request's RunID. Provider
deduplication retention is finite; repeated execution remains possible.

Tests inject official SDK interfaces and an in-memory HTTP transport for real
Google RSA signature verification. They do not discover cloud credentials or
make live provider requests.
