# GCP adapters

Provider-specific adapters for the retained Toolbox source at
`372d1c6a3f4699b8714faf15a81dff0b6d8a49a0`. No package initializer, constructor,
or import here discovers credentials. SDK clients are injected and caller-owned.
Local-mode selection remains in application composition. This package neither
selects local mode nor provisions resources implicitly during ordinary operations
(except the retained secret creation's explicit KMS key creation).

## Retained capability map

| Reference vendor / caller | Go API |
| --- | --- |
| pubsub publish, event-store notice delivery | `PubSub.Publish` JSON-encodes values; `PublishBytes` sends canonical JSON/binary bytes unchanged; both await message ID |
| pubsub subscribe, unsubscribe, topic create/delete/exists; CLI push setup | `Pull`, `Ack`, `Nack`, `Extend`; `CreateSubscription`, `SubscriptionExists`, `DeleteSubscription`; `CreateTopic`, `TopicExists`, `DeleteTopic` |
| queue enqueue/create; CLI queue rate updates | `Tasks.Enqueue`, `CreateQueue`, `QueueExists`, `UpdateRates` |
| CLI schedule_job HTTP + OIDC | `Scheduler.Create`, `Update`, `Exists`, `Delete` |
| kms sign/verify/publicKey, encrypt/decrypt/createKey; CLI keyring/key setup | `KMS.Sign`, `Verify`, `PublicKey`, `Encrypt`, `Decrypt`, `CreateKey`, `CreateKeyRing`, `KeyExists`, `KeyRingExists` |
| gateway ES256 signatures | Explicit `SignES256` and `VerifyES256` |
| secret create/get | `Secrets.Create`, `Get`, KMS encrypted GCS `<id>.txt.encrypted` objects |
| storage upload/download; gateway permissionsLookup bulk download | `Storage.Put`, `Get`, `ListPage`; callers iterate page tokens and download each returned object |
| log_transports | Synchronous structured `Logging.Write`, resource/service context in `Resource`/`Labels` |
| token internal service auth | `Auth.Token`, `Authorize`, official audience-bound ID token SDK |

All resource names are fully qualified Google names, not short IDs. Exceptions:
`CreateKey` and `CreateKeyRing` accept their final ID separately from the parent.
`Tasks.Enqueue` accepts an empty task name for provider assignment or a full
`projects/.../locations/.../queues/.../tasks/...` name for caller deduplication.
`AlreadyExists` from task creation remains an error, not an inferred successful
execution. SDK retry defaults apply; ambiguous failures do not prove nonexecution.

Pub/Sub uses the maintained v2 module, not deprecated v1. Pull is an explicit
bounded batch, with no automatic ack/nack or lease renewal. Consumers own the
processing deadline and must use the actual subscription's ack deadline, calling
`Extend` before expiry if needed. Newly created subscriptions use 60 seconds and
never expire. Existing subscriptions/backlogs are never replaced; creation returns
AlreadyExists. Push creation requires OIDC and rejects caller-specified payloads
or methods other than POST. Pub/Sub owns push payloads and HTTP delivery.

Tasks and Scheduler allow JSON bodies only on POST/PUT/PATCH. Both support OIDC;
Tasks alternatively accepts an explicit bearer token. The two modes are exclusive,
and an audience cannot accompany bearer auth. Default OIDC audience is the target
origin; set it explicitly for a distinct Cloud Run audience. Scheduler uses HTTP
OIDC, matching the CLI rather than the historical vendor's OAuth wrapper.

KMS hashes messages once with SHA-256 and validates request/response CRC32C,
including returned signing/public-key version identity and encryption key identity.
`Sign` returns native KMS bytes, DER for P-256. `Verify` accepts P-256 DER.
`SignES256`/`VerifyES256` use exactly 64 bytes, `R || S`, for JOSE. Neither method
base64-encodes signatures or constructs JWTs. Explicit positive numeric key
versions are required; there is no alias/PEM cache. Verification is cryptographic,
not a check that the provider still enables that key version. Revocation remains
caller-owned. Encryption/decryption and secret plaintext are lossless bytes,
including leading/trailing whitespace and invalid UTF-8.

Secrets preserve the retained base64 ciphertext object format and KMS key per ID.
Only AlreadyExists is ignored on key creation. A failed GCS write may leave the key
created; callers may retry. No plaintext temporary files, Secret Manager replacement,
background secret cache, bucket creation, or hidden rollback is introduced.

## Construction and lifetime

Use these official constructors with a caller-owned context and `option.ClientOption`
configuration. All errors must be checked, and clients closed after their users stop.
The wire tests instantiate these exact clients against local servers.

```go
import (
    "context"
    "github.com/mejango/blossm-toolbox-go/cloud/gcp"
    tasks "cloud.google.com/go/cloudtasks/apiv2"
    kms "cloud.google.com/go/kms/apiv1"
    logging "cloud.google.com/go/logging/apiv2"
    pubsub "cloud.google.com/go/pubsub/v2/apiv1"
    scheduler "cloud.google.com/go/scheduler/apiv1"
    "cloud.google.com/go/storage"
    "google.golang.org/api/option"
)
```

Constructor-to-adapter mapping (each `err` must be handled before use):

```go
taskClient, err := tasks.NewClient(ctx, opts...)
taskAdapter := gcp.Tasks{Client: taskClient}

schedulerClient, err := scheduler.NewCloudSchedulerClient(ctx, opts...)
schedulerAdapter := gcp.Scheduler{Client: schedulerClient}

publisher, err := pubsub.NewTopicAdminClient(ctx, opts...)
subscriber, err := pubsub.NewSubscriptionAdminClient(ctx, opts...)
pubsubAdapter := gcp.PubSub{Publisher: publisher, Subscriber: subscriber}

kmsClient, err := kms.NewKeyManagementClient(ctx, opts...)
kmsAdapter := gcp.KMS{Client: kmsClient}

storageClient, err := storage.NewClient(ctx, opts...)
storageAdapter := gcp.Storage{Client: storageClient}
secrets := gcp.Secrets{
    KMS: kmsAdapter, Storage: storageAdapter,
    Bucket: "secret-bucket", KeyRing: "projects/p/locations/global/keyRings/secrets",
}

loggingClient, err := logging.NewClient(ctx, opts...)
// Supply LogName, Resource and optional Labels at composition.
logAdapter := gcp.Logging{Client: loggingClient}

// Auth has no persistent client to close. Options may provide explicitly
// configured SDK credentials. Never accept credential configuration from users.
auth := gcp.Auth{Options: opts}
token, err := auth.Token(ctx, "https://service.example")
```

Each of `taskClient`, `schedulerClient`, `publisher`, `subscriber`, `kmsClient`,
`storageClient`, and `loggingClient` exposes `Close() error`. Do not close injected
clients per operation. `Auth` creates a token source per request so its token
exchange follows that request's context; no shared cross-audience cache is used.
`Authorize` clones an HTTP request and sets its Bearer header without mutating it.

## Exact dependencies

The isolated module's `go mod tidy` direct requirements are:

```go
require (
    cloud.google.com/go/cloudtasks v1.21.0
    cloud.google.com/go/kms v1.35.0
    cloud.google.com/go/logging v1.20.0
    cloud.google.com/go/pubsub/v2 v2.8.1
    cloud.google.com/go/scheduler v1.18.1
    cloud.google.com/go/storage v1.70.0
    github.com/googleapis/gax-go/v2 v2.27.0
    golang.org/x/oauth2 v0.38.0
    google.golang.org/api v0.313.0
    google.golang.org/genproto v0.0.0-20261012220534-4341edf3f941
    google.golang.org/genproto/googleapis/api v0.0.0-20261012220534-4341edf3f941
    google.golang.org/grpc v1.85.0
    google.golang.org/protobuf v1.36.12
)
```

All seven pinned Google client/API modules declare Go 1.26.0. Verification used
local Go 1.27.1. Parent go.mod/go.sum were not edited. The isolated lock is
`/tmp/blossm-gcp-check/go.sum`; integration owns the final parent lock.

Official version sources: [Tasks changelog](https://github.com/googleapis/google-cloud-go/blob/cloudtasks/v1.21.0/cloudtasks/CHANGES.md),
[KMS changelog](https://github.com/googleapis/google-cloud-go/blob/kms/v1.35.0/kms/CHANGES.md),
[Logging changelog](https://github.com/googleapis/google-cloud-go/blob/logging/v1.20.0/logging/CHANGES.md),
[Scheduler module](https://github.com/googleapis/google-cloud-go/blob/scheduler/v1.18.1/scheduler/go.mod),
[Storage changelog](https://github.com/googleapis/google-cloud-go/blob/storage/v1.70.0/storage/CHANGES.md),
[API release](https://github.com/googleapis/google-api-go-client/releases/tag/v0.313.0).
Pub/Sub v2 provenance is the downloaded module's verified origin:
`Subdir: pubsub`, `Ref: refs/tags/pubsub/v2.8.1`, commit
`0acc33b5654096ea7de13fc92a2ff64d4dcec290`,
[module source](https://github.com/googleapis/google-cloud-go/blob/0acc33b5654096ea7de13fc92a2ff64d4dcec290/pubsub/go.mod),
[versioned official API](https://docs.cloud.google.com/go/docs/reference/cloud.google.com/go/pubsub/v2/2.8.1).
The module sum is `h1:DyLrbgccmcqbygvbfHXne9ukTynrKtgQsOBkM0GniSY=`.
Conflicting cached v1 end-of-support dates are not used as evidence.

Behavior references: [KMS integrity](https://docs.cloud.google.com/kms/docs/data-integrity-guidelines),
[Tasks HTTP request](https://docs.cloud.google.com/tasks/docs/reference/rest/v2/projects.locations.queues.tasks#HttpRequest),
[Scheduler HTTP target](https://docs.cloud.google.com/scheduler/docs/reference/rest/v1/projects.locations.jobs#HttpTarget),
[service ID tokens](https://docs.cloud.google.com/docs/authentication/get-id-token).

## Local evidence and limits

The parent plan passed `python3 tools/check_plan_refinement.py /Users/jango/Documents/blossm/tasks/todo.md`
before implementation; the plan was not edited. Source reads included all eight
retained GCP vendors, event-store and command-gateway deployment composition,
permission-download callers and CLI topic/subscription, task, scheduler and KMS steps.

Run from `/tmp/blossm-gcp-check`, after copying only this directory into `cloud/gcp`:

```sh
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go test -count=1 ./cloud/gcp
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go test -race -count=1 ./cloud/gcp
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local go vet ./cloud/gcp
```

Tests use official SDKs, serialized gRPC frames through `bufconn`, and actual HTTP
loopback servers. HTTP clients restrict every dial to their fixture host and reject
redirects. GCS metrics are disabled via `storage.WithDisabledClientMetrics()`;
auth uses generated test-only RSA credentials and a loopback token exchange.
No ambient credentials or live cloud calls are needed. Wire fixtures validate the
adapter's requests and responses, not the provider's complete server validation.

Coverage includes resource masks, JSON/binary transmission, OIDC, fractional task
schedule time, ack/nack/lease ownership, real P-256 signatures, malformed/tampered
DER and JOSE, CRC32C/identity failures, encrypted-object composition, GCS pagination,
SDK JWT assertion signatures/audience, denied operations, in-flight deadlines and
missing/typed-nil clients. Local tests do not establish IAM grants, cloud quota,
delivery timing, production throughput, exactly-once processing, or deployment
correctness. The parent owns gateway/application wiring and combined integration.

Review lessons are enforced here by tests: preserve provider-specific request
constraints, distinguish canonical bytes from JSON encoding, make signature formats
explicit, verify returned key identity, and prevent fixture telemetry/egress. Root
lessons were deliberately not edited under the exclusive GCP directory ownership.

Final scoped results, 2026-11-12: ordinary tests passed in 1.588s; cached-only race
tests passed in 2.730s; cached-only `go vet` exited 0; `gofmt -l` returned no files.
The host sandbox required elevated execution solely to bind the local HTTP test
listeners. This did not enable any test cloud destination; fixture transports
still reject nonlocal dials. The only source change after these gates was an
Enqueue documentation comment clarifying full task names.
