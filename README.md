# Blossm Toolbox Go

Public module: `github.com/mejango/blossm-toolbox-go`. The standalone Dia consumer
is [mejango/dia-go](https://github.com/mejango/dia-go). See
[repository ownership and publication verification](docs/PUBLICATION.md).


**Verified locally:** retained scope implemented, review findings remediated, and all required local gates pass. The final bounded Claude Fable5.2 review found no blocker in the reviewed changes. See [verification](docs/VERIFICATION.md) and [review closure](docs/reviews/fable-2026-11-12/closure.md).

One Go module for the retained Blossm framework and Dia services, with `blossm`, `dia` and `framework` entrypoints. Final unit and race suites each pass1,008 test/subtest results; native PostgreSQL/Mongo passes1,211, with zero failed or skipped tests. These overlapping totals are not distinct-test counts or numerical JS parity claims. No live environment has been verified.

The original `../toolbox`, `../dia` and `../dia copy` repositories remain preserved references. This module has no deployed-system migration or JavaScript runtime dependency.

- [Architecture and protocol decisions](docs/ARCHITECTURE.md)
- [Every retained Toolbox capability](docs/capabilities.md)
- [Dia application](dia/README.md)
- [PostgreSQL](storage/postgres/README.md), [MongoDB](storage/mongo/README.md)
- [Cloud adapters](cloud/README.md), [EVM custody/signing](evm/README.md)
- [Offline CLI](cli/README.md), [deployment configuration](deploy/README.md)

## Build and verify

The selected toolchain is installed Go1.27.4, with resolved dependencies in
`go.mod`/`go.sum`. Final full unit and race runs passed753 tests/subtests each across25 test packages
(one additional package has no test files);
EVM/wallet/cryptography and runtime/Open/cloud checks now compile and pass.
Mongo uses driver v2.9.3 and the final govulncheck scan reports no vulnerabilities.
The final Linux image passes CLI/HTTP smoke, with the HTTP test taking1.133s.
All required local gates pass, including native Redis1.280s and a clean tidy diff.
[Machine evidence](docs/verification-results.json) records exact suite/package counts
and raw-log hashes.

Follow [the exact fixture and toolchain setup](docs/VERIFICATION.md#reproduce-the-required-gates),
then run from this module:

```sh
make fmt-check vet test race native native-redis config-check build
make vuln
make container
```

The checked Compose fixtures supply PostgreSQL, a Mongo replica set and Redis.
Native tests fail when required configuration is absent; Mongo fault injection is
restricted to the disposable fixture. `make container` builds linux/amd64 and
runs both CLI and HTTP session create/retry/terminate/revoke smoke tests against
PostgreSQL on the fixture network. Root CI uses these same failure-propagating
gates. None of these commands deploys to a cloud provider.

## Run a local application

`cmd/dia` is an ordinary HTTP server. Configure:

| Variable | Contract |
| --- | --- |
| `NETWORK` | Application network identity |
| `DATABASE_DRIVER` | `postgres` or `mongo` |
| `DATABASE_NAMESPACE` | Explicit isolated namespace |
| `POSTGRES_URL` | PostgreSQL connection string, if selected |
| `MONGODB_URI`, `MONGODB_DATABASE` | Mongo replica URI and database, if selected |
| `LOCAL_MODE` | Explicit `true` or `false`; true bypasses outbox/publication only |
| `JWT_PRIVATE_KEY` | P-256 PKCS8 PEM supplied securely |
| `DIA_HMAC_KEY`, `DIA_ENCRYPTION_KEY` | Independent base64-encoded32-byte secrets |
| `PORT` | HTTP port, default8080 |
| `SMTP_ADDRESS`, `SMTP_USERNAME`, `SMTP_PASSWORD` | Optional verified-TLS mail adapter |
| `EVM_RPC_URLS` | Optional JSON object mapping decimal chain IDs to explicit HTTPS RPC URLs |
| `WALLET_ENCRYPTION_KEY` | Separate base64-encoded32-byte key required when wallets are configured |

Remote mode additionally requires `PUBLISHER=gcp` plus `PUBLICATION_TOPIC` and `PUBLICATION_SUBSCRIPTION`, or `PUBLISHER=aws` plus `PUBLICATION_TOPIC` and `PUBLICATION_QUEUE_URL`. Official SDK credentials are resolved only during explicit runtime construction. Local mode does not produce a remote projection update; invoke the projection owner explicitly when exercising local-only replay, or use the integration fixture which models remote publication with a local durable consumer.

Start with `go run ./cmd/dia`. Session creation accepts `POST /commands/session/start`, strict JSON `{"device":{"type":"desktop","platform":"browser","agent":"example"}}`, and a secret32-byte base64url `Idempotency-Key` generated with `dia.NewSessionSecret()`. Subsequent calls use the returned Bearer token. The sessions view requires that token and returns only its session. See the typed transport routes for all retained command/fact paths.

Wallet endpoints require a registered account with reciprocal persisted account/principal bindings. Anonymous sessions cannot create or use custody. `create`, `prepare`, `submit` and `reconcile` live under `/commands/wallet/`. Amounts and fee caps are decimal integer wei strings; gas, nonce and confirmation values serialize as decimal strings. No private key reaches RPC. An uncertain send remains recoverable from its persisted signed bytes and hash.

Deployment plans and generated artifacts are review material. No merge, publication, deployment, cloud write or blockchain submission has been performed. Relayr payments and JB/shop are explicitly excluded; direct custodial EVM and the retained Juicebox V6 signer are included in the port scope.

Optional comms runtime settings are `TWILIO_ACCOUNT_SID`, `TWILIO_AUTH_TOKEN`,
`TWILIO_FROM`, `UPDATES_PUBLISH_URL` and `UPDATES_PUBLISH_TOKEN`. Missing adapters
return an explicit unavailable error. `CORS_ORIGINS` is a JSON array of exact
HTTP(S) origins; wildcard origins and cookie authentication are not enabled.
