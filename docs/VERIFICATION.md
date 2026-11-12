# Independent repository verification

Earlier totals below describe the preserved combined implementation and initial
public export. The current application separation moves domain/native/container
checks to dia-go, and adds framework-boundary gates. New results will be recorded
under separation verification; historical logs and source hashes remain unchanged.

# Go verification record

The records below describe the verified workspace snapshot before its public
module-path relocation. See [publication verification](PUBLICATION.md) for
checks of this standalone repository and the pinned Dia consumer.

Final frozen-source checkpoint, 2026-11-12: the retained Go framework and Dia
scope is implemented and all required local gates pass on Go1.27.4. The original
completion claim was reopened after independent review; its findings were fixed
and the complete gates rerun. This statement does not claim live deployment,
provider/chain verification, a full security audit, or one-to-one numerical
coverage equivalence with JavaScript.

| Gate | Final result |
| --- | --- |
| `go test ./...` | PASS, 1,008 test/subtest results, zero failed/skipped tests |
| `go test -race ./...` | PASS, 1,008 results, zero failed/skipped tests |
| Serial native PostgreSQL/Mongo race suite | PASS, 1,211 results, zero failed/skipped tests |
| Native Redis | PASS, 4 results including its unit cases |
| gofmt, `go vet ./...` | PASS |
| `go mod verify`, `go mod tidy -diff` | PASS, no lock drift |
| Generated configurations and three entrypoint builds | PASS |
| Linux amd64 image and CLI execution | PASS |
| Container HTTP lifecycles | PASS, both `/dia` and `/framework` |
| govulncheck v1.9.0 | PASS, “No vulnerabilities found” |
| Final bounded Claude Fable5.2 review | No blocker in the reviewed deltas; read-only source review |

Counts include parent tests and subtests. The native suite includes unit cases;
these totals must not be added or compared as distinct cases against the JS
baseline. Package-level “no test files” results are not skipped tests.

[Machine evidence](verification-results.json) contains package results, timestamps,
raw-log SHA256 hashes and compressed logs in [verification-logs](verification-logs/).
The [frozen source manifest](verification-source.json) records the 251 Go/build-gate
input files; all remained unchanged through final execution. Its aggregate digest is
`50dd9c9b76d88d6dcf913090009747f015ec02864a58f74e763b3759b652de16`.
The locally built and tested image is `sha256:930b6257d31bea01be5a2fe393cee8d2c5ea434073979d8d1a92286bb348162f`.
The final local run used an isolated `blossm-go-verify` Compose project with
loopback ports55439/55449/56379 and its own Docker network. The fixtures were
removed after verification; the image and evidence remain. Original Toolbox/Dia
repositories and existing changes remain preserved.

## Behavior and review closure

The complete native reference flow exercises generic command handling, PostgreSQL
and Mongo commitment, durable outbox publication, replay and protected projection.
It also covers refreshed-token duplicate recovery, an actual nested command Main
with signed delegation, historical-source retry after mutation, queue delivery and
replay generations, durable poison rejection followed by valid work, submitter
receipt ID -> authorized rejection lookup, observer-error acknowledgment, and
session revocation. The built Linux framework runs a real HTTP command/projection
lifecycle, rather than merely compiling package fixtures.

Native suites retain concurrency, multi-root rollback, expected revisions,
uncertain outcomes, restart recovery, acknowledgment/delete windows, callback and
cursor cancellation, binary snapshots/blocks, typed candidate queries beyond100
snapshots, >16MiB Mongo body storage and integrity failures, projection fences,
custody ownership and durable EVM nonce/signed-byte behavior. Provider and EVM
protocol fixtures run locally with synthetic credentials; no live transactions
or cloud operations were performed.

The [review closure map](reviews/fable-2026-11-12/closure.md) links findings to owning
changes and assertions. Reports and model provenance from all five Claude rounds
remain in that directory. The [last review](reviews/fable-2026-11-12/claude-remediation-5.txt)
confirmed the remaining configuration, local CLI and observability corrections,
with explicit review limits. Review is independent source evidence, not execution.
The [154-package inventory](capabilities.md) and [behavior-family ledger](behavior-coverage.md)
map retained scope and assertions without claiming every original JS branch was
individually paired with a Go test.

The user selected one signing trust domain per network. Framework GCP deployment
configuration supports application invocation and rejects the unverified IAM
intact-delivery-token composition. Dia IAM mode requires an authenticating edge.
Streams have a documented bounded deadline; malformed delivery bodies require
operator redrive policy. Relayr payments and JB/shop remain explicitly deferred.
These are documented contracts and scope decisions, not success-returning stubs.

## Historical failures and corrections

[Pre-review evidence](verification-baseline/verification-results.json) preserves the
old753/927-result checkpoint; it is not the final tree. The first review's failing
refresh/callback probes remain archived with the superseded API. A later
consolidated native run overlapped the sourceVersion copy-contract edit and failed
the older request shape; its [failure log](reviews/fable-2026-11-12/intermediate-native-failure.json)
is retained and was not counted as a pass. A frozen-source rerun passes all gates.

Earlier native Dia same-key contention was fixed by exact persisted-operation
receipt reconciliation after any append error, without replaying unknown commits
or blindly increasing retry budgets. Reducer/callback metadata is detached before
application code can mutate it. Required-empty group scopes deny access; query
writes cannot invent source versions that suppress future projections. These
corrections have owning regression tests, not just documentation promises.

The resolved graph includes the documented compression/WebSocket/crypto/Mongo
patches in go.mod/go.sum. A clean vulnerability scan applies to that graph and the
scanner database at execution time, not to unknown future advisories.

## Reproduce the required gates

Run from `blossm-go/`. Use the installed Go1.27.4 binary first on PATH so CLI
children, which intentionally set GOTOOLCHAIN=local, use the same compiler:

```sh
set -euo pipefail
export GOTOOLCHAIN=go1.27.4
export PATH="$(go env GOROOT)/bin:$PATH"
export GOTOOLCHAIN=local GOWORK=off
go version
# Expected: go1.27.4. Downloads were authorized; resolved dependencies are pinned.
go mod download
go mod verify
```

The checked Compose file uses disposable localhost ports5432/27017/6379. Use an
isolated environment with those ports available; do not replace unrelated local
services. Mongo test commands belong only on this disposable replica set.
For a fresh fixture, these are the same initialization steps used by root CI:

```sh
docker compose -f deploy/compose.integration.json up -d --wait
docker compose -f deploy/compose.integration.json exec -T mongo mongosh --quiet --eval 'rs.initiate({_id:"rs0",members:[{_id:0,host:"localhost:27017"}]})'
ready=false
for attempt in $(seq 1 60); do
  if docker compose -f deploy/compose.integration.json exec -T mongo mongosh --quiet --eval 'quit(db.hello().isWritablePrimary ? 0 : 1)'; then
    ready=true
    break
  fi
  sleep 1
done
[ "$ready" = true ] || { echo 'MongoDB did not become writable' >&2; exit 1; }
export POSTGRES_URL='postgres://postgres:postgres@127.0.0.1:5432/blossm?sslmode=disable'
export MONGODB_URI='mongodb://127.0.0.1:27017/?replicaSet=rs0&directConnection=true'
export REDIS_ADDR='127.0.0.1:6379'
make fmt-check vet test race native native-redis modules config-check build
make vuln
make container
```

`make native` requires both database variables and runs race tests with
`-p=1 -tags=integration -count=1 -timeout=10m`; serial package execution avoids
interference from Mongo's test-only failpoints. Missing required infrastructure
fails instead of silently skipping. `make native-redis` requires REDIS_ADDR.
`make vuln` invokes pinned govulncheck v1.9.0 in failure-propagating text mode.
`make container` builds linux/amd64, runs the image's CLI, then invokes
`make container-smoke`. The two lifecycle tests use `integration/container_test.go`, synthetic
keys and the default `blossm-go-native_default` network with
`CONTAINER_POSTGRES_URL=postgres://postgres:postgres@postgres:5432/blossm?sslmode=disable`.
`IMAGE`, `CONTAINER_NETWORK` and `CONTAINER_POSTGRES_URL` may be overridden for an
explicit matching local fixture. No cloud provisioning is part of these gates.

After preserving needed logs, stop only these disposable Compose fixtures:

```sh
docker compose -f deploy/compose.integration.json logs
docker compose -f deploy/compose.integration.json down -v
```

The real repository workflow is [go-verify.yml](../.github/workflows/go-verify.yml),
with `blossm-go` working directory and `blossm-go/go.sum` cache path. This document
does not claim a remote GitHub Actions run. Original Toolbox/Dia repositories and
user artifacts remain preserved. JavaScript reference-test counts are not Go-test
counts, and local database/container evidence is not live operational readiness.
