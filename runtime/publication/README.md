# Publication runtime

## Plan refinement

- **Objective:** Make actual GCP/AWS publication and consumption independently testable with injected official clients, bounded delivery contexts, verified immutable notices and explicit acknowledgments; own only runtimepub source/tests.
- **System fit:** Keep eventstore hashing/outbox and transport projection as owners; provider runtime transports notices and acknowledges successful handlers. Open retains official credential loading, while cmd/dia owns worker joining and final shutdown.
- **Reuse and simplicity:** Reuse cloud adapters and eventstore Encode/Decode/Verify. Separate credential construction from provider constructors; do not implement an AWS credential chain or add Secret Manager without a consumer.
- **Evidence and unknowns:** Existing AWS handlers use an unbounded worker context, while GCP shares a pull deadline across a batch. Parent reports cmd/dia now joins workers before closing clients. AWS config is uncached; its Open path cannot be compiled with cache-only dependencies yet.
- **Verification:** Official SDK local protocol fixtures must prove encoded notice integrity, failed/expired handlers retaining delivery, receipt-bound ack/delete, provider errors, cancellation and runtime shutdown. Run cached-only file-selected tests excluding the official-config Open file until that module is available, and label that gap explicitly.
- **Resource budget:** One runtime owner, no cloud calls, credentials, downloads or cmd/dia/wallet edits. Use the completed cloud dependency cache and temporary module; no new universal cloud abstraction or unbounded lease-renewal machinery.

## Runtime contracts

`NewGCP(publisher, subscriber, topic, subscription, Options{})` accepts official
Pub/Sub v2 topic/subscription admin clients. `NewAWS(publisher, subscriber, topic,
queueURL, Options{})` accepts official SDK v2 SNS/SQS clients. Both return a
`*Runtime` implementing `eventstore.Publisher`, plus synchronous `Consume` and
`Close` methods. Construction does no credential discovery or provider calls.
Successful construction transfers client lifecycle ownership to the runtime;
failed construction leaves cleanup with the caller. Do not share/close those
clients independently. `Open` retains its existing tuple API and environment
selection and uses the official AWS `config.LoadDefaultConfig` chain.

`Options` defaults to a 30-second pull deadline, 45-second handler deadline,
10-second acknowledgment deadline and one-second retry/empty-poll pause.
Nonpositive explicit durations are rejected except zero, which selects defaults.
Handler plus acknowledgment budgets must be below the existing 60-second lease.
Each receive requests one message, avoiding a sequential batch waiting behind a
slow handler. GCP resets its ack deadline to 60 seconds before processing; AWS
requests 60-second visibility. Work also has a conservative lease deadline measured
from the lease/pull request start, reserving the acknowledgment budget. Slow pulls
can therefore shorten the handler's available time. There is no background lease
renewal. These bounds limit local attempts; they do not guarantee exactly-once
delivery or eliminate network uncertainty.

Outgoing notices are encoded, decoded and verified before the frozen bytes reach
the SDK. Incoming notices use the same strict decoding and canonical hash owner.
`ID` must match `Event.Hash`, and the event hash and fingerprint must verify.
Optional `eventId` metadata must be nonempty when present and must agree with the
notice; SQS attributes must use the expected String type. Raw SNS-to-SQS delivery
and normal SNS Notification envelopes are supported. Envelopes must identify the
configured topic; duplicate JSON keys are rejected. Envelope signatures are not
verified here: configured SNS/SQS IAM and queue policies are the source authority,
and event hashing detects content changes, not an authorized producer by itself.

Only a handler that returns successfully before its context expires can trigger
Ack/DeleteMessage. The original receipt is captured before handler execution.
GCP negatively acknowledges invalid/failed work; AWS leaves the visibility lease
to expire. Queue/subscription redrive policy owns poison-message handling.
Acknowledgment errors are logged and never converted into a confirmed delivery.
No outbox entry is removed here; `eventstore.Store.Deliver` owns outbox recovery.

`Close` first rejects new operations, cancels active ones, waits for their return,
and then closes GCP clients once. AWS SDK clients have no close operation.
Close is idempotent and returns joined client-close errors. `Open`'s existing
`func()` cleanup signature cannot return those errors; callers needing them can
use the injected constructors and `Runtime.Close` directly. Close must be called
by the lifecycle owner, never inside an active handler. Handlers/transports must
honor cancellation: Go cannot forcibly stop arbitrary blocked application code.
No goroutine is detached merely to make shutdown appear complete.

## Review and verification, 2026-11-12

Reviewed `cmd/dia/main.go` without edits. Its current deferred cleanup cancels
workers, waits for both worker goroutines, then closes the publication runtime,
including invalid-port/wallet-initialization/listener failures. The explicit
shutdown deadline does not override that final join for noncooperative handlers.
`Run` and `Recover` retain their existing outbox behavior and parent-owned native
storage evidence; this suite focuses on the provider boundary and lifecycle.

An isolated module lives at `/tmp/blossm-runtimepub-check`; SDK dependencies were
copied from the already-verified cloud module, with no new downloads. Exact gates
from its `runtimepub` directory:

```sh
export GOCACHE=/tmp/blossm-cloud-go-cache
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
go test -count=1 -json providers.go gcp.go aws.go worker.go providers_test.go aws_test.go > test.json
go test -race -count=1 -cover -json providers.go gcp.go aws.go worker.go providers_test.go aws_test.go > race.json
go vet providers.go gcp.go aws.go worker.go providers_test.go aws_test.go
```

All pass on Go 1.27.1, eight top-level tests and 46 passing test/subtest results,
zero skips, 85.1% statement coverage for the selected files. Formatting passes.
Real official SDKs use local HTTP or in-memory serialized gRPC fixtures with
synthetic credentials. Tests enforce receipt identity, raw/envelope validation,
failed/expired/canceled handler retention, provider/ack errors, deadlines and
joining active handlers/requests before closing clients. They reproduced and
fixed the empty-metadata identity distinction. A fixture double-close error was
corrected by giving its two GCP clients distinct connections, matching production.

`open.go` is deliberately excluded from these file-selected gates because AWS's
official config module is still uncached. No replacement credential chain, fake
loader or build-tag success stub was added. Full-package and `cmd/dia` gates must
run after that dependency is available. No cloud calls, resource changes, live
credential use, Secret Manager adapter, cmd/dia edits or wallet edits were made.

## Publication size preflight plan

## Plan refinement

- **Objective:** Reject externally supplied oversized publications before provider calls using parent-owned eventstore.ValidatePublicationSize; retain Attempts on the wire.
- **System fit:** Eventstore owns the shared 240KiB prehash/precommit size contract for local and remote events; runtime repeats that validation on the frozen outgoing notice. Deployment owns AWS raw delivery and queue capacity configuration.
- **Reuse and simplicity:** Call the exported validator, without duplicating constants, envelope sizing or wire formats. Existing lifecycle and provider interfaces remain unchanged.
- **Evidence and unknowns:** Parent is implementing the validator with maximum-width field reservations. Current runtime does not cap bytes. AWS deployment must use RawMessageDelivery=true and MaximumMessageSize at least 262144; provider settings have not been checked live.
- **Verification:** Cached runtime tests prove valid boundary publications preserve Attempts, oversized valid notices make zero provider calls, and existing race/ack/timeout/close checks still pass.
- **Resource budget:** Only runtimepub source/tests/docs; no root module, deployment owner, view owner or eventstore edits. No cloud calls or downloads; wait for the actual shared API instead of creating a temporary substitute.

### Size preflight delivered

Runtime.Publish now calls eventstore.ValidatePublicationSize on the frozen,
integrity-verified notice before either provider call. The same exported
MaxPublicationBytes constant (240KiB) owns the limit; Attempts remains unchanged
on the wire. Parent precommit sizing reserves safe53 maximum-width Number and
Attempts and 64-character hashes before hashing, in both local and remote modes.
This caps individual publications, not batches, snapshots or blocks.

Official SDK local HTTP/gRPC tests prove exact 245760-byte notices publish with
Attempts=123456 preserved; 245761-byte valid notices return ErrInvalid without
reaching either provider endpoint. Existing timeout, lease, integrity, failure,
ack/delete and cancel/join tests remain in the race run. Cached-only file-selected
race/coverage/vet ran from /tmp/blossm-runtimepub-check/runtimepub, with current
source snapshots and eventstore owner code. Evidence: race-size.json.
Open credential composition remains excluded: root package compilation is blocked
by unresolved dependency sums; no download or approval workaround was attempted.

Deployment coordination sent directly to Boole: SNS-to-SQS RawMessageDelivery=true,
queue MaximumMessageSize at least 262144, with explicit QueueARN for the exact
subscription target. SNS default 262144 accommodates the cap plus the existing
77-byte eventId String attribute. Runtime still accepts SNS envelopes, but the
sizing guarantee assumes raw configured delivery. Deployment generation/tests
belong to Boole; no live configuration was inspected or changed here.

## Full constructor verification plan

## Plan refinement

- **Objective:** Close the previously excluded Open compilation/test gap now that parent dependency resolution is complete; verify official credential constructors without real credentials or provider calls.
- **System fit:** Open owns environment and official SDK composition; runtime owns lifecycle; cmd/dia owns worker joins and safe startup logging. Parent owns module files and EVM fixes.
- **Reuse and simplicity:** Add isolated-environment constructor tests using synthetic AWS static config and explicitly invalid GCP ADC; reuse existing SDK wire tests for actual Publish/Consume behavior.
- **Evidence and unknowns:** Parent reports tidy complete and Go1.27.4 available. Constructor tests do not prove live identity, IAM, network access or deployment configuration.
- **Verification:** Run complete runtimepub/cloud race tests and vet with GOTOOLCHAIN=go1.27.4 GOPROXY=off GOSUMDB=off; compile/test cmd/dia separately and report owner-specific blockers.
- **Resource budget:** Only runtimepub/cloud files; no module edits, downloads, real credentials, provider or chain calls. Environment isolation prevents default credential-file/metadata discovery in tests.

Constructor checks in open_test.go isolate AWS/Google/publication environment,
point AWS shared files to empty temporary fixtures, disable EC2 metadata, and
provide synthetic static AWS credentials only. Explicit invalid GCP ADC tests
fail locally; the successful GCP constructor uses a loopback Pub/Sub emulator
endpoint and verifies construction performs no RPC. Tests cover missing required
settings, unsupported providers, official AWS config errors, returned cleanup,
and rejection after repeated close. No production credential files are used.

Current cmd/dia source review: local mode bypasses Open; remote startup recovery
is bounded and preserves pending failures; publisher/consumer workers share a
cancelled context and are joined before SDK and backend cleanup, including early
PORT/wallet/CORS failures. Startup logStopped now emits only controlled stage and
cancellation/deadline flags, retaining causes internally; parent canary tests
cover the earlier disclosure. No cmd/dia edits were needed by this owner.

Full Go1.27.4 verification (2026-11-12): `go test -race -count=1 ./runtimepub
./cloud/...` passed: runtimepub 4.252s, cloud/aws 1.567s, cloud/gcp 3.718s.
`go vet ./runtimepub ./cloud/...` passed. These full-package checks include
open.go and its new constructor tests; the earlier file-selected compilation gap
is now closed. Executed the installed toolchain binary directly with GOPROXY=off,
GOSUMDB=off and GOCACHE=/tmp/blossm-cloud-go-cache. The Go wrapper initially tried
toolchain verification despite offline settings; direct installed-binary execution
avoided any download. No module files were edited.
`go test ./cmd/dia -count=1` also passed (1.608s), compiling the complete runtime
and EVM/wallet integration and running startup logging regressions. This is
compile/unit evidence, not a process startup against databases or cloud services.
