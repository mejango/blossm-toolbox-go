# Vendors

Provider-specific adapters share the bottom lib representation and are consumed
by procedures. This restores the familiar vendor grouping without a universal
cloud interface or a runtime compatibility bridge.

| Adapter | Current owner |
| --- | --- |
| AWS and GCP SDK operations | [aws](aws/README.md), [gcp](gcp/README.md) |
| Native PostgreSQL and MongoDB | [postgres](postgres/README.md), [mongodb](mongodb/README.md) |
| Redis, Dwolla and Chainpoint | [external integration contracts](docs/external-integrations.md) |
| Shared adapter helpers | `internal/` |

[Cloud contracts](docs/cloud.md) retain the provider capability inventory and
operation boundaries. [External integration contracts](docs/external-integrations.md)
retain their protocols and explicit limits. Shared scalar query helpers live in
[lib/query](../lib/query); integration-only query/frontier harnesses live under
`tests/querytest` and `tests/frontiertest`, not runtime adapters.

The unchanged storage and integration task histories are archived here as
[storage plan](tasks/storage-todo.md), [storage lessons](tasks/storage-lessons.md)
and [integration plan](tasks/integrations-todo.md). They describe their original
source/checkpoints and do not establish current hierarchy verification.
See [hierarchy](../docs/HIERARCHY.md) for allowed layers and parent-owned gates.
