# AWS adapters

Package `github.com/mejango/blossm-toolbox-go/cloud/aws` uses official AWS SDK v2 clients through
provider-specific interfaces. Construct each adapter with an explicitly configured
client, for example `aws.SQS{Client: sqs.NewFromConfig(cfg)}`. There is no default
credential lookup, startup network activity, environment loading, universal cloud
interface, background worker, or automatic resource provisioning in this package.
The application composition root owns credentials, region, retries and timeouts.
SDK errors and metadata remain available to callers through native output types.

## Contracts

| Owner | Operations and caller responsibility |
| --- | --- |
| SNS | Publish, create/get/delete topic, subscribe/unsubscribe. Native message attributes, FIFO group/deduplication IDs and subscription attributes are retained. |
| SQS | Receive and explicit receipt-bound `Ack` (DeleteMessage), `Nack` (ChangeMessageVisibility), send, queue creation/lookup/policy/deletion. No autoack on receive, cancellation or handler failure. |
| Dispatch | Native SQS work dispatch only. Standard per-message delay is integral 0..900 seconds. FIFO per-message delay is rejected; configure queue delay. An application worker must execute work, including any HTTP call, then acknowledge it. SQS does not deliver HTTP requests, generate OIDC credentials or provide Cloud Tasks HTTP execution. |
| Scheduler | Native create/get/update/delete with target ARN and execution RoleArn. AWS rate/cron/at expressions and time zones, flexible windows, retry policies and ClientToken are caller-owned. This is not a GCP HTTP/OAuth scheduler. |
| KMS | CreateKey, raw Sign/Verify/Encrypt/Decrypt/GetPublicKey preserve caller-selected algorithm, message type and encryption context. Bytes are never trimmed or text-converted. `ES256PublicKey`, `SignES256` and `VerifyES256` provide P-256/SPKI and 64-byte JOSE conversion. |
| Secrets Manager | CreateSecret, PutSecretValue and GetSecretValue preserve binary or string values, version IDs/stages and client request tokens. No plaintext temporary files or hidden KMS/S3 composition. |
| S3 | PutObject/GetObject preserve streaming bodies and metadata. Caller closes GetObject.Body. ListObjectsV2 and `WalkObjects` expose pagination; visitor errors/cancellation stop traversal. Missing/nonadvancing continuation tokens fail instead of silently truncating a bucket. |
| CloudWatch Logs | Explicit group/stream creation and PutLogEvents. Prefer `Write` to treat HTTP-200 partial rejection as an error while retaining rejected indexes. Caller supplies ordered timestamps, structured message text and batch limits; no background buffer or implicit retry of accepted events. |
| ServiceAuth | SigV4 request signing for an explicitly selected AWS IAM service/region, not bearer-token authentication or inbound token verification. Explicit credential provider required. Signs a clone, hashes replayable request body, includes session credentials. Caller sends/closes the returned body and re-signs retries. |

Raw SDK-shaped operations preserve AWS validation and error behavior. AWS service
limits and semantic validation remain authoritative; these methods do not claim
to locally validate every ARN, expression, size or IAM policy. High-level helpers
reject unsupported local inputs explicitly. Contexts must be nonnil as in the SDK.
Do not share/mutate request inputs concurrently. `GetBody` must return independent
identical streams; cancellation is checked between reads, but a blocking custom
reader must implement its own cancellation. Inject a transport appropriate to the
service; use HTTPS outside loopback tests. Do not forward signed requests across
hosts or log credentials/authorization headers.

ES256 signing resolves aliases to an immutable key ID, requires P-256 SIGN_VERIFY,
hashes once with SHA256, checks returned signing identity/algorithm and validates
DER scalars. Verification fetches the current public key and verifies locally;
raw KMS Verify is also available. Persist immutable key IDs with signatures if an
alias can rotate. There is no alias cache. KMS key creation uses native KeySpec
and KeyUsage; signing and symmetric encryption keys are separate resources.

Delivery retains a private copy of the receipt string, so modifying exported
message metadata cannot change Ack/Nack. Failed acknowledgment remains a failure;
retry it only while the receipt is valid. SQS is at-least-once, and callers must
make effects idempotent. Nack visibility is 0..43200 integral seconds, subject to
AWS's maximum time since receive; it does not reset that total lease limit.

## SNS to SQS subscription

Provisioning is explicit. Use `SNS.Subscribe` with the queue ARN as endpoint and
request raw delivery if the worker expects the published body directly:

```go
subscription, err := topics.Subscribe(ctx, &sns.SubscribeInput{
    TopicArn: aws.String(topicARN),
    Protocol: aws.String("sqs"),
    Endpoint: aws.String(queueARN),
    Attributes: map[string]string{"RawMessageDelivery": "true"},
    ReturnSubscriptionArn: true,
})
```

Here `aws.String` is the AWS SDK helper, not this package. The caller must also
configure the SQS queue policy through `SetQueueAttributes` to grant
`sqs:SendMessage` to `sns.amazonaws.com`, restricted by the intended topic
`aws:SourceArn` and account. Without raw delivery, SQS receives an SNS JSON
envelope; this package deliberately does not unwrap it. Native attributes,
confirmation, filter policies, redrive configuration and account/region resource
ownership remain caller-owned. Unsubscribe uses the returned subscription ARN;
it does not delete the queue or topic.

## Exact module requirements

The parent owns `go.mod`/`go.sum`; this directory introduces no nested module.
Resolved and checked against official tagged module manifests on 2026-11-12:

```go
require (
    github.com/aws/aws-sdk-go-v2 v1.48.0
    github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs v1.92.2
    github.com/aws/aws-sdk-go-v2/service/kms v1.62.6
    github.com/aws/aws-sdk-go-v2/service/s3 v1.114.10
    github.com/aws/aws-sdk-go-v2/service/scheduler v1.26.5
    github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.50.12
    github.com/aws/aws-sdk-go-v2/service/sns v1.48.0
    github.com/aws/aws-sdk-go-v2/service/sqs v1.53.0
    github.com/aws/smithy-go v1.28.9 // directly imported by tests
)
```

Resolved indirect requirements: `aws/protocol/eventstream v1.7.27`,
`internal/configsources v1.5.12`, `internal/endpoints/v2 v2.8.12`,
`internal/v4a v1.5.12`, `service/internal/accept-encoding v1.13.25`,
`service/internal/checksum v1.11.13`, `service/internal/presigned-url v1.14.12`,
`service/internal/s3shared v1.20.12`, all under `github.com/aws/aws-sdk-go-v2`.
The pinned manifests require Go 1.24; the parent module target is Go 1.26+.
Tests here ran on Go 1.27.1 darwin/arm64, not a separately installed Go 1.26.

Official manifest sources:
[core](https://github.com/aws/aws-sdk-go-v2/blob/v1.48.0/go.mod),
[SNS](https://github.com/aws/aws-sdk-go-v2/blob/service/sns/v1.48.0/service/sns/go.mod),
[SQS](https://github.com/aws/aws-sdk-go-v2/blob/service/sqs/v1.53.0/service/sqs/go.mod),
[Scheduler](https://github.com/aws/aws-sdk-go-v2/blob/service/scheduler/v1.26.5/service/scheduler/go.mod),
[KMS](https://github.com/aws/aws-sdk-go-v2/blob/service/kms/v1.62.6/service/kms/go.mod),
[Secrets Manager](https://github.com/aws/aws-sdk-go-v2/blob/service/secretsmanager/v1.50.12/service/secretsmanager/go.mod),
[S3](https://github.com/aws/aws-sdk-go-v2/blob/service/s3/v1.114.10/service/s3/go.mod),
[CloudWatch Logs](https://github.com/aws/aws-sdk-go-v2/blob/service/cloudwatchlogs/v1.92.2/service/cloudwatchlogs/go.mod).
AWS's [release feed](https://github.com/aws/aws-sdk-go-v2/releases) identifies the
active v2 release line. No v1 SDK is used.

## Verification and limits

Source was copied into `/tmp/blossm-aws-check/cloud/aws` beneath an isolated
`github.com/mejango/blossm-toolbox-go` module with `go 1.26` and the requirements above. No parent
module files or root plan were edited. Commands from that temporary module:

```sh
GOCACHE=/tmp/blossm-aws-go-cache go mod tidy
GOCACHE=/tmp/blossm-aws-go-cache go vet ./cloud/aws
GOCACHE=/tmp/blossm-aws-go-cache go test -count=1 -v ./cloud/aws
GOCACHE=/tmp/blossm-aws-go-cache go test -race -count=1 -cover ./cloud/aws
```

Final scoped results: gofmt clean, vet passes, ordinary tests pass, race tests
pass (57.1% statement coverage). All 13 Go files in the test copy are byte-equal
to the owned source. Lifecycle forwarding paths are not all covered.

Tests use actual SDK signing, serialization and response decoding against local
HTTP servers with fixed dummy credentials. They cover SNS query/XML, SQS JSON
including receipt mutation, explicit delete/change-visibility errors, delay
rejection and in-flight cancellation; Scheduler target/role/timezone/idempotency;
KMS context/algorithm/raw byte serialization and native P-256 cryptography,
tampering, invalid DER/identity/algorithm; binary secrets; S3 bytes and two-page
listing with missing/repeated-token/visitor/provider/cancellation errors;
CloudWatch partial rejection; SigV4 body preservation and credential/body errors.
Narrow malformed-response tests use injected interfaces, not as substitutes for
wire protocol evidence. Regression lessons from review are executable tests for
private receipt identity, nil receive output, body cleanup, early key validation,
provider signature identity and incomplete listings.

Live IAM permissions, deployed endpoints, quotas, resource provisioning side
effects, retry ambiguity under network loss, AWS delivery timing and application
worker integration have not been tested. Lifecycle methods expose native SDK
operations; wire tests emphasize runtime flows rather than every lifecycle call.
No AWS service calls, credential discovery, deployment or cloud resource writes
were performed. Parent integration, complete-module tests and dependency security
gates remain with the parent owner.
