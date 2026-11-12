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

Config version2 names an explicit application executable:

```json
{"application":{"package":"./cmd/server","binary":"server","entrypoint":"/server"}}
```

`package` is a canonical local Go package, `binary` a basename, and `entrypoint`
a clean absolute container path ending in that binary. Build uses the calling
application's source; Cloud Run `command` and ECS `entryPoint` use that path.
The image must actually contain it. No application executable is bundled or inferred.

`env` contains application-defined strings and `secrets` provider references.
Application startup/business requirements belong to the consumer's validator.
The CLI validates infrastructure and rejects env/secret collisions. `PORT`, provider
project/region and replay identity overrides are protected. Declared infrastructure
blocks also protect their generated environment keys from application overrides.

Optional integration contracts:

| Block | Generated settings and validation |
| --- | --- |
| `database` | postgres/mongo driver, namespace, credential reference; emits `DATABASE_DRIVER`, `DATABASE_NAMESPACE`, `POSTGRES_URL` or `MONGODB_URI` and Mongo database |
| `publication` | Exact provider topic and subscription/queue; emits `PUBLICATION_TOPIC` and subscription/queue env. AWS raw delivery and queue-size requirements below apply |
| `signing` | `JWT_SIGNER`, optional `JWT_KMS_KEY`; local private-key or KMS pinned public-key secret reference, mutually exclusive |
| `delivery` | Provider task/queue settings described below |
| `peers` | Explicit `NETWORK`, `SERVICE`, paired outbound destination/service, optional exact delegated peer |

Absent blocks generate no corresponding resource configuration or environment.
Application env selects any runtime-specific publication mode or local-mode policy.
The shared CLI does not require application-specific cryptographic secrets or an
identity namespace. Consumers must validate those rules before calling `Prepare`.
Generic examples are illustrative, not proof of resource existence or application readiness.

CLI test/build disable module and toolchain downloads and require pre-fetched
dependencies. Use the application's own image build and health-check configuration.
No credentials should enter a build context.

`make native` requires both database URLs and runs all integration-tagged suites
with the race detector. `compose.integration.json` is a disposable localhost-only
fixture, not a production database configuration. CI initializes the Mongo
replica set and fails on missing readiness, tests, formatting, vet, vulnerability,
configuration or container checks. No job deploys or publishes anything.

Current CI and image ownership are recorded in the repository-root documentation.
Local fixture checks do not imply a remote workflow run or deployment.

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

Go1.27.4 is installed after user-approved downloads. Previous monolithic image
and native results are historical; the parent owns current split-module, container
and security gates in the repository verification record. CLI-specific checks below
exercise the generic schema independently of application sources.

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

## Optional delivery and peers

The [GCP delivery example](examples/gcp-delivery-postgres.json) and
[AWS delivery example](examples/aws-delivery-mongo.json) add delivery/peers to the
basic examples. All use a generic `./cmd/server` application chosen by the consumer.

GCP delivery requires a full Tasks queue path, exact HTTPS endpoint, service account
and audience. AWS delivery requires an HTTPS queue distinct from the publication
queue, with `groupID` for FIFO only (maximum128 bytes), and optional timeout1s..11h.
Settings emit `DELIVERY_*` environment variables. Endpoint routing and authenticated
request handling remain the application's responsibility. Queue identifiers do not
prove queue existence, permissions or endpoint reachability.

Replay identity is request-scoped: each request supplies `runId` and preserves it
across retry/pages. Static replay run configuration and `REPLAY_RUN_ID` environment
settings are forbidden. No declaration creates queues or broadens IAM permissions.

Optional peers require explicit `network` and `service`. `outboundURL` and
`outboundService` must be paired; URLs require HTTPS without credentials, query or
fragment. `delegatedSubject` must name one exact peer in the configured network.
Identity segments cannot contain separators or whitespace. The CLI emits these
settings without inferring access to a consumer's identity state or authorization
policy. Application-specific namespace and peer requirements belong to that
application's validator and tests.

## Cloud Run invocation and application credentials

`gcp.invocation` is required: `application` or `iam`. Checked examples explicitly
select `application`, emitting the service-level annotation
`run.googleapis.com/invoker-iam-disabled: "true"`. This disables the platform
Invoker IAM check in the prepared manifest; application authorization still
protects container routes, and the ingress restriction is retained. Review the
edge, organization policy and exposure before execution. Preparation applies
nothing. Google documents this setting and its required deployment permissions
in [public invocation](https://docs.cloud.google.com/run/docs/authenticating/public).

`iam` emits `invoker-iam-disabled: "false"` and requires an explicitly configured
authenticating edge/client. GCP plans with delivery reject this mode because
the in-container Tasks verifier requires an intact signed token. A custom application JWT alone is
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
establish intact forwarding for every Tasks/IAM combination. The delivery/IAM
combination is therefore conservatively unsupported, not proven universally
broken. Application mode remains the explicit checked delivery configuration.

Prepared plans require operator retry limits, retention, poison-message recovery
and redrive decisions. Provision AWS SQS DLQ/redrive separately, accounting for
FIFO group blocking; review GCP Tasks retry and recovery procedures separately.
Malformed or untrusted envelopes are not successful durable deliveries. CLI
preparation and queue lifecycle commands do not provision this recovery policy.
