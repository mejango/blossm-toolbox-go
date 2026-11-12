# Blossm Toolbox Go

Blossm is an application-independent Go backend framework. Module:
`github.com/mejango/blossm-toolbox-go`. Applications consume its public packages
and own their domain models, authorization policy, routes and runtime composition.
[Dia Go](https://github.com/mejango/dia-go) is one such independent consumer.
Toolbox neither imports Dia nor ships its application executable.

## Package groups

| Folder | Responsibility |
| --- | --- |
| `lib/{event,canonical_json,query}` | Shared protocol, serialization and scalar query primitives |
| `lib/{jwt,authentication_error,merkle_tree,rlp}` | Bottom-layer tokens/errors and cryptographic representations |
| `rpc/generic`, `middleware/cors`, `onchain/evm` | Generic wire client, HTTP middleware and Ethereum primitives |
| `vendors/{aws,gcp,postgres,mongodb,redis,dwolla,chainpoint}` | Explicit native/provider adapters |
| `procedures/{command,fact,job,projection}` | Procedure execution, outcomes and projection planning |
| `procedures/{event_stores,view_stores}` | Persistence/replay, remote clients, audit and scoped views |
| `procedures/{gateways,server}` | HTTP composition and server infrastructure lifecycles |
| `cli/blossm`, `cmd/blossm`, `deploy` | Consumer scaffolding, build/test delegation and resource preparation |
| `tests` | Cross-package integration harnesses and shared crypto golden fixtures |

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

See [hierarchy](docs/HIERARCHY.md), [architecture](docs/ARCHITECTURE.md), [capability inventory](docs/capabilities.md),
[CLI](cli/blossm/README.md), [deployment](deploy/README.md) and
[prior independent-split verification](docs/SEPARATION.md) and [publication history](docs/PUBLICATION.md). Historical verification totals describe
the source snapshots recorded there, not unverified later changes. No live
provider deployment is claimed.

The familiar hierarchy refactor has fresh parent verification in progress.
Earlier source-split logs and manifests retain their exact historical meaning.
