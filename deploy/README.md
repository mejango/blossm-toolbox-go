# Deployment preparation

The Go CLI prepares offline Cloud Run and AWS ECS/Fargate HTTP configuration.
Default deployment and `deploy --dry-run` are offline, unlike the historical
JavaScript dry-run. An explicit future `deploy --execute` runs the reviewed
provider workflow; implementation tests inject isolated fake runners and do not
perform cloud writes. See the CLI operational workflows for exact options.
Generated files have `prepared-only` status, never deployed or verified status.

Run commands from the Go module root. See [CLI usage](../cli/README.md).
Cloud examples use illustrative accounts, resource names and image references;
validation checks structure and policy, not resource existence or IAM.

Cloud Run is the primary target. Its service uses internal/load-balancer ingress,
at least one instance with CPU always allocated (`minScale=1`, `cpu-throttling=false`)
for autonomous outbox/consumer polling. This assumes instance-based billing and
incurs idle-instance cost. It uses an explicit service identity, container port, resource limits and pinned secret
versions. Provider invocation follows the explicit `gcp.invocation` selection;
application mode disables the platform IAM check but retains container authorization.
JSON is a valid YAML document;
`cloud-run.json` is the Cloud Run V1 Service representation. Region is supplied
in the review command. Database networking, invocation IAM and the load balancer
must be prepared separately.

AWS output contains an ECS task definition and an HTTP service configuration for
Fargate Linux x86-64, awsvpc, 256 CPU units/512 MiB, private subnets, an existing
ALB target group, execution/task roles, secret references and awslogs. It does
not provision the cluster, load balancer, networking, logs or roles. Prepared artifacts show the task family; when invoking provider commands manually,
replace it with the exact registered revision ARN. CLI `deploy --execute` performs
this binding automatically and waits for service stability. `--update` selects
update-service for an existing ECS service. No fixed health-check path is invented: use the actual runtime
endpoint when configuring the existing ALB target group.

GCP deployment secret injection deliberately uses Secret Manager references.
The retained runtime GCP encrypted-file secret adapter remains a separate API;
its KMS/GCS format is not implicitly converted or migrated. AWS injection uses
Secrets Manager ARN selectors. Neither example contains database credentials.

The runtime contract emitted by the shared CLI configuration owner is:

| Variable | Meaning |
| --- | --- |
| `PORT` | HTTP bind port; Cloud Run injects it, ECS supplies it |
| `NETWORK` | Application network identity |
| `LOCAL_MODE` | Always false in a cloud deployment |
| `DATABASE_DRIVER` | postgres or mongo |
| `DATABASE_NAMESPACE` | PostgreSQL schema or Mongo database namespace |
| `POSTGRES_URL` | PostgreSQL credential/connection URL, injected from a secret |
| `MONGODB_URI` | Mongo connection URL, injected from a secret |
| `MONGODB_DATABASE` | Mongo database name, equal to namespace |
| `JWT_SIGNER` | Explicit local, gcp or aws signer selection |
| `JWT_PRIVATE_KEY` | Local signer only, literal PKCS8 P256 PEM secret |
| `JWT_KMS_KEY`, `JWT_PUBLIC_KEY` | Cloud signer only, explicit KMS key identifier and pinned PKIX P256 PEM public key secret reference |
| `DIA_HMAC_KEY` | Required secret containing base64 encoding of 32 random bytes |
| `DIA_ENCRYPTION_KEY` | Separate required secret containing base64 encoding of 32 random bytes |
| `PUBLISHER` | gcp or aws, matching deployment provider |
| `PUBLICATION_TOPIC` | Full GCP topic resource or SNS topic ARN |
| `PUBLICATION_SUBSCRIPTION` | GCP pull subscription for the projection consumer |
| `PUBLICATION_QUEUE_URL` | AWS SQS URL for the projection consumer |
| `GCP_PROJECT` | Configured GCP project when provider is gcp |
| `AWS_REGION` | Configured region for AWS SDK initialization |

The three cryptographic secret references and provider-specific publication/consumer
resources are mandatory. Validation checks identifiers, not secret contents. The runtime
must validate PEM/key lengths. Provision the topic/subscription or SNS-to-SQS delivery
and grant publish/consume permissions explicitly; no artifact creates these resources.
Examples are illustrative and cannot be called deployment-ready.

Additional application secrets can be declared in `secrets`, keyed by environment
name; reserved infrastructure variables cannot be overridden. Parent `cmd/dia`
owns actual HTTP behavior and must consume this contract before deployment.
Missing required authentication/service settings must fail startup, not silently
downgrade protection. These artifacts do not establish runtime compatibility.

`Dockerfile` builds cmd/dia, cmd/framework and cmd/blossm in a pinned official Go builder, then
copies only binaries and CA certificates to a non-root scratch image. Go module
download/build may use the network; the installed CLI init/validate/plan/generate
commands do not. CLI test/build explicitly disable module/toolchain downloads and
require pre-fetched dependencies. No credentials should enter the build context.
The final image does not contain source or build-time files.

`make native` requires both database URLs and runs all integration-tagged suites
with the race detector. `compose.integration.json` is a disposable localhost-only
fixture, not a production database configuration. CI initializes the Mongo
replica set and fails on missing readiness, tests, formatting, vet, vulnerability,
configuration or container checks. No job deploys or publishes anything.

The active workflow definition is at the actual repository root:
[.github/workflows/go-verify.yml](../.github/workflows/go-verify.yml).
Its push/pull-request filters cover `blossm-go/**` and the workflow itself, and
manual dispatch is available. All run steps use `blossm-go` as working directory;
setup-go caches against `blossm-go/go.sum`. The inactive nested duplicate was
removed. Every Make gate propagates failure; database logs/cleanup use explicit
failure/always conditions. Local YAML/path checks passed; no remote workflow run
is claimed; local whole-module checkpoints are recorded separately.

## Version and API provenance

Versions were checked against primary sources during implementation. They are
pins, not a guarantee of no future vulnerabilities. The vulnerability gate must
remain enabled. Registry inspection provides the Docker multi-platform digest.

- Go 1.27.4 is the selected CI/container pin and latest stable release listed
  in the official feed checked 2026-11-12; it satisfies Go1.26+: [Go release feed](https://go.dev/dl/?mode=json),
  [official Go image definition](https://raw.githubusercontent.com/docker-library/official-images/master/library/golang).
- govulncheck v1.9.0: [official package](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck@v1.9.0).
  Run default text mode because JSON/SARIF output can exit zero with findings.
- [Cloud Run Service reference](https://docs.cloud.google.com/run/docs/reference/yaml/v1).
- [Fargate task definition rules](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/fargate-tasks-services.html).

No cloud execution, deployed bytecode verification or successful production
request is claimed by generated artifacts or local validation.

## Local verification record

Go1.27.4 is installed after user-approved downloads. Earlier Go1.27.1 scoped
checks and failed container attempts before cmd/dia/dependency integration are
historical, superseded by successful local image builds, CLI smoke and actual
HTTP lifecycle tests. The image contains three executables. Parent owns current
container/native/full-module/security results in [VERIFICATION](../docs/VERIFICATION.md)
and [capabilities](../docs/capabilities.md); those records distinguish baseline
passes from subsequent remediation. No old permission blocker remains.

Owned CLI checks verify deterministic examples, provider/database combinations,
empty-PATH offline preparation, subprocess and actual Go-test failure propagation,
generated-module compilation and typed runtime contracts. New internal-token and
invocation-mode checks are reported in the current capability checkpoint. Local
fixtures do not establish live provider, IAM, DNS, TLS or chain readiness.

## Execution-code verification

Provider execution branches now cover deployment, application-role upload, secret
creation/version writes and queue create/update/delete. Default remains offline;
explicit `--execute` is required. No execution test uses a real gcloud/aws binary:
injected runners assert exact argv/stdin, fail-fast sequencing and ECS revision
binding. Local test/build child failures still propagate. Typed scaffolds compile
in a temporary single-module fixture. Authenticated job/replay HTTP calls use an
explicit bearer environment and endpoint, with isolated transport fixtures.

CLI syntax sources were checked against [gcloud queue creation](https://docs.cloud.google.com/sdk/gcloud/reference/tasks/queues/create),
[secret version writes](https://docs.cloud.google.com/secret-manager/docs/add-secret-version),
[AWS secret writes](https://docs.aws.amazon.com/cli/latest/reference/secretsmanager/put-secret-value.html)
and [ECS update-service](https://docs.aws.amazon.com/cli/latest/reference/ecs/update-service.html).
These provider command contracts and tests do not establish live permissions or
completed deployments. No new IAM grants are generated or executed.

## AWS publication delivery-size contract

AWS config explicitly supplies `publication.queueARN` matching the configured
regional queue URL, `rawMessageDelivery: true` and `maximumMessageSize` between
262144 and1048576 bytes. The checked example uses262144. `Prepare` emits:

- `sqs-publication-attributes.json`, MaximumMessageSize set to the selected bytes.
- `sns-publication-subscription.json`, exact topic/queue ARNs, protocol sqs and
  RawMessageDelivery=true.
- `preparationCommands`, explicit argv for SQS set-queue-attributes and SNS subscribe.

Apply reviewed preparation commands separately before ECS deployment; default
planning and `deploy --execute` do not silently provision publication resources.
For an existing subscription, verify its RawMessageDelivery attribute and explicitly
set it to true using its confirmed subscription ARN if needed. Subscription
creation output alone is not proof that an existing subscription was updated.
Review/verify narrowly scoped SNS-to-SQS queue policy separately. No IAM permissions
are broadened, and no provider resource state is asserted by these generated files.

The shared eventstore limit is245760bytes for the complete Publication, including
Attempts. With the runtime's77-byte eventId String attribute this fits SNS262144.
The guarantee requires raw delivery; decoding old SNS envelopes does not make an
escaped/enveloped240KiB message fit. Runtime preflight and precommit rollback
checks belong to their existing owners. Config/plan tests reject nonraw delivery,
undersized queues and mismatched/missing queue ARNs, verify artifacts/argv and
check the shared publication size budget. No live calls or deployment performed.

Provider references: [SQS queue attributes](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SetQueueAttributes.html)
and [SNS raw message delivery](https://docs.aws.amazon.com/sns/latest/dg/sns-large-payload-raw-message-delivery.html).

## Generic framework process

The checked [GCP/PostgreSQL example](examples/gcp-framework-postgres.json) and
[AWS/MongoDB example](examples/aws-framework-mongo.json) select `runtime: "framework"`.
Cloud Run uses container `command: ["/framework"]`; ECS uses
`entryPoint: ["/framework"]`. The reviewed image must contain that executable.
These example identifiers do not prove resources, credentials or connectivity exist.

Framework settings emit `SERVICE`, `INTERNAL_SUBJECT`, `DATABASE_NAMESPACE`,
`IDENTITY_DATABASE_NAMESPACE` and `PUBLICATION_MODE`. Identity/document namespaces
must differ. MongoDB shares the configured URI and physical database while using
separate namespace prefixes; PostgreSQL uses the same URL with distinct namespaces.
Remote plans set publication mode to the selected provider and retain existing
publication topic/subscription or queue settings. Process-only `local` and `inline`
modes are not cloud deployment plans; configure them explicitly when running locally.

Both processes use `runtimeauth`: `signing.provider` emits `JWT_SIGNER`. Local mode
requires only the `JWT_PRIVATE_KEY` signing secret. Cloud mode emits `JWT_KMS_KEY`
from `signing.kmsKey` and requires a `JWT_PUBLIC_KEY` secret reference, forbidding
the private key. Runtime validates key type and pins verification trust. CLI
preparation does not fetch keys, discover credentials or sign anything. Provision
only the selected signer permission on the exact key separately.

Framework `delivery` names pre-existing command/replay resources, separate from
publication consumers. GCP requires `tasksQueue`, exact HTTPS `/deliveries` `url`,
`serviceAccount` and `audience`; these emit `DELIVERY_TASKS_QUEUE`, `DELIVERY_URL`,
`DELIVERY_SERVICE_ACCOUNT`, `DELIVERY_AUDIENCE`. The runtime verifies Google issuer,
verified email, exact service-account email and audience. AWS requires `queueURL`
(`DELIVERY_QUEUE_URL`) distinct from the publication queue; FIFO additionally
requires `groupID` (`DELIVERY_GROUP_ID`, maximum128 bytes). AWS `timeout`
(`DELIVERY_TIMEOUT`) is a Go duration from1s through11h, default1m, covering a
single message's poll/consume/ack with visibility timeout plus30s. GCP timeout is
not configurable through this setting. Replay identity is request-scoped: the
caller supplies required `runId` in each replay request and preserves it across
retries/pages. Choose a new ID only for a deliberate new replay. Static
`delivery.replayRunID` configuration and `REPLAY_RUN_ID` environment settings are
not supported. These settings do not create queues or broaden IAM permissions.

The CLI's offline gates reject missing delivery configuration, shared identity
namespaces, mixed signing modes and unsafe delivery targets. Cross-service native
flows and process/container gates are owned by their implementations and reported
separately; no live deployment is inferred from successful plan generation.

The [framework executable](../cmd/framework/README.md) mounts `/healthz`; use it
for the framework target-group health check. Its [queue runtime contract](../runtimequeue/README.md)
records worker cancellation, acknowledgment, retry and visibility limits. Local
mode bypasses automatic publication, while inline mode retains outboxes and
verified projections; neither promises async queue acceptance without a provider.

Optional framework `peers` config binds `outboundURL` and `outboundService`
together, emitting `OUTBOUND_URL` and `OUTBOUND_SERVICE`. Deployment endpoints
require HTTPS. Optional `delegatedSubject` emits `DELEGATED_SUBJECT` and must be
an exact `NETWORK/peer` in the configured identity network. The runtime derives
outbound audience/Target as `NETWORK/OUTBOUND_SERVICE`, issuing subject as
`NETWORK/SERVICE`, and incoming audience as `NETWORK/SERVICE`. No selectable
outbound subject or network override exists. Local process tests may use loopback
HTTP; cloud deployment plans do not. Endpoint ownership, shared identity-state
access and least-privilege command/resource policies require explicit review.

## Cloud Run invocation and application credentials

`gcp.invocation` is required: `application` or `iam`. Checked examples explicitly
select `application`, emitting the service-level annotation
`run.googleapis.com/invoker-iam-disabled: "true"`. This disables the platform
Invoker IAM check in the prepared manifest; application authorization still
protects container routes, and the ingress restriction is retained. Review the
edge, organization policy and exposure before execution. Preparation applies
nothing. Google documents this setting and its required deployment permissions
in [public invocation](https://docs.cloud.google.com/run/docs/authenticating/public).

`iam` is supported only for Dia plans with the explicitly required authenticating
edge/client; it emits `invoker-iam-disabled: "false"`. Framework GCP plans reject
this mode because their in-container Tasks verifier requires an intact signed token. A custom application JWT alone is
insufficient for the platform check. Use an explicitly configured authenticating
edge/client with a Google ID token in `X-Serverless-Authorization` and the
application token in `Authorization`; the former takes precedence for the
Cloud Run check. Configure the Google token's service audience and existing
invoker permission separately. See Google's [service-to-service authentication](https://docs.cloud.google.com/run/docs/authenticating/service-to-service).
The built-in application-bearer clients do not acquire this second credential.
Neither mode silently grants an IAM role or changes a live service.

Official forwarding evidence is narrower than a universal stripping claim:
Google's [service-to-service documentation](https://docs.cloud.google.com/run/docs/authenticating/service-to-service)
describes signature removal for `X-Serverless-Authorization`, and its
[troubleshooting guide](https://docs.cloud.google.com/run/docs/troubleshooting#identity_token_signature_redacted_by_google)
documents redaction for the described gcloud-issued-token cases. The
[Cloud Tasks API](https://docs.cloud.google.com/tasks/docs/reference/rpc/google.cloud.tasks.v2#httprequest)
places OIDC credentials in `Authorization`. These sources do not conclusively
establish intact forwarding for every Tasks/IAM combination. The framework/IAM
combination is therefore conservatively unsupported, not proven universally
broken. Application mode remains the explicit checked framework configuration.

Prepared plans require operator retry limits, retention, poison-message recovery
and redrive decisions. Provision AWS SQS DLQ/redrive separately, accounting for
FIFO group blocking; review GCP Tasks retry and recovery procedures separately.
Malformed or untrusted envelopes are not successful durable deliveries. CLI
preparation and queue lifecycle commands do not provision this recovery policy.
