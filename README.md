# Blossm Toolbox Go

Blossm is an application-independent Go backend framework. Module:
`github.com/mejango/blossm-toolbox-go`. Applications consume its public packages
and own their domain models, authorization policy, routes and runtime composition.
[Dia Go](https://github.com/mejango/dia-go) is one such independent consumer.
Toolbox neither imports Dia nor ships its application executable.

## Package groups

| Folder | Responsibility |
| --- | --- |
| `procedures/{command,fact,job,projection}` | Procedure execution, outcomes, replay and projection planning |
| `stores/{event,view}` | Atomic events, replay, durable outbox and scoped versioned views |
| `storage/{postgres,mongo}` | Native implementations of the shared storage protocol |
| `transport`, `rpc`, `delivery` | Authenticated HTTP, clients, durable provider delivery |
| `security/{authn,token,cryptography,audit}` | Generic errors, typed JWT/signing, codecs and private audit export |
| `onchain/evm` | Ethereum encoding, signing and custody primitives, without application ownership policy |
| `cloud/{gcp,aws}`, `integrations` | Provider SDK adapters and external services |
| `runtime/{auth,env,publication,queue}` | Explicit infrastructure construction and lifecycle |
| `cli`, `cmd/blossm`, `deploy` | Consumer scaffolding, project build/test and deployment preparation |

Folders group cohesive Go packages; they do not create separately versioned
modules. Applications choose their own typed JWT context and provide verified
identity and authorization callbacks. The framework does not choose session,
account, scene, role or wallet ownership semantics.

## Build and verify

Use Go 1.27.4 and the pinned disposable fixtures described in
[verification](docs/VERIFICATION.md). From this repository:

```sh
make fmt-check vet test race modules config-check build
# With POSTGRES_URL, MONGODB_URI and REDIS_ADDR for disposable local fixtures:
make native native-redis
make vuln container
```

`fmt-check` also enforces the application dependency boundary. Native gates fail
when required fixture configuration is missing. The image contains the Blossm
CLI; application repositories build their own HTTP containers.

See [architecture](docs/ARCHITECTURE.md), [capability inventory](docs/capabilities.md),
[CLI](cli/README.md), [deployment](deploy/README.md) and
[publication history](docs/PUBLICATION.md). Historical verification totals describe
the source snapshots recorded there, not unverified later changes. No live
provider deployment is claimed.
