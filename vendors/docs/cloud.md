# Cloud adapters

Provider-specific Go packages for the retained Blossm cloud operations. They
belong to the parent `github.com/mejango/blossm-toolbox-go` module, Go 1.26 or newer. There is no
shared cloud interface: applications compose the operations they consume.
SDK construction and credential configuration are explicit application concerns.
Importing these packages does not discover credentials or contact a provider.
See [GCP APIs and composition](../gcp/README.md) and
[AWS APIs and composition](../aws/README.md) for concrete client wiring and pins.

## Reference inventory

Inspected source: `tasks/worktrees/go-reference` at
`372d1c6a3f4699b8714faf15a81dff0b6d8a49a0`. Paths below are relative to that
checkout. Its package source is the released `18a2e086e` tree; the later commit
changes the release guide. It has eight GCP vendors and no AWS vendor.

| Retained source | Actual consumers and behavior | Provider boundary |
| --- | --- | --- |
| `packages/vendors/gcp/pubsub` | Event-store `publishFn`; integration subscriptions; topic/subscription lifecycle | Publish bytes and attributes, explicit pull acknowledgment; deployment may provision authenticated push subscriptions |
| `packages/vendors/gcp/queue` | Command async calls, projection play/replay and view mutations, block creation; CLI execute/queue-create | GCP HTTP task with JSON bytes, delay, supplied Bearer token or service-account OIDC; AWS SQS task delivery requires a consumer |
| `packages/vendors/gcp/schedule` | Wrapper supplies HTTP schedule; generated builds use `steps/schedule_job.js` instead | GCP cron HTTP target with OIDC and timezone; AWS Scheduler expression, target ARN and execution role |
| `packages/vendors/gcp/kms` | Event-store block sign/encrypt/public-key; gateway token verification; secret encryption | SHA-256 asymmetric signing, verification, public keys, symmetric encryption/decryption; exact key identities and binary data |
| `packages/vendors/gcp/secret` | Database connection secrets, node external-token credentials, CLI secret-create | Retained GCP format is base64 KMS ciphertext in `<key>.txt.encrypted` GCS objects, not Secret Manager |
| `packages/vendors/gcp/storage` | Encrypted secret objects, CLI roles upload, gateway role downloads | Object upload/download and paginated listing; role download previously enumerated the whole bucket |
| `packages/vendors/gcp/log_transports` | Shared logger used by servers, RPC and middleware | Structured cloud logs; application selects local console versus cloud logging |
| `packages/vendors/gcp/token` | Internal RPC calls throughout commands, facts, gateways, stores and projections | Audience-bound GCP service identity token; AWS service requests use SigV4, not GCP Bearer tokens |

Consumer source is concentrated in
`packages/cli/blossm/src/{event_store,command,projection,command_gateway,fact_gateway,view_gateway}/deploy/src/index.js`,
`packages/cli/utils/{execute_cli_template,node_external_token}` and
`packages/logging/logger`. The Go adapters accept bytes and explicit resource
identifiers instead of reproducing ambient environment lookup, temporary secret
files or lossy plaintext trimming. The caller owns JSON encoding and the existing
local-mode infrastructure bypass. No adapter silently treats an absent SDK or an
unimplemented operation as success.

## Deployment requirements from the CLI

The reference `packages/cli/utils/merge_cli_template/src/write_build/steps/`
generates the following requirements. This inventory is source evidence, not
confirmation that any resources, IAM grants or APIs exist.

- Pub/Sub topics and push subscriptions, a 60-second acknowledgment deadline,
  nonexpiring subscriptions, explicit push audience and
  `cloud-run-pubsub-invoker` service identity.
- Cloud Tasks queue creation and rate/concurrency updates. The old generator
  fixes queue location to `us-central1`; runtime adapters take explicit resource
  paths so region selection belongs to deployment configuration.
- Scheduler create/update with cron, HTTP method, target URI, OIDC audience and
  `executer` service identity. This is distinct from the older vendor wrapper's
  OAuth token field.
- KMS key rings, P-256/SHA-256 signing keys and symmetric keys with rotation
  policy. Historical `deploy --dry-run` still executes remote integration tests
  using KMS; it does not mean a side-effect-free plan.
- GCS role/secret buckets and object permissions, Cloud Logging writes, Cloud
  Run invocation rights, service-account impersonation rights and Pub/Sub push
  token creation rights.
- Cloud Build submission, image build/push, Cloud Run deployment and environment
  configuration, VPC connector/egress, domain mappings and DNS record-set
  transactions. These are deployment tooling concerns, outside these runtime
  adapters. This change does not run or reimplement `gcloud`, `gsutil`, Docker,
  DNS, IAM provisioning or Cloud Run provisioning.

AWS resource names, queue/topic policies, Scheduler execution role and target
permissions must be configured explicitly. GCP IAM and resource names cannot be
translated mechanically to AWS. Provider-specific README files describe concrete
APIs, acknowledgment ownership, dependency pins and local verification.

## Delivery and recovery contract

Publishing or enqueueing successfully means the provider accepted the request,
not that a consumer committed its work. Keep stable application event/task IDs
in the payload or metadata and acknowledge only after durable processing. An
uncertain response, expired lease or failed acknowledgment can produce duplicate
delivery. Database outbox deletion and application idempotency remain application
responsibilities. There is no cross-provider transaction or exactly-once claim.

SQS acknowledgment deletes the receipt handle; negative acknowledgment changes
visibility. SNS fan-out needs a subscribed queue and its resource policy.
SQS does not make HTTP calls. EventBridge Scheduler uses AWS targets and its own
expression/timezone rules, not a translated GCP cron HTTP job. Credential/token
issuance and request signing are outbound service authentication; application
authorization and inbound identity verification remain separate boundaries.

## Verification boundary

Tests use injected narrow SDK interfaces and actual official SDK transports
against local HTTP/gRPC protocol servers, with synthetic credentials or disabled
authentication. No live provider endpoint, credential discovery, cloud mutation
or deployment is part of the test suite. These checks can establish request
encoding, response handling and failure propagation; they cannot establish IAM,
managed-service retries, delivery timing, regional availability, production
quotas or live resource reconciliation.

Use the parent module's pinned dependencies, then run:

```sh
go test ./vendors/aws ./vendors/gcp
go test -race ./vendors/aws ./vendors/gcp
go vet ./vendors/aws ./vendors/gcp
```

Exact dependency blocks and official pin provenance are in the provider READMEs.
No parent `go.mod` or `go.sum` was edited by this work.

## Historical combined verification, 2026-11-12

Both packages were copied into `/tmp/blossm-cloud-verify-jnot20si`, a temporary
`github.com/mejango/blossm-toolbox-go` module with `go 1.26.0`. Its `go.mod` and `go.sum` contain
the complete resolved cloud dependency set. Go 1.27.1 on darwin/arm64 ran:

```sh
export GOCACHE=/tmp/blossm-cloud-go-cache
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
go mod tidy
go mod verify
go test -count=1 -json ./cloud/... > test.json
go test -race -count=1 -cover -json ./cloud/... > race.json
go vet ./cloud/...
```

All gates passed, with 24 top-level tests, 138 passing test cases including
subtests, and zero skips. Race coverage is 57.1% for AWS and 87.0% for GCP.
`gofmt -l` reports no files. The first sandboxed test invocation failed to bind
localhost ports; the successful ordinary and race runs used permission to bind
the local fixtures. Dependency downloads remained disabled. Tests use no ambient
credentials or live cloud endpoints. The only Go-source difference after the
combined test snapshot is an Enqueue documentation comment clarifying full task
resource names; executable code is unchanged.

These gates cover this cloud subtree only. They do not prove `cmd/dia` wiring,
whole-module compatibility with the concurrent application work, live resource
creation/reconciliation, IAM, provider timing/retries or production capacity.
Not every native resource-lifecycle forwarding method has its own wire case.
The application owner must compose the clients, credentials, resource names,
workers and outbox callbacks and run the complete application gates.

The cached dependencies support all official service constructors shown in the
provider READMEs. AWS's separate `config` and `credentials` modules were not
cached or added. An injected core `aws.Config`/`aws.CredentialsProvider` works
without them; use of `config.LoadDefaultConfig` requires separately selecting
and obtaining that module and its dependencies. GCP's optional explicit auth
construction can use the already-resolved `cloud.google.com/go/auth v0.24.1`.

## Official contract references

Checked on 2026-11-12. Google's official client repository lists Go 1.26 and
1.27 as supported; AWS SDK v2's repository requires Go 1.24 or newer and describes
its Go support policy. These statements establish runtime compatibility policy,
not a promise that an individual pinned release receives indefinite fixes:
[Google support policy](https://github.com/googleapis/google-cloud-go#go-versions-supported),
[AWS support policy](https://github.com/aws/aws-sdk-go-v2#go-version-support-policy).

Use Pub/Sub v2: v1 is deprecated according to the
[official release notes](https://docs.cloud.google.com/pubsub/docs/release-notes).
KMS checksums follow Google's
[data integrity guidance](https://docs.cloud.google.com/kms/docs/data-integrity-guidelines).
SQS receipt handles and possible post-delete redelivery follow the
[DeleteMessage contract](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_DeleteMessage.html).
Scheduler targets require the permissions on their execution role described in
[the AWS target guide](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-targets.html).
